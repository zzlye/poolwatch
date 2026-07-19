package monitor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateTargetURL默认拒绝私网和元数据(t *testing.T) {
	tests := []string{
		"http://127.0.0.1/test",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/latest/meta-data",
	}
	for _, rawURL := range tests {
		if err := ValidateTargetURL(context.Background(), rawURL, false); err == nil {
			t.Fatalf("地址应被拒绝：%s", rawURL)
		}
	}
	if err := ValidateTargetURL(context.Background(), "http://127.0.0.1/test", true); err != nil {
		t.Fatalf("显式允许私网后应放行回环地址：%v", err)
	}
	if err := ValidateTargetURL(context.Background(), "http://169.254.169.254/latest/meta-data", true); err == nil {
		t.Fatal("云元数据地址即使允许私网也必须拒绝")
	}
	if err := ValidateTargetURL(context.Background(), "http://169.254.170.2/v2/credentials", true); err == nil {
		t.Fatal("链路本地容器凭据地址即使允许私网也必须拒绝")
	}
}

func TestValidateResolvedIPRejectsNonPublicAndReservedRanges(t *testing.T) {
	for _, rawIP := range []string{
		"0.0.0.1", "100.64.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "240.0.0.1", "2001:db8::1",
	} {
		address := net.ParseIP(rawIP)
		if address == nil {
			t.Fatalf("测试地址解析失败：%s", rawIP)
		}
		if err := validateResolvedIP(address, false); err == nil {
			t.Fatalf("默认策略应拒绝非公网或保留地址：%s", rawIP)
		}
		if err := validateResolvedIP(address, true); err == nil {
			t.Fatalf("允许私网后仍应拒绝保留地址：%s", rawIP)
		}
	}
}

func TestValidateResolvedIPAllowsPrivateOnlyWhenConfigured(t *testing.T) {
	privateAddress := net.ParseIP("10.0.0.1")
	if err := validateResolvedIP(privateAddress, false); err == nil {
		t.Fatal("默认策略应拒绝私网地址")
	}
	if err := validateResolvedIP(privateAddress, true); err != nil {
		t.Fatalf("显式允许私网后应允许私网地址：%v", err)
	}
	publicAddress := net.ParseIP("8.8.8.8")
	if err := validateResolvedIP(publicAddress, false); err != nil {
		t.Fatalf("默认策略应允许公网单播地址：%v", err)
	}
}

func TestSecureHTTPClient拒绝HTTPS降级重定向(t *testing.T) {
	session := newSecureHTTPClient(HTTPOptions{}).newSession(true)
	original, _ := http.NewRequest(http.MethodGet, "https://example.com:443/start", nil)
	downgraded, _ := http.NewRequest(http.MethodGet, "http://example.com:443/final", nil)
	err := session.client.CheckRedirect(downgraded, []*http.Request{original})
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("HTTPS 降级重定向应被拒绝：%v", err)
	}
}

func TestSecureHTTPClient拒绝跨主机重定向(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"value":"1"}`))
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	session := newSecureHTTPClient(HTTPOptions{}).newSession(true)
	var payload any
	err := session.doJSON(context.Background(), http.MethodGet, source.URL, nil, nil, &payload)
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("跨主机重定向应返回配置错误，实际为：%v", err)
	}
}

func TestSecureHTTPClient允许同主机重定向(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"value":"1"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	session := newSecureHTTPClient(HTTPOptions{}).newSession(true)
	var payload any
	if err := session.doJSON(context.Background(), http.MethodGet, server.URL+"/start", nil, nil, &payload); err != nil {
		t.Fatalf("同主机重定向应被允许：%v", err)
	}
}

func TestSecureHTTPClient限制响应大小(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"value":"` + strings.Repeat("x", int(defaultBodyLimit)) + `"}`))
	}))
	defer server.Close()

	session := newSecureHTTPClient(HTTPOptions{}).newSession(true)
	var payload any
	err := session.doJSON(context.Background(), http.MethodGet, server.URL, nil, nil, &payload)
	if err == nil || ErrorClassOf(err) != ErrorClassResponse {
		t.Fatalf("超大响应应返回响应错误，实际为：%v", err)
	}
}

func TestRegistry只对服务器错误最多重试两次(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := attempts.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if current < 3 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"error":"temporary"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"value":"1"}`))
	}))
	defer server.Close()

	registry := NewRegistry(HTTPOptions{})
	_, err := registry.Run(context.Background(), TargetConfig{
		Kind:                TargetKindCustom,
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Custom: CustomHTTPConfig{
			Method:  http.MethodGet,
			Metrics: []CustomMetricMapping{{Pointer: "/value"}},
		},
	})
	if err != nil {
		t.Fatalf("第三次请求成功后不应返回错误：%v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("重试次数不符合预期：%d", attempts.Load())
	}
}

func TestRegistry认证错误不重试(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid"}`))
	}))
	defer server.Close()

	registry := NewRegistry(HTTPOptions{})
	_, err := registry.Run(context.Background(), TargetConfig{
		Kind:                TargetKindCustom,
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Custom: CustomHTTPConfig{
			Method:  http.MethodGet,
			Metrics: []CustomMetricMapping{{Pointer: "/value"}},
		},
	})
	if !IsAuthFailure(err) {
		t.Fatalf("401 应分类为认证错误，实际为：%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("认证错误不应重试，实际请求次数：%d", attempts.Load())
	}
}

func TestRegistry退避期间尊重上下文取消(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	adapter := &cancelingTestAdapter{cancel: cancel}
	registry := NewRegistry(HTTPOptions{})
	registry.Register(adapter)

	_, err := registry.Run(ctx, TargetConfig{Kind: TargetKindCustom})
	if err == nil || ErrorClassOf(err) != ErrorClassNetwork {
		t.Fatalf("取消上下文后应返回网络错误：%v", err)
	}
	if adapter.attempts.Load() != 1 {
		t.Fatalf("取消后不应继续重试，实际次数：%d", adapter.attempts.Load())
	}
}

type cancelingTestAdapter struct {
	cancel   context.CancelFunc
	attempts atomic.Int32
}

func (adapter *cancelingTestAdapter) Kind() TargetKind {
	return TargetKindCustom
}

func (adapter *cancelingTestAdapter) Check(context.Context, TargetConfig) (Snapshot, error) {
	adapter.attempts.Add(1)
	adapter.cancel()
	return Snapshot{}, checkError(ErrorClassServer, "测试重试", "模拟服务器错误", http.StatusBadGateway, nil)
}
