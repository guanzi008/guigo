// Copyright 2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package guigo

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// resourceOrigin 为WebKitGTK拦截的内部资源来源
const resourceOrigin = "guigo://localhost"

var active atomic.Pointer[nativeWindow]
var linuxRunning atomic.Bool
var linuxThread sync.Once
var linuxRuns = make(chan linuxRun)
var linuxCallbacks = struct {
	tick, closing, closed, loaded, message, scheme, headers, policy, failed, dialog, drop, loadFailed, startup uintptr
}{}

// nativeWindow 保存GTK线程拥有的窗口及页面状态
type nativeWindow struct {
	options                                          Options
	mu                                               sync.Mutex
	queue                                            []linuxJob
	closed                                           bool
	ctx                                              context.Context
	cancel                                           context.CancelFunc
	done                                             chan struct{}
	window, view, manager, webContext, session, loop uintptr
	dropTarget                                       uintptr
	pageCtx                                          context.Context
	pageCancel                                       context.CancelFunc
	calls                                            *callQueue
	generation                                       uint64
	ready, closing                                   bool
	pendingEvents                                    []string
	resources                                        map[uintptr]bool
	dialogsPending                                   map[uintptr]*linuxDialog
	nextDialog                                       uintptr
	iconDir                                          string
	bridgeToken                                      string
	source, startupTimer                             uint32
	shown                                            bool
	err                                              error
}

// linuxJob 保存线程操作及关闭时的资源释放操作
type linuxJob struct {
	run     func()
	discard func()
}

// linuxRun 保存传给固定GTK线程的运行请求
type linuxRun struct {
	parent  context.Context
	options Options
	result  chan error
}

// init 注册进程生命周期内复用的原生回调
func init() {
	linuxCallbacks.tick = purego.NewCallback(linuxTick)
	linuxCallbacks.closing = purego.NewCallback(linuxClosing)
	linuxCallbacks.closed = purego.NewCallback(linuxClosed)
	linuxCallbacks.loaded = purego.NewCallback(linuxLoadedPage)
	linuxCallbacks.message = purego.NewCallback(linuxMessage)
	linuxCallbacks.scheme = purego.NewCallback(linuxResource)
	linuxCallbacks.headers = purego.NewCallback(linuxHeaders)
	linuxCallbacks.policy = purego.NewCallback(linuxPolicy)
	linuxCallbacks.failed = purego.NewCallback(linuxProcessFailed)
	linuxCallbacks.dialog = purego.NewCallback(linuxDialogFinished)
	linuxCallbacks.drop = purego.NewCallback(linuxDrop)
	linuxCallbacks.loadFailed = purego.NewCallback(linuxLoadFailed)
	linuxCallbacks.startup = purego.NewCallback(linuxStartupTimeout)
}

// run 在固定GTK线程创建窗口并等待退出
// 入参: parent 运行上下文, o 宿主选项
// 返回: error 启动或上下文错误
func run(parent context.Context, o Options) error {
	if !linuxRunning.CompareAndSwap(false, true) {
		return errors.New("another Run is active")
	}
	defer linuxRunning.Store(false)
	linuxThread.Do(func() {
		go func() {
			runtime.LockOSThread()
			for request := range linuxRuns {
				request.result <- runLinux(request.parent, request.options)
			}
		}()
	})
	result := make(chan error, 1)
	linuxRuns <- linuxRun{parent: parent, options: o, result: result}
	return <-result
}

// runLinux 创建GTK窗口并运行原生事件循环
// 入参: parent 运行上下文, o 宿主选项
// 返回: error 启动或上下文错误
func runLinux(parent context.Context, o Options) error {
	n := &nativeWindow{options: o, bridgeToken: rand.Text(), done: make(chan struct{}), resources: make(map[uintptr]bool), dialogsPending: make(map[uintptr]*linuxDialog)}
	if !active.CompareAndSwap(nil, n) {
		return errors.New("another Run is active")
	}
	defer active.Store(nil)
	if err := parent.Err(); err != nil {
		return err
	}
	if err := loadLinux(o.LibraryPath); err != nil {
		return err
	}
	if o.AppID != "" {
		gtk.Prgname(o.AppID)
	}
	if !gtk.Init() {
		return errors.New("GTK could not open display")
	}
	data, err := iconData(o)
	if err != nil {
		return err
	}
	dir := o.UserDataDir
	if dir == "" {
		dir, err = DefaultUserDataDir(o.AppID)
	} else {
		dir, err = filepath.Abs(dir)
	}
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	n.ctx, n.cancel = context.WithCancel(parent)
	defer n.cancel()
	n.webContext = gtk.ContextNew()
	n.session = gtk.SessionNew(filepath.Join(dir, "data"), filepath.Join(dir, "cache"))
	n.manager = gtk.ManagerNew()
	n.loop = gtk.LoopNew(0, false)
	defer gtk.Unref(n.webContext)
	defer gtk.Unref(n.session)
	defer gtk.Unref(n.manager)
	defer gtk.LoopUnref(n.loop)
	security := gtk.Security(n.webContext)
	gtk.SecureScheme(security, "guigo")
	gtk.CorsScheme(security, "guigo")
	gtk.RegisterScheme(n.webContext, "guigo", linuxCallbacks.scheme, 0, 0)
	gtk.Connect(n.manager, "script-message-received::guigo", linuxCallbacks.message, 0, 0, 0)
	if !gtk.RegisterMessage(n.manager, "guigo", 0) {
		return errors.New("register script message handler")
	}
	script := gtk.ScriptNew(linuxBridgeScript(n.bridgeToken), 1, 0, 0, 0)
	gtk.AddScript(n.manager, script)
	gtk.ScriptUnref(script)
	n.view = n.newView()
	if n.view == 0 {
		return errors.New("create WebKitGTK view")
	}
	gtk.Sink(n.view)
	defer gtk.Unref(n.view)
	n.window = gtk.WindowNew()
	gtk.Ref(n.window)
	defer gtk.Unref(n.window)
	defer func() {
		gtk.Destroy(n.window)
		if n.iconDir != "" {
			_ = os.RemoveAll(n.iconDir)
		}
		if o.OnClose != nil {
			o.OnClose()
		}
	}()
	gtk.Title(n.window, o.Title)
	gtk.Size(n.window, int32(o.Width), int32(o.Height))
	gtk.Minimum(n.view, int32(o.MinWidth), int32(o.MinHeight))
	gtk.Resizable(n.window, !o.FixedSize)
	gtk.Child(n.window, n.view)
	if len(data) != 0 {
		n.iconDir, err = linuxIconDirectory()
		if err != nil {
			return err
		}
		iconName := filepath.Base(n.iconDir)
		if err = os.WriteFile(filepath.Join(n.iconDir, iconName+".png"), data, 0600); err != nil {
			return err
		}
		gtk.IconPath(gtk.IconTheme(gtk.Display(n.window)), n.iconDir)
		gtk.IconName(n.window, iconName)
	}
	gtk.Connect(n.window, "close-request", linuxCallbacks.closing, 0, 0, 0)
	gtk.Connect(n.view, "close", linuxCallbacks.closed, 0, 0, 0)
	gtk.Connect(n.view, "load-changed", linuxCallbacks.loaded, 0, 0, 0)
	gtk.Connect(n.view, "load-failed", linuxCallbacks.loadFailed, 0, 0, 0)
	gtk.Connect(n.view, "decide-policy", linuxCallbacks.policy, 0, 0, 0)
	gtk.Connect(n.view, "web-process-terminated", linuxCallbacks.failed, 0, 0, 0)
	if o.OnDrop != nil {
		target := gtk.DropNew(gtk.DropType(), 1)
		n.dropTarget = target
		gtk.ControllerPhase(target, 1)
		gtk.Connect(target, "drop", linuxCallbacks.drop, 0, 0, 0)
		gtk.AddController(n.view, target)
	}
	n.newPage()
	defer n.shutdown()
	stopCancel := context.AfterFunc(n.ctx, func() { _ = n.enqueue(n.shutdown) })
	defer stopCancel()
	defer func() {
		if n.startupTimer != 0 {
			gtk.SourceRemove(n.startupTimer)
		}
		n.mu.Lock()
		source := n.source
		n.source = 0
		n.mu.Unlock()
		if source != 0 {
			gtk.SourceRemove(source)
		}
	}()
	go func() {
		if o.OnReady != nil {
			func() {
				defer func() {
					if v := recover(); v != nil {
						n.fail(fmt.Errorf("OnReady panic: %v", v))
					}
				}()
				o.OnReady(&Window{native: n})
			}()
		}
		_ = n.enqueue(func() {
			n.startupTimer = gtk.Timeout(0, 30000, linuxCallbacks.startup, 0, 0)
			gtk.Load(n.view, resourceURL(o.EntryPath))
		})
	}()
	gtk.LoopRun(n.loop)
	if n.err != nil {
		return n.err
	}
	return parent.Err()
}

// newView 使用固定参数属性接口创建浏览器对象
// 返回: uintptr 浏览器对象
func (n *nativeWindow) newView() uintptr {
	names := [][]byte{[]byte("web-context\x00"), []byte("network-session\x00"), []byte("user-content-manager\x00")}
	pointers := [3]*byte{&names[0][0], &names[1][0], &names[2][0]}
	objects := [3]uintptr{n.webContext, n.session, n.manager}
	var values [3]nativeValue
	for i := range values {
		gtk.ValueInit(&values[i], 80)
		gtk.ValueObject(&values[i], objects[i])
		defer gtk.ValueUnset(&values[i])
	}
	return gtk.ObjectNew(gtk.ViewType(), 3, &pointers[0], &values[0])
}

// linuxIconDirectory 创建仅用于图标的私有临时目录
// 返回: string 目录路径, error 创建错误
func linuxIconDirectory() (string, error) {
	for range 32 {
		number, err := rand.Int(rand.Reader, big.NewInt(10000000))
		if err != nil {
			return "", err
		}
		path := filepath.Join(os.TempDir(), fmt.Sprintf("guigo-%07d", number.Int64()))
		if err = os.Mkdir(path, 0700); err == nil {
			return path, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("could not allocate icon directory")
}

// linuxLoadFailed 报告导航错误并终止首次加载失败
// 入参: view 浏览器对象, event 加载阶段, uri 失败地址, nativeErr 原生错误, data 用户数据
// 返回: uintptr 是否已经处理
func linuxLoadFailed(view uintptr, event int32, uri *byte, nativeErr *linuxError, data uintptr) uintptr {
	n := active.Load()
	if n == nil || n.view != view {
		return 0
	}
	if n.ctx.Err() != nil {
		return 1
	}
	message := "navigation failed"
	if nativeErr != nil {
		message = nativeString(nativeErr.Message)
	}
	err := fmt.Errorf("load %s: %s", nativeString(uri), message)
	n.fail(err)
	if !n.shown {
		n.err = err
		n.shutdown()
	}
	return 1
}

// linuxStartupTimeout 终止超时的首次页面加载
// 入参: data 用户数据
// 返回: uintptr 是否继续计时
func linuxStartupTimeout(data uintptr) uintptr {
	if n := active.Load(); n != nil && !n.shown {
		n.startupTimer = 0
		n.err = errors.New("initial page load timed out")
		n.fail(n.err)
		n.shutdown()
	}
	return 0
}

// linuxBridgeScript 适配WebKit消息入口并保留公共桥接协议
// 入参: token 仅向受信顶层页面注入的消息凭据
// 返回: string 文档初始化脚本
func linuxBridgeScript(token string) string {
	encoded, _ := json.Marshal(token)
	script := strings.Replace(bridgeScript, `location.origin !== "`+resourceOrigin+`"`, `(location.protocol !== "guigo:" || location.host !== "localhost")`, 1)
	script = strings.ReplaceAll(script, "chrome.webview", "transport")
	script = strings.ReplaceAll(script, `method:"POST", body, signal`, `method:"POST", body:await body.arrayBuffer(), signal`)
	return `(() => {
 if (location.protocol !== "guigo:" || location.host !== "localhost" || window !== window.top) return;
 const nativeFetch = window.fetch.bind(window);
 window.fetch = async (input, options) => {
  const url = new URL(input instanceof Request ? input.url : input, location.href);
  if (url.protocol === "guigo:" && url.host === "localhost") {
   if (options?.body instanceof Blob) options = {...options, body:await options.body.arrayBuffer()};
   else if (input instanceof Request && options?.body === undefined && input.method !== "GET" && input.method !== "HEAD") {
    options = {...options, body:await input.clone().arrayBuffer()};
   }
  }
  return nativeFetch(input, options);
 };
 let receive;
 const transport = {
  postMessage: message => window.webkit.messageHandlers.guigo.postMessage(JSON.stringify({...message,token:` + string(encoded) + `})),
  addEventListener: (name, listener) => { receive = listener; }
 };
 Object.defineProperty(window,"__guigoReceive",{value:message => receive?.({data:message})});
 ` + script + `
 })();`
}

// enqueue 将操作交给GTK所属线程
// 入参: fn 原生操作
// 返回: error 关闭错误
func (n *nativeWindow) enqueue(fn func()) error {
	return n.enqueueCleanup(fn, nil)
}

// enqueueCleanup 将操作及资源释放函数交给GTK线程
// 入参: fn 原生操作, discard 关闭时的清理操作
// 返回: error 关闭错误
func (n *nativeWindow) enqueueCleanup(fn, discard func()) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return ErrClosed
	}
	n.queue = append(n.queue, linuxJob{run: fn, discard: discard})
	if n.source == 0 {
		n.source = gtk.Idle(0, linuxCallbacks.tick, 0, 0)
	}
	return nil
}

// linuxTick 处理跨线程操作及上下文取消
// 入参: data 原生用户数据
// 返回: uintptr 是否继续计时
func linuxTick(data uintptr) uintptr {
	n := active.Load()
	if n == nil {
		return 0
	}
	n.mu.Lock()
	work := n.queue
	n.queue = nil
	n.source = 0
	n.mu.Unlock()
	for _, job := range work {
		n.mu.Lock()
		closed := n.closed
		n.mu.Unlock()
		if !closed {
			job.run()
		} else if job.discard != nil {
			job.discard()
		}
	}
	if n.ctx.Err() != nil {
		n.shutdown()
	}
	return 0
}

// newPage 更新页面上下文及调用队列
func (n *nativeWindow) newPage() {
	if n.pageCancel != nil {
		n.pageCancel()
	}
	n.pageCtx, n.pageCancel = context.WithCancel(n.ctx)
	n.calls = newCallQueue(n.pageCtx)
	n.generation++
	n.ready = false
}

// linuxLoadedPage 跟踪导航并发送等待中的事件
// 入参: view 浏览器对象, event 加载状态, data 用户数据
func linuxLoadedPage(view uintptr, event int32, data uintptr) {
	n := active.Load()
	if n == nil || n.view != view {
		return
	}
	if event == 0 {
		n.newPage()
	}
	if event == 3 {
		if !n.shown && n.err == nil && n.ctx.Err() == nil {
			n.shown = true
			if n.startupTimer != 0 {
				gtk.SourceRemove(n.startupTimer)
				n.startupTimer = 0
			}
			if n.options.Maximized && !n.options.FixedSize {
				gtk.Maximize(n.window)
			}
			gtk.Present(n.window)
		}
		n.ready = true
		events := n.pendingEvents
		n.pendingEvents = nil
		for _, raw := range events {
			n.evaluate(raw)
		}
	}
}

// evaluate 将消息投递到当前页面
// 入参: raw JSON消息
func (n *nativeWindow) evaluate(raw string) {
	gtk.Evaluate(n.view, "window.__guigoReceive?.("+raw+")", -1, 0, 0, 0, 0, 0)
}

// emit 在线程队列中发送事件
// 入参: raw JSON消息
// 返回: error 关闭错误
func (n *nativeWindow) emit(raw string) error {
	return n.enqueue(func() {
		if !n.ready {
			n.pendingEvents = append(n.pendingEvents, raw)
		} else {
			n.evaluate(raw)
		}
	})
}

// linuxMessage 解析页面消息并启动异步调用
// 入参: manager 内容管理器, value JavaScript值, data 用户数据
func linuxMessage(manager, value, data uintptr) {
	n := active.Load()
	if n == nil || n.manager != manager || !localURL(gtk.URI(n.view)) {
		return
	}
	p := gtk.JSText(value)
	raw := nativeString(p)
	gtk.Free(unsafe.Pointer(p))
	var received struct {
		envelope
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &received); err != nil {
		n.fail(err)
		return
	}
	if received.Token != n.bridgeToken {
		return
	}
	message := received.envelope
	calls := n.calls
	switch message.Type {
	case "cancel":
		calls.remove(message.ID)
	case "accept":
		calls.accept(message.ID)
	case "call":
		call, err := calls.add(message)
		if err != nil {
			n.evaluate(string(marshalReply(envelope{Type: "reply", ID: message.ID, Error: err.Error()})))
			return
		}
		if message.Binary {
			n.evaluate(string(marshalReply(envelope{Type: "upload", ID: message.ID})))
			return
		}
		generation := n.generation
		go func() {
			reply := calls.run(call, n.options.Bindings, nil, 0)
			encoded := string(marshalReply(reply))
			_ = n.enqueue(func() {
				if generation == n.generation && n.pageCtx.Err() == nil {
					n.evaluate(encoded)
				}
			})
		}()
	}
}

// close 请求执行窗口关闭检查
// 返回: error 关闭错误
func (n *nativeWindow) close() error {
	return n.enqueue(n.requestClose)
}

// linuxClosing 截获用户关闭请求
// 入参: window 窗口对象, data 用户数据
// 返回: uintptr 是否阻止默认销毁
func linuxClosing(window, data uintptr) uintptr {
	if n := active.Load(); n != nil && n.window == window {
		n.requestClose()
	}
	return 1
}

// requestClose 异步检查业务关闭回调并触发beforeunload
func (n *nativeWindow) requestClose() {
	if n.closing {
		return
	}
	n.closing = true
	go func() {
		allow := true
		if n.options.OnClosing != nil {
			func() {
				defer func() {
					if v := recover(); v != nil {
						allow = false
						n.fail(fmt.Errorf("OnClosing panic: %v", v))
					}
				}()
				allow = n.options.OnClosing(n.ctx)
			}()
		}
		_ = n.enqueue(func() {
			n.closing = false
			if allow {
				gtk.TryClose(n.view)
			}
		})
	}()
}

// linuxClosed 完成WebKit允许的关闭请求
// 入参: view 浏览器对象, data 用户数据
func linuxClosed(view, data uintptr) {
	if n := active.Load(); n != nil && n.view == view {
		n.shutdown()
	}
}

// shutdown 取消所有页面操作并退出主循环
func (n *nativeWindow) shutdown() {
	n.mu.Lock()
	first := !n.closed
	n.closed = true
	work := n.queue
	n.queue = nil
	source := n.source
	n.source = 0
	n.mu.Unlock()
	if source != 0 {
		gtk.SourceRemove(source)
	}
	for _, job := range work {
		if job.discard != nil {
			job.discard()
		}
	}
	if first {
		n.cancel()
		n.pageCancel()
		close(n.done)
		for _, d := range n.dialogsPending {
			gtk.Cancel(d.cancel)
		}
		for request := range n.resources {
			n.finishResponse(request, 499, nil)
		}
	}
	if len(n.dialogsPending) == 0 {
		gtk.LoopQuit(n.loop)
	}
}

// setTitle 修改原生窗口标题
// 入参: title 标题
// 返回: error 关闭错误
func (n *nativeWindow) setTitle(title string) error {
	return n.enqueue(func() { gtk.Title(n.window, title) })
}

// setSize 校验并设置窗口尺寸
// 入参: width 宽度, height 高度
// 返回: error 尺寸或关闭错误
func (n *nativeWindow) setSize(width, height int) error {
	if width < 1 || height < 1 || width > 32767 || height > 32767 {
		return errors.New("invalid window dimensions")
	}
	o := n.options
	width = max(width, o.MinWidth)
	height = max(height, o.MinHeight)
	if o.MaxWidth > 0 {
		width = min(width, o.MaxWidth)
	}
	if o.MaxHeight > 0 {
		height = min(height, o.MaxHeight)
	}
	return n.enqueue(func() { gtk.Size(n.window, int32(width), int32(height)) })
}

// openURL 使用系统浏览器打开外部链接
// 入参: raw 外部地址
// 返回: error 启动错误
func openURL(raw string) error {
	linuxLoadMu.Lock()
	path := linuxLoadedPath
	linuxLoadMu.Unlock()
	if err := loadLinux(path); err != nil {
		return err
	}
	var nativeErr *linuxError
	if !gtk.LaunchURI(raw, 0, &nativeErr) {
		if err := nativeError(nativeErr); err != nil {
			return err
		}
		return errors.New("could not launch default URI handler")
	}
	return nil
}

// linuxPolicy 限制页面导航到受信来源
// 入参: view 浏览器对象, decision 导航决策, kind 决策类型, data 用户数据
// 返回: uintptr 是否已经处理
func linuxPolicy(view, decision uintptr, kind int32, data uintptr) uintptr {
	if kind != 0 && kind != 1 {
		return 0
	}
	n := active.Load()
	if n == nil || n.view != view {
		return 0
	}
	action := gtk.NavigationAction(decision)
	uri := gtk.NavigationURI(gtk.ActionRequest(action))
	if kind == 0 && localURL(uri) {
		return 0
	}
	gtk.PolicyIgnore(decision)
	if externalURL(uri) {
		go func() {
			if err := openURL(uri); err != nil {
				n.fail(err)
			}
		}()
	}
	return 1
}

// linuxProcessFailed 报告浏览器进程异常并关闭窗口
// 入参: view 浏览器对象, reason 退出原因, data 用户数据
func linuxProcessFailed(view uintptr, reason int32, data uintptr) {
	if n := active.Load(); n != nil && n.view == view {
		n.err = fmt.Errorf("WebKit web process terminated: %d", reason)
		n.fail(n.err)
		n.shutdown()
	}
}

// fail 异步报告错误并隔离用户回调异常
// 入参: err 异步错误
func (n *nativeWindow) fail(err error) {
	if err != nil && n.options.OnError != nil {
		go func() { defer func() { _ = recover() }(); n.options.OnError(err) }()
	}
}

// linuxDrop 读取GdkFileList并异步通知业务
// 入参: target 拖放目标, value 文件列表, x 横坐标, y 纵坐标, data 用户数据
// 返回: uintptr 是否接收文件
func linuxDrop(target, value uintptr, x, y float64, data uintptr) uintptr {
	n := active.Load()
	if n == nil || target == 0 || n.dropTarget != target || n.options.OnDrop == nil {
		return 0
	}
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed || n.ctx.Err() != nil {
		return 0
	}
	list := gtk.DropFiles(gtk.ValueBoxed(value))
	var paths []string
	for list != nil {
		p := gtk.FilePath(list.Data)
		if p != nil {
			paths = append(paths, nativeString(p))
			gtk.Free(unsafe.Pointer(p))
		}
		list = list.Next
	}
	if len(paths) == 0 {
		return 0
	}
	go func() {
		defer func() {
			if v := recover(); v != nil {
				n.fail(fmt.Errorf("OnDrop panic: %v", v))
			}
		}()
		n.mu.Lock()
		closed := n.closed
		n.mu.Unlock()
		if closed || n.ctx.Err() != nil || active.Load() != n {
			return
		}
		n.options.OnDrop(paths)
	}()
	return 1
}
