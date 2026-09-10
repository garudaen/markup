package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Windows 双击关联文件时路径走命令行参数（shell open command），缓存为
	// pending，前端在启动恢复后经 GetPendingOpenFile 拉取（同 macOS 冷启动）。
	if path := markdownFileArg(os.Args[1:], ""); path != "" {
		app.onFileOpen(path)
	}

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "markup",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: app.localFileMiddleware,
		},
		// 原生文件拖放：回调经 runtime.OnFileDrop 拿到绝对路径数组；
		// 同时禁用 WebView 自身的 drop（避免与编辑器内 drop 处理双重触发）
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 1},
		// 单实例：第二个进程把参数（含双击的文件）转发给已运行实例后退出，
		// 运行中再双击 md 文件不会新开窗口。macOS 上 LaunchServices 本就复用
		// 进程，此锁只影响 open -n 这类强制新实例的场景。
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "b3d9f0a2-7c4e-4f1a-9e2b-8d0c6a5f4e31",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		// 非 nil 的 Mac 选项：绕开 wails#5519（Mac 为 nil 时绿色缩放按钮被禁用）。
		// OnFileOpen：Finder 双击 / "打开方式" / Dock 拖放的文件路径回调。
		Mac: &mac.Options{
			OnFileOpen: app.onFileOpen,
		},
		OnStartup: app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
