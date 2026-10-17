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

// resourceOrigin 为WebView2拦截的内部资源来源
const resourceOrigin = "https://guigo.local"

// bridgeScript 在文档创建时安装Promise调用桥与事件订阅
const bridgeScript = `(() => {
	if (location.origin !== "` + resourceOrigin + `" || window !== window.top) return;
	let serial = 0;
	const pending = new Map();
	const listeners = new Map();
	chrome.webview.addEventListener("message", event => {
		const message = event.data;
		if (!message || typeof message !== "object") return;
		if (message.type === "reply") {
			const call = pending.get(message.id);
			if (!call) return;
			pending.delete(message.id);
			if (message.error) {
				const error = new Error(message.error);
				error.name = message.errorName || "Error";
				if (message.code) error.code = message.code;
				call.reject(error);
			} else {
				call.resolve(message.value);
			}
		}
		if (message.type === "event") {
			for (const listener of [...(listeners.get(message.name) || [])]) {
				try {
					listener(message.value);
				} catch (error) {
					reportError(error);
				}
			}
		}
	});
	Object.defineProperty(window, "guigo", {
		value: Object.freeze({
			call(name, args = null) {
				return new Promise((resolve, reject) => {
					if (typeof name !== "string" || !name) throw new TypeError("method must be a non-empty string");
					const id = String(++serial);
					pending.set(id, {resolve, reject});
					try {
						chrome.webview.postMessage({type:"call", id, name, args});
					} catch (error) {
						pending.delete(id);
						reject(error);
					}
				});
			},
			on(name, listener) {
				if (typeof name !== "string" || !name) throw new TypeError("event must be a non-empty string");
				if (typeof listener !== "function") throw new TypeError("listener must be a function");
				let set = listeners.get(name);
				if (!set) listeners.set(name, set = new Set());
				set.add(listener);
				return () => {
					set.delete(listener);
					if (!set.size) listeners.delete(name);
				};
			}
		}),
		writable: false,
		configurable: false,
	});
	addEventListener("pagehide", () => {
		for (const call of pending.values()) call.reject(new Error("document closed"));
		pending.clear();
	});
	addEventListener("dragover", event => {
		if (event.dataTransfer && Array.from(event.dataTransfer.types).includes("Files")) event.preventDefault();
	});
	addEventListener("drop", event => {
		const files = Array.from(event.dataTransfer?.files || []);
		if (files.length && chrome.webview.postMessageWithAdditionalObjects) {
			event.preventDefault();
			chrome.webview.postMessageWithAdditionalObjects({type:"drop"}, files);
		}
	});
})();`

// localURL 判断请求是否属于WebView2宿主来源
// 入参: raw 资源地址
// 返回: bool 是否属于宿主来源
func localURL(raw string) bool {
	return localResource(resourceOrigin, raw)
}

// resourceURL 构造WebView2宿主资源地址
// 入参: path 资源路径
// 返回: string 宿主资源地址
func resourceURL(path string) string {
	return resourceAddress(resourceOrigin, path)
}
