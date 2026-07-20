package monitor

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type cliProxyAPIAdapter struct {
	http *secureHTTPClient
}

func newCLIProxyAPIAdapter(client *secureHTTPClient) *cliProxyAPIAdapter {
	return &cliProxyAPIAdapter{http: client}
}

func (adapter *cliProxyAPIAdapter) Kind() TargetKind {
	return TargetKindCLIProxyAPI
}

func (adapter *cliProxyAPIAdapter) Check(ctx context.Context, target TargetConfig) (Snapshot, error) {
	target = ensureTargetKind(target, adapter.Kind())
	managementKey := strings.TrimSpace(target.Credential.AdminKey)
	if managementKey == "" {
		return Snapshot{}, checkError(ErrorClassConfig, "读取 CLIProxyAPI 账号", "CLIProxyAPI 需要管理密钥", 0, nil)
	}
	endpoint, err := joinTargetURL(target.BaseURL, "/v0/management/auth-files")
	if err != nil {
		return Snapshot{}, err
	}
	headers := make(http.Header)
	setBearer(headers, managementKey)
	var payload any
	if err := adapter.http.newSession(target.AllowPrivateNetwork).doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return Snapshot{}, err
	}
	accounts, err := parseCLIProxyAPIAccounts(payload, time.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}

	counts := map[TargetStatus]int64{
		TargetStatusHealthy:  0,
		TargetStatusWarning:  0,
		TargetStatusError:    0,
		TargetStatusDisabled: 0,
	}
	for _, account := range accounts {
		status := TargetStatus(account.Status)
		if status == TargetStatusUnknown {
			// 未知账号不具备可用性保证，因此归入异常数量，但明细仍保留未知状态供筛选。
			status = TargetStatusError
		}
		counts[status]++
	}
	snapshot := newSnapshot(target)
	snapshot.Accounts = accounts
	snapshot.Metrics = append(snapshot.Metrics,
		metricWithThreshold(target, MetricAccountTotal, "账号总数", decimal.NewFromInt(int64(len(accounts))), "个"),
		metricWithThreshold(target, MetricHealthyAccounts, "可用账号", decimal.NewFromInt(counts[TargetStatusHealthy]), "个"),
		metricWithThreshold(target, MetricLimitedAccounts, "限流账号", decimal.NewFromInt(counts[TargetStatusWarning]), "个"),
		metricWithThreshold(target, MetricErrorAccounts, "异常账号", decimal.NewFromInt(counts[TargetStatusError]), "个"),
		metricWithThreshold(target, MetricDisabledAccounts, "禁用账号", decimal.NewFromInt(counts[TargetStatusDisabled]), "个"),
	)
	if len(accounts) == 0 || counts[TargetStatusHealthy] == 0 {
		snapshot.Status = TargetStatusWarning
		snapshot.Message = "CLIProxyAPI 当前没有可用账号"
	}
	return snapshot, nil
}

func parseCLIProxyAPIAccounts(payload any, now time.Time) ([]AccountStatus, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 CLIProxyAPI 账号", "CLIProxyAPI 响应格式无效", 0, nil)
	}
	items, ok := root["files"].([]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 CLIProxyAPI 账号", "CLIProxyAPI 响应缺少 files", 0, nil)
	}
	result := make([]AccountStatus, 0, len(items))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		status, statusText, recoveryAt := classifyCLIProxyAPIAccount(raw, now)
		provider := safeCLIProxyAPIIdentifier(stringField(raw, "provider", "type"), 80)
		result = append(result, AccountStatus{
			// 上游标识只进入后续哈希流程，序列化时会被忽略。
			ExternalID:  stringField(raw, "auth_index", "id"),
			DisplayName: safeCLIProxyAPIText(stringField(raw, "label"), 120),
			Provider:    provider,
			Email:       safeCLIProxyAPIEmail(stringField(raw, "email")),
			Type:        safeCLIProxyAPIIdentifier(stringField(raw, "account_type"), 80),
			Status:      string(status),
			StatusText:  statusText,
			RecoveryAt:  recoveryAt,
			Success:     int64Field(raw, "success"),
			Fail:        int64Field(raw, "failed"),
		})
	}
	return result, nil
}

func classifyCLIProxyAPIAccount(account map[string]any, now time.Time) (TargetStatus, string, string) {
	status := strings.ToLower(strings.TrimSpace(stringField(account, "status")))
	disabled, _ := boolField(account, "disabled")
	if disabled || status == "disabled" {
		return TargetStatusDisabled, "已禁用", parseCLIProxyAPIRecoveryAt(account["next_retry_after"])
	}
	recoveryAt := parseCLIProxyAPIRecoveryAt(account["next_retry_after"])
	unavailable, _ := boolField(account, "unavailable")
	if unavailable || cliProxyAPIRetryPending(recoveryAt, now) {
		return TargetStatusWarning, "限流或冷却中", recoveryAt
	}
	switch status {
	case "active":
		return TargetStatusHealthy, "可用", recoveryAt
	case "pending":
		return TargetStatusWarning, "等待处理", recoveryAt
	case "refreshing":
		return TargetStatusWarning, "刷新中", recoveryAt
	case "error":
		return TargetStatusError, "异常", recoveryAt
	default:
		return TargetStatusUnknown, "状态未知", recoveryAt
	}
}

func parseCLIProxyAPIRecoveryAt(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil || parsed.IsZero() {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func cliProxyAPIRetryPending(recoveryAt string, now time.Time) bool {
	if recoveryAt == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, recoveryAt)
	return err == nil && parsed.After(now)
}

func safeCLIProxyAPIText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if looksLikeCLIProxyAPISecret(value) {
		return ""
	}
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(value))
	for _, marker := range []string{"token", "secret", "authorization", "password", "apikey", "cookie", "bearer"} {
		if strings.Contains(normalized, marker) {
			return ""
		}
	}
	characters := []rune(value)
	if maximum > 0 && len(characters) > maximum {
		value = string(characters[:maximum])
	}
	return value
}

func safeCLIProxyAPIEmail(value string) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if value == "" || len(characters) > 320 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func looksLikeCLIProxyAPISecret(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "sk-") || strings.Contains(lower, "sk_") {
		return true
	}
	parts := strings.Split(value, ".")
	if len(parts) == 3 && tokenPart(parts[0], 8) && tokenPart(parts[1], 8) && tokenPart(parts[2], 8) {
		return true
	}
	characters := []rune(value)
	if len(characters) < 32 || strings.IndexFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t' || character == '\r' || character == '\n'
	}) >= 0 {
		return false
	}
	unique := make(map[rune]struct{})
	for _, character := range characters {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_+/=.", character)) {
			return false
		}
		unique[character] = struct{}{}
	}
	return len(unique) >= 10
}

func tokenPart(value string, minimum int) bool {
	if len(value) < minimum {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func safeCLIProxyAPIIdentifier(value string, maximum int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if value == "" || maximum <= 0 || len(characters) > maximum {
		return ""
	}
	for _, character := range characters {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.:/+", character) {
			continue
		}
		return ""
	}
	return value
}

var _ Adapter = (*cliProxyAPIAdapter)(nil)
