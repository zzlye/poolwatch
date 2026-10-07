package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestIndependentModelPriceMonitoring(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		t.Run(kind, func(t *testing.T) {
			server, db, _ := newAPITestServer(t)
			defer server.Close()
			defer db.Close()
			client := server.Client()
			client.Jar, _ = cookiejar.New(nil)
			status, body := requestJSON(t, client, http.MethodPost, server.URL+"/api/setup", map[string]any{"initializationToken": "setup-token", "username": "admin", "password": "long-password-123"}, "")
			if status != http.StatusCreated {
				t.Fatalf("初始化失败：%d %s", status, body)
			}
			status, body = requestJSON(t, client, http.MethodPost, server.URL+"/api/targets", map[string]any{
				"name": "独立价格渠道", "kind": kind, "baseUrl": "https://api.example.com", "enabled": true, "checkIntervalMinutes": 5, "accessToken": "synthetic-price-token", "userId": "42", "authType": "bearer", "requestMethod": "GET", "customHeaders": "{}", "jsonPointer": "/data/balance", "thresholds": []map[string]any{{"key": "wallet_balance", "label": "钱包余额", "value": "0", "unit": "USD", "alertEnabled": false}},
			}, "")
			if status != http.StatusCreated {
				t.Fatalf("创建失败：%d %s", status, body)
			}
			var target targetResponse
			if err := json.Unmarshal([]byte(body), &target); err != nil {
				t.Fatal(err)
			}
			base := server.URL + "/api/targets/" + target.ID + "/model-prices"
			for _, endpoint := range []struct{ method, path string }{{"GET", ""}, {"POST", "/groups"}, {"GET", "/catalog?groupKey=default"}} {
				status, body = requestJSON(t, client, endpoint.method, base+endpoint.path, nil, "")
				if status != http.StatusOK || strings.Contains(body, "synthetic-price-token") {
					t.Fatalf("无倍率配置时独立价格接口应可用且脱敏：%s %d %s", endpoint.path, status, body)
				}
			}
			status, body = requestJSON(t, client, http.MethodPut, base, map[string]any{"groupKey": "default", "models": []string{"gpt-price"}}, "")
			if status != http.StatusOK || !strings.Contains(body, `"value":"1.25"`) {
				t.Fatalf("价格选择未保存：%d %s", status, body)
			}
			groups, _ := db.ListGroupMultiplierMonitors(t.Context(), target.ID)
			if len(groups) != 0 {
				t.Fatal("价格监控不应自动启用倍率")
			}
			status, body = requestJSON(t, client, http.MethodPost, base+"/check", nil, "")
			if status != http.StatusOK {
				t.Fatalf("检测价格失败：%d %s", status, body)
			}
			status, _ = requestJSON(t, client, http.MethodPut, base, map[string]any{"groupKey": "default", "models": []string{}}, "https://evil.example")
			if status != http.StatusForbidden {
				t.Fatal("必须校验来源")
			}
			status, _ = requestJSON(t, client, http.MethodGet, base+"/catalog?groupKey=default&groupKey=vip", nil, "")
			if status != http.StatusBadRequest {
				t.Fatal("必须拒绝重复分组参数")
			}
			status, _ = requestJSON(t, client, http.MethodPut, base, map[string]any{"groupKey": "default"}, "")
			if status != http.StatusBadRequest {
				t.Fatal("缺失模型字段不能清空配置")
			}
			status, _ = requestJSON(t, client, http.MethodPut, base, map[string]any{"groupKey": "default", "models": []string{"不存在"}}, "")
			if status != http.StatusBadRequest {
				t.Fatal("必须校验上游模型")
			}
			items, _ := db.ListModelPriceMonitors(t.Context(), target.ID)
			if len(items) != 1 {
				t.Fatal("错误请求不能修改选择")
			}
			status, body = requestJSON(t, client, http.MethodPut, base, map[string]any{"groupKey": "default", "models": []string{}}, "")
			if status != http.StatusOK || !strings.Contains(body, "[]") {
				t.Fatalf("清空价格配置失败：%d %s", status, body)
			}
			alerts, _ := db.ListAlerts(t.Context(), "all", 20)
			if len(alerts) != 0 {
				t.Fatal("首次价格基准和相同价格不应通知")
			}
		})
	}
}
