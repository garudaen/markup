WAILS   := $(shell command -v wails 2>/dev/null || echo $(HOME)/go/bin/wails)
APP     := build/bin/markup.app
# 版本号取最新 git tag（无 tag 时为 dev），架构按当前机器
VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
ARCH    := $(shell uname -m | sed 's/x86_64/amd64/')

.PHONY: all help dev build dmg run clean deps bindings frontend-build build-windows installer-windows

all: build

## 显示本帮助信息
help:
	@awk '/^## /{desc=substr($$0,4); getline line; sub(/:.*/, "", line); printf "  make \033[36m%-18s\033[0m %s\n", line, desc}' $(MAKEFILE_LIST)

## 开发模式（前端热更新，Go 改动自动重启）
dev:
	$(WAILS) dev

## 打包 macOS 应用，产物在 build/bin/markup.app
build:
	$(WAILS) build

## 打包 macOS DMG 安装镜像，产物如 build/bin/markup-0.0.1-darwin-arm64.dmg
dmg: build
	rm -rf build/dmg && mkdir -p build/dmg
	cp -R $(APP) build/dmg/
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname markup -srcfolder build/dmg -ov -format UDZO build/bin/markup-$(VERSION)-darwin-$(ARCH).dmg
	rm -rf build/dmg

## 打开已打包的应用
run:
	open $(APP)

## 交叉编译 Windows 可执行文件（实验性）
build-windows:
	$(WAILS) build -platform windows/amd64

## 交叉编译 Windows NSIS 安装包（需要 makensis），产物如 build/bin/markup-0.0.1-windows-amd64-installer.exe
installer-windows:
	$(WAILS) build -platform windows/amd64 -nsis
	mv build/bin/markup-amd64-installer.exe build/bin/markup-$(VERSION)-windows-amd64-installer.exe

## 安装前端依赖
deps:
	npm install --prefix frontend

## 重新生成 Wails 前后端绑定（改了 app.go 的绑定方法后用）
bindings:
	$(WAILS) generate module

## 仅构建前端（类型检查 + vite build）
frontend-build:
	npm run build --prefix frontend

## 清理构建产物
clean:
	rm -rf build/bin frontend/dist
