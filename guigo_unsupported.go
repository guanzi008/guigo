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

//go:build (!windows && !linux) || !amd64

package guigo

import "context"

// nativeWindow 提供未适配平台的宿主占位类型
type nativeWindow struct{}

// resourceURL 返回未适配平台的空资源地址
// 入参: path 资源路径
// 返回: string 空地址
func resourceURL(path string) string {
	return ""
}

// openURL 返回平台不支持错误
// 入参: raw 外部地址
// 返回: error 平台错误
func openURL(raw string) error {
	return ErrUnsupportedPlatform
}

// run 返回平台不支持错误
// 入参: parent 运行上下文, o 宿主选项
// 返回: error 平台错误
func run(parent context.Context, o Options) error {
	return ErrUnsupportedPlatform
}

// emit 返回平台不支持错误
// 入参: raw 消息文本
// 返回: error 平台错误
func (n *nativeWindow) emit(raw string) error {
	return ErrUnsupportedPlatform
}

// close 返回平台不支持错误
// 返回: error 平台错误
func (n *nativeWindow) close() error {
	return ErrUnsupportedPlatform
}

// setTitle 返回平台不支持错误
// 入参: title 窗口标题
// 返回: error 平台错误
func (n *nativeWindow) setTitle(title string) error {
	return ErrUnsupportedPlatform
}

// setSize 返回平台不支持错误
// 入参: w 客户区宽度, h 客户区高度
// 返回: error 平台错误
func (n *nativeWindow) setSize(w, h int) error {
	return ErrUnsupportedPlatform
}

// dialog 返回平台不支持错误
// 入参: o 对话框选项, mode 对话框类型
// 返回: string 空路径, error 平台错误
func (n *nativeWindow) dialog(o DialogOptions, mode int) (string, error) {
	return "", ErrUnsupportedPlatform
}

// dialogs 返回平台不支持错误
// 入参: o 对话框选项, mode 对话框类型
// 返回: []string 空路径列表, error 平台错误
func (n *nativeWindow) dialogs(o DialogOptions, mode int) ([]string, error) {
	return nil, ErrUnsupportedPlatform
}
