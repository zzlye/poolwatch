package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDetect优先识别ChatGPT2API(t *testing.T) {
	var statusRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{
			"status":   "ok",
			"accounts": map[string]any{"active": 3, "total_quota": 20},
		})
	})
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		statusRequests.Add(1)
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 500000, "quota_display_type": "USD"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	kind, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, true)
	if err != nil || kind != TargetKindChatGPT2API {
		t.Fatalf("应优先识别 chatgpt2api，kind=%s err=%v", kind, err)
	}
	if statusRequests.Load() != 0 {
		t.Fatalf("命中 chatgpt2api 后不应继续探测 New API：%d", statusRequests.Load())
	}
}

func TestDetect识别NewAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{
			"success": true,
			"data":    map[string]any{"quota_per_unit": "500000", "quota_display_type": "TOKENS"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	kind, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, true)
	if err != nil || kind != TargetKindNewAPI {
		t.Fatalf("应识别 New API，kind=%s err=%v", kind, err)
	}
}

func TestDetect通过公开状态识别Sub2API(t *testing.T) {
	var meRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})
	mux.HandleFunc("/setup/status", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"needs_setup": false, "step": "completed"}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		meRequests.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	kind, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, true)
	if err != nil || kind != TargetKindSub2API {
		t.Fatalf("应识别 Sub2API，kind=%s err=%v", kind, err)
	}
	if meRequests.Load() != 0 {
		t.Fatalf("公开状态已确认时不应请求受保护端点：%d", meRequests.Load())
	}
}

func TestDetect通过认证端点回退识别Sub2API(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})
	mux.HandleFunc("/setup/status", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		writeTestJSON(writer, map[string]any{"code": http.StatusUnauthorized, "message": "unauthorized"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	kind, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, true)
	if err != nil || kind != TargetKindSub2API {
		t.Fatalf("应通过认证端点识别 Sub2API，kind=%s err=%v", kind, err)
	}
}

func TestDetect不会把普通健康页识别为Sub2API(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	kind, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, true)
	if err == nil || kind != "" || !strings.Contains(err.Error(), "无法识别") {
		t.Fatalf("普通站点不应被误判，kind=%s err=%v", kind, err)
	}
}

func TestDetect复用SSRF策略(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("SSRF 校验失败时不应发出请求")
	}))
	defer server.Close()

	_, err := NewRegistry(HTTPOptions{}).Detect(context.Background(), server.URL, false)
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("默认应拒绝回环地址：%v", err)
	}
}
