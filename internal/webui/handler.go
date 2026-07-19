package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// 构建产物与服务端一起嵌入，部署时只需要运行单个程序。
//
//go:embed all:dist
var embedded embed.FS

// NewHandler 创建支持前端路由回退和静态缓存策略的处理器。
func NewHandler() (http.Handler, error) {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServerFS(assets)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestedPath := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		serveIndex := requestedPath == "." || requestedPath == ""
		if !serveIndex {
			info, statErr := fs.Stat(assets, requestedPath)
			if statErr != nil {
				acceptsHTML := strings.Contains(request.Header.Get("Accept"), "text/html")
				serveIndex = acceptsHTML && path.Ext(requestedPath) == ""
				if !serveIndex {
					http.NotFound(response, request)
					return
				}
			} else if info.IsDir() {
				serveIndex = true
			}
		}
		if serveIndex || requestedPath == "index.html" {
			response.Header().Set("Cache-Control", "no-cache")
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(response, request, "index.html", time.Time{}, bytes.NewReader(indexHTML))
			return
		}
		if requestedPath == "sw.js" || requestedPath == "manifest.webmanifest" {
			response.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(requestedPath, "assets/") {
			response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		clone := request.Clone(request.Context())
		clone.URL.Path = "/" + requestedPath
		fileServer.ServeHTTP(response, clone)
	}), nil
}
