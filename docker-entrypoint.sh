#!/bin/sh
# 启动脚本：以 root 修正数据目录属主，然后降权到 erp 用户运行应用。
#
# 为什么不能只看目录属主：bind mount 的属主映射经常不可靠
# （宿主机 uid 与容器内 uid 对不上），而且子目录可能与顶层不一致——
# 之前只检查 $DATA_DIR 的属主，结果 $DATA_DIR 属主是对的、
# $DATA_DIR/backups 属主是 root，递归 chown 被跳过，手动备份就报
# "unable to open database file"。所以这里改成**实际写一次测试文件**。
set -e

DATA_DIR="${ERP_DATA_DIR:-/data}"
BACKUP_DIR="${ERP_BACKUP_DIR:-$DATA_DIR/backups}"

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$DATA_DIR" "$BACKUP_DIR"

    # 用 erp 用户的身份真的写一下：比看属主可靠得多
    writable() {
        su-exec erp:erp sh -c "touch '$1/.write-test' 2>/dev/null && rm -f '$1/.write-test'" 2>/dev/null
    }

    if ! writable "$DATA_DIR" || ! writable "$BACKUP_DIR"; then
        echo "[entrypoint] 数据目录不可写，正在修正属主: $DATA_DIR $BACKUP_DIR"
        chown -R erp:erp "$DATA_DIR" 2>/dev/null || true
        [ "$BACKUP_DIR" != "$DATA_DIR" ] && chown -R erp:erp "$BACKUP_DIR" 2>/dev/null || true
    fi

    if ! writable "$DATA_DIR"; then
        echo "[entrypoint] 警告：$DATA_DIR 仍然不可写，数据库可能无法保存。"
        echo "[entrypoint] 请检查宿主机的挂载目录属主，例如：sudo chown -R 1000:1000 ./data"
    elif ! writable "$BACKUP_DIR"; then
        echo "[entrypoint] 警告：备份目录 $BACKUP_DIR 不可写，手动备份会失败。"
        echo "[entrypoint] 请检查宿主机目录属主，例如：sudo chown -R 1000:1000 ./data"
    fi

    exec su-exec erp:erp "$0" "$@"
fi

echo "[entrypoint] 以用户 $(id -un)($(id -u)) 启动，数据目录: $DATA_DIR"
exec "$@"
