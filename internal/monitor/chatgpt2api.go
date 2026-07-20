package monitor

import (
	"context"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
)

type chatGPT2APIAdapter struct {
	http *secureHTTPClient
}

func newChatGPT2APIAdapter(client *secureHTTPClient) *chatGPT2APIAdapter {
	return &chatGPT2APIAdapter{http: client}
}

func (adapter *chatGPT2APIAdapter) Kind() TargetKind {
	return TargetKindChatGPT2API
}

func (adapter *chatGPT2APIAdapter) Check(ctx context.Context, target TargetConfig) (Snapshot, error) {
	target = ensureTargetKind(target, adapter.Kind())
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	endpoint, err := joinTargetURL(target.BaseURL, "/health?format=json")
	if err != nil {
		return Snapshot{}, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, nil, nil, &payload); err != nil {
		return Snapshot{}, err
	}
	health, ok := payload.(map[string]any)
	if !ok {
		return Snapshot{}, checkError(ErrorClassResponse, "解析 chatgpt2api 健康状态", "chatgpt2api 响应格式无效", 0, nil)
	}
	accounts, ok := health["accounts"].(map[string]any)
	if !ok {
		return Snapshot{}, checkError(ErrorClassResponse, "解析 chatgpt2api 健康状态", "chatgpt2api 响应缺少账号汇总", 0, nil)
	}

	snapshot := newSnapshot(target)
	snapshot.Metrics = append(snapshot.Metrics,
		metricWithThreshold(target, MetricAccountTotal, "账号总数", decimal.NewFromInt(int64Field(accounts, "total")), "个"),
		metricWithThreshold(target, MetricHealthyAccounts, "正常账号", decimal.NewFromInt(int64Field(accounts, "active")), "个"),
		metricWithThreshold(target, MetricLimitedAccounts, "限流账号", decimal.NewFromInt(int64Field(accounts, "limited")), "个"),
		metricWithThreshold(target, MetricErrorAccounts, "异常账号", decimal.NewFromInt(int64Field(accounts, "abnormal")), "个"),
		metricWithThreshold(target, MetricDisabledAccounts, "禁用账号", decimal.NewFromInt(int64Field(accounts, "disabled")), "个"),
		metricWithThreshold(target, MetricImageQuota, "图片生成剩余额度", decimal.NewFromInt(int64Field(accounts, "total_quota")), "次"),
		metricWithThreshold(target, MetricAccountSuccess, "累计成功", decimal.NewFromInt(int64Field(accounts, "total_success")), "次"),
		metricWithThreshold(target, MetricAccountFail, "累计失败", decimal.NewFromInt(int64Field(accounts, "total_fail")), "次"),
	)
	healthy, hasHealthy := boolField(health, "healthy")
	if (hasHealthy && !healthy) || int64Field(accounts, "active") <= 0 {
		snapshot.Status = TargetStatusDegraded
		snapshot.Message = "chatgpt2api 当前没有可用账号"
	}

	includeAccounts := target.ChatGPT2API.IncludeAccounts || strings.TrimSpace(target.Credential.AdminKey) != ""
	if includeAccounts {
		if strings.TrimSpace(target.Credential.AdminKey) == "" {
			return Snapshot{}, checkError(ErrorClassConfig, "读取 chatgpt2api 账号明细", "读取账号明细需要管理员密钥", 0, nil)
		}
		details, err := adapter.readAccounts(ctx, session, target)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Accounts = details
	}
	return snapshot, nil
}

func (adapter *chatGPT2APIAdapter) readAccounts(ctx context.Context, session *requestSession, target TargetConfig) ([]AccountStatus, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/accounts")
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	setBearer(headers, target.Credential.AdminKey)
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return nil, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 chatgpt2api 账号明细", "账号明细响应格式无效", 0, nil)
	}
	items, ok := object["items"].([]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 chatgpt2api 账号明细", "账号明细响应缺少 items", 0, nil)
	}
	result := make([]AccountStatus, 0, len(items))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		quota, err := decimalField(raw, "quota")
		if err != nil || quota.IsNegative() {
			quota = decimal.Zero
		}
		// 这里只复制明确允许的展示字段，原始账号对象和任何 Token 都不会离开当前函数。
		recoveryAt := safeChatAccountField(raw, "restore_at", "recover_at", "reset_at")
		result = append(result, AccountStatus{
			Email:         safeChatAccountField(raw, "email"),
			Type:          safeChatAccountField(raw, "type", "plan_type"),
			Status:        safeChatAccountField(raw, "status"),
			Quota:         quota,
			RecoveryAt:    recoveryAt,
			RestoreAt:     recoveryAt,
			Success:       int64Field(raw, "success"),
			Fail:          int64Field(raw, "fail"),
			ImageInflight: int64Field(raw, "image_inflight"),
		})
	}
	return result, nil
}

func safeChatAccountField(account map[string]any, names ...string) string {
	value := stringField(account, names...)
	if value == "" {
		return ""
	}
	for _, secretName := range []string{"access_token", "accessToken", "refresh_token", "id_token", "password", "token"} {
		secret := stringField(account, secretName)
		if secret == "" {
			continue
		}
		if value == secret || (len(secret) >= 8 && strings.Contains(value, secret)) {
			return ""
		}
	}
	return value
}

var _ Adapter = (*chatGPT2APIAdapter)(nil)
