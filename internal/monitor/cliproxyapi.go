package monitor

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type cliProxyAPIAdapter struct {
	http                *secureHTTPClient
	quotaRequestTimeout time.Duration
}

func newCLIProxyAPIAdapter(client *secureHTTPClient) *cliProxyAPIAdapter {
	return &cliProxyAPIAdapter{
		http: client, quotaRequestTimeout: cliProxyAPIQuotaAccountTimeout,
	}
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
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return Snapshot{}, err
	}
	accounts, _, err := parseCLIProxyAPIAccounts(payload, time.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	// 常规检测只读取账号健康状态；额度由详情页“刷新本页额度”按需读取。
	for index := range accounts {
		if !cliProxyAPIProviderHasQuotaEndpoint(normalizeCLIProxyAPIProvider(accounts[index].Provider)) {
			accounts[index].QuotaState = AccountQuotaStateUnsupported
		}
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
		metricWithThreshold(target, MetricLimitedAccounts, "警告账号", decimal.NewFromInt(counts[TargetStatusWarning]), "个"),
		metricWithThreshold(target, MetricErrorAccounts, "异常账号", decimal.NewFromInt(counts[TargetStatusError]), "个"),
		metricWithThreshold(target, MetricDisabledAccounts, "禁用账号", decimal.NewFromInt(counts[TargetStatusDisabled]), "个"),
	)
	if len(accounts) == 0 || counts[TargetStatusHealthy] == 0 {
		snapshot.Status = TargetStatusWarning
		snapshot.Message = "CLIProxyAPI 当前没有可用账号"
	}
	return snapshot, nil
}

// RefreshAccountQuotas 仅刷新前端当前页指定账号的额度，不修改账号健康状态。
func (adapter *cliProxyAPIAdapter) RefreshAccountQuotas(ctx context.Context, target TargetConfig, accountIDs []string) ([]AccountStatus, error) {
	target = ensureTargetKind(target, adapter.Kind())
	requested, err := normalizeCLIProxyAPIAccountIDs(accountIDs)
	if err != nil {
		return nil, err
	}
	managementKey := strings.TrimSpace(target.Credential.AdminKey)
	if managementKey == "" {
		return nil, checkError(ErrorClassConfig, "刷新 CLIProxyAPI 额度", "CLIProxyAPI 需要管理密钥", 0, nil)
	}
	endpoint, err := joinTargetURL(target.BaseURL, "/v0/management/auth-files")
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	setBearer(headers, managementKey)
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return nil, err
	}
	accounts, rawAccounts, err := parseCLIProxyAPIAccounts(payload, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	indexes := make(map[string]int, len(accounts))
	for index, account := range accounts {
		publicID := PublicAccountID(TargetKindCLIProxyAPI, account.ExternalID)
		if publicID != "" {
			indexes[publicID] = index
		}
	}
	selectedAccounts := make([]AccountStatus, 0, len(requested))
	selectedRaw := make([]map[string]any, 0, len(requested))
	for _, accountID := range requested {
		index, exists := indexes[accountID]
		if !exists {
			return nil, checkError(ErrorClassResponse, "刷新 CLIProxyAPI 额度", "账号列表已经变化，请刷新页面后重试", 0, nil)
		}
		selectedAccounts = append(selectedAccounts, accounts[index])
		selectedRaw = append(selectedRaw, rawAccounts[index])
	}
	adapter.refreshCLIProxyAPIAccounts(ctx, session, target, managementKey, selectedAccounts, selectedRaw)
	return selectedAccounts, nil
}

func normalizeCLIProxyAPIAccountIDs(accountIDs []string) ([]string, error) {
	if len(accountIDs) == 0 || len(accountIDs) > MaxAccountQuotaRefreshAccounts {
		return nil, checkError(ErrorClassConfig, "刷新 CLIProxyAPI 额度", "每次需要选择 1 至 100 个账号", 0, nil)
	}
	result := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, value := range accountIDs {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) != 24 || strings.IndexFunc(value, func(character rune) bool {
			return !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f'))
		}) >= 0 {
			return nil, checkError(ErrorClassConfig, "刷新 CLIProxyAPI 额度", "账号标识格式无效", 0, nil)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, checkError(ErrorClassConfig, "刷新 CLIProxyAPI 额度", "至少需要选择一个账号", 0, nil)
	}
	return result, nil
}

func parseCLIProxyAPIAccounts(payload any, now time.Time) ([]AccountStatus, []map[string]any, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, nil, checkError(ErrorClassResponse, "解析 CLIProxyAPI 账号", "CLIProxyAPI 响应格式无效", 0, nil)
	}
	items, ok := root["files"].([]any)
	if !ok {
		return nil, nil, checkError(ErrorClassResponse, "解析 CLIProxyAPI 账号", "CLIProxyAPI 响应缺少 files", 0, nil)
	}
	result := make([]AccountStatus, 0, len(items))
	rawAccounts := make([]map[string]any, 0, len(items))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		status, statusText, recoveryAt := classifyCLIProxyAPIAccount(raw, now)
		provider := safeCLIProxyAPIIdentifier(stringField(raw, "provider", "type"), 80)
		accountType := cliProxyAPIPlanType(raw)
		if accountType == "" {
			accountType = safeCLIProxyAPIIdentifier(stringField(raw, "account_type"), 80)
		}
		result = append(result, AccountStatus{
			// 上游标识只进入后续哈希流程，序列化时会被忽略。
			ExternalID:            stringField(raw, "auth_index", "id"),
			DisplayName:           safeCLIProxyAPIText(stringField(raw, "label"), 120),
			Provider:              provider,
			Email:                 safeCLIProxyAPIEmail(stringField(raw, "email")),
			Type:                  accountType,
			Status:                string(status),
			StatusText:            statusText,
			SubscriptionExpiresAt: parseCLIProxyAPISubscriptionExpiry(raw),
			RecoveryAt:            recoveryAt,
			Success:               int64Field(raw, "success"),
			Fail:                  int64Field(raw, "failed"),
		})
		rawAccounts = append(rawAccounts, raw)
	}
	return result, rawAccounts, nil
}

func classifyCLIProxyAPIAccount(account map[string]any, now time.Time) (TargetStatus, string, string) {
	status := strings.ToLower(strings.TrimSpace(stringField(account, "status")))
	disabled, _ := boolField(account, "disabled")
	if disabled || status == "disabled" {
		return TargetStatusDisabled, "已禁用", parseCLIProxyAPIRecoveryAt(account["next_retry_after"])
	}
	recoveryAt := parseCLIProxyAPIRecoveryAt(account["next_retry_after"])
	reason := cliProxyAPIAccountReason(account)
	statusCode := cliProxyAPIAccountHTTPStatusCode(account, reason)
	// CLIProxyAPI 会把请求参数错误也记成 error，但这类错误通常只说明
	// 当前请求参数与上游版本不兼容，账号本身仍可继续使用。
	if cliProxyAPIAccountCredentialFailure(status, reason, statusCode) {
		return TargetStatusError, "凭据失效", recoveryAt
	}
	if cliProxyAPIAccountParameterWarning(reason, statusCode) {
		return TargetStatusWarning, "参数警告，账号仍可用", recoveryAt
	}
	if cliProxyAPIAccountTransientHTTPWarning(statusCode) {
		return TargetStatusWarning, "暂时不可用", recoveryAt
	}
	unavailable, _ := boolField(account, "unavailable")
	if unavailable || cliProxyAPIRetryPending(recoveryAt, now) {
		return TargetStatusWarning, "限流或冷却中", recoveryAt
	}
	switch status {
	case "active":
		if cliProxyAPIAccountTransientWarning(reason) {
			return TargetStatusWarning, "暂时不可用", recoveryAt
		}
		return TargetStatusHealthy, "可用", recoveryAt
	case "pending":
		return TargetStatusWarning, "等待处理", recoveryAt
	case "refreshing":
		return TargetStatusWarning, "刷新中", recoveryAt
	case "error":
		// 上游的 error 同时覆盖参数错误、限流和短暂的服务端错误，
		// 没有明确凭据失效证据时按警告展示，避免把可用账号当成死号。
		return TargetStatusWarning, "暂时不可用", recoveryAt
	default:
		if cliProxyAPIAccountTransientWarning(reason) {
			return TargetStatusWarning, "暂时不可用", recoveryAt
		}
		return TargetStatusUnknown, "状态未知", recoveryAt
	}
}

// cliProxyAPIAccountReason 从管理接口可能返回的错误字段提取分类依据。
// 只在内存中做关键词判断，不把原始错误内容写入账号快照。
func cliProxyAPIAccountReason(account map[string]any) string {
	parts := make([]string, 0, 4)
	for _, key := range []string{"status_message", "error_message", "message", "error", "last_error"} {
		appendCLIProxyAPIReason(&parts, account[key], 0)
	}
	return strings.ToLower(strings.TrimSpace(strings.Join(parts, " ")))
}

func appendCLIProxyAPIReason(parts *[]string, value any, depth int) {
	if value == nil || depth > 2 {
		return
	}
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			*parts = append(*parts, text)
		}
	case float64:
		*parts = append(*parts, strconv.FormatFloat(typed, 'f', -1, 64))
	case int:
		*parts = append(*parts, strconv.Itoa(typed))
	case int64:
		*parts = append(*parts, strconv.FormatInt(typed, 10))
	case map[string]any:
		for _, key := range []string{"message", "detail", "error", "code", "status", "status_code", "http_status"} {
			if nested, exists := typed[key]; exists {
				appendCLIProxyAPIReason(parts, nested, depth+1)
			}
		}
	case []any:
		for _, nested := range typed {
			appendCLIProxyAPIReason(parts, nested, depth+1)
		}
	}
}

// cliProxyAPIAccountHTTPStatusCode 兼容不同 CLIProxyAPI 版本的状态码字段。
func cliProxyAPIAccountHTTPStatusCode(account map[string]any, reason string) int64 {
	for _, key := range []string{"status_code", "statusCode", "http_status", "httpStatus"} {
		if code := int64Field(account, key); code > 0 {
			return code
		}
	}
	for _, token := range strings.FieldsFunc(reason, func(character rune) bool {
		return character < '0' || character > '9'
	}) {
		code, err := strconv.ParseInt(token, 10, 64)
		if err == nil && code >= 100 && code <= 599 {
			return code
		}
	}
	return 0
}

// cliProxyAPIAccountCredentialFailure 只把有明确凭据失效证据的账号判为异常。
func cliProxyAPIAccountCredentialFailure(status, reason string, statusCode int64) bool {
	if statusCode == 401 || statusCode == 403 {
		return true
	}
	if status == "invalid" || status == "revoked" || status == "expired" {
		return true
	}
	if strings.Contains(reason, "token") && (strings.Contains(reason, "expired") || strings.Contains(reason, "revoked")) {
		return true
	}
	if strings.Contains(reason, "refresh") && (strings.Contains(reason, "failed") || strings.Contains(reason, "failure")) {
		return true
	}
	for _, marker := range []string{
		"unauthorized", "unauthenticated", "forbidden", "permission denied", "access denied",
		"invalid_grant", "invalid grant", "invalid credential", "invalid credentials", "invalid token",
		"token expired", "expired token", "token has expired", "expired_access_token", "token revoked",
		"revoked token", "refresh failed", "failed to refresh", "refresh error", "login required",
		"credential expired", "credential revoked", "account disabled", "account deactivated", "account suspended",
		"account banned", "user disabled", "user suspended", "user banned", "凭据失效", "认证失败", "授权失败",
		"令牌过期", "令牌撤销", "禁止访问", "未授权", "刷新失败", "账号停用", "账号已停用", "账号封禁",
		"账号已封禁", "账号被封", "账户停用", "账户已停用", "账户封禁", "账户已封禁", "账户被封",
	} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

// cliProxyAPIAccountParameterWarning 识别不会让账号失效的请求格式问题。
func cliProxyAPIAccountParameterWarning(reason string, statusCode int64) bool {
	if statusCode == 400 || statusCode == 422 {
		return true
	}
	for _, marker := range []string{
		"unsupported parameter", "unsupported field", "unknown parameter", "unknown field",
		"unrecognized parameter", "unrecognized field", "unexpected keyword", "invalid parameter",
		"invalid argument", "invalid_request_error", "bad_request_error", "failed_precondition",
		"parameter not supported", "max_tool_calls", "参数不支持", "不支持参数", "未知参数", "参数无效",
	} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

// cliProxyAPIAccountTransientHTTPWarning 把可重试的 HTTP 状态归为警告。
func cliProxyAPIAccountTransientHTTPWarning(statusCode int64) bool {
	return statusCode == 408 || statusCode == 425 || statusCode == 429 || statusCode >= 500
}

// cliProxyAPIAccountTransientWarning 判断账号是否仍可能恢复，不涉及凭据失效。
func cliProxyAPIAccountTransientWarning(reason string) bool {
	for _, marker := range []string{
		"rate limit", "rate_limited", "too many requests", "quota exhausted", "temporarily unavailable",
		"transient upstream error", "cloudflare challenge", "cooldown", "retry", "server error", "timeout",
		"timed out", "deadline exceeded", "network error", "connection reset", "connection refused",
		"no such host", "temporary failure", "service unavailable", "gateway timeout", "bad gateway",
		"internal server error", "context canceled", "request failed", "限流", "临时不可用", "冷却",
		"网络错误", "连接超时", "连接失败", "服务暂时不可用",
	} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
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
var _ AccountQuotaRefresher = (*cliProxyAPIAdapter)(nil)
