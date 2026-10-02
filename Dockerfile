# ---------- 构建阶段 ----------
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

# 先只拷贝依赖清单，充分利用镜像层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 不使用 cgo：modernc.org/sqlite 是纯 Go 实现，因此可以编译出完全静态的二进制
ARG VERSION=1.0.0
ARG TARGETARCH
RUN set -eux; \
    if [ -n "${TARGETARCH:-}" ]; then export GOARCH="${TARGETARCH}"; fi; \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.buildVersion=${VERSION}" \
        -o /out/erp ./cmd/erp; \
    /out/erp version

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
    ERP_ADDR=:8000 \
    ERP_VERSION=1.0.0 \
    TZ=Asia/Shanghai

EXPOSE 8000
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO /dev/null http://127.0.0.1:8000/healthz || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/erp"]
