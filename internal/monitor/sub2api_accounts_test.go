package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSub2API管理员JWT分页读取脱敏号池(t *testing.T) {
	var pages atomic.Int32
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-jwt" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"balance": "18.5", "status": "active", "role": "admin",
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		pages.Add(1)
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer admin-jwt" || request.Header.Get("x-api-key") != "" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Query().Get("page_size") != "100" || request.URL.Query().Get("sort_by") != "id" || request.URL.Query().Get("sort_order") != "asc" {
			t.Errorf("账号分页大小不正确：%s", request.URL.RawQuery)
		}
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		items := []any{}
		switch page {
		case 1:
			items = []any{
				map[string]any{
					"id": 11, "name": "Claude 主号", "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true,
					"expires_at": now.Add(24 * time.Hour).Unix(), "credentials": map[string]any{"access_token": "never-return-this-token"},
				},
				map[string]any{
					"id": 12, "name": "Bedrock 备用", "platform": "anthropic", "type": "bedrock", "status": "active", "schedulable": true,
					"rate_limit_reset_at": now.Add(30 * time.Minute).Format(time.RFC3339),
					"quota_limit":         "200", "quota_used": "50", "quota_daily_limit": "20", "quota_daily_used": "5",
					"quota_weekly_limit": "100", "quota_weekly_used": "100",
					"quota_daily_reset_at":  now.Add(12 * time.Hour).Format(time.RFC3339),
					"quota_weekly_reset_at": now.Add(4 * 24 * time.Hour).Format(time.RFC3339),
				},
			}
		case 2:
			items = []any{
				map[string]any{"id": 12, "name": "重复账号", "platform": "anthropic", "type": "bedrock", "status": "active", "schedulable": true},
				map[string]any{
					"id": 13, "name": "停用号", "platform": "openai", "type": "oauth", "status": "inactive", "schedulable": false,
					"error_message": "refresh token never-return-this-error", "extra": map[string]any{"api_key": "never-return-this-key"},
				},
			}
		default:
			t.Errorf("不应请求第 %d 页", page)
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 3, "page": page, "page_size": 100, "items": items,
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.now = func() time.Time { return now }
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID: "sub2-admin", Kind: TargetKindSub2API, BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "admin-jwt"},
	})
	if err != nil {
		t.Fatalf("读取 Sub2API 管理员号池失败：%v", err)
	}
	if pages.Load() != 2 || len(snapshot.Accounts) != 3 {
		t.Fatalf("应分页读取全部账号，pages=%d accounts=%d", pages.Load(), len(snapshot.Accounts))
	}
	first := findSub2APIAccount(t, snapshot.Accounts, "11")
	if first.DisplayName != "Claude 主号" || first.Provider != "anthropic" || first.Type != "oauth" || first.Status != string(TargetStatusHealthy) || first.QuotaState != "" {
		t.Fatalf("Anthropic 账号白名单映射不正确：%#v", first)
	}
	if first.SubscriptionExpiresAt != now.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("账号到期时间不正确：%s", first.SubscriptionExpiresAt)
	}
	second := findSub2APIAccount(t, snapshot.Accounts, "12")
	if second.Status != string(TargetStatusWarning) || second.StatusText != "限流或冷却中" || second.RecoveryAt != now.Add(30*time.Minute).Format(time.RFC3339) {
		t.Fatalf("限流账号状态不正确：%#v", second)
	}
	assertSub2APIQuotaWindow(t, second.QuotaWindows, "internal-total", "75", "")
	assertSub2APIQuotaWindow(t, second.QuotaWindows, "internal-daily", "75", now.Add(12*time.Hour).Format(time.RFC3339))
	assertSub2APIQuotaWindow(t, second.QuotaWindows, "internal-weekly", "0", now.Add(4*24*time.Hour).Format(time.RFC3339))
	assertSub2APIAbsoluteQuotaWindow(t, second.QuotaWindows, "internal-total", "150", "200", "USD")
	assertSub2APIAbsoluteQuotaWindow(t, second.QuotaWindows, "internal-daily", "15", "20", "USD")
	third := findSub2APIAccount(t, snapshot.Accounts, "13")
	if third.Status != string(TargetStatusDisabled) || third.StatusText != "已停用" {
		t.Fatalf("停用账号状态不正确：%#v", third)
	}
	serialized, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"never-return-this-token", "never-return-this-error", "never-return-this-key", "credentials", "error_message", "extra"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("脱敏快照泄露了字段或秘密 %q：%s", forbidden, serialized)
		}
	}
	assertMetric(t, snapshot, MetricHealthyAccounts, "1", "个")
	assertMetric(t, snapshot, MetricAccountTotal, "3", "个")
	assertMetric(t, snapshot, MetricLimitedAccounts, "1", "个")
	assertMetric(t, snapshot, MetricDisabledAccounts, "1", "个")
}

func TestSub2API普通用户不读取管理员号池(t *testing.T) {
	var adminCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "3", "status": "active", "role": "user"}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		adminCalls.Add(1)
		writer.WriteHeader(http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "user-jwt"},
	})
	if err != nil {
		t.Fatalf("普通用户余额检测不应受号池权限影响：%v", err)
	}
	if adminCalls.Load() != 0 || snapshot.Accounts == nil || len(snapshot.Accounts) != 0 {
		t.Fatalf("普通用户不应读取管理员号池，calls=%d accounts=%d", adminCalls.Load(), len(snapshot.Accounts))
	}
	assertMetric(t, snapshot, MetricWalletBalance, "3", "USD")
}

func TestSub2API号池故障不影响钱包和令牌保存(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"balance": "12.5", "status": "active", "role": "admin",
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "admin-jwt"},
	})
	if err != nil {
		t.Fatalf("号池故障不应拖垮钱包检测：%v", err)
	}
	assertMetric(t, snapshot, MetricWalletBalance, "12.5", "USD")
	if snapshot.Status != TargetStatusWarning || snapshot.Accounts != nil || snapshot.CredentialUpdate == nil ||
		!strings.Contains(snapshot.Message, "号池暂时无法读取") {
		t.Fatalf("号池故障降级结果不正确：%#v", snapshot)
	}
}

func TestSub2API账号可调度状态遵循官方规则(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		raw        map[string]any
		wantStatus TargetStatus
		wantText   string
	}{
		{name: "到期但未开启自动暂停", raw: map[string]any{
			"id": 51, "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true,
			"expires_at": now.Add(-time.Hour).Unix(), "auto_pause_on_expired": false,
		}, wantStatus: TargetStatusHealthy, wantText: "可用"},
		{name: "到期自动暂停", raw: map[string]any{
			"id": 52, "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true,
			"expires_at": now.Add(-time.Hour).Unix(), "auto_pause_on_expired": true,
		}, wantStatus: TargetStatusDisabled, wantText: "已过期自动停用"},
		{name: "手动暂停调度", raw: map[string]any{
			"id": 53, "platform": "openai", "type": "oauth", "status": "active", "schedulable": false,
		}, wantStatus: TargetStatusDisabled, wantText: "已暂停调度"},
		{name: "内部额度耗尽", raw: map[string]any{
			"id": 54, "platform": "anthropic", "type": "apikey", "status": "active", "schedulable": true,
			"quota_limit": "10", "quota_used": "10",
		}, wantStatus: TargetStatusWarning, wantText: "内部额度已耗尽"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account, err := parseSub2APIAccount(test.raw, now)
			if err != nil {
				t.Fatalf("解析账号失败：%v", err)
			}
			if TargetStatus(account.Status) != test.wantStatus || account.StatusText != test.wantText {
				t.Fatalf("账号状态不正确：%#v", account)
			}
		})
	}
}

func TestSub2API管理密钥使用独立请求头(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer user-jwt" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "3", "status": "active", "role": "user"}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-api-key") != "admin-key" || request.Header.Get("Authorization") != "" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 1, "items": []any{map[string]any{"id": 21, "name": "管理员密钥账号", "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true}},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "user-jwt", AdminKey: "admin-key"},
	})
	if err != nil || len(snapshot.Accounts) != 1 {
		t.Fatalf("管理密钥读取账号池失败：accounts=%d err=%v", len(snapshot.Accounts), err)
	}
}

func TestSub2API仅被动刷新Anthropic额度(t *testing.T) {
	var usageCalls atomic.Int32
	var nonGetCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "8", "status": "active", "role": "admin"}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 5,
			"items": []any{
				map[string]any{"id": 31, "name": "OAuth", "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true},
				map[string]any{"id": 32, "name": "Setup", "platform": "anthropic", "type": "setup-token", "status": "active", "schedulable": true},
				map[string]any{"id": 33, "name": "API Key", "platform": "anthropic", "type": "apikey", "status": "active", "schedulable": true, "quota_limit": 80, "quota_used": 20},
				map[string]any{"id": 34, "name": "Bedrock", "platform": "anthropic", "type": "bedrock", "status": "active", "schedulable": true, "quota_daily_limit": 10, "quota_daily_used": 2},
				map[string]any{"id": 35, "name": "OpenAI", "platform": "openai", "type": "oauth", "status": "active", "schedulable": true},
			},
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts/31/usage", func(writer http.ResponseWriter, request *http.Request) {
		usageCalls.Add(1)
		if request.Method != http.MethodGet {
			nonGetCalls.Add(1)
		}
		if request.URL.Query().Get("source") != "passive" || request.URL.Query().Has("force") {
			t.Errorf("额度刷新必须只使用被动来源：%s", request.URL.RawQuery)
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"five_hour":        map[string]any{"utilization": "25", "resets_at": "2026-08-01T01:00:00Z"},
			"seven_day":        map[string]any{"utilization": 100, "resets_at": "2026-08-07T01:00:00Z"},
			"seven_day_sonnet": map[string]any{"utilization": 12.5},
			"seven_day_fable":  map[string]any{"utilization": 150},
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts/32/usage", func(writer http.ResponseWriter, request *http.Request) {
		usageCalls.Add(1)
		if request.Method != http.MethodGet {
			nonGetCalls.Add(1)
		}
		if request.URL.Query().Get("source") != "passive" || request.URL.Query().Has("force") {
			t.Errorf("额度刷新必须只使用被动来源：%s", request.URL.RawQuery)
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"five_hour": nil, "seven_day": nil, "error": "被动采样尚不可用",
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	target := TargetConfig{BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "admin-jwt"}}
	ids := []string{
		PublicAccountID(TargetKindSub2API, "31"), PublicAccountID(TargetKindSub2API, "32"),
		PublicAccountID(TargetKindSub2API, "33"), PublicAccountID(TargetKindSub2API, "34"), PublicAccountID(TargetKindSub2API, "35"),
	}
	result, err := adapter.RefreshAccountQuotas(context.Background(), target, ids)
	if err != nil {
		t.Fatalf("刷新 Sub2API 额度失败：%v", err)
	}
	accounts := result.Accounts
	if usageCalls.Load() != 2 || nonGetCalls.Load() != 0 || len(accounts) != 5 {
		t.Fatalf("只应 GET 两个 Anthropic 订阅账号，usage=%d nonGET=%d accounts=%d", usageCalls.Load(), nonGetCalls.Load(), len(accounts))
	}
	oauth := findSub2APIAccount(t, accounts, "31")
	if oauth.QuotaState != AccountQuotaStateAvailable {
		t.Fatalf("OAuth 被动额度应可用：%#v", oauth)
	}
	assertSub2APIQuotaWindow(t, oauth.QuotaWindows, "five-hour", "75", "2026-08-01T01:00:00Z")
	assertSub2APIQuotaWindow(t, oauth.QuotaWindows, "seven-day", "0", "2026-08-07T01:00:00Z")
	assertSub2APIQuotaWindow(t, oauth.QuotaWindows, "seven-day-sonnet", "87.5", "")
	assertSub2APIQuotaWindow(t, oauth.QuotaWindows, "seven-day-fable", "0", "")
	if setup := findSub2APIAccount(t, accounts, "32"); setup.QuotaState != AccountQuotaStateUnavailable || len(setup.QuotaWindows) != 0 {
		t.Fatalf("没有被动采样的 Setup Token 应标为暂未获取：%#v", setup)
	}
	if apiKey := findSub2APIAccount(t, accounts, "33"); apiKey.QuotaState != AccountQuotaStateAvailable || len(apiKey.QuotaWindows) != 1 {
		t.Fatalf("API Key 应保留内部额度：%#v", apiKey)
	}
	if bedrock := findSub2APIAccount(t, accounts, "34"); bedrock.QuotaState != AccountQuotaStateAvailable || len(bedrock.QuotaWindows) != 1 {
		t.Fatalf("Bedrock 应保留内部额度：%#v", bedrock)
	}
	if other := findSub2APIAccount(t, accounts, "35"); other.QuotaState != AccountQuotaStateUnsupported {
		t.Fatalf("其他平台 OAuth 应明确不支持额度读取：%#v", other)
	}
}

func TestSub2API额度刷新返回轮换后的令牌(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/refresh", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600,
		}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer new-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"balance": "8", "status": "active", "role": "admin"}})
	})
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer new-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 1, "items": []any{map[string]any{
				"id": 61, "name": "OpenAI", "platform": "openai", "type": "oauth", "status": "active", "schedulable": true,
			}},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.RefreshAccountQuotas(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{RefreshToken: "old-refresh"},
	}, []string{PublicAccountID(TargetKindSub2API, "61")})
	if err != nil {
		t.Fatalf("刷新 Sub2API 额度失败：%v", err)
	}
	if len(result.Accounts) != 1 || result.CredentialUpdate == nil || result.CredentialUpdate.AccessToken != "new-access" ||
		result.CredentialUpdate.RefreshToken != "new-refresh" {
		t.Fatalf("额度刷新没有返回轮换后的令牌：%#v", result)
	}
}

func TestSub2API被动额度接口错误不会返回可覆盖结果(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 1, "items": []any{map[string]any{
				"id": 62, "name": "Claude", "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true,
			}},
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts/62/usage", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.RefreshAccountQuotas(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AdminKey: "admin-key"},
	}, []string{PublicAccountID(TargetKindSub2API, "62")})
	if err == nil || ErrorClassOf(err) != ErrorClassServer || len(result.Accounts) != 0 {
		t.Fatalf("被动额度接口错误不应返回可覆盖旧额度的成功结果：result=%#v err=%v", result, err)
	}
}

func TestSub2API单账号被动额度使用独立短超时(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"total": 1, "items": []any{map[string]any{
				"id": 63, "name": "慢响应 Claude", "platform": "anthropic", "type": "oauth", "status": "active", "schedulable": true,
			}},
		}})
	})
	mux.HandleFunc("/api/v1/admin/accounts/63/usage", func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.quotaRequestTimeout = 30 * time.Millisecond
	startedAt := time.Now()
	_, err := adapter.RefreshAccountQuotas(context.Background(), TargetConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AdminKey: "admin-key"},
	}, []string{PublicAccountID(TargetKindSub2API, "63")})
	if err == nil || time.Since(startedAt) > time.Second {
		t.Fatalf("单账号慢响应应快速超时：elapsed=%s err=%v", time.Since(startedAt), err)
	}
}

func TestSub2API额度刷新校验脱敏账号标识(t *testing.T) {
	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.RefreshAccountQuotas(context.Background(), TargetConfig{}, []string{"31"})
	if err == nil || !strings.Contains(err.Error(), "账号标识格式无效") {
		t.Fatalf("应拒绝直接使用上游账号 ID：%v", err)
	}
}

func findSub2APIAccount(t *testing.T, accounts []AccountStatus, externalID string) AccountStatus {
	t.Helper()
	for _, account := range accounts {
		if account.ExternalID == externalID {
			return account
		}
	}
	t.Fatalf("没有找到 Sub2API 账号 %s", externalID)
	return AccountStatus{}
}

func assertSub2APIQuotaWindow(t *testing.T, windows []AccountQuotaWindow, key, remaining, resetAt string) {
	t.Helper()
	for _, window := range windows {
		if window.Key != key {
			continue
		}
		if window.RemainingPercent == nil || window.RemainingPercent.String() != remaining || window.ResetAt != resetAt {
			t.Fatalf("额度窗口 %s 不正确：%#v", key, window)
		}
		return
	}
	t.Fatalf("缺少额度窗口 %s：%#v", key, windows)
}

func assertSub2APIAbsoluteQuotaWindow(t *testing.T, windows []AccountQuotaWindow, key, remaining, limit, unit string) {
	t.Helper()
	for _, window := range windows {
		if window.Key != key {
			continue
		}
		if window.RemainingValue == nil || window.RemainingValue.String() != remaining ||
			window.LimitValue == nil || window.LimitValue.String() != limit || window.Unit != unit {
			t.Fatalf("额度窗口 %s 的绝对值不正确：%#v", key, window)
		}
		return
	}
	t.Fatalf("缺少额度窗口 %s：%#v", key, windows)
}
