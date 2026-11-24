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

//go:build (windows && amd64) || (linux && !android && (amd64 || arm64) && !guigo_gtk3)

package guigo

// localURL 判断请求是否属于宿主来源
// 入参: raw 资源地址
// 返回: bool 是否属于宿主来源
func localURL(raw string) bool {
	return localResource(resourceOrigin, raw)
}

// resourceURL 构造宿主资源地址
// 入参: path 资源路径
// 返回: string 宿主资源地址
func resourceURL(path string) string {
	return resourceAddress(resourceOrigin, path)
}
