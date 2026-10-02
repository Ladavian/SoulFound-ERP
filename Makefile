# 本地开发与运维快捷命令。Docker 部署不需要 Makefile。
#
# 注意：这里把 Go 的模块缓存放在工作区内的 .gopath/，方便在受限环境下工作；
# 你自己的机器上如果已有全局 GOPATH，可以直接删掉下面三行 export。

GOPATH_LOCAL := $(CURDIR)/.gopath
export GOPATH := $(GOPATH_LOCAL)
export GOMODCACHE := $(GOPATH_LOCAL)/pkg/mod
export GOCACHE := $(GOPATH_LOCAL)/build-cache

.PHONY: help run dev seed build test fmt vet backup reset-password \
        docker-build docker-up docker-https docker-down docker-logs clean

help: ## 显示所有可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

run: ## 本地启动（数据写到 ./data）
	ERP_DATA_DIR=$(CURDIR)/data go run ./cmd/erp

dev: ## 开发模式：改模板后刷新浏览器即可生效
	ERP_DEV=1 ERP_DATA_DIR=$(CURDIR)/data go run ./cmd/erp

seed: ## 带演示数据启动（第一次运行时才写入）
	ERP_SEED_DEMO=1 ERP_DATA_DIR=$(CURDIR)/data go run ./cmd/erp

build: ## 编译静态二进制到 bin/erp
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/erp ./cmd/erp

test: ## 运行全部测试（含库存与市集利润核算）
	go test ./...

fmt: ## 格式化代码
	gofmt -w ./cmd ./internal

vet: ## 静态检查
	go vet ./...

backup: ## 生成一次数据库备份
	go run ./cmd/erp backup

reset-password: ## 重置密码：make reset-password USER=admin
	go run ./cmd/erp reset-password $(USER)

docker-build: ## 构建镜像
	docker compose build

docker-up: ## 启动（HTTP，局域网直接用）
	docker compose up -d

docker-https: ## 启动并带上 Caddy 提供 HTTPS（PWA 需要）
	docker compose --profile https up -d

docker-down: ## 停止
	docker compose down

docker-logs: ## 查看应用日志
	docker compose logs -f erp

clean: ## 清理编译产物与缓存
	rm -rf bin
	go clean -cache -testcache
