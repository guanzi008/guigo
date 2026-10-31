//go:build linux && loong64 && cgo

package guigo

/*
#cgo pkg-config: gtk+-3.0
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include "guigo_linux_loong64.h"
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const guigo3Bridge = `(() => {
	if (window !== window.top || location.origin !== __GUIGO_ORIGIN__) return;
	const token = __GUIGO_TOKEN__;
	const listeners = new Map();
	const pending = new Set();
	const events = new EventSource("/__guigo/events?token=" + encodeURIComponent(token));
	const connected = new Promise(resolve => events.addEventListener("open", resolve, {once:true}));
	events.onmessage = ({data}) => {
		const message = JSON.parse(data);
		if (message.type !== "event") return;
		for (const listener of [...(listeners.get(message.name) || [])]) {
			try { listener(message.value); } catch (error) { console.error(error); }
		}
	};
	async function encode(value) {
		if (value instanceof Blob || value instanceof ArrayBuffer || ArrayBuffer.isView(value)) {
			const bytes = value instanceof Blob ? new Uint8Array(await value.arrayBuffer()) :
				value instanceof ArrayBuffer ? new Uint8Array(value) :
				new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
			let binary = "";
			for (let offset = 0; offset < bytes.length; offset += 16384)
				binary += String.fromCharCode(...bytes.subarray(offset, offset + 16384));
			return {__guigoBytes:btoa(binary)};
		}
		if (value && typeof value.toJSON === "function") return encode(value.toJSON());
		if (Array.isArray(value)) return Promise.all(value.map(encode));
		if (value && typeof value === "object")
			return Object.fromEntries(await Promise.all(Object.entries(value).map(async ([key, item]) => [key, await encode(item)])));
		return value;
	}
	Object.defineProperty(window, "guigo", {value:Object.freeze({
		on(name, listener) {
			let group = listeners.get(name);
			if (!group) listeners.set(name, group = new Set());
			group.add(listener);
			return () => { group.delete(listener); if (!group.size) listeners.delete(name); };
		},
		async call(name, args = {}, options = {}) {
			await connected;
			options.signal?.throwIfAborted();
			const response = await fetch("/__guigo/call", {
				method:"POST",
				headers:{"Content-Type":"application/json", "X-Guigo-Token":token},
				body:JSON.stringify({name, args:await encode(args)}),
				signal:options.signal
			});
			if (!response.ok) throw new Error("GUIGo request failed (" + response.status + ")");
			const result = await response.json();
			if (!result.ok) {
				const error = new Error(result.error);
				error.name = result.errorName || "Error";
				if (result.code) error.code = result.code;
				throw error;
			}
			return result.value;
		}
	}), configurable:false});
	addEventListener("pagehide", () => events.close(), {once:true});
})();`

var guigo3Active atomic.Pointer[nativeWindow]

// SupportsWindowMaterial 查询当前GTK窗口支持的材质。
func SupportsWindowMaterial(material WindowMaterial) bool {
	return material == WindowSolid
}

type nativeWindow struct {
	options        Options
	origin         string
	token          string
	pageCtx        context.Context
	cancel         context.CancelFunc
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
		cancel:  cancel,
		peers:   make(map[chan string]struct{}),
	}
	if options.TitleBar == TitleBarContent {
		n.info = TitleBarInfo{Height: 36, Right: 108, Colors: options.TitleBarColors}
	}
	if !guigo3Active.CompareAndSwap(nil, n) {
		return errors.New("another Run is active")
	}
	defer guigo3Active.Store(nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	n.origin = "http://" + listener.Addr().String()
	server := &http.Server{Handler: n.handler()}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && options.OnError != nil {
			options.OnError(err)
		}
	}()
	defer server.Close()
	url := cString(resourceAddress(n.origin, options.EntryPath))
	defer C.free(unsafe.Pointer(url))
	bridge := strings.ReplaceAll(guigo3Bridge, "__GUIGO_ORIGIN__", strconv.Quote(n.origin))
	bridge = strings.ReplaceAll(bridge, "__GUIGO_TOKEN__", strconv.Quote(n.token))
	script := cString(bridge)
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
	if _, set := os.LookupEnv("WEBKIT_DISABLE_COMPOSITING_MODE"); !set {
		_ = os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
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
	if options.OnClose != nil {
		options.OnClose()
	}
	if code != 0 {
		return fmt.Errorf("GTK 3 / WebKit2GTK startup failed (%d)", int(code))
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
		switch r.URL.Path {
		case "/__guigo/events":
			n.serveEvents(w, r)
			return
		case "/__guigo/call":
			n.serveCall(w, r)
			return
		}
		if n.options.ResourceHandler != nil {
			resource, err := n.options.ResourceHandler(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if resource != nil {
				defer resource.Body.Close()
				for key, values := range resource.Header {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				status := resource.StatusCode
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				if r.Method != http.MethodHead {
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
	for {
		select {
		case <-r.Context().Done():
			return
		case <-n.pageCtx.Done():
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

func (n *nativeWindow) serveCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("X-Guigo-Token") != n.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var request struct {
		Name string `json:"name"`
		Args any    `json:"args"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid call", http.StatusBadRequest)
		return
	}
	args, err := guigo3Decode(request.Args)
	if err != nil {
		http.Error(w, "invalid arguments", http.StatusBadRequest)
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	ctx := context.WithValue(r.Context(), pageContextKey{}, n.pageCtx)
	reply := dispatch(ctx, n.options.Bindings, envelope{Type: "call", ID: "1", Name: request.Name, Args: args})
	response := map[string]any{"ok": reply.Error == "", "value": reply.Value}
	if reply.Error != "" {
		response["error"] = reply.Error
		response["errorName"] = reply.ErrorName
		response["code"] = reply.Code
	}
	if result, ok := reply.Value.(Result); ok {
		response["value"] = result.Value
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(response); err != nil && n.options.OnError != nil {
		n.options.OnError(err)
	}
}

func guigo3Decode(value any) (any, error) {
	switch item := value.(type) {
	case map[string]any:
		if raw, ok := item["__guigoBytes"].(string); ok && len(item) == 1 {
			return base64.StdEncoding.DecodeString(raw)
		}
		for key, child := range item {
			decoded, err := guigo3Decode(child)
			if err != nil {
				return nil, err
			}
			item[key] = decoded
		}
	case []any:
		for i, child := range item {
			decoded, err := guigo3Decode(child)
			if err != nil {
				return nil, err
			}
			item[i] = decoded
		}
	}
	return value, nil
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

func (n *nativeWindow) dialogs(options DialogOptions, mode int) ([]string, error) {
	if n.closed.Load() || n.pageCtx.Err() != nil {
		return nil, ErrClosed
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
	case <-n.pageCtx.Done():
		n.dialogMu.Lock()
		delete(n.dialogsPending, id)
		n.dialogMu.Unlock()
		return nil, ErrClosed
	}
}

type guigo3DialogResult struct {
	paths []string
	err   error
}

func (n *nativeWindow) dialog(options DialogOptions, mode int) (string, error) {
	paths, err := n.dialogs(options, mode)
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
func (n *nativeWindow) showPrintUI(context.Context, string) error { return ErrPrintUnsupported }
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

func resourceURL(path string) string {
	n := guigo3Active.Load()
	if n == nil {
		return ""
	}
	return resourceAddress(n.origin, path)
}

func openURL(raw string) error {
	cmd := exec.Command("xdg-open", raw)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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
