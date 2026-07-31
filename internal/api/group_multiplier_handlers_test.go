package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func Test倍率接口检测选择和清空流程(t *testing.T) {
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
		"name": "倍率测试站", "kind": "new_api", "baseUrl": "https://api.example.com", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "accessToken": "private-access-token", "userId": "42",
		"authType": "bearer", "requestMethod": "GET", "customHeaders": "{}", "jsonPointer": "/data/balance",
		"thresholds": []map[string]any{{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元", "alertEnabled": true}},
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("创建测试渠道失败：%d %s", status, body)
	}
	var target targetResponse
	if err := json.Unmarshal([]byte(body), &target); err != nil || target.ID == "" {
		t.Fatalf("解析测试渠道失败：%v，%s", err, body)
	}

	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"groups":[]`) {
		t.Fatalf("初始倍率状态应为空：%d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers/detect", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"multiplier":"0.333333"`) || strings.Contains(body, "private-access-token") {
		t.Fatalf("倍率自动检测响应错误或泄漏凭据：%d %s", status, body)
	}
	var detected targetMultiplierStateResponse
	if err := json.Unmarshal([]byte(body), &detected); err != nil || len(detected.Groups) != 2 || detected.Groups[0].Monitored {
		t.Fatalf("倍率自动检测结果不正确：%#v，%v", detected, err)
	}

	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", map[string]any{
		"groupKeys": []string{"default"},
	}, "")
	if status != http.StatusOK {
		t.Fatalf("保存倍率监控失败：%d %s", status, body)
	}
	var saved targetMultiplierStateResponse
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("解析倍率监控结果失败：%v", err)
	}
	foundMonitored := false
	for _, group := range saved.Groups {
		if group.Key == "default" && group.Monitored && group.Multiplier == "1" && group.Status == "stable" {
			foundMonitored = true
		}
	}
	if !foundMonitored {
		t.Fatalf("新选择分组没有建立基准：%#v", saved.Groups)
	}
	alertsFound, err := database.ListAlerts(t.Context(), "all", 20)
	if err != nil || len(alertsFound) != 0 {
		t.Fatalf("首次建立倍率基准不应产生告警：%#v，%v", alertsFound, err)
	}

	status, _ = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", map[string]any{
		"groupKeys": []string{},
	}, "https://evil.example")
	if status != http.StatusForbidden {
		t.Fatalf("倍率配置应受来源校验保护：%d", status)
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", map[string]any{
		"groupKeys": []string{},
	}, "")
	if status != http.StatusOK {
		t.Fatalf("清空倍率监控失败：%d %s", status, body)
	}
	items, err := database.ListGroupMultiplierMonitors(t.Context(), target.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("倍率选择未清空：%#v，%v", items, err)
	}
}

func Test倍率接口拒绝不支持渠道(t *testing.T) {
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
		"name": "自定义站", "kind": "custom", "baseUrl": "https://custom.example.com", "enabled": true,
		"checkIntervalMinutes": 5, "authType": "none", "requestMethod": "GET", "customHeaders": "{}",
		"jsonPointer": "/data/value", "thresholds": []map[string]any{{"key": "custom_value", "label": "数值", "value": "0", "unit": "个", "alertEnabled": false}},
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("创建自定义渠道失败：%d %s", status, body)
	}
	var target targetResponse
	_ = json.Unmarshal([]byte(body), &target)
	status, _ = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+target.ID+"/group-multipliers", nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("不支持渠道应返回参数错误：%d", status)
	}
}
