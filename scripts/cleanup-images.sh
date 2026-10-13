#!/usr/bin/env bash
# 清理 soulfound-erp 镜像，只保留最近 5 次构建
# 用法：先 gh auth refresh -h github.com -s delete:packages  再跑本脚本
set -u
PKG="user/packages/container/soulfound-erp/versions"
gh api "$PKG?per_page=100" --paginate \
  --jq '.[] | "\(.created_at) \(.id) \([.metadata.container.tags[]?] | join(","))"' > /tmp/vers.txt
CUTOFF=$(grep -v " $" /tmp/vers.txt | sort -r | sed -n '5p' | cut -d' ' -f1)
echo "保留最近 5 次构建（cutoff=$CUTOFF），其余删除"
DEL=$(awk -v c="$CUTOFF" '$1 < c {print $2}' /tmp/vers.txt)
echo "共 $(echo "$DEL" | wc -l | tr -d ' ') 个待删"
ok=0; fail=0
for id in $DEL; do
  if gh api -X DELETE "$PKG/$id" >/dev/null 2>&1; then ok=$((ok+1)); else fail=$((fail+1)); fi
  [ $((ok % 50)) -eq 0 ] && [ $ok -gt 0 ] && echo "  已删 $ok"
done
echo "完成：成功 $ok，失败 $fail"
gh api "$PKG?per_page=100" --paginate --jq 'length' | paste -sd+ - | bc | xargs echo "剩余版本数:"
