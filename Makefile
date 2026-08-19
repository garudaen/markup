WAILS := $(shell command -v wails 2>/dev/null || echo $(HOME)/go/bin/wails)
APP   := build/bin/markup.app

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

## 打包 macOS DMG 安装镜像，产物在 build/bin/markup.dmg
dmg: build
	rm -rf build/dmg && mkdir -p build/dmg
	cp -R $(APP) build/dmg/
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname markup -srcfolder build/dmg -ov -format UDZO build/bin/markup.dmg
	rm -rf build/dmg

## 打开已打包的应用
run:
	open $(APP)

## 交叉编译 Windows 可执行文件（实验性）
build-windows:
	$(WAILS) build -platform windows/amd64

## 交叉编译 Windows NSIS 安装包（需要 makensis，brew install makensis）
installer-windows:
	$(WAILS) build -platform windows/amd64 -nsis

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
