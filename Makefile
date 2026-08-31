# ============================================================
# DockOrae-Agent — 构建 Makefile
#
# 常用目标:
#   make            构建当前平台二进制(默认 linux/当前 arch;本机直接跑)
#   make cross      交叉编译全部 Linux 架构 → dist/*.tar.gz(发版用)
#   make test       质量检查:go vet + go test(与 CI 一致)
#   make image      构建 docker 镜像(DOCKER_IMAGE 可覆盖)
#   make clean      清理构建产物
# ============================================================

GO        ?= go
BIN       := dockorae-agent
DIST_DIR  := dist

VERSION   ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo unknown)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# ldflags:注入 cmd.Version/Commit/BuildTime + binary.Version
LDFLAGS := -s -w \
	-X github.com/DockOrae/DockOrae-Agent/cmd.Version=$(VERSION) \
	-X github.com/DockOrae/DockOrae-Agent/cmd.Commit=$(GIT_COMMIT) \
	-X github.com/DockOrae/DockOrae-Agent/cmd.BuildTime=$(BUILD_TIME) \
	-X github.com/DockOrae/DockOrae-Agent/internal/binary.Version=$(VERSION)

# Linux 交叉编译架构
LINUX_SPECS := amd64: arm64: arm:7 arm:6

.DEFAULT_GOAL := build

.PHONY: build
build:
	@echo "==> 编译 $(BIN) (v$(VERSION), linux/$(shell go env GOARCH))"
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) .

.PHONY: cross
cross:
	@mkdir -p $(DIST_DIR)
	@for spec in $(LINUX_SPECS); do \
	  arch=$${spec%%:*}; arm=$${spec##*:}; \
	  if [ "$$arch" = "arm" ]; then \
	    goarch=arm; goarm=$$arm; suffix=armv$$arm; \
	  else \
	    goarch=$$arch; goarm=; suffix=$$arch; \
	  fi; \
	  echo "==> 交叉编译 linux/$$goarch"; \
	  rm -rf $(DIST_DIR)/$(BIN); \
	  mkdir -p $(DIST_DIR)/$(BIN); \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$goarch GOARM=$$goarm \
	    $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BIN)/$(BIN) . || exit 1; \
	  cp install.sh README.md $(DIST_DIR)/$(BIN)/ 2>/dev/null || true; \
	  tar -czf $(DIST_DIR)/$(BIN)-linux-$$suffix.tar.gz -C $(DIST_DIR) $(BIN) || exit 1; \
	  (cd $(DIST_DIR) && sha256sum $(BIN)-linux-$$suffix.tar.gz > $(BIN)-linux-$$suffix.tar.gz.sha256) || exit 1; \
	  rm -rf $(DIST_DIR)/$(BIN); \
	done
	@echo "✅ 交叉编译完成:"
	@ls -lh $(DIST_DIR)/*.tar.gz $(DIST_DIR)/*.sha256

.PHONY: image
image:
	docker build -t dockorae/dockorae-agent:$(VERSION) .

.PHONY: test
test: vet
	$(GO) test ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: clean
clean:
	rm -rf $(DIST_DIR)
	rm -f $(BIN)
	@echo "✅ 已清理"
