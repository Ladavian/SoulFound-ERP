#!/bin/sh
# 数据库备份脚本（可放进 crontab 定时执行）
#
# 用法：
#   ./scripts/backup.sh                # 备份到 ./data/backups，保留最近 30 份
#   BACKUP_DIR=/mnt/nas/erp ./scripts/backup.sh
#   KEEP=90 ./scripts/backup.sh
#
# crontab 示例（每天凌晨 3 点）：
#   0 3 * * * cd /opt/icewine-erp && ./scripts/backup.sh >> /var/log/erp-backup.log 2>&1

set -eu

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BACKUP_DIR="${BACKUP_DIR:-$PROJECT_DIR/data/backups}"
KEEP="${KEEP:-30}"

mkdir -p "$BACKUP_DIR"

echo "[$(date '+%F %T')] 开始备份 -> $BACKUP_DIR"

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
   && [ -n "$(docker compose ps -q erp 2>/dev/null || true)" ]; then
    # 容器在跑：用容器内的运维命令做 VACUUM INTO 一致性快照
    docker compose exec -T erp /app/erp backup /data/backups
else
    # 容器没跑：直接调用本地二进制
    if [ -x "$PROJECT_DIR/bin/erp" ]; then
        (cd "$PROJECT_DIR" && ERP_DATA_DIR="$PROJECT_DIR/data" ./bin/erp backup "$BACKUP_DIR")
    else
        echo "错误：找不到运行中的容器，也没有编译好的 bin/erp" >&2
        exit 1
    fi
fi

# 只保留最近 KEEP 份
if [ "$KEEP" -gt 0 ]; then
    ls -1t "$BACKUP_DIR"/erp-backup-*.sqlite3 2>/dev/null \
        | tail -n "+$((KEEP + 1))" \
        | while read -r old; do
            echo "清理旧备份: $old"
            rm -f "$old"
        done
fi

echo "[$(date '+%F %T')] 备份完成，当前共 $(ls -1 "$BACKUP_DIR"/erp-backup-*.sqlite3 2>/dev/null | wc -l | tr -d ' ') 份"
