package monitor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	sub2APIAccountPageSize     = 100
	sub2APIMaxAccounts         = 10000
	sub2APIQuotaAccountTimeout = 6 * time.Second
)

// readSub2APIAdminAccounts 只读取管理端已经脱敏的账号列表，并再次按本应用白名单转换。
func (adapter *sub2APIAdapter) readSub2APIAdminAccounts(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	accessToken string,
) ([]AccountStatus, error) {
	headers := make(http.Header)
	if adminKey := strings.TrimSpace(target.Credential.AdminKey); adminKey != "" {
		headers.Set("X-API-Key", adminKey)
	} else {
		setBearer(headers, accessToken)
	}

	result := make([]AccountStatus, 0)
	seen := make(map[string]struct{})
	for page := 1; page <= sub2APIMaxAccounts/sub2APIAccountPageSize; page++ {
		path := fmt.Sprintf("/api/v1/admin/accounts?page=%d&page_size=%d&sort_by=id&sort_order=asc", page, sub2APIAccountPageSize)
		endpoint, err := joinTargetURL(target.BaseURL, path)
		if err != nil {
			return nil, err
		}
		var payload any
		if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
			return nil, err
		}
		data, err := sub2APIEnvelopeObject(payload, true)
		if err != nil {
			return nil, err
		}
		items, ok := data["items"].([]any)
		if !ok {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 账号", "Sub2API 管理账号响应缺少 items", 0, nil)
		}
		for _, value := range items {
			raw, ok := value.(map[string]any)
			if !ok {
				return nil, checkError(ErrorClassResponse, "解析 Sub2API 账号", "Sub2API 管理账号条目格式无效", 0, nil)
			}
			account, err := parseSub2APIAccount(raw, adapter.now().UTC())
			if err != nil {
				return nil, err
			}
			if _, exists := seen[account.ExternalID]; exists {
				continue
			}
			seen[account.ExternalID] = struct{}{}
			result = append(result, account)
			if len(result) >= sub2APIMaxAccounts {
				return result, nil
			}
		}

		total := int64Field(data, "total")
		if total > 0 && int64(len(result)) >= total {
			break
		}
		if len(items) == 0 || (total <= 0 && len(items) < sub2APIAccountPageSize) {
			break
		}
	}
	return result, nil
}

func parseSub2APIAccount(raw map[string]any, now time.Time) (AccountStatus, error) {
	idValue, exists := firstValue(raw, "id")
	if !exists {
		return AccountStatus{}, checkError(ErrorClassResponse, "解析 Sub2API 账号", "Sub2API 管理账号缺少标识", 0, nil)
	}
	externalID, err := parsePositiveInt64String(idValue)
	if err != nil {
		return AccountStatus{}, checkError(ErrorClassResponse, "解析 Sub2API 账号", "Sub2API 管理账号标识无效", 0, err)
	}
	expiresAt := parseSub2APIAccountTime(raw["expires_at"])
	windows := parseSub2APIInternalQuotaWindows(raw)
	status, statusText, recoveryAt := classifySub2APIAccount(raw, expiresAt, windows, now)
	quotaState := AccountQuotaStateUnsupported
	if len(windows) > 0 {
		quotaState = AccountQuotaStateAvailable
	} else if sub2APIAccountSupportsPassiveUsage(raw) {
		// 常规账号列表没有真实订阅额度，留空以便持久层保留上一次按页读取的被动快照。
		quotaState = ""
	}
	return AccountStatus{
		// 上游数字标识仅供服务端生成公开哈希，JSON 序列化会忽略它。
		ExternalID:            externalID,
		DisplayName:           safeSub2APIAccountText(stringField(raw, "name"), 120),
		Provider:              safeSub2APIAccountText(stringField(raw, "platform"), 80),
		Type:                  safeSub2APIAccountText(stringField(raw, "type"), 80),
		Status:                string(status),
		StatusText:            statusText,
		QuotaState:            quotaState,
		QuotaWindows:          windows,
		SubscriptionExpiresAt: expiresAt,
		RecoveryAt:            recoveryAt,
	}, nil
}

func classifySub2APIAccount(raw map[string]any, expiresAt string, windows []AccountQuotaWindow, now time.Time) (TargetStatus, string, string) {
	status := strings.ToLower(strings.TrimSpace(stringField(raw, "status")))
	autoPauseOnExpired, _ := boolField(raw, "auto_pause_on_expired")
	if autoPauseOnExpired && expiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, expiresAt); err == nil && !parsed.After(now) {
			return TargetStatusDisabled, "已过期自动停用", ""
		}
	}
	switch status {
	case "inactive", "disabled":
		return TargetStatusDisabled, "已停用", ""
	case "error":
		return TargetStatusError, "账号异常", ""
	case "active":
		// 继续判断调度与冷却状态。
	default:
		return TargetStatusUnknown, "状态未知", ""
	}

	schedulable, exists := boolField(raw, "schedulable")
	if exists && !schedulable {
		return TargetStatusDisabled, "已暂停调度", ""
	}
	recoveryAt := latestSub2APIRecoveryAt(raw, now)
	if recoveryAt != "" {
		return TargetStatusWarning, "限流或冷却中", recoveryAt
	}
	if exhausted, quotaResetAt := sub2APIInternalQuotaExhausted(windows); exhausted {
		return TargetStatusWarning, "内部额度已耗尽", quotaResetAt
	}
	return TargetStatusHealthy, "可用", ""
}

func sub2APIInternalQuotaExhausted(windows []AccountQuotaWindow) (bool, string) {
	var latestReset time.Time
	exhausted := false
	for _, window := range windows {
		if window.RemainingValue == nil || !window.RemainingValue.IsZero() {
			continue
		}
		exhausted = true
		if reset, err := time.Parse(time.RFC3339Nano, window.ResetAt); err == nil && reset.After(latestReset) {
			latestReset = reset
		}
		if window.Key == "internal-total" {
			return true, ""
		}
	}
	if !exhausted {
		return false, ""
	}
	if latestReset.IsZero() {
		return true, ""
	}
	return true, latestReset.UTC().Format(time.RFC3339)
}

func latestSub2APIRecoveryAt(raw map[string]any, now time.Time) string {
	var latest time.Time
	for _, key := range []string{"rate_limit_reset_at", "overload_until", "temp_unschedulable_until"} {
		value := parseSub2APIAccountTime(raw[key])
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err == nil && parsed.After(now) && parsed.After(latest) {
			latest = parsed
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
}

func parseSub2APIAccountTime(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	seconds, err := parseInt64(value)
	if err != nil || seconds <= 0 {
		return ""
	}
	// 兼容少数部署把 Unix 时间误以毫秒返回，避免展示到遥远未来。
	if seconds > 100000000000 {
		seconds /= 1000
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func safeSub2APIAccountText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if value == "" || maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func parseSub2APIInternalQuotaWindows(raw map[string]any) []AccountQuotaWindow {
	accountType := normalizeSub2APIAccountType(stringField(raw, "type"))
	if accountType != "apikey" && accountType != "bedrock" {
		return nil
	}
	definitions := []struct {
		key        string
		label      string
		limitField string
		usedField  string
		resetField string
	}{
		{key: "internal-total", label: "内部总额度", limitField: "quota_limit", usedField: "quota_used"},
		{key: "internal-daily", label: "内部日额度", limitField: "quota_daily_limit", usedField: "quota_daily_used", resetField: "quota_daily_reset_at"},
		{key: "internal-weekly", label: "内部周额度", limitField: "quota_weekly_limit", usedField: "quota_weekly_used", resetField: "quota_weekly_reset_at"},
	}
	result := make([]AccountQuotaWindow, 0, len(definitions))
	for _, definition := range definitions {
		limitValue, exists := raw[definition.limitField]
		if !exists || limitValue == nil {
			continue
		}
		limit, err := parseDecimal(limitValue)
		if err != nil || !limit.IsPositive() {
			continue
		}
		used := decimal.Zero
		if usedValue, exists := raw[definition.usedField]; exists && usedValue != nil {
			parsed, parseErr := parseDecimal(usedValue)
			if parseErr != nil {
				continue
			}
			used = parsed
		}
		if used.IsNegative() {
			used = decimal.Zero
		}
		remaining := limit.Sub(used)
		if remaining.IsNegative() {
			remaining = decimal.Zero
		}
		percent := remaining.Div(limit).Mul(decimal.NewFromInt(100))
		if percent.GreaterThan(decimal.NewFromInt(100)) {
			percent = decimal.NewFromInt(100)
		}
		window := AccountQuotaWindow{
			Key: definition.key, Label: definition.label, RemainingPercent: &percent,
			RemainingValue: &remaining, LimitValue: &limit, Unit: "USD",
		}
		if definition.resetField != "" {
			window.ResetAt = parseSub2APIAccountTime(raw[definition.resetField])
		}
		result = append(result, window)
	}
	return result
}

func normalizeSub2APIAccountType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func sub2APIAccountSupportsPassiveUsage(raw map[string]any) bool {
	platform := strings.ToLower(strings.TrimSpace(stringField(raw, "platform")))
	accountType := normalizeSub2APIAccountType(stringField(raw, "type"))
	return platform == "anthropic" && (accountType == "oauth" || accountType == "setup-token")
}

func sub2APIUserIsAdmin(user map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(stringField(user, "role")), "admin")
}

func appendSub2APIAccountMetrics(snapshot *Snapshot, target TargetConfig, accounts []AccountStatus) {
	counts := map[TargetStatus]int64{
		TargetStatusHealthy: 0, TargetStatusWarning: 0, TargetStatusError: 0, TargetStatusDisabled: 0,
	}
	for _, account := range accounts {
		status := TargetStatus(account.Status)
		if status == TargetStatusUnknown {
			status = TargetStatusError
		}
		counts[status]++
	}
	snapshot.Accounts = accounts
	snapshot.Metrics = append(snapshot.Metrics,
		metricWithThreshold(target, MetricHealthyAccounts, "可用账号", decimal.NewFromInt(counts[TargetStatusHealthy]), "个"),
		metricWithThreshold(target, MetricAccountTotal, "账号总数", decimal.NewFromInt(int64(len(accounts))), "个"),
		metricWithThreshold(target, MetricLimitedAccounts, "警告账号", decimal.NewFromInt(counts[TargetStatusWarning]), "个"),
		metricWithThreshold(target, MetricErrorAccounts, "异常账号", decimal.NewFromInt(counts[TargetStatusError]), "个"),
		metricWithThreshold(target, MetricDisabledAccounts, "禁用账号", decimal.NewFromInt(counts[TargetStatusDisabled]), "个"),
	)
	if len(accounts) == 0 || counts[TargetStatusHealthy] == 0 {
		if snapshot.Status == TargetStatusHealthy {
			snapshot.Status = TargetStatusWarning
			snapshot.Message = "Sub2API 当前没有可用账号"
		}
	}
}

// RefreshAccountQuotas 重新读取账号白名单，并只查询所选 Anthropic 账号的被动额度快照。
func (adapter *sub2APIAdapter) RefreshAccountQuotas(ctx context.Context, target TargetConfig, accountIDs []string) (AccountQuotaRefreshResult, error) {
	target = ensureTargetKind(target, adapter.Kind())
	requested, err := normalizeSub2APIAccountIDs(accountIDs)
	if err != nil {
		return AccountQuotaRefreshResult{}, err
	}
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	var accessToken string
	var credentialUpdate *Credential
	if strings.TrimSpace(target.Credential.AdminKey) == "" {
		token, user, resolveErr := adapter.resolveCurrentUser(ctx, session, target)
		if resolveErr != nil {
			return AccountQuotaRefreshResult{}, resolveErr
		}
		if !sub2APIUserIsAdmin(user) {
			return AccountQuotaRefreshResult{}, checkError(ErrorClassConfig, "刷新 Sub2API 额度", "Sub2API 号池需要管理员账号或管理密钥", 0, nil)
		}
		accessToken = token.accessToken
		credentialUpdate = sub2APIUpdatedCredential(target.Credential, token)
	}
	accounts, err := adapter.readSub2APIAdminAccounts(ctx, session, target, accessToken)
	if err != nil {
		return AccountQuotaRefreshResult{CredentialUpdate: credentialUpdate}, err
	}
	indexes := make(map[string]int, len(accounts))
	for index, account := range accounts {
		indexes[PublicAccountID(TargetKindSub2API, account.ExternalID)] = index
	}
	selected := make([]AccountStatus, 0, len(requested))
	for _, publicID := range requested {
		index, exists := indexes[publicID]
		if !exists {
			return AccountQuotaRefreshResult{CredentialUpdate: credentialUpdate}, checkError(ErrorClassResponse, "刷新 Sub2API 额度", "账号列表已经变化，请刷新页面后重试", 0, nil)
		}
		selected = append(selected, accounts[index])
	}
	if err := adapter.refreshSub2APIPassiveAccounts(ctx, session, target, accessToken, selected); err != nil {
		return AccountQuotaRefreshResult{CredentialUpdate: credentialUpdate}, err
	}
	return AccountQuotaRefreshResult{Accounts: selected, CredentialUpdate: credentialUpdate}, nil
}

func normalizeSub2APIAccountIDs(accountIDs []string) ([]string, error) {
	if len(accountIDs) == 0 || len(accountIDs) > MaxAccountQuotaRefreshAccounts {
		return nil, checkError(ErrorClassConfig, "刷新 Sub2API 额度", "每次需要选择 1 至 100 个账号", 0, nil)
	}
	result := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, value := range accountIDs {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) != 24 || strings.IndexFunc(value, func(character rune) bool {
			return !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f'))
		}) >= 0 {
			return nil, checkError(ErrorClassConfig, "刷新 Sub2API 额度", "账号标识格式无效", 0, nil)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, checkError(ErrorClassConfig, "刷新 Sub2API 额度", "至少需要选择一个账号", 0, nil)
	}
	return result, nil
}

func isSub2APIPassiveUsageAccount(account AccountStatus) bool {
	platform := strings.ToLower(strings.TrimSpace(account.Provider))
	accountType := normalizeSub2APIAccountType(account.Type)
	return platform == "anthropic" && (accountType == "oauth" || accountType == "setup-token")
}

func (adapter *sub2APIAdapter) refreshSub2APIPassiveAccounts(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	accessToken string,
	accounts []AccountStatus,
) error {
	jobs := make(chan int, len(accounts))
	for index := range accounts {
		if isSub2APIPassiveUsageAccount(accounts[index]) {
			jobs <- index
		}
	}
	close(jobs)
	if len(jobs) == 0 {
		return nil
	}
	workerCount := 4
	if len(jobs) < workerCount {
		workerCount = len(jobs)
	}
	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstError error
	var errorOnce sync.Once
	var waitGroup sync.WaitGroup
	waitGroup.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer waitGroup.Done()
			for index := range jobs {
				if workContext.Err() != nil {
					return
				}
				requestTimeout := adapter.quotaRequestTimeout
				if requestTimeout <= 0 {
					requestTimeout = sub2APIQuotaAccountTimeout
				}
				accountContext, accountCancel := context.WithTimeout(workContext, requestTimeout)
				err := adapter.refreshSub2APIPassiveUsage(accountContext, session, target, accessToken, &accounts[index])
				accountCancel()
				if err != nil {
					errorOnce.Do(func() {
						firstError = err
						cancel()
					})
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	if firstError != nil {
		return firstError
	}
	return ctx.Err()
}

func (adapter *sub2APIAdapter) refreshSub2APIPassiveUsage(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	accessToken string,
	account *AccountStatus,
) error {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/admin/accounts/"+account.ExternalID+"/usage?source=passive")
	if err != nil {
		return err
	}
	headers := make(http.Header)
	if adminKey := strings.TrimSpace(target.Credential.AdminKey); adminKey != "" {
		headers.Set("X-API-Key", adminKey)
	} else {
		setBearer(headers, accessToken)
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return err
	}
	data, err := sub2APIEnvelopeObject(payload, true)
	if err != nil {
		return err
	}
	account.QuotaWindows = parseSub2APIPassiveUsageWindows(data)
	if len(account.QuotaWindows) == 0 {
		account.QuotaState = AccountQuotaStateUnavailable
		return nil
	}
	account.QuotaState = AccountQuotaStateAvailable
	return nil
}

func parseSub2APIPassiveUsageWindows(data map[string]any) []AccountQuotaWindow {
	definitions := []struct {
		field string
		key   string
		label string
	}{
		{field: "five_hour", key: "five-hour", label: "5 小时"},
		{field: "seven_day", key: "seven-day", label: "7 天"},
		{field: "seven_day_sonnet", key: "seven-day-sonnet", label: "Sonnet 7 天"},
		{field: "seven_day_fable", key: "seven-day-fable", label: "Fable 7 天"},
	}
	result := make([]AccountQuotaWindow, 0, len(definitions))
	for _, definition := range definitions {
		raw, ok := data[definition.field].(map[string]any)
		if !ok {
			continue
		}
		utilization, err := decimalField(raw, "utilization")
		if err != nil {
			continue
		}
		if utilization.IsNegative() {
			utilization = decimal.Zero
		}
		remaining := decimal.NewFromInt(100).Sub(utilization)
		if remaining.IsNegative() {
			remaining = decimal.Zero
		}
		if remaining.GreaterThan(decimal.NewFromInt(100)) {
			remaining = decimal.NewFromInt(100)
		}
		result = append(result, AccountQuotaWindow{
			Key: definition.key, Label: definition.label, RemainingPercent: &remaining,
			ResetAt: parseSub2APIAccountTime(raw["resets_at"]),
		})
	}
	return result
}

var _ AccountQuotaRefresher = (*sub2APIAdapter)(nil)
