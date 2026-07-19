#!/bin/sh
set -eu

# 默认在当前目录生成部署环境文件，已有文件不会被覆盖。
output_path="${1:-.env}"
if [ -e "$output_path" ]; then
    echo "目标文件已存在，未覆盖：$output_path" >&2
    exit 1
fi

umask 077
encryption_key="$(openssl rand -base64 32 | tr -d '\n')"
setup_token="$(openssl rand -hex 24)"

{
    printf '%s\n' "APP_ENCRYPTION_KEY=$encryption_key"
    printf '%s\n' "SETUP_TOKEN=$setup_token"
    printf '%s\n' "PUBLIC_BASE_URL=https://monitor.example.com"
    printf '%s\n' "APP_PORT=8080"
    printf '%s\n' "ALLOW_PRIVATE_TARGETS=false"
    printf '%s\n' "TZ=Asia/Shanghai"
    printf '%s\n' "DOMAIN=monitor.example.com"
    printf '%s\n' "ACME_EMAIL=admin@example.com"
} > "$output_path"

echo "已生成 $output_path，请修改域名和邮箱后再启动。"
