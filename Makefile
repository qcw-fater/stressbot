# ── 变量 ──────────────────────────────────────────────────

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X main.Version=$(VERSION)
GO_FLAGS := -trimpath -ldflags "$(LDFLAGS)"

BIN_DIR := bin
CMD_WEB := cmd/web

# ── 默认目标 ──────────────────────────────────────────────

.PHONY: all
all: frontend build

# ── 前端 ──────────────────────────────────────────────────

.PHONY: frontend
frontend:
	cd $(CMD_WEB) && npm install && npm run build

# ── Go 编译 ──────────────────────────────────────────────

.PHONY: build
build: build-linux build-windows

.PHONY: build-linux
build-linux: ## 构建 Linux amd64
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/agent ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/admin ./cmd/admin

.PHONY: build-windows
build-windows: ## 构建 Windows amd64
	@mkdir -p $(BIN_DIR)
	GOOS=windows GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/agent.exe ./cmd/agent
	GOOS=windows GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/admin.exe ./cmd/admin

.PHONY: build-current
build-current: ## 构建当前平台
	@mkdir -p $(BIN_DIR)
	go build $(GO_FLAGS) -o $(BIN_DIR)/agent ./cmd/agent
	go build $(GO_FLAGS) -o $(BIN_DIR)/admin ./cmd/admin

# ── 开发 ──────────────────────────────────────────────────

.PHONY: dev-agent
dev-agent: ## 开发运行 agent
	go run ./cmd/agent -config conf/config.json

.PHONY: dev-admin
dev-admin: ## 开发运行 admin
	go run ./cmd/admin -config conf/admin-config.json

.PHONY: dev-web
dev-web: ## 开发运行前端
	cd $(CMD_WEB) && npm run dev

# ── 检查 ──────────────────────────────────────────────────

.PHONY: check
check: ## 编译检查（不输出二进制）
	go build ./...
	cd $(CMD_WEB) && npx tsc --noEmit

.PHONY: test
test: ## 运行前端测试
	cd $(CMD_WEB) && npm run test

# ── 清理 ──────────────────────────────────────────────────

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR)
	rm -rf $(CMD_WEB)/dist

# ── 信息 ──────────────────────────────────────────────────

.PHONY: version
version: ## 显示版本号
	@echo $(VERSION)
