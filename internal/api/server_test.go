package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/alerts"
	"poolwatch/internal/auth"
	"poolwatch/internal/events"
	"poolwatch/internal/monitor"
	"poolwatch/internal/push"
	"poolwatch/internal/scheduler"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

type apiTestRunner struct{}

func (apiTestRunner) Run(_ context.Context, target monitor.TargetInput) (monitor.Result, error) {
	threshold := target.Thresholds[monitor.MetricWalletBalance]
	return monitor.Snapshot{
		TargetID: target.ID, Kind: target.Kind, Status: monitor.TargetStatusHealthy, ObservedAt: time.Now().UTC(),
		Metrics: []monitor.Metric{{
			Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(5), Unit: "元", Threshold: &threshold,
		}},
	}, nil
}

func (runner apiTestRunner) Probe(ctx context.Context, target monitor.TargetInput) (monitor.Result, any, error) {
	if target.Kind == monitor.TargetKindCustom {
		return monitor.Snapshot{TargetID: target.ID, Kind: target.Kind}, map[string]any{
			"data": map[string]any{"balance": "12.50", "state": "ok", "access_token": "sample-secret"},
		}, &monitor.CheckError{Kind: monitor.ErrorClassResponse, Message: "自定义指标字段不存在"}
	}
	result, err := runner.Run(ctx, target)
	return result, nil, err
}

func (apiTestRunner) Detect(_ context.Context, _ string, _ bool) (monitor.TargetKind, error) {
	return monitor.TargetKindNewAPI, nil
}

func TestHTTPInitializationTargetHistoryAndSecretBoundary(t *testing.T) {
	testServer, database, vault := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar

	status, body := requestJSON(t, client, http.MethodGet, testServer.URL+"/api/bootstrap", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"initialized":false`) {
		t.Fatalf("首次初始化状态不正确: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("首次设置失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/detect", map[string]string{
		"baseUrl": "https://api.example.com",
	}, "")
	if status != http.StatusOK || !strings.Contains(body, `"kind":"new_api"`) {
		t.Fatalf("自动识别接口失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/push/subscriptions", map[string]any{
		"endpoint": "https://push.example.com/subscription", "expirationTime": nil, "name": "测试设备",
		"keys": map[string]string{"p256dh": "public-browser-key", "auth": "browser-auth-secret"},
	}, "")
	if status != http.StatusNoContent {
		t.Fatalf("保存标准浏览器订阅失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/push", nil, "")
	if status != http.StatusOK || !strings.Contains(body, "测试设备") || strings.Contains(body, "browser-auth-secret") || strings.Contains(body, "push.example.com") {
		t.Fatalf("推送设备响应不正确或泄漏订阅秘密: %d %s", status, body)
	}

	createBody := map[string]any{
		"name": "主站", "kind": "new_api", "baseUrl": "https://api.example.com", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "username": "", "email": "", "password": "",
		"totpCode": "", "totpSecret": "", "accessToken": "secret-access-token", "refreshToken": "",
		"adminKey": "", "userId": "42", "authType": "bearer", "requestMethod": "GET",
		"confirmPost": false, "customHeaders": "{}", "jsonPointer": "/data/balance", "statusPointer": "",
		"thresholds": []map[string]string{{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"}},
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", createBody, "")
	if status != http.StatusCreated || strings.Contains(body, "secret-access-token") {
		t.Fatalf("创建渠道响应不正确或泄漏秘密: %d %s", status, body)
	}
	var created targetResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" || !created.AuthConfigured {
		t.Fatalf("解析新渠道失败: %#v, %v", created, err)
	}

	createBody["name"] = "主站更新"
	createBody["accessToken"] = ""
	createBody["userId"] = ""
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK || strings.Contains(body, "secret-access-token") {
		t.Fatalf("更新渠道失败或泄漏秘密: %d %s", status, body)
	}
	stored, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取已保存渠道失败: %v", err)
	}
	credentialsJSON, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(credentialsJSON), "secret-access-token") {
		t.Fatalf("空白秘密字段未沿用: %s, %v", credentialsJSON, err)
	}
	createBody["username"] = "password-user"
	createBody["password"] = "channel-password"
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK {
		t.Fatalf("切换为密码登录失败: %d %s", status, body)
	}
	stored, _ = database.TargetByID(context.Background(), created.ID)
	credentialsJSON, err = vault.Decrypt(stored.CredentialsEnc)
	if err != nil || strings.Contains(string(credentialsJSON), "secret-access-token") || !strings.Contains(string(credentialsJSON), "channel-password") {
		t.Fatalf("登录方式切换未清除旧令牌: %s, %v", credentialsJSON, err)
	}

	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+created.ID+"/check", nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("手动检测失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+created.ID, nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"value":"5"`) || !strings.Contains(body, `"status":"warning"`) {
		t.Fatalf("渠道指标未更新: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+created.ID+"/history", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"metricKey":"wallet_balance"`) {
		t.Fatalf("历史接口结果不正确: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/alerts?status=all", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"type":"threshold"`) {
		t.Fatalf("阈值告警未生成: %d %s", status, body)
	}
	var alertItems []alertResponse
	if err := json.Unmarshal([]byte(body), &alertItems); err != nil || len(alertItems) == 0 {
		t.Fatalf("解析告警失败: %v, %s", err, body)
	}
	status, body = requestJSON(t, client, http.MethodPatch, testServer.URL+"/api/alerts/"+alertItems[0].ID, map[string]string{"status": "acknowledged"}, "")
	if status != http.StatusOK {
		t.Fatalf("确认告警失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/alerts?status=open", nil, "")
	if status != http.StatusOK || body != "[]\n" {
		t.Fatalf("未处理筛选不应包含已确认告警: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/alerts?status=acknowledged", nil, "")
	if status != http.StatusOK || !strings.Contains(body, alertItems[0].ID) {
		t.Fatalf("已确认筛选缺少告警: %d %s", status, body)
	}

	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/test", map[string]any{
		"name": "自定义测试", "kind": "custom", "baseUrl": "https://custom.example.com/value", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "username": "", "email": "", "password": "",
		"totpCode": "", "totpSecret": "", "accessToken": "", "refreshToken": "", "adminKey": "", "userId": "",
		"authType": "none", "requestMethod": "GET", "confirmPost": false, "customHeaders": "{}",
		"jsonPointer": "/missing", "statusPointer": "",
		"thresholds": []map[string]string{{"key": "wallet_balance", "label": "剩余额度", "value": "3", "unit": "次"}},
	}, "")
	if status != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, `"sample"`) || strings.Contains(body, "sample-secret") || !strings.Contains(body, "[已隐藏]") {
		t.Fatalf("自定义响应样本未返回: %d %s", status, body)
	}
	customBody := map[string]any{
		"name": "自定义请求头", "kind": "custom", "baseUrl": "https://custom.example.com/value", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "username": "", "email": "", "password": "",
		"totpCode": "", "totpSecret": "", "accessToken": "", "refreshToken": "", "adminKey": "", "userId": "",
		"authType": "headers", "requestMethod": "GET", "confirmPost": false,
		"customHeaders": `{"X-API-Key":"header-secret"}`, "jsonPointer": "/data/balance", "statusPointer": "",
		"thresholds": []map[string]string{{"key": "wallet_balance", "label": "剩余额度", "value": "3", "unit": "次"}},
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", customBody, "")
	if status != http.StatusCreated || !strings.Contains(body, `"authType":"headers"`) || !strings.Contains(body, `"customHeadersConfigured":true`) || strings.Contains(body, "header-secret") {
		t.Fatalf("自定义请求头渠道响应不正确: %d %s", status, body)
	}
	var customCreated targetResponse
	_ = json.Unmarshal([]byte(body), &customCreated)
	customBody["customHeaders"] = ""
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+customCreated.ID, customBody, "")
	if status != http.StatusOK || !strings.Contains(body, `"authType":"headers"`) || strings.Contains(body, "header-secret") {
		t.Fatalf("编辑时未沿用自定义请求头: %d %s", status, body)
	}

	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/settings", map[string]any{
		"historyRetentionDays": 0,
	}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("非法历史保留期限未被拒绝: %d %s", status, body)
	}

	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/settings", map[string]any{
		"productName": "跨站修改",
	}, "https://evil.example")
	if status != http.StatusForbidden {
		t.Fatalf("跨站状态修改未被拒绝: %d %s", status, body)
	}
}

func TestProtectedAPIRejectsAnonymousRequest(t *testing.T) {
	testServer, database, _ := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	status, body := requestJSON(t, testServer.Client(), http.MethodGet, testServer.URL+"/api/dashboard", nil, "")
	if status != http.StatusUnauthorized || !strings.Contains(body, "请先登录") {
		t.Fatalf("未登录请求未被拒绝: %d %s", status, body)
	}
}

func TestSensitiveSampleKeyCoversCompositeAndCamelCase(t *testing.T) {
	for _, key := range []string{
		"client_secret", "refreshToken", "authorizationHeader", "x-api-key", "privateKeyPem",
	} {
		if !sensitiveSampleKey(key) {
			t.Fatalf("敏感字段未被识别：%s", key)
		}
	}
	if sensitiveSampleKey("quota_remaining") {
		t.Fatal("普通指标字段不应被标记为敏感")
	}
}

func newAPITestServer(t *testing.T) (*httptest.Server, *store.Store, *secure.Vault) {
	t.Helper()
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		database.Close()
		t.Fatalf("创建保险箱失败: %v", err)
	}
	authService := auth.NewService(database, vault, "setup-token", time.Hour)
	pushService := push.NewService(database, vault, "")
	if err := pushService.EnsureKeys(context.Background()); err != nil {
		database.Close()
		t.Fatalf("初始化推送失败: %v", err)
	}
	hub := events.NewHub()
	alertEngine := alerts.NewEngine(database, nil)
	schedulerService := scheduler.NewService(database, vault, apiTestRunner{}, alertEngine, false)
	server := NewServer(Dependencies{
		Store: database, Vault: vault, Auth: authService, Scheduler: schedulerService,
		Push: pushService, Events: hub, Static: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusOK)
		}),
	})
	return httptest.NewServer(server.Handler()), database, vault
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint string, value any, origin string) (int, string) {
	t.Helper()
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("编码请求失败: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("执行请求失败: %v", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return response.StatusCode, string(content)
}
