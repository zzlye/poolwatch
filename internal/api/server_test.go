package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	"poolwatch/internal/mailnotify"
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

func (apiTestRunner) RefreshAccountQuotas(_ context.Context, target monitor.TargetInput, accountIDs []string) (monitor.AccountQuotaRefreshResult, error) {
	remaining := decimal.NewFromInt(72)
	remainingValue, limitValue := decimal.NewFromInt(18), decimal.NewFromInt(25)
	result := make([]monitor.AccountStatus, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		switch accountID {
		case monitor.PublicAccountID(target.Kind, "api-ready"):
			window := monitor.AccountQuotaWindow{Key: "code-5h", Label: "5 小时", RemainingPercent: &remaining}
			if target.Kind == monitor.TargetKindSub2API {
				window = monitor.AccountQuotaWindow{
					Key: "daily-usd", Label: "每日额度", RemainingPercent: &remaining,
					RemainingValue: &remainingValue, LimitValue: &limitValue, Unit: "USD",
				}
			}
			result = append(result, monitor.AccountStatus{
				ExternalID: "api-ready", Provider: "codex", Type: "plus", Status: string(monitor.TargetStatusError),
				QuotaState: monitor.AccountQuotaStateAvailable, QuotaWindows: []monitor.AccountQuotaWindow{window},
			})
		case monitor.PublicAccountID(target.Kind, "api-unavailable"):
			result = append(result, monitor.AccountStatus{
				ExternalID: "api-unavailable", Provider: "codex", Status: string(monitor.TargetStatusError),
				QuotaState: monitor.AccountQuotaStateUnavailable,
			})
		case monitor.PublicAccountID(target.Kind, "api-unsupported"):
			result = append(result, monitor.AccountStatus{
				ExternalID: "api-unsupported", Provider: "claude", Status: string(monitor.TargetStatusError),
				QuotaState: monitor.AccountQuotaStateUnsupported,
			})
		default:
			return monitor.AccountQuotaRefreshResult{}, &monitor.CheckError{Kind: monitor.ErrorClassResponse, Message: "账号列表已经变化，请刷新页面后重试"}
		}
	}
	return monitor.AccountQuotaRefreshResult{Accounts: result}, nil
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

func (apiTestRunner) VerifyBrowserCredential(_ context.Context, target monitor.TargetInput) (monitor.Credential, error) {
	switch target.Kind {
	case monitor.TargetKindNewAPI:
		if target.Credential.Cookie != "session=oauth-cookie" {
			return monitor.Credential{}, errors.New("网页登录会话无效")
		}
		return monitor.Credential{Cookie: target.Credential.Cookie, UserID: "42"}, nil
	case monitor.TargetKindSub2API:
		if target.Credential.AccessToken != "oauth-access" {
			return monitor.Credential{}, errors.New("网页登录令牌无效")
		}
		return target.Credential, nil
	default:
		return monitor.Credential{}, errors.New("渠道不支持网页登录")
	}
}

func (apiTestRunner) ReadGroupMultipliers(_ context.Context, target monitor.TargetInput) (monitor.GroupMultiplierResult, error) {
	if target.Kind != monitor.TargetKindNewAPI && target.Kind != monitor.TargetKindSub2API {
		return monitor.GroupMultiplierResult{}, &monitor.CheckError{Kind: monitor.ErrorClassConfig, Message: "渠道不支持倍率监控"}
	}
	return monitor.GroupMultiplierResult{Groups: []monitor.GroupMultiplier{
		{Key: "default", Name: "默认分组", Description: "默认可用分组", Multiplier: decimal.NewFromInt(1)},
		{Key: "vip", Name: "会员分组", Multiplier: decimal.RequireFromString("0.333333")},
	}}, nil
}

func (apiTestRunner) ReadGroupPrices(_ context.Context, target monitor.TargetInput, groupKey string) (monitor.GroupPriceResult, error) {
	if (target.Kind != monitor.TargetKindNewAPI && target.Kind != monitor.TargetKindSub2API) || groupKey != "default" {
		return monitor.GroupPriceResult{}, &monitor.CheckError{Kind: monitor.ErrorClassConfig, Message: "分组价格不可用"}
	}
	return monitor.GroupPriceResult{Catalog: monitor.GroupPriceCatalog{
		GroupKey: groupKey, GroupName: "默认分组", Multiplier: decimal.RequireFromString("0.5"),
		Models: []monitor.GroupModelPrice{{
			Name: "gpt-price", BillingMode: "tiered",
			Prices: []monitor.GroupPriceItem{{Key: "input", Label: "输入", Value: decimal.RequireFromString("1.25"), Unit: "USD/百万令牌"}},
			Intervals: []monitor.GroupPriceInterval{{
				Label: "长上下文", Condition: "上下文 Token > 200000", MinTokens: "200001", Prices: []monitor.GroupPriceItem{{
					Key: "output", Label: "输出", Value: decimal.RequireFromString("7.5"), Unit: "USD/百万令牌",
				}},
			}},
		}},
	}}, nil
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
	storedWithoutSubscription, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取未启用订阅监控的渠道失败: %v", err)
	}
	var configWithoutSubscription storedTargetConfig
	if err := json.Unmarshal([]byte(storedWithoutSubscription.ConfigJSON), &configWithoutSubscription); err != nil {
		t.Fatalf("解析渠道监控配置失败: %v", err)
	}
	if configWithoutSubscription.NewAPI.IncludeSubscription {
		t.Fatal("未提交订阅阈值时不应读取订阅额度")
	}
	if _, exists := configWithoutSubscription.Thresholds[monitor.MetricSubscriptionBalance]; exists {
		t.Fatal("未提交订阅阈值时不应保存订阅告警配置")
	}
	if threshold, exists := configWithoutSubscription.Thresholds[monitor.MetricWalletBalance]; !exists || !threshold.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("钱包阈值应保持有效: %s", threshold)
	}
	oldSubscriptionThreshold := decimal.Zero
	oldMetrics, err := json.Marshal([]monitor.Metric{
		{Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(5), Unit: "元"},
		{Key: monitor.MetricSubscriptionBalance, Label: "订阅余额", Value: decimal.Zero, Unit: "元", Threshold: &oldSubscriptionThreshold},
	})
	if err != nil {
		t.Fatalf("编码旧渠道快照失败: %v", err)
	}
	if err := database.InsertSnapshot(context.Background(), &store.Snapshot{
		TargetID: created.ID, ObservedAt: time.Now().UTC().Add(-time.Minute), Status: string(monitor.TargetStatusWarning), MetricsJSON: string(oldMetrics),
	}); err != nil {
		t.Fatalf("保存旧渠道快照失败: %v", err)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+created.ID, nil, "")
	if status != http.StatusOK || strings.Contains(body, "subscription_balance") || strings.Contains(body, `"threshold":"0"`) {
		t.Fatalf("关闭订阅监控后仍返回旧订阅指标或阈值: %d %s", status, body)
	}
	createBody["thresholds"] = []map[string]string{
		{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"},
		{"key": "subscription_balance", "label": "订阅余额", "value": "0", "unit": "元"},
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK {
		t.Fatalf("启用订阅监控失败: %d %s", status, body)
	}
	walletOnlyMetrics, err := json.Marshal([]monitor.Metric{
		{Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(5), Unit: "元"},
	})
	if err != nil {
		t.Fatalf("编码缺少订阅数据的快照失败: %v", err)
	}
	if err := database.InsertSnapshot(context.Background(), &store.Snapshot{
		TargetID: created.ID, ObservedAt: time.Now().UTC().Add(-30 * time.Second), Status: string(monitor.TargetStatusHealthy), MetricsJSON: string(walletOnlyMetrics),
	}); err != nil {
		t.Fatalf("保存缺少订阅数据的快照失败: %v", err)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+created.ID, nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"key":"subscription_balance"`) || !strings.Contains(body, `"threshold":"0"`) {
		t.Fatalf("已配置但尚未读取到的订阅指标未保留: %d %s", status, body)
	}
	if err := database.CreateAlert(context.Background(), store.Alert{
		ID: "alert_disabled_subscription", TargetID: created.ID, Type: string(monitor.AlertTypeQuotaLow),
		MetricKey: string(monitor.MetricSubscriptionBalance), State: "open", Title: "订阅余额不足", OpenedAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("创建待关闭告警的订阅事件失败: %v", err)
	}
	createBody["thresholds"] = []map[string]any{
		{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"},
		{"key": "subscription_balance", "label": "订阅余额", "value": "", "unit": "元", "alertEnabled": false},
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK {
		t.Fatalf("仅关闭订阅额度告警失败: %d %s", status, body)
	}
	storedSubscriptionDisplay, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取仅展示订阅额度的渠道失败: %v", err)
	}
	var subscriptionDisplayConfig storedTargetConfig
	if err := json.Unmarshal([]byte(storedSubscriptionDisplay.ConfigJSON), &subscriptionDisplayConfig); err != nil ||
		!subscriptionDisplayConfig.NewAPI.IncludeSubscription {
		t.Fatalf("关闭告警后仍应读取订阅额度: %s, %v", storedSubscriptionDisplay.ConfigJSON, err)
	}
	if _, exists := subscriptionDisplayConfig.Thresholds[monitor.MetricSubscriptionBalance]; exists {
		t.Fatalf("关闭告警后不应保留订阅生效阈值: %s", storedSubscriptionDisplay.ConfigJSON)
	}
	var subscriptionDisplayTarget targetResponse
	if err := json.Unmarshal([]byte(body), &subscriptionDisplayTarget); err != nil {
		t.Fatalf("解析仅展示订阅额度的渠道失败: %v", err)
	}
	var subscriptionDisplayMetric *metricResponse
	for index := range subscriptionDisplayTarget.Metrics {
		if subscriptionDisplayTarget.Metrics[index].Key == string(monitor.MetricSubscriptionBalance) {
			subscriptionDisplayMetric = &subscriptionDisplayTarget.Metrics[index]
			break
		}
	}
	if subscriptionDisplayMetric == nil || subscriptionDisplayMetric.AlertEnabled || subscriptionDisplayMetric.Threshold != "" || subscriptionDisplayMetric.AlertThreshold != "0" {
		t.Fatalf("关闭告警后订阅额度展示配置不正确: %#v", subscriptionDisplayMetric)
	}
	disabledAlert, err := database.AlertByID(context.Background(), "alert_disabled_subscription")
	if err != nil || disabledAlert.State != "resolved" || disabledAlert.RecoveredAt == nil {
		t.Fatalf("关闭已有指标告警后应立即解决活跃事件：%#v, %v", disabledAlert, err)
	}
	createBody["thresholds"] = []map[string]any{
		{"key": "wallet_balance", "label": "钱包余额", "value": "", "unit": "元", "alertEnabled": false},
		{"key": "subscription_balance", "label": "订阅余额", "value": "", "unit": "元", "alertEnabled": false},
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK || !strings.Contains(body, `"key":"wallet_balance"`) || !strings.Contains(body, `"alertThreshold":"10"`) {
		t.Fatalf("关闭告警并清空阈值后未保留原配置值: %d %s", status, body)
	}
	createBody["thresholds"] = []map[string]string{
		{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"},
		{"key": "subscription_balance", "label": "订阅余额", "value": "0", "unit": "元"},
	}
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/targets/"+created.ID, createBody, "")
	if status != http.StatusOK {
		t.Fatalf("重新开启订阅额度告警失败: %d %s", status, body)
	}
	if err := database.CreateAlert(context.Background(), store.Alert{
		ID: "alert_removed_subscription", TargetID: created.ID, Type: string(monitor.AlertTypeQuotaLow),
		MetricKey: string(monitor.MetricSubscriptionBalance), State: "open", Title: "订阅余额不足", OpenedAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("创建待清理的订阅告警失败: %v", err)
	}
	if err := database.UpdateTargetCheck(context.Background(), created.ID, string(monitor.TargetStatusWarning), 2, "旧检测错误", time.Now().UTC()); err != nil {
		t.Fatalf("准备旧渠道状态失败: %v", err)
	}
	createBody["thresholds"] = []map[string]string{{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"}}

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
	if stored.Status != string(monitor.TargetStatusUnknown) || stored.FailureCount != 0 || stored.LastError != "" || !stored.LastCheckedAt.IsZero() {
		t.Fatalf("取消订阅监控后渠道应等待立即重检: %#v", stored)
	}
	if _, err := database.ActiveAlert(context.Background(), created.ID, string(monitor.AlertTypeQuotaLow), string(monitor.MetricSubscriptionBalance)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("取消订阅监控后不应保留订阅告警: %v", err)
	}
	if _, err := database.LatestSnapshot(context.Background(), created.ID); err != nil {
		t.Fatalf("取消订阅监控不应删除历史快照: %v", err)
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

func TestEmailSettingsAPIEncryptsPasswordAndPreservesBlankValue(t *testing.T) {
	testServer, database, _ := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	if status, body := requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, ""); status != http.StatusCreated {
		t.Fatalf("准备管理员失败: %d %s", status, body)
	}
	payload := map[string]any{
		"enabled": true, "provider": "qq", "host": "smtp.qq.com", "port": 465, "security": "tls",
		"username": "sender@example.com", "password": "smtp-api-secret", "fromName": "号池监控",
		"fromAddress": "sender@example.com", "recipients": []string{"receiver@example.com"},
	}
	status, body := requestJSON(t, client, http.MethodPut, testServer.URL+"/api/email", payload, "")
	if status != http.StatusOK || !strings.Contains(body, `"passwordConfigured":true`) || strings.Contains(body, "smtp-api-secret") {
		t.Fatalf("保存邮箱配置响应不正确: %d %s", status, body)
	}
	encrypted, err := database.GetSetting(context.Background(), "smtp_config_enc")
	if err != nil || strings.Contains(encrypted, "smtp-api-secret") || strings.Contains(encrypted, "sender@example.com") {
		t.Fatalf("邮箱配置没有完整加密: %q, %v", encrypted, err)
	}
	payload["password"] = ""
	payload["fromName"] = "新的发件人"
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/email", payload, "")
	if status != http.StatusOK || !strings.Contains(body, `"passwordConfigured":true`) {
		t.Fatalf("空密码沿用失败: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/email", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"fromName":"新的发件人"`) || strings.Contains(body, `"password":`) {
		t.Fatalf("读取邮箱配置响应不正确: %d %s", status, body)
	}
	payload["username"] = "other@example.com"
	status, body = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/email", payload, "")
	if status != http.StatusBadRequest || !strings.Contains(body, "重新填写授权码") {
		t.Fatalf("更换发件账号时没有要求新授权码: %d %s", status, body)
	}
	payload["username"] = "sender@example.com"
	payload["security"] = "plain"
	status, _ = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/email/test", payload, "")
	if status != http.StatusBadRequest {
		t.Fatalf("测试接口没有拒绝明文 SMTP: %d", status)
	}
	status, _ = requestJSON(t, client, http.MethodPut, testServer.URL+"/api/email", payload, "https://evil.example")
	if status != http.StatusForbidden {
		t.Fatalf("跨站修改邮箱配置未被拒绝: %d", status)
	}
	status, _ = requestJSON(t, client, http.MethodDelete, testServer.URL+"/api/email", nil, "https://evil.example")
	if status != http.StatusForbidden {
		t.Fatalf("跨站清除邮箱配置未被拒绝: %d", status)
	}
	status, body = requestJSON(t, client, http.MethodDelete, testServer.URL+"/api/email", nil, "")
	if status != http.StatusOK || !strings.Contains(body, `"provider":"qq"`) ||
		!strings.Contains(body, `"host":"smtp.qq.com"`) || !strings.Contains(body, `"passwordConfigured":false`) {
		t.Fatalf("清除邮箱配置后没有恢复默认状态: %d %s", status, body)
	}
	stored, err := database.GetSetting(context.Background(), "smtp_config_enc")
	if err != nil || stored != "" {
		t.Fatalf("清除邮箱配置后仍保留加密凭据: %q, %v", stored, err)
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

func TestTargetAuthAttemptCaptureAndConsume(t *testing.T) {
	testServer, database, vault := newAPITestServerWithPrivateTargets(t, true)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar

	status, body := requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("首次设置失败: %d %s", status, body)
	}
	baseURL := "http://127.0.0.1:18080"
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/target-auth/attempts", map[string]any{
		"kind": "new_api", "baseUrl": baseURL,
	}, "")
	if status != http.StatusCreated || strings.Contains(body, "captureToken") || strings.Contains(body, "oauth-cookie") {
		t.Fatalf("创建网页登录任务失败或泄漏秘密: %d %s", status, body)
	}
	var attempt targetAuthAttemptResponse
	if err := json.Unmarshal([]byte(body), &attempt); err != nil || !strings.HasPrefix(attempt.ID, "auth_") || attempt.Status != "waiting" {
		t.Fatalf("网页登录任务响应无效: %#v, %v", attempt, err)
	}

	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/target-auth/native/"+attempt.ID, nil, "")
	if status != http.StatusOK {
		t.Fatalf("读取原生网页登录票据失败: %d %s", status, body)
	}
	var native nativeTargetAuthAttemptResponse
	if err := json.Unmarshal([]byte(body), &native); err != nil || native.CaptureToken == "" || native.BaseURL != baseURL {
		t.Fatalf("原生网页登录票据无效: %#v, %v", native, err)
	}
	status, _ = requestJSONWithHeaders(t, client, http.MethodPost, testServer.URL+"/api/target-auth/native/"+attempt.ID+"/capture", map[string]any{
		"cookie": "session=oauth-cookie",
	}, "", map[string]string{"X-Target-Auth-Token": "wrong-token"})
	if status != http.StatusUnauthorized {
		t.Fatalf("错误捕获票据应被拒绝: %d", status)
	}
	status, body = requestJSONWithHeaders(t, client, http.MethodPost, testServer.URL+"/api/target-auth/native/"+attempt.ID+"/capture", map[string]any{
		"cookie": "session=oauth-cookie",
	}, "", map[string]string{"X-Target-Auth-Token": native.CaptureToken})
	if status != http.StatusOK || strings.Contains(body, "oauth-cookie") || strings.Contains(body, native.CaptureToken) {
		t.Fatalf("捕获网页登录会话失败或响应泄漏秘密: %d %s", status, body)
	}
	var ready targetAuthAttemptResponse
	if err := json.Unmarshal([]byte(body), &ready); err != nil || ready.Status != "ready" || ready.UserID != "42" {
		t.Fatalf("网页登录完成状态无效: %#v, %v", ready, err)
	}

	createBody := map[string]any{
		"name": "OAuth 主站", "kind": "new_api", "baseUrl": baseURL, "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "username": "", "email": "", "password": "",
		"totpCode": "", "totpSecret": "", "accessToken": "", "refreshToken": "", "adminKey": "", "userId": "",
		"credentialMode": "browser_session", "cookie": "", "browserAuthAttemptId": attempt.ID,
		"authType": "bearer", "requestMethod": "GET", "confirmPost": false, "customHeaders": "{}",
		"jsonPointer": "/data/balance", "statusPointer": "",
		"thresholds": []map[string]string{{"key": "wallet_balance", "label": "钱包余额", "value": "10", "unit": "元"}},
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", createBody, "")
	if status != http.StatusCreated || strings.Contains(body, "oauth-cookie") || !strings.Contains(body, `"credentialMode":"browser_session"`) {
		t.Fatalf("使用网页登录任务创建渠道失败: %d %s", status, body)
	}
	var created targetResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析渠道响应失败: %v", err)
	}
	stored, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取网页登录渠道失败: %v", err)
	}
	decoded, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(decoded), "oauth-cookie") || !strings.Contains(string(decoded), `"user_id":"42"`) {
		t.Fatalf("网页登录凭据未正确加密保存: %s, %v", decoded, err)
	}
	status, _ = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/target-auth/attempts/"+attempt.ID, nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("保存渠道后网页登录任务应被消费: %d", status)
	}
}

func TestCapturedCredentialRejectsInjectedSecrets(t *testing.T) {
	if _, err := capturedCredential(monitor.TargetKindNewAPI, targetAuthCaptureRequest{Cookie: "session=ok\r\nX-Test: injected"}); err == nil {
		t.Fatal("含换行的 Cookie 应被拒绝")
	}
	if _, err := capturedCredential(monitor.TargetKindSub2API, targetAuthCaptureRequest{AccessToken: strings.Repeat("a", maxImportedTokenBytes+1)}); err == nil {
		t.Fatal("超长网页登录令牌应被拒绝")
	}
}

func TestCapturedCredentialAllowsSub2APIRefreshTokenOnly(t *testing.T) {
	credential, err := capturedCredential(monitor.TargetKindSub2API, targetAuthCaptureRequest{RefreshToken: "oauth-refresh-only"})
	if err != nil {
		t.Fatalf("仅包含刷新令牌的 Sub2API 网页凭据应被接受: %v", err)
	}
	if credential.AccessToken != "" || credential.RefreshToken != "oauth-refresh-only" {
		t.Fatalf("Sub2API 刷新令牌凭据解析结果不正确: %#v", credential)
	}
}

func TestCLIProxyAPITargetPersistsComparisonAndManagementKey(t *testing.T) {
	testServer, database, vault := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	status, body := requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("首次设置失败: %d %s", status, body)
	}

	draft := map[string]any{
		"name": "CLIProxyAPI", "kind": "cliproxyapi", "baseUrl": "https://cli.example.com", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "adminKey": "management-secret",
		"thresholds": []map[string]any{
			{"key": "healthy_accounts", "label": "可用账号", "value": "1", "unit": "个", "comparison": "lte"},
			{"key": "limited_accounts", "label": "限流账号", "value": "", "unit": "个", "comparison": "gte", "alertEnabled": false},
			{"key": "error_accounts", "label": "异常账号", "value": "1", "unit": "个", "comparison": "gte"},
		},
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", draft, "")
	if status != http.StatusCreated || strings.Contains(body, "management-secret") || !strings.Contains(body, `"comparison":"gte"`) {
		t.Fatalf("创建 CLIProxyAPI 渠道失败或响应不安全: %d %s", status, body)
	}
	var created targetResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Kind != string(monitor.TargetKindCLIProxyAPI) || !created.AuthConfigured {
		t.Fatalf("CLIProxyAPI 渠道响应不正确：%#v, %v", created, err)
	}
	status, _ = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+created.ID+"/accounts/quota/refresh", map[string]any{
		"accountIds": []string{},
	}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("空账号额度刷新请求应被拒绝: %d", status)
	}
	tooManyAccountIDs := make([]string, monitor.MaxAccountQuotaRefreshAccounts+1)
	for index := range tooManyAccountIDs {
		tooManyAccountIDs[index] = monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, fmt.Sprintf("account-%d", index))
	}
	status, _ = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+created.ID+"/accounts/quota/refresh", map[string]any{
		"accountIds": tooManyAccountIDs,
	}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("超过一百个账号的额度刷新请求应被拒绝: %d", status)
	}
	readyID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "api-ready")
	unavailableID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "api-unavailable")
	unsupportedID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "api-unsupported")
	otherPageID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "api-other-page")
	if err := database.ReplaceChatAccounts(context.Background(), created.ID, []store.ChatAccount{
		{TargetID: created.ID, ExternalID: readyID, Provider: "codex", Type: "free", Status: string(monitor.TargetStatusHealthy), StatusText: "可用"},
		{TargetID: created.ID, ExternalID: unavailableID, Provider: "codex", Type: "plus", Status: string(monitor.TargetStatusWarning), StatusText: "参数警告"},
		{TargetID: created.ID, ExternalID: unsupportedID, Provider: "claude", Type: "api_key", Status: string(monitor.TargetStatusHealthy), StatusText: "可用"},
		{
			TargetID: created.ID, ExternalID: otherPageID, Provider: "codex", Type: "plus", Status: string(monitor.TargetStatusHealthy),
			QuotaState:   monitor.AccountQuotaStateAvailable,
			QuotaWindows: []store.AccountQuotaWindow{{Key: "code-5h", Label: "5 小时", RemainingPercent: "33"}},
		},
	}); err != nil {
		t.Fatalf("保存接口测试账号失败: %v", err)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+created.ID+"/accounts/quota/refresh", map[string]any{
		"accountIds": []string{readyID, unavailableID, unsupportedID},
	}, "")
	if status != http.StatusOK || strings.Contains(body, "api-ready") || strings.Contains(body, "api-unavailable") {
		t.Fatalf("刷新当前页额度接口失败或泄漏上游标识: %d %s", status, body)
	}
	var quotaResult accountQuotaRefreshResponse
	if err := json.Unmarshal([]byte(body), &quotaResult); err != nil || len(quotaResult.Accounts) != 3 ||
		quotaResult.RefreshedCount != 1 || quotaResult.UnavailableCount != 1 || quotaResult.UnsupportedCount != 1 {
		t.Fatalf("额度刷新统计不正确：%#v, %v", quotaResult, err)
	}
	storedAccounts, err := database.ListChatAccounts(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取接口刷新后的账号失败: %v", err)
	}
	storedByID := make(map[string]store.ChatAccount, len(storedAccounts))
	for _, account := range storedAccounts {
		storedByID[account.ExternalID] = account
	}
	if storedByID[readyID].Status != string(monitor.TargetStatusHealthy) || storedByID[readyID].Type != "plus" ||
		storedByID[readyID].QuotaState != monitor.AccountQuotaStateAvailable {
		t.Fatalf("额度刷新不应覆盖账号健康状态：%#v", storedByID[readyID])
	}
	if storedByID[otherPageID].QuotaState != monitor.AccountQuotaStateAvailable || len(storedByID[otherPageID].QuotaWindows) != 1 ||
		storedByID[otherPageID].QuotaWindows[0].RemainingPercent != "33" {
		t.Fatalf("其他页面账号额度被意外修改：%#v", storedByID[otherPageID])
	}
	stored, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取 CLIProxyAPI 渠道失败: %v", err)
	}
	var config storedTargetConfig
	if err := json.Unmarshal([]byte(stored.ConfigJSON), &config); err != nil ||
		config.ThresholdComparisons[monitor.MetricErrorAccounts] != monitor.ThresholdComparisonGTE {
		t.Fatalf("比较方向未正确保存：%s, %v", stored.ConfigJSON, err)
	}
	if _, exists := config.Thresholds[monitor.MetricLimitedAccounts]; exists {
		t.Fatalf("关闭告警的限流指标不应生成生效阈值：%s", stored.ConfigJSON)
	}
	if len(config.ThresholdMeta) != 3 || thresholdAlertEnabled(config.ThresholdMeta[1]) {
		t.Fatalf("关闭告警的限流指标配置未保留：%s", stored.ConfigJSON)
	}
	var limitedMetric *metricResponse
	for index := range created.Metrics {
		if created.Metrics[index].Key == string(monitor.MetricLimitedAccounts) {
			limitedMetric = &created.Metrics[index]
			break
		}
	}
	if limitedMetric == nil || limitedMetric.AlertEnabled || limitedMetric.Threshold != "" || limitedMetric.AlertThreshold != "0" {
		t.Fatalf("关闭告警的限流指标仍应返回展示配置：%#v", limitedMetric)
	}
	credentialJSON, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(credentialJSON), "management-secret") {
		t.Fatalf("管理密钥未加密保存: %v", err)
	}

	draft["adminKey"] = ""
	draft["thresholds"] = []map[string]string{{
		"key": "error_accounts", "label": "异常账号", "value": "1", "unit": "个", "comparison": "invalid",
	}}
	status, _ = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", draft, "")
	if status != http.StatusBadRequest {
		t.Fatalf("无效比较方向应被拒绝: %d", status)
	}
}

func TestSub2APITargetReturnsAccountPoolAndRefreshesAbsoluteQuota(t *testing.T) {
	testServer, database, vault := newAPITestServer(t)
	defer testServer.Close()
	defer database.Close()
	client := testServer.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	status, body := requestJSON(t, client, http.MethodPost, testServer.URL+"/api/setup", map[string]any{
		"initializationToken": "setup-token", "username": "admin", "password": "long-password-123",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("首次设置失败: %d %s", status, body)
	}
	draft := map[string]any{
		"name": "Sub2API 号池", "kind": "sub2api", "baseUrl": "https://sub2.example.com", "topupUrl": "",
		"enabled": true, "checkIntervalMinutes": 5, "email": "user@example.com", "password": "password-123",
		"credentialMode": "password", "adminKey": "sub2-admin-secret",
		"thresholds": []map[string]any{{
			"key": "wallet_balance", "label": "钱包余额", "value": "5", "unit": "USD",
		}},
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets", draft, "")
	if status != http.StatusCreated || strings.Contains(body, "sub2-admin-secret") {
		t.Fatalf("创建 Sub2API 号池失败或响应泄漏管理密钥: %d %s", status, body)
	}
	var created targetResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Kind != string(monitor.TargetKindSub2API) {
		t.Fatalf("Sub2API 渠道响应不正确：%#v, %v", created, err)
	}
	publicID := monitor.PublicAccountID(monitor.TargetKindSub2API, "api-ready")
	if err := database.ReplaceChatAccounts(context.Background(), created.ID, []store.ChatAccount{{
		TargetID: created.ID, ExternalID: publicID, DisplayName: "主账号", Provider: "openai", Type: "plus",
		Status: string(monitor.TargetStatusHealthy), StatusText: "可用",
	}}); err != nil {
		t.Fatalf("保存 Sub2API 接口测试账号失败: %v", err)
	}
	status, body = requestJSON(t, client, http.MethodGet, testServer.URL+"/api/targets/"+created.ID, nil, "")
	if status != http.StatusOK || !strings.Contains(body, publicID) || strings.Contains(body, "api-ready") {
		t.Fatalf("Sub2API 渠道详情未返回脱敏账号池: %d %s", status, body)
	}
	status, body = requestJSON(t, client, http.MethodPost, testServer.URL+"/api/targets/"+created.ID+"/accounts/quota/refresh", map[string]any{
		"accountIds": []string{publicID},
	}, "")
	if status != http.StatusOK || strings.Contains(body, "api-ready") {
		t.Fatalf("刷新 Sub2API 账号额度失败或泄漏上游标识: %d %s", status, body)
	}
	var refreshed accountQuotaRefreshResponse
	if err := json.Unmarshal([]byte(body), &refreshed); err != nil || refreshed.RefreshedCount != 1 ||
		len(refreshed.Accounts) != 1 || len(refreshed.Accounts[0].QuotaWindows) != 1 ||
		refreshed.Accounts[0].QuotaWindows[0].RemainingValue != "18" ||
		refreshed.Accounts[0].QuotaWindows[0].LimitValue != "25" ||
		refreshed.Accounts[0].QuotaWindows[0].Unit != "USD" {
		t.Fatalf("Sub2API 额度刷新响应不完整：%#v, %v", refreshed, err)
	}
	stored, err := database.TargetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取 Sub2API 渠道失败: %v", err)
	}
	decoded, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(decoded), "sub2-admin-secret") || strings.Contains(body, "sub2-admin-secret") {
		t.Fatalf("Sub2API 管理密钥没有仅加密保存: %v", err)
	}
}

func TestCLIProxyAPIAccountResponseOmitsImageQuota(t *testing.T) {
	account := store.ChatAccount{
		ExternalID: "hashed-account", Provider: "codex", Type: "oauth", Status: string(monitor.TargetStatusHealthy), Quota: 77,
		QuotaState: monitor.AccountQuotaStateAvailable, QuotaWindows: []store.AccountQuotaWindow{{
			Key: "code-5h", Label: "5 小时", RemainingPercent: "75", ResetAt: "2026-07-20T09:00:00Z",
		}},
		SubscriptionExpiresAt: "2026-08-20T08:00:00Z",
	}
	cliResponse := mapAccountResponse(string(monitor.TargetKindCLIProxyAPI), account)
	cliPayload, err := json.Marshal(cliResponse)
	if err != nil {
		t.Fatalf("序列化 CLIProxyAPI 账号响应失败: %v", err)
	}
	if strings.Contains(string(cliPayload), "imageQuota") {
		t.Fatalf("CLIProxyAPI 账号响应不应包含图片额度：%s", cliPayload)
	}
	if cliResponse.QuotaState != monitor.AccountQuotaStateAvailable || cliResponse.SubscriptionExpiresAt != "2026-08-20T08:00:00Z" ||
		len(cliResponse.QuotaWindows) != 1 || cliResponse.QuotaWindows[0].RemainingPercent != "75" {
		t.Fatalf("CLIProxyAPI 账号额度响应不完整：%#v", cliResponse)
	}

	chatResponse := mapAccountResponse(string(monitor.TargetKindChatGPT2API), account)
	chatPayload, err := json.Marshal(chatResponse)
	if err != nil {
		t.Fatalf("序列化 chatgpt2api 账号响应失败: %v", err)
	}
	if !strings.Contains(string(chatPayload), `"imageQuota":"77"`) {
		t.Fatalf("chatgpt2api 账号响应应继续包含图片额度：%s", chatPayload)
	}
	if strings.Contains(string(chatPayload), "quotaState") || strings.Contains(string(chatPayload), "quotaWindows") ||
		strings.Contains(string(chatPayload), "subscriptionExpiresAt") {
		t.Fatalf("chatgpt2api 账号响应不应包含 CLIProxyAPI 额度字段：%s", chatPayload)
	}

	sub2Account := account
	sub2Account.QuotaWindows[0].RemainingValue = "18"
	sub2Account.QuotaWindows[0].LimitValue = "25"
	sub2Account.QuotaWindows[0].Unit = "USD"
	sub2Response := mapAccountResponse(string(monitor.TargetKindSub2API), sub2Account)
	sub2Payload, err := json.Marshal(sub2Response)
	if err != nil {
		t.Fatalf("序列化 Sub2API 账号响应失败: %v", err)
	}
	if strings.Contains(string(sub2Payload), "imageQuota") || len(sub2Response.QuotaWindows) != 1 ||
		sub2Response.QuotaWindows[0].RemainingValue != "18" || sub2Response.QuotaWindows[0].LimitValue != "25" ||
		sub2Response.QuotaWindows[0].Unit != "USD" {
		t.Fatalf("Sub2API 账号额度响应不完整或包含无关字段：%s", sub2Payload)
	}
}

func TestSub2APIBrowserImportPreservesAdminKey(t *testing.T) {
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("创建测试保险箱失败: %v", err)
	}
	authAttempts := newTargetAuthAttemptStore(vault)
	server := &Server{dependencies: Dependencies{Vault: vault}, targetAuth: authAttempts}
	baseURL := "https://sub2.example.com"
	attempt, err := authAttempts.create(42, monitor.TargetKindSub2API, baseURL, baseURL+"/login")
	if err != nil {
		t.Fatalf("创建 Sub2API 网页登录任务失败: %v", err)
	}
	_, captureToken, err := authAttempts.native(attempt.ID)
	if err != nil {
		t.Fatalf("读取 Sub2API 网页登录票据失败: %v", err)
	}
	if _, err := authAttempts.markReady(attempt.ID, captureToken, monitor.Credential{
		AccessToken: "browser-access", RefreshToken: "browser-refresh",
	}); err != nil {
		t.Fatalf("保存 Sub2API 网页登录凭据失败: %v", err)
	}
	existingJSON, _ := json.Marshal(monitor.Credential{
		Email: "old@example.com", Password: "old-password", AdminKey: "existing-admin-key",
	})
	existingEncrypted, err := vault.Encrypt(existingJSON)
	if err != nil {
		t.Fatalf("加密原 Sub2API 凭据失败: %v", err)
	}
	existing := &store.Target{
		Kind: string(monitor.TargetKindSub2API), BaseURL: baseURL,
		ConfigJSON: `{"credential_mode":"password"}`, CredentialsEnc: existingEncrypted,
	}
	ctx := context.WithValue(context.Background(), adminContextKey, store.Admin{ID: 42})
	credential, mode, err := server.mergeCredential(ctx, targetDraft{
		CredentialMode: credentialModeSub2APIBrowserOAuth, BrowserAuthAttemptID: attempt.ID,
	}, existing, monitor.TargetKindSub2API, baseURL)
	if err != nil {
		t.Fatalf("合并 Sub2API 网页登录凭据失败: %v", err)
	}
	if mode != credentialModeSub2APIBrowserOAuth || credential.AccessToken != "browser-access" ||
		credential.RefreshToken != "browser-refresh" || credential.AdminKey != "existing-admin-key" {
		t.Fatalf("切换网页登录后丢失 Sub2API 管理密钥：%#v", credential)
	}
}

func TestSub2APIChangingOriginDoesNotReuseAdminKey(t *testing.T) {
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("创建测试保险箱失败: %v", err)
	}
	server := &Server{dependencies: Dependencies{Vault: vault}}
	existingJSON, _ := json.Marshal(monitor.Credential{
		Email: "old@example.com", Password: "old-password", AdminKey: "old-origin-admin-key",
	})
	existingEncrypted, err := vault.Encrypt(existingJSON)
	if err != nil {
		t.Fatalf("加密原 Sub2API 凭据失败: %v", err)
	}
	existing := &store.Target{
		Kind: string(monitor.TargetKindSub2API), BaseURL: "https://old.example.com",
		ConfigJSON: `{"credential_mode":"password"}`, CredentialsEnc: existingEncrypted,
	}
	credential, mode, err := server.mergeCredential(context.Background(), targetDraft{
		CredentialMode: credentialModeSub2APIPassword,
		Email:          "new@example.com",
		Password:       "new-password",
	}, existing, monitor.TargetKindSub2API, "https://new.example.com")
	if err != nil {
		t.Fatalf("更换 Sub2API 来源后合并新凭据失败: %v", err)
	}
	if mode != credentialModeSub2APIPassword || credential.AdminKey != "" {
		t.Fatalf("更换站点来源后不应复用旧管理员密钥：%#v", credential)
	}
}

func TestSub2APICredentialAcceptsOptionalAdminKey(t *testing.T) {
	credential, err := mergeSub2APICredential(monitor.Credential{}, targetDraft{
		Email: "user@example.com", Password: "password", AdminKey: "sub2-admin-key",
	}, credentialModeSub2APIPassword)
	if err != nil {
		t.Fatalf("合并 Sub2API 密码凭据失败: %v", err)
	}
	if credential.AdminKey != "sub2-admin-key" {
		t.Fatalf("Sub2API 可选管理密钥没有进入连接配置：%#v", credential)
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
	return newAPITestServerWithPrivateTargets(t, false)
}

func newAPITestServerWithPrivateTargets(t *testing.T, allowPrivateTargets bool) (*httptest.Server, *store.Store, *secure.Vault) {
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
	emailService := mailnotify.NewService(database, vault, "", allowPrivateTargets)
	alertEngine := alerts.NewEngine(database, nil)
	schedulerService := scheduler.NewService(database, vault, apiTestRunner{}, alertEngine, allowPrivateTargets)
	server := NewServer(Dependencies{
		Store: database, Vault: vault, Auth: authService, Scheduler: schedulerService,
		Push: pushService, Email: emailService, Events: hub, Static: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusOK)
		}),
		AllowPrivateTargets: allowPrivateTargets,
	})
	return httptest.NewServer(server.Handler()), database, vault
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint string, value any, origin string) (int, string) {
	return requestJSONWithHeaders(t, client, method, endpoint, value, origin, nil)
}

func requestJSONWithHeaders(t *testing.T, client *http.Client, method, endpoint string, value any, origin string, headers map[string]string) (int, string) {
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
	for name, headerValue := range headers {
		request.Header.Set(name, headerValue)
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
