# urMail.cn — developer entry points.
#
# The whole toolchain is driven from here so that a new contributor has a
# single place to look. Every target is safe to run repeatedly.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO          ?= go
GOFLAGS     ?=
NPM         ?= npm
DIST        := bin
WEB         := web
EMBED_DIR   := internal/web/dist
# The version comes from the VERSION file so that a checkout of the old
# Mail-in-a-Box tags cannot leak into the package metadata.
VERSION     ?= $(shell cat VERSION 2>/dev/null || echo 0.0.0-dev)
LDFLAGS     := -X main.version=$(VERSION)
PKG_VERSION := $(VERSION)

# 国内网络下可直接使用；海外环境可覆盖为 proxy.golang.org
export GOPROXY ?= https://goproxy.cn,direct
export npm_config_registry ?= https://registry.npmmirror.com

.PHONY: help
help: ## 显示可用命令
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: frontend build-go ## 构建前端并打包进 Go 二进制

.PHONY: build-go
build-go: ## 只构建 Go 二进制（前端资源需已存在）
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/urmaild ./cmd/urmaild
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/urmaild-agent ./cmd/urmaild-agent
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/urmailctl ./cmd/urmailctl
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/urmail-archive ./cmd/urmail-archive

.PHONY: frontend
frontend: ## 构建前端并拷贝到 embed 目录
	cd $(WEB) && $(NPM) ci && $(NPM) run build
	rm -rf $(EMBED_DIR)
	cp -R $(WEB)/dist $(EMBED_DIR)
	@# keep the tracked placeholder so `go:embed` keeps working
	touch $(EMBED_DIR)/.gitkeep

# ---------------------------------------------------------------------------
# Test and lint
# ---------------------------------------------------------------------------

.PHONY: test
test: test-go test-web ## 跑全部测试

.PHONY: test-go
test-go: ## 跑 Go 测试
	$(GO) test ./...

.PHONY: test-web
test-web: ## 跑前端测试
	cd $(WEB) && $(NPM) test -- --run

.PHONY: test-integration
test-integration: ## 跑需要 PostgreSQL 的集成测试（需 URMAIL_TEST_DATABASE_URL）
	$(GO) test -race ./internal/db/... ./internal/api/middleware/... ./internal/api/handlers/...

.PHONY: lint
lint: lint-go lint-web ## 跑全部静态检查

.PHONY: lint-go
lint-go: ## golangci-lint
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else "$$(go env GOPATH)/bin/golangci-lint" run; fi

.PHONY: lint-web
lint-web: ## eslint + prettier
	cd $(WEB) && $(NPM) run lint

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## 格式化 Go 代码
	$(GO) fmt ./...

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

.PHONY: migrate
migrate: ## 应用数据库迁移
	$(GO) run ./cmd/urmailctl migrate up

.PHONY: migrate-down
migrate-down: ## 回滚最近一次数据库迁移
	$(GO) run ./cmd/urmailctl migrate down

.PHONY: migrate-version
migrate-version: ## 打印当前 schema 版本
	$(GO) run ./cmd/urmailctl migrate version

.PHONY: sqlc
sqlc: ## 生成 sqlc 代码
	sqlc generate

.PHONY: sqlc-check
sqlc-check: ## 校验 sqlc 生成物与仓库一致
	sqlc generate
	git diff --exit-code -- internal/store

.PHONY: i18n-check
i18n-check: ## 校验所有 locale 的 key 集合一致
	$(GO) run ./cmd/i18ncheck

# ---------------------------------------------------------------------------
# Local environment
# ---------------------------------------------------------------------------

.PHONY: dev
dev: ## 启动本地全栈（PG + 服务 + 前端）
	bash scripts/dev.sh up

.PHONY: dev-down
dev-down: ## 停止本地全栈
	bash scripts/dev.sh down

# ---------------------------------------------------------------------------
# Package
# ---------------------------------------------------------------------------

.PHONY: package
package: build ## 构建 deb 包
	bash packaging/deb/build.sh $(PKG_VERSION)

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(DIST) $(WEB)/dist $(EMBED_DIR)
