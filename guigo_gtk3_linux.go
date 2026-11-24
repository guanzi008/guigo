//go:build linux && !android && cgo && (loong64 || (guigo_gtk3 && (amd64 || arm64)))

package guigo

/*
#cgo pkg-config: gtk+-3.0
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include "guigo_gtk3_linux.h"
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// guigo3Script 通过本机 HTTP 和 SSE 接入共用的 GUIGo 调用桥。
func guigo3Script(origin, token string) string {
	return `(() => {
	if (window !== window.top || location.origin !== ` + bridgeScriptQuoted(origin) + `) return;
	const bridgeToken = ` + bridgeScriptQuoted(token) + `;
	const dropEnabled = false;
	const reportError = error => console.error(error);
	const events = new EventSource("/__guigo/events?token=" + encodeURIComponent(bridgeToken));
	const connected = new Promise(resolve => events.addEventListener("open", resolve, {once:true}));
	let onMessage = () => {};
	const receive = data => onMessage({data});
	events.onmessage = event => receive(JSON.parse(event.data));
	const requests = new Map();
	const transport = {
		addEventListener: (name, listener) => { if (name === "message") onMessage = listener; },
		postMessage(message) {
			if (message.type === "cancel") requests.get(message.id)?.abort();
			const controller = message.type === "call" ? new AbortController() : null;
			if (controller) requests.set(message.id, controller);
			connected.then(async () => {
				if (controller?.signal.aborted) return;
				const response = await fetch("/__guigo/message", {
					method: "POST",
					headers: {"Content-Type": "application/json", "X-Guigo-Token": bridgeToken},
					signal: controller?.signal,
					body: JSON.stringify(message)
				});
				if (!response.ok) throw new Error("GUIGo request failed (" + response.status + ")");
				if (message.type === "call") receive(await response.json());
			}).catch(error => {
				if (message.type === "call") receive({type:"reply", id:message.id, error:error.message});
			}).finally(() => {
				if (controller) requests.delete(message.id);
			});
		}
	};
	addEventListener("pagehide", () => events.close(), {once:true});
` + bridgeScriptCore + `
})();`
}

var guigo3Active atomic.Pointer[nativeWindow]

func supportsWindowMaterial(material WindowMaterial) bool {
	return material == WindowSolid
}

type nativeWindow struct {
	options        Options
	origin         string
	token          string
	pageCtx        context.Context
	calls          atomic.Pointer[callQueue]
	pageCancel     context.CancelFunc
	closed         atomic.Bool
	infoMu         sync.Mutex
	info           TitleBarInfo
	dialogMu       sync.Mutex
	dialogsPending map[uint64]chan guigo3DialogResult
	nextDialog     uint64
	peersMu        sync.Mutex
	peers          map[chan string]struct{}
}

func run(parent context.Context, options Options) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	n := &nativeWindow{
		options: options,
		token:   rand.Text(),
		pageCtx: ctx,
		peers:   make(map[chan string]struct{}),
	}
	n.resetPage()
	if options.TitleBar == TitleBarContent {
		n.info = TitleBarInfo{Height: 36, Right: 108, Colors: options.TitleBarColors, Material: WindowSolid}
	}
	if !guigo3Active.CompareAndSwap(nil, n) {
		return ErrAlreadyRunning
	}
	defer guigo3Active.Store(nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	n.origin = "http://" + listener.Addr().String()
	server := &http.Server{Handler: n.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && options.OnError != nil {
			options.OnError(err)
		}
	}()
	defer server.Close()
	url := cString(resourceAddress(n.origin, options.EntryPath))
	defer C.free(unsafe.Pointer(url))
	script := cString(guigo3Script(n.origin, n.token))
	defer C.free(unsafe.Pointer(script))
	title := cString(options.Title)
	defer C.free(unsafe.Pointer(title))
	colors := cString(titlebarCSS(options.TitleBarColors))
	defer C.free(unsafe.Pointer(colors))
	var icon *C.uchar
	iconData, err := iconData(options)
	if err != nil {
		return err
	}
	if len(iconData) > 0 {
		icon = (*C.uchar)(unsafe.Pointer(&iconData[0]))
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	stop := context.AfterFunc(parent, func() { C.guigo3Close() })
	defer stop()
	code := C.guigo3Run(url, script, title, C.int(options.Width), C.int(options.Height),
		C.int(options.MinWidth), C.int(options.MinHeight),
		cBool(options.TitleBar == TitleBarContent), cBool(options.Maximized),
		colors, icon, C.int(len(iconData)))
	runtime.KeepAlive(iconData)
	n.closed.Store(true)
	cancel()
	if options.OnClose != nil {
		options.OnClose()
	}
	if code != 0 {
		return fmt.Errorf("%w: GTK 3 / WebKit2GTK (%d)", ErrRuntimeUnavailable, int(code))
	}
	return parent.Err()
}

func cBool(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func cString(value string) *C.char { return C.CString(value) }

func (n *nativeWindow) resetPage() {
	if n.pageCancel != nil {
		n.pageCancel()
	}
	ctx, cancel := context.WithCancel(n.pageCtx)
	n.pageCancel = cancel
	n.calls.Store(newCallQueue(ctx))
}

func titlebarCSS(colors TitleBarColors) string {
	if colors == (TitleBarColors{}) {
		return ""
	}
	return fmt.Sprintf(".guigo-controls button { background:%s; color:%s; border:0; border-radius:0; }"+
		".guigo-controls:backdrop button { background:%s; color:%s; }",
		colors.Background, colors.Foreground, colors.InactiveBackground, colors.InactiveForeground)
}

func (n *nativeWindow) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "http://"+r.Host != n.origin || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != n.origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		switch r.URL.Path {
		case "/__guigo/events":
			n.serveEvents(w, r)
			return
		case "/__guigo/call":
			n.serveBinaryCall(w, r)
			return
		case "/__guigo/message":
			n.serveMessage(w, r)
			return
		}
		if isReservedPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if n.options.ResourceHandler != nil {
			resource, err := n.options.ResourceHandler(r)
			if resource != nil && resource.Body != nil {
				defer resource.Body.Close()
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if resource != nil {
				status := resource.StatusCode
				if status == 0 {
					status = http.StatusOK
				}
				if resource.Body == nil && status != http.StatusNoContent && status != http.StatusNotModified {
					http.Error(w, "missing response body", http.StatusInternalServerError)
					return
				}
				for key, values := range resource.Header {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(status)
				if resource.Body != nil && r.Method != http.MethodHead {
					_, _ = io.Copy(w, resource.Body)
				}
				return
			}
		}
		n.options.Handler.ServeHTTP(w, r)
	})
}

func (n *nativeWindow) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Query().Get("token") != n.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	peer := make(chan string, 64)
	n.peersMu.Lock()
	n.peers[peer] = struct{}{}
	n.peersMu.Unlock()
	defer func() { n.peersMu.Lock(); delete(n.peers, peer); n.peersMu.Unlock() }()
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	page := n.calls.Load().ctx
	for {
		select {
		case <-r.Context().Done():
			return
		case <-page.Done():
			return
		case message := <-peer:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", message)
			flusher.Flush()
		case <-tick.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (n *nativeWindow) serveMessage(w http.ResponseWriter, r *http.Request) {
	if !n.authorizedCall(w, r) {
		return
	}
	var message envelope
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&message); err != nil {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	calls := n.calls.Load()
	switch message.Type {
	case "cancel":
		calls.remove(message.ID)
		w.WriteHeader(http.StatusNoContent)
	case "accept":
		calls.accept(message.ID)
		w.WriteHeader(http.StatusNoContent)
	case "call":
		call, err := calls.add(message)
		if err != nil {
			writeGuigo3Reply(w, envelope{Type: "reply", ID: message.ID, Error: err.Error()})
			return
		}
		if message.Binary {
			if !writeGuigo3Reply(w, envelope{Type: "upload", ID: message.ID}) {
				calls.remove(message.ID)
			}
			return
		}
		n.runCall(w, r, calls, call, nil, 0)
	default:
		http.Error(w, "invalid message", http.StatusBadRequest)
	}
}

func (n *nativeWindow) authorizedCall(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.Header.Get("X-Guigo-Token") != n.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (n *nativeWindow) serveBinaryCall(w http.ResponseWriter, r *http.Request) {
	if !n.authorizedCall(w, r) {
		return
	}
	size, err := strconv.ParseInt(r.Header.Get("X-Guigo-Length"), 10, 64)
	if err != nil || size < 4 || size > maxBinaryCallSize || (r.ContentLength >= 0 && r.ContentLength != size) {
		http.Error(w, "invalid binary size", http.StatusBadRequest)
		return
	}
	calls := n.calls.Load()
	call := calls.take(r.URL.Query().Get("id"))
	if call == nil {
		http.Error(w, "call closed", http.StatusGone)
		return
	}
	n.runCall(w, r, calls, call, http.MaxBytesReader(w, r.Body, size), size)
}

func (n *nativeWindow) runCall(w http.ResponseWriter, r *http.Request, calls *callQueue, call *pendingCall, body io.Reader, size int64) {
	stop := context.AfterFunc(r.Context(), func() { calls.remove(call.message.ID) })
	defer stop()
	reply := calls.run(call, n.options.Bindings, body, size)
	if !writeGuigo3Reply(w, reply) {
		calls.remove(call.message.ID)
	}
}

func writeGuigo3Reply(w http.ResponseWriter, reply envelope) bool {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err := w.Write(marshalReply(reply))
	return err == nil
}

func (n *nativeWindow) emit(raw string) error {
	if n.closed.Load() {
		return ErrClosed
	}
	n.peersMu.Lock()
	defer n.peersMu.Unlock()
	for peer := range n.peers {
		select {
		case peer <- raw:
		default:
		}
	}
	return nil
}

func (n *nativeWindow) close() error {
	if n.closed.Load() {
		return ErrClosed
	}
	C.guigo3Close()
	return nil
}

func (n *nativeWindow) activate() error {
	if n.closed.Load() {
		return ErrClosed
	}
	C.guigo3Activate()
	return nil
}

func (n *nativeWindow) hide() error {
	if n.closed.Load() {
		return ErrClosed
	}
	C.guigo3Hide()
	return nil
}

func (n *nativeWindow) setTitle(title string) error {
	if n.closed.Load() {
		return ErrClosed
	}
	value := cString(title)
	C.guigo3SetTitle(value)
	C.free(unsafe.Pointer(value))
	return nil
}

func (n *nativeWindow) titleBarInfo() (TitleBarInfo, error) {
	if n.closed.Load() {
		return TitleBarInfo{}, ErrClosed
	}
	n.infoMu.Lock()
	defer n.infoMu.Unlock()
	return n.info, nil
}

func (n *nativeWindow) setTitleBarColors(colors TitleBarColors) error {
	if n.closed.Load() {
		return ErrClosed
	}
	if n.options.TitleBar != TitleBarContent {
		return errors.New("title bar colors require content title bar")
	}
	value := cString(titlebarCSS(colors))
	C.guigo3SetTitleBarColors(value)
	C.free(unsafe.Pointer(value))
	n.infoMu.Lock()
	info := n.info
	if info.Colors == colors {
		n.infoMu.Unlock()
		return nil
	}
	info.Colors = colors
	n.info = info
	n.infoMu.Unlock()
	if n.options.OnTitleBarChanged != nil {
		go n.options.OnTitleBarChanged(info)
	}
	return nil
}

func (n *nativeWindow) setWindowMaterial(material WindowMaterial) error {
	if n.closed.Load() {
		return ErrClosed
	}
	if material != WindowSolid {
		return ErrUnsupportedPlatform
	}
	return nil
}

func (n *nativeWindow) setDragRegions(regions []Rect) error {
	if n.closed.Load() {
		return ErrClosed
	}
	if n.options.TitleBar != TitleBarContent {
		return errors.New("content title bar is not enabled")
	}
	var region Rect
	if len(regions) > 0 {
		region = regions[0]
	}
	C.guigo3SetDrag(C.double(region.X), C.double(region.Y),
		C.double(region.Width), C.double(region.Height))
	return nil
}

func (n *nativeWindow) setSize(width, height int) error {
	if n.closed.Load() {
		return ErrClosed
	}
	C.guigo3SetSize(C.int(width), C.int(height))
	return nil
}

func (n *nativeWindow) dialogs(ctx context.Context, options DialogOptions, mode int) ([]string, error) {
	if n.closed.Load() || n.pageCtx.Err() != nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.DefaultPath != "" {
		path, err := filepath.Abs(options.DefaultPath)
		if err != nil {
			return nil, err
		}
		options.DefaultPath = path
	}
	result := make(chan guigo3DialogResult, 1)
	n.dialogMu.Lock()
	if len(n.dialogsPending) > 0 {
		n.dialogMu.Unlock()
		return nil, ErrDialogBusy
	}
	if n.dialogsPending == nil {
		n.dialogsPending = make(map[uint64]chan guigo3DialogResult)
	}
	n.nextDialog++
	id := n.nextDialog
	n.dialogsPending[id] = result
	n.dialogMu.Unlock()

	title := cString(options.Title)
	defer C.free(unsafe.Pointer(title))
	path := cString(options.DefaultPath)
	defer C.free(unsafe.Pointer(path))
	name := cString(options.DefaultName)
	defer C.free(unsafe.Pointer(name))
	filterNames := make([]*C.char, len(options.Filters))
	filterPatterns := make([]*C.char, len(options.Filters))
	for i, filter := range options.Filters {
		filterNames[i] = cString(filter.Name)
		filterPatterns[i] = cString(filter.Pattern)
	}
	defer func() {
		for i := range filterNames {
			C.free(unsafe.Pointer(filterNames[i]))
			C.free(unsafe.Pointer(filterPatterns[i]))
		}
	}()
	var names, patterns **C.char
	if len(filterNames) > 0 {
		names = &filterNames[0]
		patterns = &filterPatterns[0]
	}
	started := C.guigo3DialogStart(C.ulonglong(id), C.int(mode), title, path, name,
		names, patterns, C.int(len(filterNames)))
	runtime.KeepAlive(filterNames)
	runtime.KeepAlive(filterPatterns)
	if started == 0 {
		n.dialogMu.Lock()
		delete(n.dialogsPending, id)
		n.dialogMu.Unlock()
		return nil, ErrClosed
	}
	select {
	case value := <-result:
		if n.pageCtx.Err() != nil {
			return nil, ErrClosed
		}
		return value.paths, value.err
	case <-ctx.Done():
		C.guigo3DialogCancel(C.ulonglong(id))
		return nil, ctx.Err()
	case <-n.pageCtx.Done():
		C.guigo3DialogCancel(C.ulonglong(id))
		return nil, ErrClosed
	}
}

type guigo3DialogResult struct {
	paths []string
	err   error
}

func (n *nativeWindow) dialog(ctx context.Context, options DialogOptions, mode int) (string, error) {
	paths, err := n.dialogs(ctx, options, mode)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", ErrCanceled
	}
	return paths[0], nil
}

//export guigo3DialogFinished
func guigo3DialogFinished(id C.ulonglong, status C.int, paths **C.char, count C.int) {
	n := guigo3Active.Load()
	if n == nil {
		return
	}
	value := guigo3DialogResult{}
	if status == 1 && count > 0 && paths != nil {
		for _, path := range unsafe.Slice(paths, int(count)) {
			value.paths = append(value.paths, C.GoString(path))
		}
	} else if status < 0 {
		value.err = ErrClosed
	} else {
		value.err = ErrCanceled
	}
	n.dialogMu.Lock()
	result := n.dialogsPending[uint64(id)]
	delete(n.dialogsPending, uint64(id))
	n.dialogMu.Unlock()
	if result != nil {
		result <- value
	}
}

func (n *nativeWindow) printCapabilities(ctx context.Context) (PrintCapabilities, error) {
	return PrintCapabilities{}, ctx.Err()
}
func (n *nativeWindow) showPrintDialog(context.Context, PrintDialogKind) error {
	return ErrPrintUnsupported
}
func (n *nativeWindow) listPrinters(context.Context) ([]PrinterInfo, error) {
	return nil, ErrPrintUnsupported
}
func (n *nativeWindow) queryPrinter(context.Context, PrintOptions) (PrinterProfile, error) {
	return PrinterProfile{}, ErrPrintUnsupported
}
func (n *nativeWindow) showPrinterProperties(context.Context, string) error {
	return ErrPrintUnsupported
}
func (n *nativeWindow) print(context.Context, PrintOptions) (PrintResult, error) {
	return PrintResult{}, ErrPrintUnsupported
}

func (n *nativeWindow) newTray(context.Context, TrayOptions) (*Tray, error) {
	return nil, ErrUnsupportedPlatform
}

func openURL(raw string) error {
	cmd := exec.Command("xdg-open", raw)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

//export guigo3AllowNavigation
func guigo3AllowNavigation(uri *C.char, newWindow, userGesture C.int) C.int {
	n := guigo3Active.Load()
	if n == nil {
		return 0
	}
	raw := C.GoString(uri)
	local := localResource(n.origin, raw)
	if newWindow == 0 && local {
		return 1
	}
	if userGesture != 0 && !local && externalURL(raw) {
		go func() {
			if err := openURL(raw); err != nil && n.options.OnError != nil {
				n.options.OnError(err)
			}
		}()
	}
	return 0
}

//export guigo3NavigationStarted
func guigo3NavigationStarted() {
	if n := guigo3Active.Load(); n != nil {
		n.resetPage()
	}
}

//export guigo3CanClose
func guigo3CanClose() C.int {
	n := guigo3Active.Load()
	if n == nil {
		return 1
	}
	if n.options.OnClosing != nil && !n.options.OnClosing(n.pageCtx) {
		return 0
	}
	return 1
}

//export guigo3Ready
func guigo3Ready() {
	n := guigo3Active.Load()
	if n != nil && n.options.OnReady != nil {
		n.options.OnReady(&Window{native: n})
	}
}

//export guigo3TitleChanged
func guigo3TitleChanged(active, maximized C.int) {
	n := guigo3Active.Load()
	if n == nil {
		return
	}
	n.infoMu.Lock()
	info := n.info
	info.Active = active != 0
	info.Maximized = maximized != 0
	changed := info != n.info
	n.info = info
	n.infoMu.Unlock()
	if changed && n.options.OnTitleBarChanged != nil {
		go n.options.OnTitleBarChanged(info)
	}
}
