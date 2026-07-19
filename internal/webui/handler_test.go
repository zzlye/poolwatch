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
