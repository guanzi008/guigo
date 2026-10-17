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

// Package guigo 轻量、基于系统 WebView 的 Go 语言图形界面库
package guigo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// ErrUnsupportedPlatform 表示当前平台没有可用宿主实现
	ErrUnsupportedPlatform = errors.New("unsupported platform")
	// ErrCanceled 表示用户取消对话框
	ErrCanceled = errors.New("dialog canceled")
	// ErrClosed 表示窗口已经关闭
	ErrClosed = errors.New("window closed")
)

// HandlerFunc 异步处理页面调用，窗口关闭时上下文取消
// 入参: ctx 调用上下文, args JSON参数
// 返回: any 可JSON编码的结果, error 错误信息
type HandlerFunc func(context.Context, json.RawMessage) (any, error)

// Options 定义窗口、资源和生命周期配置，客户区尺寸单位为逻辑像素
type Options struct {
	// AppID 指定稳定的应用标识，长度不超过128字节，以小写字母开头，不以点结尾
	// 允许小写字母、数字、点和连字符，不随程序版本或位置变化
	AppID string
	// Title 指定窗口标题
	Title string
	// Icon 指定正方形PNG窗口图标数据，边长不超过2048像素，数据不超过16MiB，与IconPath互斥
	Icon []byte
	// IconPath 指定外置PNG图标路径，格式及尺寸限制与Icon一致，两者互斥
	IconPath string
	// Width 指定初始客户区宽度，零值使用1024逻辑像素
	Width int
	// Height 指定初始客户区高度，零值使用768逻辑像素
	Height int
	// MinWidth 指定最小客户区宽度，零值表示不限制
	MinWidth int
	// MinHeight 指定最小客户区高度，零值表示不限制
	MinHeight int
	// MaxWidth 指定最大客户区宽度，零值表示不限制
	MaxWidth int
	// MaxHeight 指定最大客户区高度，零值表示不限制
	MaxHeight int
	// FixedSize 禁止用户调整窗口大小，程序仍可调用SetSize
	FixedSize bool
	// Center 在首次显示前按所在屏幕工作区居中，超出工作区的方向靠齐起点，不改变窗口尺寸
	Center bool
	// UserDataDir 指定持久数据目录，优先于AppID，两者至少设置一个，目录由调用方清理
	// 相对路径基于当前工作目录，便携程序可传入可执行文件所在目录下的绝对路径
	UserDataDir string
	// LibraryPath 指定原生运行时模块的路径，空值查找系统安装的运行时
	LibraryPath string
	// EntryPath 指定资源入口路径，空值使用根路径
	EntryPath string
	// FS 提供内嵌或外置静态资源，Handler非空时不使用
	FS fs.FS
	// Handler 并发处理本地资源请求，不启动网络服务
	Handler http.Handler
	// ResourceHandler 优先提供可寻址资源，返回nil, nil时使用Handler
	ResourceHandler func(*http.Request) (*Resource, error)
	// Bindings 注册页面可调用的方法，处理器可能并发执行
	Bindings map[string]HandlerFunc
	// OnReady 在宿主就绪且首次导航前调用，回调返回后才加载页面
	OnReady func(*Window)
	// OnClosing 返回是否允许用户关闭窗口，上下文取消时直接关闭
	OnClosing func(context.Context) bool
	// OnClose 在窗口退出后调用，回调结束后Run返回
	OnClose func()
	// OnDrop 在后台协程接收系统文件拖放的路径，多次回调可能并发执行
	OnDrop func([]string)
	// OnError 接收异步操作错误，多次回调可能并发执行
	OnError func(error)
}

// Resource 提供已就绪的可寻址响应流，Body所有权交给宿主并由宿主关闭
// ResourceHandler返回nil, nil时回退到Handler或FS
// 状态码默认200，范围请求需由处理器提供正确状态和范围内容
type Resource struct {
	// StatusCode 指定HTTP响应状态码，零值使用200
	StatusCode int
	// Header 指定响应头，nil表示不附加响应头
	Header http.Header
	// Body 提供可寻址响应流，返回资源后由宿主负责关闭
	Body io.ReadSeekCloser
}

// FileFilter 定义文件类型，Pattern使用*.txt;*.md格式
type FileFilter struct {
	// Name 指定文件类型的显示名称
	Name string
	// Pattern 指定通配符，多个模式使用分号分隔
	Pattern string
}

// DialogOptions 定义原生文件对话框
type DialogOptions struct {
	// Title 指定对话框标题
	Title string
	// DefaultPath 指定初始目录或文件路径
	DefaultPath string
	// DefaultName 指定初始文件名，目录选择时不使用
	DefaultName string
	// Filters 指定可选文件类型，目录选择时不使用
	Filters []FileFilter
}

// BridgeError 定义传递给JavaScript的结构化错误
type BridgeError struct {
	// Name 指定JavaScript错误名称，空值使用Error
	Name string
	// Code 指定业务错误代码，空值不传递
	Code string
	// Message 指定错误说明
	Message string
}

// Window 持有窗口宿主，由OnReady提供，所有公开方法可由后台协程调用
type Window struct {
	native *nativeWindow
}

// envelope 表示双向消息信封
type envelope struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Value     any             `json:"value"`
	Error     string          `json:"error,omitempty"`
	ErrorName string          `json:"errorName,omitempty"`
	Code      string          `json:"code,omitempty"`
}

// Error 返回错误说明
// 返回: string 错误信息
func (e *BridgeError) Error() string {
	return e.Message
}

// DefaultUserDataDir 返回应用的默认浏览器数据目录，不创建或删除目录
// 目录位于os.UserCacheDir返回的位置下，使用guigo/app-应用标识作为子目录，Windows使用GUIGo目录名
// 入参: appID 稳定的应用标识
// 返回: string 绝对目录路径, error 参数或路径错误
func DefaultUserDataDir(appID string) (string, error) {
	if err := validateAppID(appID); err != nil {
		return "", err
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	name := "guigo"
	if runtime.GOOS == "windows" {
		name = "GUIGo"
	}
	return filepath.Abs(filepath.Join(base, name, "app-"+appID))
}

// OpenURL 使用系统默认浏览器打开http或https地址
// 入参: rawURL 外部地址
// 返回: error 错误信息
func OpenURL(rawURL string) error {
	if !externalURL(rawURL) {
		return errors.New("external URL must use http or https")
	}
	return openURL(rawURL)
}

// Run 在调用线程运行窗口事件循环，关闭后返回，同一进程不支持同时运行多个实例
// 浏览器数据在关闭、启动失败和版本升级时保留，删除由调用方负责
// 入参: ctx 运行上下文, options 宿主选项
// 返回: error 启动、运行或上下文错误
func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if err := validate(&options); err != nil {
		return err
	}
	if options.AppID == "" && options.UserDataDir == "" {
		return errors.New("AppID or UserDataDir is required")
	}
	return run(ctx, options)
}

// Emit 向页面发送命名事件，返回值表示消息已交给宿主而非页面已消费
// 入参: name 非空事件名称, value 可JSON编码的数据
// 返回: error 错误信息
func (w *Window) Emit(name string, value any) error {
	if w == nil || w.native == nil {
		return ErrClosed
	}
	if name == "" {
		return errors.New("empty event name")
	}
	b, err := json.Marshal(envelope{Type: "event", Name: name, Value: value})
	if err != nil {
		return err
	}
	return w.native.emit(string(b))
}

// Close 请求关闭窗口，OnClosing和页面beforeunload可以取消此次请求
// 返回: error 请求投递错误
func (w *Window) Close() error {
	if w == nil || w.native == nil {
		return ErrClosed
	}
	return w.native.close()
}

// SetTitle 修改窗口标题
// 入参: title 窗口标题
// 返回: error 错误信息
func (w *Window) SetTitle(title string) error {
	if w == nil || w.native == nil {
		return ErrClosed
	}
	if strings.ContainsRune(title, 0) {
		return errors.New("title contains NUL")
	}
	return w.native.setTitle(title)
}

// SetSize 修改客户区尺寸并遵循约束
// 入参: width 客户区宽度, height 客户区高度
// 返回: error 错误信息
func (w *Window) SetSize(width, height int) error {
	if w == nil || w.native == nil {
		return ErrClosed
	}
	return w.native.setSize(width, height)
}

// ResourceURL 返回当前平台的宿主资源地址，不支持的平台返回空字符串
// 入参: path 资源路径
// 返回: string 宿主资源地址
func (w *Window) ResourceURL(path string) string {
	return resourceURL(path)
}

// OpenFile 选择单个已有文件
// 入参: o 对话框选项
// 返回: string 文件路径, error 错误信息，用户取消返回ErrCanceled
func (w *Window) OpenFile(o DialogOptions) (string, error) {
	if w == nil || w.native == nil {
		return "", ErrClosed
	}
	if err := validateDialog(o); err != nil {
		return "", err
	}
	return w.native.dialog(o, 0)
}

// OpenFiles 选择多个已有文件
// 入参: o 对话框选项
// 返回: []string 文件路径, error 错误信息，用户取消返回ErrCanceled
func (w *Window) OpenFiles(o DialogOptions) ([]string, error) {
	if w == nil || w.native == nil {
		return nil, ErrClosed
	}
	if err := validateDialog(o); err != nil {
		return nil, err
	}
	return w.native.dialogs(o, 3)
}

// SaveFile 选择保存路径并提示覆盖已有文件
// 入参: o 对话框选项
// 返回: string 保存路径, error 错误信息，用户取消返回ErrCanceled
func (w *Window) SaveFile(o DialogOptions) (string, error) {
	if w == nil || w.native == nil {
		return "", ErrClosed
	}
	if err := validateDialog(o); err != nil {
		return "", err
	}
	return w.native.dialog(o, 1)
}

// OpenDirectory 选择已有目录
// 入参: o 对话框选项
// 返回: string 目录路径, error 错误信息，用户取消返回ErrCanceled
func (w *Window) OpenDirectory(o DialogOptions) (string, error) {
	if w == nil || w.native == nil {
		return "", ErrClosed
	}
	if err := validateDialog(o); err != nil {
		return "", err
	}
	return w.native.dialog(o, 2)
}

// validateDialog 检查传入原生对话框的字符串
// 入参: o 对话框选项
// 返回: error 校验错误
func validateDialog(o DialogOptions) error {
	values := []string{o.Title, o.DefaultPath, o.DefaultName}
	for _, filter := range o.Filters {
		values = append(values, filter.Name, filter.Pattern)
	}
	for _, value := range values {
		if strings.ContainsRune(value, 0) {
			return errors.New("dialog option contains NUL")
		}
	}
	return nil
}

// dispatch 校验调用并隔离业务处理器异常
// 入参: ctx 调用上下文, bindings 方法集合, raw 请求数据
// 返回: envelope 调用结果
func dispatch(ctx context.Context, bindings map[string]HandlerFunc, raw []byte) (reply envelope) {
	reply.Type = "reply"
	defer func() {
		if recover() != nil {
			reply.Error = "handler panic"
			reply.Value = nil
		}
	}()
	var call envelope
	if err := json.Unmarshal(raw, &call); err != nil {
		reply.Error = "invalid JSON"
		return
	}
	reply.ID = call.ID
	if call.Type != "call" || call.ID == "" || call.Name == "" {
		reply.Error = "invalid call"
		return
	}
	handler := bindings[call.Name]
	if handler == nil {
		reply.Error = "unknown method: " + call.Name
		return
	}
	if len(call.Args) == 0 {
		call.Args = json.RawMessage("null")
	}
	value, err := handler(ctx, call.Args)
	if err != nil {
		reply.Error = err.Error()
		if reply.Error == "" {
			reply.Error = "handler failed"
		}
		var bridge *BridgeError
		if errors.As(err, &bridge) {
			reply.ErrorName = bridge.Name
			reply.Code = bridge.Code
		}
		if errors.Is(err, ErrCanceled) {
			reply.ErrorName = "AbortError"
			reply.Code = "canceled"
		}
		return
	}
	if _, err = json.Marshal(value); err != nil {
		reply.Error = "result is not JSON encodable"
		return
	}
	reply.Value = value
	return
}

// validateAppID 检查应用标识及目录字符限制
// 入参: appID 应用标识
// 返回: error 校验错误
func validateAppID(appID string) error {
	if len(appID) == 0 || len(appID) > 128 || appID[0] < 'a' || appID[0] > 'z' || strings.HasSuffix(appID, ".") {
		return errors.New("invalid application ID")
	}
	for _, c := range appID {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return errors.New("invalid application ID")
		}
	}
	return nil
}

// validate 补全默认尺寸并检查约束
// 入参: o 宿主选项
// 返回: error 校验错误
func validate(o *Options) error {
	if o.AppID != "" {
		if err := validateAppID(o.AppID); err != nil {
			return err
		}
	}
	for _, value := range []string{o.Title, o.IconPath, o.EntryPath, o.LibraryPath, o.UserDataDir} {
		if strings.ContainsRune(value, 0) {
			return errors.New("option contains NUL")
		}
	}
	if len(o.Icon) > 0 && o.IconPath != "" {
		return errors.New("icon data and path are mutually exclusive")
	}
	if len(o.Icon) > 16<<20 {
		return errors.New("icon exceeds 16 MiB")
	}
	o.Icon = append([]byte(nil), o.Icon...)
	for _, value := range []int{o.Width, o.Height, o.MinWidth, o.MinHeight, o.MaxWidth, o.MaxHeight} {
		if value > 32767 {
			return errors.New("window dimension exceeds 32767")
		}
	}
	if o.Width == 0 {
		o.Width = 1024
	}
	if o.Height == 0 {
		o.Height = 768
	}
	if o.Width < 1 || o.Height < 1 || o.MinWidth < 0 || o.MinHeight < 0 || o.MaxWidth < 0 || o.MaxHeight < 0 {
		return errors.New("invalid window dimensions")
	}
	if o.MaxWidth > 0 && o.MinWidth > o.MaxWidth || o.MaxHeight > 0 && o.MinHeight > o.MaxHeight {
		return errors.New("invalid size constraints")
	}
	if o.MinWidth > o.Width {
		o.Width = o.MinWidth
	}
	if o.MinHeight > o.Height {
		o.Height = o.MinHeight
	}
	if o.MaxWidth > 0 && o.Width > o.MaxWidth {
		o.Width = o.MaxWidth
	}
	if o.MaxHeight > 0 && o.Height > o.MaxHeight {
		o.Height = o.MaxHeight
	}
	if o.Handler == nil {
		if o.FS != nil {
			o.Handler = http.FileServer(http.FS(o.FS))
		} else {
			o.Handler = http.NotFoundHandler()
		}
	}
	bindings := make(map[string]HandlerFunc, len(o.Bindings))
	for k, v := range o.Bindings {
		if k == "" || v == nil {
			return errors.New("invalid binding")
		}
		bindings[k] = v
	}
	o.Bindings = bindings
	return nil
}
