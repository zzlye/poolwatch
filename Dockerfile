# syntax=docker/dockerfile:1.7

# 构建前端静态资源
FROM node:24-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

# 构建服务端并嵌入前端资源
FROM golang:1.26.1-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./internal/webui/dist
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/poolwatch ./cmd/server

# 最终镜像只保留运行所需文件，并使用非特权用户
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 poolwatch \
    && adduser -S -D -H -u 10001 -G poolwatch poolwatch \
    && mkdir -p /data \
    && chown poolwatch:poolwatch /data
COPY --from=go-builder /out/poolwatch /usr/local/bin/poolwatch
USER poolwatch:poolwatch
WORKDIR /app
ENV DATA_DIR=/data \
    LISTEN_ADDRESS=:8080 \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/poolwatch"]
