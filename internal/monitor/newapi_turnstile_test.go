package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// 验证浏览器验证只约束重新登录，不影响已授权的只读余额查询。
func TestNewAPITurnstileAllowsAuthorizedReadAfterRestart(t *testing.T) {
	for _, mode := range []string{"cookie", "token"} {
		t.Run(mode, func(t *testing.T) {
			var logins atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"turnstile_check": true, "quota_per_unit": 100}})
			})
			mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
				logins.Add(1)
				w.WriteHeader(http.StatusForbidden)
			})
			mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
				valid := r.Header.Get("New-Api-User") == "42"
				if mode == "cookie" {
					valid = valid && r.Header.Get("Cookie") == "session=authorized"
				} else {
					valid = valid && r.Header.Get("Authorization") == "management-token"
				}
				if !valid {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 42, "quota": 500, "status": 1}})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			credential := Credential{UserID: "42"}
			if mode == "cookie" {
				credential.Cookie = "session=authorized"
			} else {
				credential.AccessToken = "management-token"
			}
			target := TargetConfig{ID: "authorized", Kind: TargetKindNewAPI, BaseURL: server.URL, AllowPrivateNetwork: true, Credential: credential}
			// 每次使用全新适配器，模拟服务重启后从加密存储恢复同一有效凭据。
			for range 2 {
				adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
				snapshot, err := adapter.Check(context.Background(), target)
				if err != nil {
					t.Fatalf("已授权凭据应可读取额度：%v", err)
				}
				assertMetric(t, snapshot, MetricWalletBalance, "5", "USD")
			}
			if logins.Load() != 0 {
				t.Fatalf("只读检测不应重新发起密码登录，次数=%d", logins.Load())
			}
		})
	}
}

// 人工完成网页登录后，先验证会话再接管检测，不消费或重复利用验证码。
func TestNewAPITurnstileRecoversWithVerifiedBrowserSession(t *testing.T) {
	var logins atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"turnstile_check": true, "quota_per_unit": 100}})
	})
	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) { logins.Add(1); w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=authorized" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 42, "quota": 500, "status": 1}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	target := TargetConfig{ID: "recover", Kind: TargetKindNewAPI, BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{Username: "demo", Password: "test-password"}}
	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	if _, err := adapter.Check(context.Background(), target); !IsAuthFailure(err) {
		t.Fatalf("密码方式应要求网页授权：%v", err)
	}
	target.Credential = Credential{Cookie: "session=authorized"}
	credential, err := adapter.VerifyBrowserCredential(context.Background(), target)
	if err != nil {
		t.Fatalf("验证已登录浏览器会话失败：%v", err)
	}
	if credential.UserID != "42" {
		t.Fatalf("应从已验证会话读取用户 ID")
	}
	target.Credential = credential
	snapshot, err := adapter.Check(context.Background(), target)
	if err != nil {
		t.Fatalf("接管会话后应恢复检测：%v", err)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "5", "USD")
	// 会话过期必须要求重新授权，不静默退回密码登录或把失败当成零余额。
	target.Credential.Cookie = "session=expired"
	if _, err := adapter.Check(context.Background(), target); !IsAuthFailure(err) {
		t.Fatalf("过期会话应报告授权失效：%v", err)
	}
	if logins.Load() != 0 {
		t.Fatalf("恢复流程不应发送密码登录请求")
	}
}
