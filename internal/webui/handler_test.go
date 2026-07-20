package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesShellWithoutRedirect(t *testing.T) {
	handler, err := NewHandler()
	if err != nil {
		t.Fatalf("创建前端处理器失败: %v", err)
	}
	for _, target := range []string{"/", "/targets/example"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "<!doctype html>") {
			t.Fatalf("应用壳响应不正确: %s, %d, %q", target, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestHandlerDoesNotMaskMissingStaticAsset(t *testing.T) {
	handler, err := NewHandler()
	if err != nil {
		t.Fatalf("创建前端处理器失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("缺失静态资源应返回 404，实际 %d", response.Code)
	}
}

func TestHandlerServesBrowserHelperPackage(t *testing.T) {
	handler, err := NewHandler()
	if err != nil {
		t.Fatalf("创建前端处理器失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/downloads/poolwatch-browser-helper-v1.0.0.zip", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	// ZIP 文件必须作为静态资源返回，不能被前端路由回退替换成页面壳。
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "PK") {
		t.Fatalf("浏览器助手安装包响应不正确: %d, %q", response.Code, response.Body.String())
	}
}
