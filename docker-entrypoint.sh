#!/bin/sh
# 启动脚本：以 root 修正数据目录属主，然后降权到 erp 用户运行应用。
# 这样即使用户把宿主机目录直接挂载进 /data（属主可能是 root 或任意 UID），
# 也不会出现「数据库无法写入」的问题。
set -e

DATA_DIR="${ERP_DATA_DIR:-/data}"

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$DATA_DIR" "$DATA_DIR/backups"
    # 仅当属主不是 erp 时才做递归 chown，避免每次启动都遍历大量文件
    OWNER="$(stat -c '%u' "$DATA_DIR" 2>/dev/null || echo 0)"
    if [ "$OWNER" != "1000" ]; then
        echo "[entrypoint] 修正数据目录属主: $DATA_DIR"
        chown -R erp:erp "$DATA_DIR" || true
    fi
    exec su-exec erp:erp "$0" "$@"
fi

echo "[entrypoint] 以用户 $(id -un)($(id -u)) 启动，数据目录: $DATA_DIR"
exec "$@"
