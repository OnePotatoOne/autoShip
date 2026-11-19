# autoShip 统一构建、前端内嵌、x86_64 Linux 跨平台编译与自动化测试 Makefile

SHELL := /bin/bash
BIN_DIR := $(CURDIR)/bin
SERVER_DIR := $(CURDIR)/server
WEB_DIR := $(CURDIR)/web
EMBED_DIST := $(SERVER_DIR)/internal/static/dist
VERSION ?= v0.1.0

.PHONY: all build build-web embed-web build-go build-linux build-linux-server build-linux-agents test vet clean

all: build

## 1. 构建管理后台前端（Angular 21.2.25 + ng-zorro-antd 21.3.3）
build-web:
	cd $(WEB_DIR) && if [ -f package-lock.json ]; then npm ci; else npm install; fi && npm run build

## 2. 将前端静态产物同步至 Go embed 目录以生成单文件可执行程序
embed-web: build-web
	rm -rf $(EMBED_DIST) && mkdir -p $(EMBED_DIST)
	@if [ -d "$(WEB_DIR)/dist/web/browser" ]; then \
		cp -R $(WEB_DIR)/dist/web/browser/* $(EMBED_DIST)/; \
	elif [ -d "$(WEB_DIR)/dist/browser" ]; then \
		cp -R $(WEB_DIR)/dist/browser/* $(EMBED_DIST)/; \
	else \
		cp -R $(WEB_DIR)/dist/* $(EMBED_DIST)/; \
	fi
	touch $(EMBED_DIST)/.gitkeep
	cp -f $(CURDIR)/docs/open-api-integration.md $(SERVER_DIR)/internal/static/open-api-integration.md

## 3. 编译当前宿主机全套 6 个可执行文件
build-go:
	mkdir -p $(BIN_DIR)
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-server ./cmd/autoship-server
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-dev-agent ./cmd/autoship-dev-agent
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-release ./cmd/autoship-release
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-bench ./cmd/autoship-bench
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-agent ./cmd/autoship-agent
	cd $(SERVER_DIR) && go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/autoship-apply ./cmd/autoship-apply

## 4. 一键构建本机全套（前端编译 -> 内嵌 -> 全套二进制）
build: embed-web build-go

## 5. 编译 x86_64 Linux (linux/amd64) 单文件内嵌前端服务端
build-linux-server: embed-web
	mkdir -p $(BIN_DIR)/linux-amd64
	cd $(SERVER_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/linux-amd64/autoship-server ./cmd/autoship-server

## 6. 编译 x86_64 Linux (linux/amd64) 纯静态更新 Agent 与 autoship-apply
build-linux-agents:
	mkdir -p $(BIN_DIR)/linux-amd64
	cd $(SERVER_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/linux-amd64/autoship-agent ./cmd/autoship-agent
	cd $(SERVER_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/linux-amd64/autoship-apply ./cmd/autoship-apply

## 7. 一键构建全部 x86_64 Linux 生产产物（Server + Agent + Apply）
build-linux: build-linux-server build-linux-agents

## 8. 运行全套静态检查与单元测试
vet:
	cd $(SERVER_DIR) && go vet ./...

test:
	cd $(SERVER_DIR) && go test ./...

clean:
	rm -rf $(BIN_DIR)
