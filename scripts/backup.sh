#!/bin/sh
set -eu

# 备份期间暂停应用，保证数据库主文件与日志文件处于一致状态。
project_dir="${PROJECT_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
backup_dir="${1:-$project_dir/backups}"
compose_file="$project_dir/docker-compose.yml"
timestamp="$(date '+%Y%m%d-%H%M%S')"
archive_name="poolwatch-$timestamp.tar.gz"

mkdir -p "$backup_dir"
backup_dir="$(CDPATH= cd -- "$backup_dir" && pwd)"
umask 077

container_id="$(docker compose --project-directory "$project_dir" --env-file "$project_dir/.env" -f "$compose_file" ps -q app)"
if [ -z "$container_id" ]; then
    echo "未找到正在运行的 app 容器。" >&2
    exit 1
fi

volume_name="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$container_id")"
if [ -z "$volume_name" ]; then
    echo "未找到挂载到 /data 的数据卷。" >&2
    exit 1
fi

# 首次备份可能需要辅助镜像，先准备好再进入停机窗口。
docker image inspect alpine:3.22 >/dev/null 2>&1 || docker pull alpine:3.22 >/dev/null

restart_app() {
    docker compose --project-directory "$project_dir" --env-file "$project_dir/.env" -f "$compose_file" start app >/dev/null 2>&1 || true
}

# 收到中断或终止信号时先退出，再由退出陷阱恢复应用，避免应用恢复后脚本仍继续归档。
stop_backup() {
    exit 130
}
trap restart_app EXIT
trap stop_backup INT TERM

docker compose --project-directory "$project_dir" --env-file "$project_dir/.env" -f "$compose_file" stop app
docker run --rm \
    --read-only \
    --volume "$volume_name:/source:ro" \
    --volume "$backup_dir:/backup" \
    alpine:3.22 \
    tar -czf "/backup/$archive_name" -C /source .
(cd "$backup_dir" && sha256sum "$archive_name" > "$archive_name.sha256")

restart_app
trap - EXIT INT TERM
echo "备份已生成：$backup_dir/$archive_name"
