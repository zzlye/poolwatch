package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func Test分组价格接口只允许读取已监控分组并返回十进制字符串(t *testing.T) {
	testServer, database, _ := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar

	status, body := requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("首次设置失败：%d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", map[string]any{
		"name": "价格测试站", "kind": "new_api", "baseUrl": "https://api.example.com", "enabled": true,
		"checkIntervalMinutes": 5, "accessToken": "private-price-token", "userId": "42",
		"authType": "bearer", "requestMethod": "GET", "customHeaders": "{}", "jsonPointer": "/data/balance",
		"thresholds": []map[string]any{{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元", "alertEnabled": true}},
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("创建价格测试渠道失败：%d %s", status, body)
	}
	var target targetResponse
	if err := json.Unmarshal([]byte(body), &target); err != nil {
		t.Fatalf("解析价格测试渠道失败：%v", err)
	}

	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+target.ID+"/group-prices?groupKey=default", nil, "")
	if status != http.StatusNotFound || !strings.Contains(body, "尚未加入倍率监控") {
		t.Fatalf("未监控分组不应读取价格：%d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", map[string]any{
		"groupKeys": []string{"default"},
	}, "")
	if status != http.StatusOK {
		t.Fatalf("保存价格测试分组失败：%d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+target.ID+"/group-prices?groupKey=default", nil, "")
	if status != http.StatusOK || strings.Contains(body, "private-price-token") {
		t.Fatalf("读取分组价格失败或泄漏凭据：%d %s", status, body)
	}
	var response targetGroupPriceResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析分组价格响应失败：%v", err)
	}
	if response.TargetID != target.ID || response.GroupKey != "default" || response.Multiplier != "0.5" || len(response.Models) != 1 {
		t.Fatalf("分组价格响应基础字段不正确：%#v", response)
	}
	model := response.Models[0]
	if model.Prices[0].Value != "1.25" || model.Intervals[0].MinTokens != "200001" ||
		model.Intervals[0].Condition != "上下文 Token > 200000" || model.Intervals[0].Prices[0].Value != "7.5" {
		t.Fatalf("价格或 Token 边界没有按十进制字符串返回：%#v", model)
	}

	status, _ = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+target.ID+"/group-prices?groupKey=default&groupKey=vip", nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("重复分组参数必须拒绝：%d", status)
	}
}
