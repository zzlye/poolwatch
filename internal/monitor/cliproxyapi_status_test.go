package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCLIProxyAPI账号错误按可用性分类(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		account    map[string]any
		wantStatus TargetStatus
		wantText   string
	}{
		{
			name: "不支持的请求参数只算警告",
			account: map[string]any{
				"status": "error", "status_message": `{"detail":"Unsupported parameter: max_tool_calls"}`,
			},
			wantStatus: TargetStatusWarning,
			wantText:   "参数警告，账号仍可用",
		},
		{
			name: "活跃账号遗留参数错误也算警告",
			account: map[string]any{
				"status": "active", "status_message": "invalid_request_error: unknown parameter reasoning_effort",
			},
			wantStatus: TargetStatusWarning,
			wantText:   "参数警告，账号仍可用",
		},
		{
			name: "临时上游错误只算警告",
			account: map[string]any{
				"status": "error", "unavailable": true, "status_message": "transient upstream error",
				"next_retry_after": "2026-07-21T08:10:00Z",
			},
			wantStatus: TargetStatusWarning,
			wantText:   "限流或冷却中",
		},
		{
			name: "未授权属于真正异常",
			account: map[string]any{
				"status": "error", "unavailable": true, "status_message": "unauthorized",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "禁止访问属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": `{"status_code":403,"detail":"Forbidden"}`,
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "嵌套状态码属于真正异常",
			account: map[string]any{
				"status": "error", "error": map[string]any{"status_code": float64(401)},
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "令牌过期属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": "access token expired",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "令牌撤销属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": "refresh token has been revoked",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "刷新失败属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": "failed to refresh token: invalid_grant",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "账号被停用属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": "account deactivated by provider",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "账号封禁属于真正异常",
			account: map[string]any{
				"status": "error", "status_message": "账号已封禁",
			},
			wantStatus: TargetStatusError,
			wantText:   "凭据失效",
		},
		{
			name: "请求超时只算警告",
			account: map[string]any{
				"status": "active", "status_message": "upstream request timeout",
			},
			wantStatus: TargetStatusWarning,
			wantText:   "暂时不可用",
		},
		{
			name: "网络错误只算警告",
			account: map[string]any{
				"status": "active", "status_message": "network error: connection reset by peer",
			},
			wantStatus: TargetStatusWarning,
			wantText:   "暂时不可用",
		},
		{
			name: "限流状态码只算警告",
			account: map[string]any{
				"status": "error", "status_code": 429, "status_message": "too many requests",
			},
			wantStatus: TargetStatusWarning,
			wantText:   "暂时不可用",
		},
		{
			name: "服务端错误只算警告",
			account: map[string]any{
				"status": "error", "status_message": `{"status_code":503,"detail":"Service Unavailable"}`,
			},
			wantStatus: TargetStatusWarning,
			wantText:   "暂时不可用",
		},
		{
			name: "显式禁用保持禁用",
			account: map[string]any{
				"status": "active", "disabled": true,
			},
			wantStatus: TargetStatusDisabled,
			wantText:   "已禁用",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, statusText, _ := classifyCLIProxyAPIAccount(test.account, now)
			if status != test.wantStatus || statusText != test.wantText {
				t.Fatalf("账号分类不正确：状态=%s 文案=%q，期望状态=%s 文案=%q", status, statusText, test.wantStatus, test.wantText)
			}
		})
	}
}

func TestCLIProxyAPI参数警告不计入异常账号(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"files": []any{
			map[string]any{
				"auth_index": "warning-account", "provider": "codex", "status": "error",
				"status_message": `{"detail":"Unsupported parameter: max_tool_calls"}`,
			},
			map[string]any{
				"auth_index": "dead-account", "provider": "codex", "status": "error", "status_message": "unauthorized",
			},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	snapshot, err := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{})).Check(context.Background(), TargetConfig{
		ID: "status-count", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AdminKey: "management-secret"},
	})
	if err != nil {
		t.Fatalf("检测 CLIProxyAPI 状态失败: %v", err)
	}
	values := make(map[MetricKey]string, len(snapshot.Metrics))
	for _, metric := range snapshot.Metrics {
		values[metric.Key] = metric.Value.String()
	}
	if values[MetricLimitedAccounts] != "1" || values[MetricErrorAccounts] != "1" {
		t.Fatalf("警告与异常账号计数未分开：%#v", values)
	}
	if snapshot.Accounts[0].Status != string(TargetStatusWarning) || snapshot.Accounts[0].StatusText != "参数警告，账号仍可用" {
		t.Fatalf("参数错误账号未按警告展示：%#v", snapshot.Accounts[0])
	}
}
