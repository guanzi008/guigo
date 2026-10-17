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
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// streamReader 在所属COM线程读取原生IStream
type streamReader struct {
	object *comObject
}

// resourceWriter 将响应写入临时文件而不累积整个响应到内存
type resourceWriter struct {
	header http.Header
	status int
	spool  *responseSpool
	err    error
}

// Read 读取原生流并传播HRESULT
// 入参: b 输出缓冲区
// 返回: int 读取字节数, error 读取错误
func (s streamReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	var read uint32
	hr := com(s.object, 3, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&read)))
	runtime.KeepAlive(b)
	if err := hresult(hr); err != nil {
		return int(read), err
	}
	if read == 0 {
		return 0, io.EOF
	}
	return int(read), nil
}

// Header 返回响应头
// 返回: http.Header 响应头
func (w *resourceWriter) Header() http.Header {
	return w.header
}

// WriteHeader 设置响应状态码
// 入参: status 响应状态码
func (w *resourceWriter) WriteHeader(status int) {
	if w.status == 0 {
		if status < 200 || status > 599 {
			status = 500
		}
		w.status = status
	}
}

// Write 写入响应内容
// 入参: b 响应数据
// 返回: int 写入字节数, error 写入错误
func (w *resourceWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		if w.header.Get("Content-Type") == "" {
			w.header.Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(200)
	}
	n, err := w.spool.Write(b)
	if err != nil {
		w.err = err
	}
	return n, err
}

// resource 拦截请求并将业务处理移交后台协程
// 入参: args 资源请求参数
func (n *nativeWindow) resource(args *comObject) {
	var request *comObject
	if err := hresult(com(args, 3, uintptr(unsafe.Pointer(&request)))); err != nil {
		n.fail(err)
		return
	}
	defer release(request)
	uri, err := comString(request, 3)
	if err != nil {
		n.fail(err)
		return
	}
	method, err := comString(request, 5)
	if err != nil {
		n.fail(err)
		return
	}
	var content *comObject
	if err := hresult(com(request, 7, uintptr(unsafe.Pointer(&content)))); err != nil {
		n.fail(err)
		return
	}
	var body *os.File
	if content != nil {
		body, err = newTemporaryFile("guigo-request-")
		if err == nil {
			_, err = io.Copy(body, streamReader{content})
		}
		release(content)
		if err != nil {
			if body != nil {
				body.Close()
			}
			n.fail(err)
			return
		}
		_, err = body.Seek(0, 0)
		if err != nil {
			body.Close()
			n.fail(err)
			return
		}
	}
	var input io.Reader
	if body != nil {
		input = body
	}
	requestContext := n.pageCtx
	if requestContext == nil {
		requestContext = n.ctx
	}
	req, err := http.NewRequestWithContext(requestContext, method, uri, input)
	if err != nil {
		if body != nil {
			body.Close()
		}
		n.fail(err)
		return
	}
	req.RequestURI = req.URL.RequestURI()
	if body != nil {
		info, err := body.Stat()
		if err != nil {
			body.Close()
			n.fail(err)
			return
		}
		req.ContentLength = info.Size()
	}
	var headers, iterator *comObject
	if hresult(com(request, 9, uintptr(unsafe.Pointer(&headers)))) == nil && headers != nil {
		if hresult(com(headers, 8, uintptr(unsafe.Pointer(&iterator)))) == nil && iterator != nil {
			var has int32
			com(iterator, 4, uintptr(unsafe.Pointer(&has)))
			for has != 0 {
				var key, value *uint16
				if hresult(com(iterator, 3, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value)))) != nil {
					break
				}
				req.Header.Add(utf16String(key), utf16String(value))
				ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(key)))
				ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(value)))
				com(iterator, 5, uintptr(unsafe.Pointer(&has)))
			}
			release(iterator)
		}
		release(headers)
	}
	var deferral *comObject
	if err := hresult(com(args, 6, uintptr(unsafe.Pointer(&deferral)))); err != nil {
		if body != nil {
			body.Close()
		}
		n.fail(err)
		return
	}
	com(args, 1)
	if n.resources == nil {
		n.resources = make(map[*comObject]*comObject)
	}
	n.resources[args] = deferral
	go func() {
		if body != nil {
			defer body.Close()
		}
		if localURL(uri) && n.options.ResourceHandler != nil {
			var resource *Resource
			func() {
				defer func() {
					if recover() != nil {
						err = fmt.Errorf("resource handler panic")
					}
				}()
				resource, err = n.options.ResourceHandler(req)
			}()
			if err != nil {
				if resource != nil && resource.Body != nil {
					resource.Body.Close()
				}
				_ = n.enqueue(func() {
					n.respond(args, nil, 500, "")
					n.finishResource(args)
				})
				return
			}
			if resource != nil {
				if resource.Body == nil {
					_ = n.enqueue(func() {
						n.respond(args, nil, 500, "")
						n.finishResource(args)
					})
					return
				}
				size, e := resource.Body.Seek(0, io.SeekEnd)
				if e != nil || size < 0 {
					resource.Body.Close()
					_ = n.enqueue(func() {
						n.respond(args, nil, 500, "")
						n.finishResource(args)
					})
					return
				}
				direct := &responseSpool{file: resource.Body, size: size, complete: true, refs: 1}
				direct.cond = sync.NewCond(&direct.mu)
				defer direct.drop()
				n.deliver(args, direct, resource.StatusCode, resource.Header.Clone())
				return
			}
		}
		spool, err := newResponseSpool(req.Context())
		if err != nil {
			_ = n.enqueue(func() {
				n.finishResource(args)
				n.fail(err)
			})
			return
		}
		defer spool.drop()
		w := &resourceWriter{header: make(http.Header), spool: spool}
		func() {
			defer func() {
				if recover() != nil {
					w.err = fmt.Errorf("resource handler panic")
				}
			}()
			if !localURL(uri) {
				http.Error(w, "Forbidden", 403)
			} else {
				n.options.Handler.ServeHTTP(w, req)
			}
		}()
		if w.err != nil {
			spool.finish(w.err)
			_ = n.enqueue(func() {
				n.respond(args, nil, 500, "")
				n.finishResource(args)
			})
			return
		}
		w.WriteHeader(200)
		spool.finish(w.err)
		n.deliver(args, spool, w.status, w.header.Clone())
	}()
}

// deliver 把完整可读资源交给原生流，不在内存复制整个响应
// 入参: args 请求参数, spool 响应存储, status 状态码, headers 响应头
func (n *nativeWindow) deliver(args *comObject, spool *responseSpool, status int, headers http.Header) {
	stream, err := spool.native(0)
	if err != nil {
		_ = n.enqueue(func() {
			n.finishResource(args)
			n.fail(err)
		})
		return
	}
	if status == 0 {
		status = 200
	}
	if err := n.enqueueCleanup(func() {
		defer release(stream)
		if _, ok := n.resources[args]; !ok {
			return
		}
		defer n.finishResource(args)
		var header strings.Builder
		for key, values := range headers {
			for _, value := range values {
				if !strings.ContainsAny(key+value, "\r\n\x00") {
					fmt.Fprintf(&header, "%s: %s\r\n", key, value)
				}
			}
		}
		n.respond(args, stream, status, header.String())
	}, func() { release(stream) }); err != nil {
		release(stream)
	}
}

// finishResource 完成原生异步资源请求
// 入参: args 请求参数
func (n *nativeWindow) finishResource(args *comObject) {
	if deferral, ok := n.resources[args]; ok {
		delete(n.resources, args)
		com(deferral, 3)
		release(deferral)
		release(args)
	}
}

// respond 创建WebView2资源响应
// 入参: args 请求参数, stream 响应流, status 状态码, headers 响应头文本
func (n *nativeWindow) respond(args, stream *comObject, status int, headers string) {
	if _, ok := n.resources[args]; !ok {
		return
	}
	var response *comObject
	if err := hresult(com(n.environment, 4, uintptr(unsafe.Pointer(stream)), uintptr(status), uintptr(unsafe.Pointer(wide(http.StatusText(status)))), uintptr(unsafe.Pointer(wide(headers))), uintptr(unsafe.Pointer(&response)))); err != nil {
		n.fail(err)
		return
	}
	defer release(response)
	if err := hresult(com(args, 5, uintptr(unsafe.Pointer(response)))); err != nil {
		n.fail(err)
	}
}
