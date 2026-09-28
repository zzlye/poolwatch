package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 新版管理令牌直接使用 Bearer 身份，不依赖旧版额外用户编号。
func TestNewAPIManagementTokenWithoutUserID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"turnstile_check": true, "quota_per_unit": 100}})
	})
	mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-management-token" || r.Header.Get("New-Api-User") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 42, "quota": 500, "status": 1}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, token := range []string{"synthetic-management-token", "Bearer synthetic-management-token"} {
		adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
		snapshot, err := adapter.Check(context.Background(), TargetConfig{BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: token}})
		if err != nil {
			t.Fatalf("新版管理令牌不应要求用户编号：%v", err)
		}
		assertMetric(t, snapshot, MetricWalletBalance, "5", "USD")
	}
}
