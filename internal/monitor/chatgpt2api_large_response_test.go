package monitor

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 测试只使用合成凭据，复现上游全量返回冗长账号信息。
func chatLargeResponseFixture(t *testing.T, status int, body string) (*chatGPT2APIAdapter, TargetConfig) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("format") != "json" {
			t.Error("汇总接口必须保持只读")
		}
		writeTestJSON(w, map[string]any{"healthy": true, "accounts": map[string]any{"total": 2, "active": 2, "total_quota": 123}})
	})
	mux.HandleFunc("/api/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-admin" {
			t.Error("账号接口必须使用只读认证请求")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return newChatGPT2APIAdapter(newSecureHTTPClient(HTTPOptions{})), TargetConfig{ID: "chat-large", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AdminKey: "synthetic-admin"}}
}

func TestChatLargeAccountResponse(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"email": "first@example.com", "status": "正常", "quota": 100, "access_token": strings.Repeat("synthetic-secret", 80000)},
		map[string]any{"email": "last@example.com", "status": "正常", "quota": 23},
	}})
	adapter, target := chatLargeResponseFixture(t, http.StatusOK, string(body))
	snapshot, err := adapter.Check(context.Background(), target)
	if err != nil {
		t.Fatalf("正常大号池不应被通用响应上限拒绝：%v", err)
	}
	assertMetric(t, snapshot, MetricImageQuota, "123", "次")
	// 独立读取不能污染共用客户端，普通请求仍受原来的上限保护。
	var genericPayload any
	err = adapter.http.newSession(true).doJSON(context.Background(), http.MethodGet, target.BaseURL+"/api/accounts", http.Header{"Authorization": []string{"Bearer synthetic-admin"}}, nil, &genericPayload)
	if ErrorClassOf(err) != ErrorClassResponse {
		t.Fatal("账号接口的独立上限不应影响其他请求")
	}
	if len(snapshot.Accounts) != 2 || snapshot.Accounts[1].Email != "last@example.com" {
		t.Fatal("大响应必须读取完整账号列表")
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "synthetic-secret") || strings.Contains(string(encoded), "synthetic-admin") {
		t.Fatal("快照泄漏合成凭据")
	}
}

func TestChatAccountResponseFailureKeepsSummary(t *testing.T) {
	large, _ := json.Marshal(map[string]any{"items": []any{}, "padding": strings.Repeat("x", 16<<20)})
	for _, test := range []struct{ name, body string }{
		{"超过独立上限", string(large)},
		{"明细格式损坏", "{"},
		{"缺少账号列表", "{}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, target := chatLargeResponseFixture(t, http.StatusOK, test.body)
			snapshot, err := adapter.Check(context.Background(), target)
			if err != nil {
				t.Fatalf("明细响应问题不应丢弃有效汇总：%v", err)
			}
			assertMetric(t, snapshot, MetricImageQuota, "123", "次")
			if snapshot.Status != TargetStatusWarning || snapshot.Accounts != nil || !strings.Contains(snapshot.Message, "账号明细") {
				t.Fatal("必须明确提示明细失败，且不返回可覆盖原数据的空列表")
			}
		})
	}
}

func TestChatAccountAuthenticationFailureRemainsVisible(t *testing.T) {
	for _, status := range []int{401, 403} {
		// 即使认证错误正文很大，也不能降级成明细容量警告。
		adapter, target := chatLargeResponseFixture(t, status, strings.Repeat("x", 17<<20))
		_, err := adapter.Check(context.Background(), target)
		if ErrorClassOf(err) != ErrorClassAuth {
			t.Fatalf("凭据错误必须保留立即告警语义：%v", err)
		}
	}
}

func TestChatAccountsLimitAppliesAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		_, _ = compressed.Write([]byte(strings.Repeat(" ", (16<<20)+1)))
		_ = compressed.Close()
	}))
	defer server.Close()
	adapter := newChatGPT2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.readAccounts(context.Background(), adapter.http.newSession(true), TargetConfig{BaseURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "16 MB") {
		t.Fatalf("压缩响应仍必须限制解压后体积：%v", err)
	}
}
