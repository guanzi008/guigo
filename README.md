# GUIGo [![PkgGoDev](https://pkg.go.dev/badge/github.com/xiaoqidun/guigo)](https://pkg.go.dev/github.com/xiaoqidun/guigo)
轻量、基于系统 WebView 的 Go 语言图形界面库

# 安装指南
```shell
go get -u github.com/xiaoqidun/guigo
```

# 应用案例
[OFDGo](https://github.com/xiaoqidun/ofdgo)：基于 GUIGo 构建桌面版 OFD、PDF 阅读与编辑工具

# 系统依赖
## Windows
系统未内置时需安装 [Microsoft Edge WebView2](https://developer.microsoft.com/zh-cn/microsoft-edge/webview2)

## Linux（Debian 13，Ubuntu 24.04/26.04）
```shell
apt-get install libwebkitgtk-6.0-4
```

## UOS 20 龙芯
我给这个 fork 补了龙芯上的 GTK 3 / WebKit2GTK 4.0 后端。用龙芯社区 Go 构建；构建时需要 `gcc`、`pkg-config`、`libgtk-3-dev`，运行时需要 `libgtk-3-0` 和 `libwebkit2gtk-4.0-37`。文件对话框直接调用 GTK 3。

模块路径仍是 `github.com/xiaoqidun/guigo`。OFDGo 的 `go.mod` 固定引用这个 fork。

# 授权协议
本项目使用 [Apache License 2.0](https://github.com/xiaoqidun/guigo/blob/main/LICENSE) 授权协议
