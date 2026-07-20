package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestNewAPI令牌认证和额度换算(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{
			"success": true,
			"data": map[string]any{
				"quota_per_unit":     "100",
				"quota_display_type": "CNY",
				"usd_exchange_rate":  "7",
			},
		})
	})
	mux.HandleFunc("/api/user/self", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "manage-token" || request.Header.Get("New-Api-User") != "42" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota": "250", "status": 1}})
	})
	mux.HandleFunc("/api/subscription/self", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{
			"success": true,
			"data": map[string]any{
				"subscriptions": []any{
					map[string]any{"subscription": map[string]any{"status": "active", "amount_total": "200", "amount_used": "50"}},
					map[string]any{"subscription": map[string]any{"status": "expired", "amount_total": "999", "amount_used": "0"}},
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "new-1",
		Kind:                TargetKindNewAPI,
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{AccessToken: "manage-token", UserID: "42"},
		Thresholds:          map[MetricKey]decimal.Decimal{MetricSubscriptionBalance: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatalf("检测 New API 失败：%v", err)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "17.5", "CNY")
	assertMetric(t, snapshot, MetricSubscriptionBalance, "10.5", "CNY")
}

func TestNewAPI密码登录支持两步验证Cookie(t *testing.T) {
	var loginRequests atomic.Int32
	var verifyRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 500000, "turnstile_check": false}})
	})
	mux.HandleFunc("/api/user/login", func(writer http.ResponseWriter, request *http.Request) {
		loginRequests.Add(1)
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["username"] != "demo" || body["password"] != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: "pending", Path: "/"})
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"require_2fa": true}})
	})
	mux.HandleFunc("/api/user/login/2fa", func(writer http.ResponseWriter, request *http.Request) {
		verifyRequests.Add(1)
		cookie, _ := request.Cookie("session")
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if cookie == nil || cookie.Value != "pending" || body["code"] != "123456" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: "ready", Path: "/"})
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"id": 7}})
	})
	mux.HandleFunc("/api/user/self", func(writer http.ResponseWriter, request *http.Request) {
		cookie, _ := request.Cookie("session")
		if cookie == nil || cookie.Value != "ready" || request.Header.Get("New-Api-User") != "7" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota": 500000, "status": 1}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "new-login",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{Username: "demo", Password: "secret", TOTPCode: "123456"},
	})
	if err != nil {
		t.Fatalf("两步登录检测失败：%v", err)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "1", "USD")
	if _, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "new-login",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{Username: "demo", Password: "secret", TOTPCode: "123456"},
	}); err != nil {
		t.Fatalf("复用登录会话检测失败：%v", err)
	}
	if loginRequests.Load() != 1 || verifyRequests.Load() != 1 {
		t.Fatalf("后续检测不应重复登录，login=%d verify=%d", loginRequests.Load(), verifyRequests.Load())
	}
}

func TestNewAPI遇到浏览器验证要求令牌(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"turnstile_check": true}})
	}))
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{Username: "demo", Password: "secret"},
	})
	if !IsAuthFailure(err) || !strings.Contains(err.Error(), "访问令牌") {
		t.Fatalf("应明确提示改用访问令牌：%v", err)
	}
}

func TestNewAPI网页登录会话可自动识别用户ID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user/self", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "session=oauth" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"id": 52, "quota": 100}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	credential, err := adapter.VerifyBrowserCredential(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{Cookie: "session=oauth"},
	})
	if err != nil {
		t.Fatalf("校验 New API 网页登录失败：%v", err)
	}
	if credential.Cookie != "session=oauth" || credential.UserID != "52" {
		t.Fatalf("网页登录凭据规范化结果不正确：%#v", credential)
	}
}

func TestSub2API网页登录令牌会读取当前用户(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer oauth-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "9.5", "status": "active"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	credential, err := adapter.VerifyBrowserCredential(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "oauth-access", RefreshToken: "oauth-refresh"},
	})
	if err != nil {
		t.Fatalf("校验 Sub2API 网页登录失败：%v", err)
	}
	if credential.AccessToken != "oauth-access" || credential.RefreshToken != "oauth-refresh" {
		t.Fatalf("Sub2API 网页登录凭据规范化结果不正确：%#v", credential)
	}
}

func TestNewAPI缓存会话失效后重新登录(t *testing.T) {
	var loginRequests atomic.Int32
	var selfRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 100}})
	})
	mux.HandleFunc("/api/user/login", func(writer http.ResponseWriter, request *http.Request) {
		number := loginRequests.Add(1)
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: fmt.Sprintf("session-%d", number), Path: "/"})
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"id": 8}})
	})
	mux.HandleFunc("/api/user/self", func(writer http.ResponseWriter, request *http.Request) {
		current := selfRequests.Add(1)
		cookie, _ := request.Cookie("session")
		if cookie == nil {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if current == 2 && cookie.Value == "session-1" {
			writeTestJSON(writer, map[string]any{"success": false, "message": "expired"})
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"quota": 100, "status": 1}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	target := TargetConfig{
		ID:                  "new-expired",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{Username: "demo", Password: "secret"},
	}
	if _, err := adapter.Check(context.Background(), target); err != nil {
		t.Fatalf("首次检测失败：%v", err)
	}
	if _, err := adapter.Check(context.Background(), target); err != nil {
		t.Fatalf("会话失效后重新登录失败：%v", err)
	}
	if loginRequests.Load() != 2 || selfRequests.Load() != 3 {
		t.Fatalf("重新登录次数不符合预期，login=%d self=%d", loginRequests.Load(), selfRequests.Load())
	}
}

func TestSub2API支持两步登录(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"requires_2fa": true, "temp_token": "temporary"}})
	})
	mux.HandleFunc("/api/v1/auth/login/2fa", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["temp_token"] != "temporary" || body["totp_code"] != "654321" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"access_token": "access-ok", "refresh_token": "refresh-ok", "expires_in": 3600}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-ok" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "12.34", "status": "active"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "sub-login",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{Email: "demo@example.com", Password: "secret", TOTPCode: "654321"},
	})
	if err != nil {
		t.Fatalf("Sub2API 两步登录失败：%v", err)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "12.34", "USD")
}

func TestSub2API访问令牌失效后自动刷新(t *testing.T) {
	var meRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		meRequests.Add(1)
		if request.Header.Get("Authorization") == "Bearer expired" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Header.Get("Authorization") != "Bearer renewed" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": 9.5, "status": "active"}})
	})
	mux.HandleFunc("/api/v1/auth/refresh", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["refresh_token"] != "refresh" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"access_token": "renewed", "refresh_token": "rotated", "expires_in": 7200}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "sub-refresh",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{AccessToken: "expired", RefreshToken: "refresh"},
	})
	if err != nil {
		t.Fatalf("自动刷新失败：%v", err)
	}
	if meRequests.Load() != 2 {
		t.Fatalf("用户信息接口调用次数不符合预期：%d", meRequests.Load())
	}
	if snapshot.CredentialUpdate == nil || snapshot.CredentialUpdate.RefreshToken != "rotated" || snapshot.CredentialUpdate.AccessToken != "renewed" {
		t.Fatalf("轮换后的令牌未返回给调度层持久化：%#v", snapshot.CredentialUpdate)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "9.5", "USD")
}

func TestSub2API缓存过期后优先使用轮换刷新令牌(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/refresh", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["refresh_token"] != "rotated-refresh" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"access_token": "new-access", "refresh_token": "next-refresh", "expires_in": 3600}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer new-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "8", "status": "active"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	target := TargetConfig{
		ID: "sub-rotated", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "expired-access", RefreshToken: "stale-refresh"},
	}
	adapter.tokens[sub2APICacheKey(target)] = sub2APIToken{
		accessToken: "expired-access", refreshToken: "rotated-refresh", validUntil: time.Now().Add(-time.Minute),
	}
	snapshot, err := adapter.Check(context.Background(), target)
	if err != nil {
		t.Fatalf("缓存轮换令牌续期失败：%v", err)
	}
	if snapshot.CredentialUpdate == nil || snapshot.CredentialUpdate.RefreshToken != "next-refresh" {
		t.Fatalf("新的轮换令牌未返回：%#v", snapshot.CredentialUpdate)
	}
}

func TestChatGPT2API聚合并严格脱敏(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Query().Get("format") != "json" || request.Header.Get("Authorization") != "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writeTestJSON(writer, map[string]any{
			"healthy": true,
			"accounts": map[string]any{
				"total": 4, "active": 2, "limited": 1, "abnormal": 1, "disabled": 0,
				"total_quota": 37, "total_success": 18, "total_fail": 3,
			},
		})
	})
	mux.HandleFunc("/api/accounts", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer admin-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"items": []any{
			map[string]any{
				// 这里使用上游实际的整数额度和计数类型，避免测试掩盖类型不兼容。
				"email": "safe@example.com", "type": "Plus", "status": "正常", "quota": 20, "restore_at": "2026-07-20T00:00:00Z",
				"success": 8, "fail": 1, "image_inflight": 2,
				"access_token": "access-secret", "refresh_token": "refresh-secret", "password": "password-secret", "id_token": "id-secret",
			},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newChatGPT2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID:                  "chat-1",
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Credential:          Credential{AdminKey: "admin-secret"},
	})
	if err != nil {
		t.Fatalf("检测 chatgpt2api 失败：%v", err)
	}
	assertMetric(t, snapshot, MetricImageQuota, "37", "次")
	assertMetric(t, snapshot, MetricAccountTotal, "4", "个")
	assertMetric(t, snapshot, MetricHealthyAccounts, "2", "个")
	assertMetric(t, snapshot, MetricAccountSuccess, "18", "次")
	assertMetric(t, snapshot, MetricAccountFail, "3", "次")
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Email != "safe@example.com" || snapshot.Accounts[0].Quota.String() != "20" {
		t.Fatalf("脱敏账号明细不符合预期：%+v", snapshot.Accounts)
	}
	if account := snapshot.Accounts[0]; account.Type != "Plus" || account.Status != "正常" || account.RestoreAt != "2026-07-20T00:00:00Z" || account.Success != 8 || account.Fail != 1 || account.ImageInflight != 2 {
		t.Fatalf("账号状态、恢复时间或统计字段不符合预期：%+v", account)
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化快照失败：%v", err)
	}
	for _, secret := range []string{"admin-secret", "access-secret", "refresh-secret", "password-secret", "id-secret"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("快照泄漏了秘密字段 %q：%s", secret, serialized)
		}
	}
}

func TestCLIProxyAPI只读聚合并严格白名单(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v0/management/auth-files" ||
			request.Header.Get("Authorization") != "Bearer management-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"files": []any{
			map[string]any{
				"auth_index": "stable-auth-index", "id": "raw-active-id", "label": "主账号", "provider": "codex", "email": "active@example.com",
				"account_type": "plus", "status": "active", "success": 12, "failed": 1,
				"account": "ignored-account", "path": "ignored-path", "name": "ignored-name", "id_token": "ignored-token",
			},
			map[string]any{
				"id": "raw-limited-id", "provider": "claude", "account_type": "api_key", "status": "active", "unavailable": true,
				"next_retry_after": "2099-07-20T00:00:00Z", "status_message": "Bearer raw-status-secret",
			},
			map[string]any{"id": "raw-error-id", "provider": "gemini", "status": "error"},
			map[string]any{"id": "raw-disabled-id", "provider": "qwen", "status": "active", "disabled": true},
			map[string]any{"id": "raw-pending-id", "provider": "kimi", "status": "pending"},
			map[string]any{"id": "raw-unknown-id", "provider": "custom", "status": "mystery"},
		}})
	}))
	defer server.Close()

	healthyThreshold := decimal.NewFromInt(1)
	errorThreshold := decimal.NewFromInt(1)
	snapshot, err := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{})).Check(context.Background(), TargetConfig{
		ID: "cli-1", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AdminKey: "management-secret"},
		Thresholds: map[MetricKey]decimal.Decimal{
			MetricHealthyAccounts: healthyThreshold,
			MetricErrorAccounts:   errorThreshold,
		},
		ThresholdComparisons: map[MetricKey]ThresholdComparison{
			MetricHealthyAccounts: ThresholdComparisonLTE,
			MetricErrorAccounts:   ThresholdComparisonGTE,
		},
	})
	if err != nil {
		t.Fatalf("检测 CLIProxyAPI 失败：%v", err)
	}
	assertMetric(t, snapshot, MetricAccountTotal, "6", "个")
	assertMetric(t, snapshot, MetricHealthyAccounts, "1", "个")
	assertMetric(t, snapshot, MetricLimitedAccounts, "2", "个")
	assertMetric(t, snapshot, MetricErrorAccounts, "2", "个")
	assertMetric(t, snapshot, MetricDisabledAccounts, "1", "个")
	if len(snapshot.Accounts) != 6 || snapshot.Accounts[0].DisplayName != "主账号" || snapshot.Accounts[0].Type != "plus" {
		t.Fatalf("CLIProxyAPI 账号白名单字段不正确：%+v", snapshot.Accounts)
	}
	if snapshot.Accounts[0].ExternalID != "stable-auth-index" {
		t.Fatalf("账号标识应优先使用 auth_index：%+v", snapshot.Accounts[0])
	}
	if snapshot.Accounts[1].Status != string(TargetStatusWarning) || snapshot.Accounts[1].RecoveryAt != "2099-07-20T00:00:00Z" ||
		snapshot.Accounts[1].Type != "api_key" || snapshot.Accounts[1].StatusText != "限流或冷却中" {
		t.Fatalf("限流账号分类或恢复时间不正确：%+v", snapshot.Accounts[1])
	}
	if snapshot.Accounts[2].Status != string(TargetStatusError) || snapshot.Accounts[3].Status != string(TargetStatusDisabled) ||
		snapshot.Accounts[4].Status != string(TargetStatusWarning) || snapshot.Accounts[5].Status != string(TargetStatusUnknown) {
		t.Fatalf("CLIProxyAPI 状态分类不正确：%+v", snapshot.Accounts)
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化 CLIProxyAPI 快照失败：%v", err)
	}
	for _, forbidden := range []string{"stable-auth-index", "raw-active-id", "ignored-account", "ignored-path", "ignored-name", "ignored-token", "raw-status-secret", "management-secret"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("CLIProxyAPI 快照包含未允许字段 %q：%s", forbidden, serialized)
		}
	}
	for _, metric := range snapshot.Metrics {
		if metric.Key == MetricErrorAccounts && metric.Comparison != ThresholdComparisonGTE {
			t.Fatalf("异常账号指标比较方向不正确：%+v", metric)
		}
	}
}

func TestCLIProxyAPI要求管理密钥(t *testing.T) {
	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.Check(context.Background(), TargetConfig{BaseURL: "https://example.com"})
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("缺少管理密钥应返回配置错误：%v", err)
	}
}

func TestCustomHTTP支持四种认证方式(t *testing.T) {
	tests := []struct {
		name       string
		mode       AuthMode
		credential Credential
		verify     func(*http.Request) bool
	}{
		{name: "无认证", mode: AuthModeNone, verify: func(request *http.Request) bool { return request.Header.Get("Authorization") == "" }},
		{name: "Bearer", mode: AuthModeBearer, credential: Credential{BearerToken: "bearer-secret"}, verify: func(request *http.Request) bool { return request.Header.Get("Authorization") == "Bearer bearer-secret" }},
		{name: "Basic", mode: AuthModeBasic, credential: Credential{Username: "demo", Password: "secret"}, verify: func(request *http.Request) bool {
			username, password, ok := request.BasicAuth()
			return ok && username == "demo" && password == "secret"
		}},
		{name: "请求头", mode: AuthModeHeader, credential: Credential{Headers: map[string]string{"X-API-Key": "header-secret"}}, verify: func(request *http.Request) bool { return request.Header.Get("X-API-Key") == "header-secret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if !test.verify(request) {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				writeTestJSON(writer, map[string]any{"balance": "3.25"})
			}))
			defer server.Close()

			adapter := newCustomHTTPAdapter(newSecureHTTPClient(HTTPOptions{}))
			snapshot, err := adapter.Check(context.Background(), TargetConfig{
				BaseURL:             server.URL,
				AllowPrivateNetwork: true,
				Credential:          test.credential,
				Custom: CustomHTTPConfig{
					Method:   http.MethodGet,
					AuthMode: test.mode,
					Metrics:  []CustomMetricMapping{{Key: MetricWalletBalance, Pointer: "/balance", Unit: "USD"}},
				},
			})
			if err != nil {
				t.Fatalf("自定义认证检测失败：%v", err)
			}
			assertMetric(t, snapshot, MetricWalletBalance, "3.25", "USD")
		})
	}
}

func TestCustomHTTP支持显式POST数字字符串和状态字段(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := make(map[string]any)
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["probe"] != "balance" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writeTestJSON(writer, map[string]any{"data": map[string]any{"balance": "12.3400", "states": []any{map[string]any{"state": "OK"}}}})
	}))
	defer server.Close()

	adapter := newCustomHTTPAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Custom: CustomHTTPConfig{
			Method:        http.MethodPost,
			ConfirmPOST:   true,
			Body:          json.RawMessage(`{"probe":"balance"}`),
			Metrics:       []CustomMetricMapping{{Key: MetricWalletBalance, Label: "余额", Pointer: "/data/balance", Unit: "USD"}},
			StatusPointer: "/data/states/0/state",
			HealthyValues: []string{"ok"},
		},
	})
	if err != nil {
		t.Fatalf("自定义 POST 检测失败：%v", err)
	}
	if snapshot.Status != TargetStatusHealthy {
		t.Fatalf("自定义状态判断错误：%s", snapshot.Status)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "12.34", "USD")
}

func TestCustomHTTP未确认POST时拒绝请求(t *testing.T) {
	adapter := newCustomHTTPAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL: "https://example.com/check",
		Custom: CustomHTTPConfig{
			Method:  http.MethodPost,
			Metrics: []CustomMetricMapping{{Pointer: "/value"}},
		},
	})
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("未确认 POST 应返回配置错误：%v", err)
	}
}

func TestRegistryProbe字段映射无效仍返回Sample(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"data": map[string]any{"balance": "7.5", "status": "ok"}})
	}))
	defer server.Close()

	registry := NewRegistry(HTTPOptions{})
	_, sample, err := registry.Probe(context.Background(), TargetConfig{
		Kind:                TargetKindCustom,
		BaseURL:             server.URL,
		AllowPrivateNetwork: true,
		Custom:              CustomHTTPConfig{Method: http.MethodGet},
	})
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("缺少字段映射时应返回配置错误：%v", err)
	}
	object, ok := sample.(map[string]any)
	if !ok {
		t.Fatalf("应返回解码后的临时 sample：%T", sample)
	}
	data, ok := object["data"].(map[string]any)
	if !ok || data["balance"] != "7.5" {
		t.Fatalf("sample 内容不符合预期：%v", sample)
	}
}

func TestRegistry按渠道类型分派(t *testing.T) {
	registry := NewRegistry(HTTPOptions{})
	for _, kind := range []TargetKind{TargetKindNewAPI, TargetKindSub2API, TargetKindChatGPT2API, TargetKindCLIProxyAPI, TargetKindCustom} {
		adapter, err := registry.Adapter(kind)
		if err != nil || adapter.Kind() != kind {
			t.Fatalf("注册器缺少 %s 适配器：%v", kind, err)
		}
	}
	if _, err := registry.Adapter(TargetKind("unknown")); err == nil {
		t.Fatal("未知渠道类型应返回错误")
	}
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(fmt.Sprintf("写入测试响应失败：%v", err))
	}
}

func assertMetric(t *testing.T, snapshot Snapshot, key MetricKey, expectedValue, expectedUnit string) {
	t.Helper()
	for _, metric := range snapshot.Metrics {
		if metric.Key != key {
			continue
		}
		expected := decimal.RequireFromString(expectedValue)
		if !metric.Value.Equal(expected) || metric.Unit != expectedUnit {
			t.Fatalf("指标 %s 不符合预期：value=%s unit=%s", key, metric.Value, metric.Unit)
		}
		return
	}
	t.Fatalf("缺少指标：%s", key)
}
