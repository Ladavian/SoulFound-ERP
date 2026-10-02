# ---------- 构建阶段 ----------
#
# 用 --platform=$BUILDPLATFORM 让构建阶段始终跑在「构建机原生架构」上
# （GitHub 上就是 amd64），再靠 Go 的交叉编译产出 arm64 二进制。
# 本项目是纯 Go（SQLite 驱动不依赖 cgo），所以交叉编译毫无障碍，
# 这样能避免在 QEMU 模拟环境里编译 modernc.org/sqlite 那种超大包
# （模拟编译要十几分钟，原生交叉编译只要几十秒）。
ARG GO_VERSION=1.26
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

# 先只拷贝依赖清单，充分利用镜像层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION 由 CI 注入（形如 main-1f9e85e）；本地构建留空则用构建时间，
# 这样每次重编译都会得到不同的版本号——前端 css/js 的 ?v= 参数随之变化，
# 浏览器与 Service Worker 都能正确拿到新文件，不会继续用旧缓存。
ARG VERSION=
# BuildKit 会为每个目标平台注入 TARGETARCH（amd64 / arm64）
ARG TARGETARCH
RUN set -eux; \
    ver="${VERSION}"; \
    if [ -z "$ver" ]; then ver="dev-$(date -u +%Y%m%d-%H%M)"; fi; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    echo "构建版本: ${ver}  目标架构: linux/${target_arch}"; \
    CGO_ENABLED=0 GOOS=linux GOARCH="${target_arch}" go build \
        -trimpath \
        -ldflags="-s -w -X main.buildVersion=${ver}" \
        -o /out/erp ./cmd/erp; \
    if [ "${target_arch}" = "$(go env GOHOSTARCH)" ]; then \
        /out/erp version; \
    else \
        echo "已交叉编译 linux/${target_arch}（不在构建机上执行）"; \
    fi

# ---------- 运行阶段 ----------
FROM alpine:3.22

# ca-certificates/tzdata 保证证书与时区正确；su-exec 用于把数据目录属主修好后降权运行
RUN apk add --no-cache ca-certificates tzdata su-exec \
 && addgroup -g 1000 -S erp \
 && adduser -u 1000 -S -G erp -h /app erp \
 && mkdir -p /data/backups /app \
 && chown -R erp:erp /data /app

WORKDIR /app
COPY --from=builder /out/erp /app/erp
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 0755 /app/docker-entrypoint.sh

ENV ERP_DATA_DIR=/data \
    ERP_ADDR=:8123 \
    ERP_VERSION=1.0.0 \
    TZ=Asia/Shanghai

EXPOSE 8123
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO /dev/null http://127.0.0.1:8123/healthz || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/erp"]
