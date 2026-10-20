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
	"net/http"
	"net/url"
	"strings"
)

// fileHandler 标识由宿主选项自动创建的静态文件处理器
type fileHandler struct{ http.Handler }

// localResource 判断资源是否属于指定宿主来源
// 入参: origin 宿主来源, raw 资源地址
// 返回: bool 是否属于宿主来源
func localResource(origin, raw string) bool {
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil && u.Opaque == ""
}

// externalURL 限制外部浏览器可接受的地址
// 入参: raw 外部地址
// 返回: bool 是否允许打开
func externalURL(raw string) bool {
	if strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

// resourceAddress 将资源路径限制在指定宿主来源
// 入参: origin 宿主来源, path 资源路径
// 返回: string 宿主资源地址
func resourceAddress(origin, path string) string {
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil {
		return ""
	}
	u, err := url.Parse(path)
	if err != nil {
		return origin + "/"
	}
	u.Scheme = base.Scheme
	u.Host = base.Host
	u.User = nil
	u.Opaque = ""
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}
