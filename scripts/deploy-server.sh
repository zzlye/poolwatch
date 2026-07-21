#!/usr/bin/env bash
set -euo pipefail

new_image="${1:?缺少新镜像地址}"
expected_revision="${2:?缺少预期提交编号}"
app_dir="/opt/poolwatch"
backup_dir="$app_dir/backups"
data_dir="/var/lib/docker/volumes/poolwatch_app-data/_data"
app_port="18081"

cd "$app_dir"
grep -q '^POOLWATCH_IMAGE=' .env
old_image="$(grep '^POOLWATCH_IMAGE=' .env | head -n 1 | cut -d= -f2-)"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
staging="$backup_dir/.staging-$timestamp"
archive="$backup_dir/poolwatch-data-$timestamp.tar.gz"

install -d -m 700 "$backup_dir" "$staging"
cleanup() {
  rm -rf -- "$staging"
}
trap cleanup EXIT

# 使用 SQLite 在线备份接口生成一致性副本，避免直接复制正在写入的 WAL 文件。
python3 - "$data_dir/poolwatch.db" "$staging/poolwatch.db" <<'PY'
import sqlite3
import sys

source_path, backup_path = sys.argv[1:3]
source = sqlite3.connect(f"file:{source_path}?mode=ro", uri=True)
destination = sqlite3.connect(backup_path)
try:
    source.backup(destination)
    result = destination.execute("PRAGMA integrity_check").fetchone()[0]
    if result != "ok":
        raise SystemExit(f"备份数据库完整性检查失败：{result}")
finally:
    destination.close()
    source.close()
PY

cp -p .env "$staging/.env"
cp -p docker-compose.yml "$staging/docker-compose.yml"
printf '%s\n' "$old_image" > "$staging/image-before.txt"
chmod 600 "$staging/.env" "$staging/poolwatch.db" "$staging/image-before.txt"
tar -C "$staging" -czf "$archive" .
chmod 600 "$archive"
sha256sum "$archive" > "$archive.sha256"
chmod 600 "$archive.sha256"

rollback() {
  echo "新版本验证失败，正在恢复上一镜像" >&2
  cp -p "$staging/.env" .env
  docker compose up -d --no-deps app >/dev/null
}

sed -i "s#^POOLWATCH_IMAGE=.*#POOLWATCH_IMAGE=$new_image#" .env
docker compose pull app
if ! docker compose up -d --no-deps app; then
  rollback
  exit 1
fi

healthy=false
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 "http://127.0.0.1:$app_port/healthz" >/dev/null; then
    healthy=true
    break
  fi
  sleep 2
done
if [ "$healthy" != true ]; then
  docker compose logs --tail=100 app >&2 || true
  rollback
  exit 1
fi

container_id="$(docker compose ps -q app)"
actual_revision="$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$container_id")"
if [ "$actual_revision" != "$expected_revision" ]; then
  echo "镜像提交编号不符合预期：$actual_revision" >&2
  rollback
  exit 1
fi

# 验证实时数据库、额度字段和新增接口均已就绪。
python3 - "$data_dir/poolwatch.db" <<'PY'
import sqlite3
import sys

path = sys.argv[1]
connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
try:
    integrity = connection.execute("PRAGMA integrity_check").fetchone()[0]
    if integrity != "ok":
        raise SystemExit(f"实时数据库完整性检查失败：{integrity}")
    columns = {row[1] for row in connection.execute("PRAGMA table_info(chat_accounts)")}
    required = {"quota_state", "quota_windows_json", "subscription_expires_at"}
    missing = sorted(required - columns)
    if missing:
        raise SystemExit("数据库迁移缺少字段：" + ", ".join(missing))
    counts = {}
    for table in ("admins", "targets", "alerts", "push_subscriptions"):
        counts[table] = connection.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
    print("数据计数：" + ", ".join(f"{key}={value}" for key, value in counts.items()))
finally:
    connection.close()
PY

route_status="$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' -X POST \
  "http://127.0.0.1:$app_port/api/targets/test/accounts/quota/refresh" \
  -H 'Content-Type: application/json' --data '{"accountIds":[]}')"
if [ "$route_status" != "401" ]; then
  echo "账号额度刷新接口未生效，状态码：$route_status" >&2
  rollback
  exit 1
fi

asset="$(curl -fsS --max-time 5 "http://127.0.0.1:$app_port/" | grep -o 'assets/index-[A-Za-z0-9_-]*\.js' | head -n 1)"
cat > DEPLOYMENT.txt <<EOF
号池监控服务器部署记录
访问地址：https://jiance.zzlye.xyz
容器镜像：$new_image
提交编号：$actual_revision
应用监听：127.0.0.1:$app_port
反向代理：宿主机 Caddy
数据卷：poolwatch_app-data
部署时间：$(date '+%Y-%m-%d %H:%M:%S %Z')
前端资源：$asset
最近备份：$archive
EOF
chmod 644 DEPLOYMENT.txt

echo "部署完成"
echo "备份：$archive"
echo "提交：$actual_revision"
echo "资源：$asset"
docker compose ps
