package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	cliProxyAPICodexUsageURL                = "https://chatgpt.com/backend-api/wham/usage"
	cliProxyAPIGeminiLoadAssistURL          = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	cliProxyAPIGeminiQuotaURL               = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	cliProxyAPIAntigravitySummaryURL        = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	cliProxyAPIAntigravityDailySummaryURL   = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	cliProxyAPIAntigravitySandboxSummaryURL = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary"
	cliProxyAPIAntigravityModelsURL         = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	cliProxyAPIAntigravityDailyURL          = "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	cliProxyAPIAntigravitySandboxURL        = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels"
	cliProxyAPIQuotaWorkerCount             = 4
	cliProxyAPIMaxQuotaWindows              = 64
	cliProxyAPIQuotaCacheTTL                = 15 * time.Minute
	cliProxyAPIQuotaAccountTimeout          = 6 * time.Second
)

type cliProxyAPIQuotaResult struct {
	Windows  []AccountQuotaWindow
	PlanType string
}

type cliProxyAPIQuotaCacheEntry struct {
	Result    cliProxyAPIQuotaResult
	ExpiresAt time.Time
}

// cliProxyAPIQuotaCache 缓存成功读取的真实额度，降低频繁检测对上游账号接口的压力。
type cliProxyAPIQuotaCache struct {
	mu      sync.Mutex
	entries map[string]cliProxyAPIQuotaCacheEntry
	ttl     time.Duration
	now     func() time.Time
}

func newCLIProxyAPIQuotaCache() *cliProxyAPIQuotaCache {
	return &cliProxyAPIQuotaCache{
		entries: make(map[string]cliProxyAPIQuotaCacheEntry),
		ttl:     cliProxyAPIQuotaCacheTTL,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (cache *cliProxyAPIQuotaCache) load(key string) (cliProxyAPIQuotaResult, bool) {
	if cache == nil || key == "" {
		return cliProxyAPIQuotaResult{}, false
	}
	now := cache.currentTime()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, exists := cache.entries[key]
	if !exists {
		return cliProxyAPIQuotaResult{}, false
	}
	if !entry.ExpiresAt.After(now) {
		delete(cache.entries, key)
		return cliProxyAPIQuotaResult{}, false
	}
	return cloneCLIProxyAPIQuotaResult(entry.Result), true
}

func (cache *cliProxyAPIQuotaCache) store(key string, result cliProxyAPIQuotaResult) {
	if cache == nil || key == "" {
		return
	}
	now := cache.currentTime()
	ttl := cache.ttl
	if ttl <= 0 {
		ttl = cliProxyAPIQuotaCacheTTL
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	// 写入时顺便淘汰过期项，避免已删除渠道或账号长期占用内存。
	for existingKey, entry := range cache.entries {
		if !entry.ExpiresAt.After(now) {
			delete(cache.entries, existingKey)
		}
	}
	if cache.entries == nil {
		cache.entries = make(map[string]cliProxyAPIQuotaCacheEntry)
	}
	cache.entries[key] = cliProxyAPIQuotaCacheEntry{
		Result:    cloneCLIProxyAPIQuotaResult(result),
		ExpiresAt: now.Add(ttl),
	}
}

func (cache *cliProxyAPIQuotaCache) currentTime() time.Time {
	if cache != nil && cache.now != nil {
		return cache.now().UTC()
	}
	return time.Now().UTC()
}

func cloneCLIProxyAPIQuotaResult(result cliProxyAPIQuotaResult) cliProxyAPIQuotaResult {
	cloned := cliProxyAPIQuotaResult{PlanType: result.PlanType}
	cloned.Windows = make([]AccountQuotaWindow, 0, len(result.Windows))
	for _, window := range result.Windows {
		copyWindow := window
		if window.RemainingPercent != nil {
			remaining := *window.RemainingPercent
			copyWindow.RemainingPercent = &remaining
		}
		cloned.Windows = append(cloned.Windows, copyWindow)
	}
	return cloned
}

// enrichCLIProxyAPIAccounts 仅调用 CLIProxyAPI 官方管理端点和固定的上游额度查询地址。
// 任一账号额度读取失败都只保留“暂未获取”，不会让账号状态检测整体失败。
func (adapter *cliProxyAPIAdapter) enrichCLIProxyAPIAccounts(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	accounts []AccountStatus,
	rawAccounts []map[string]any,
) {
	if len(accounts) == 0 || len(accounts) != len(rawAccounts) {
		return
	}

	jobs := make(chan int, len(accounts))
	now := time.Now().UTC()
	for index := range accounts {
		if prepareCLIProxyAPIQuotaAccount(&accounts[index], rawAccounts[index], now) {
			jobs <- index
		}
	}
	close(jobs)
	if len(jobs) == 0 {
		return
	}

	workers := cliProxyAPIQuotaWorkerCount
	if len(jobs) < workers {
		workers = len(jobs)
	}
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer waitGroup.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				accountContext := ctx
				cancel := func() {}
				timeout := adapter.quotaRequestTimeout
				if timeout <= 0 {
					timeout = cliProxyAPIQuotaAccountTimeout
				}
				accountContext, cancel = context.WithTimeout(ctx, timeout)
				adapter.enrichCLIProxyAPIAccount(
					accountContext, session, target, managementKey, &accounts[index], rawAccounts[index],
				)
				cancel()
			}
		}()
	}
	waitGroup.Wait()
}

func prepareCLIProxyAPIQuotaAccount(account *AccountStatus, raw map[string]any, now time.Time) bool {
	if account == nil {
		return false
	}
	account.QuotaWindows = parseCLIProxyAPIDirectQuotaWindows(raw, now)
	if len(account.QuotaWindows) > 0 {
		account.QuotaState = AccountQuotaStateAvailable
	}
	if !cliProxyAPIProviderHasQuotaEndpoint(normalizeCLIProxyAPIProvider(account.Provider)) {
		if account.QuotaState == "" {
			account.QuotaState = AccountQuotaStateUnsupported
		}
		return false
	}
	if account.QuotaState == "" {
		account.QuotaState = AccountQuotaStateUnavailable
	}
	return true
}

func (adapter *cliProxyAPIAdapter) enrichCLIProxyAPIAccount(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	account *AccountStatus,
	raw map[string]any,
) {
	if account == nil {
		return
	}
	provider := normalizeCLIProxyAPIProvider(account.Provider)
	cacheKey := cliProxyAPIQuotaCacheKey(target, managementKey, provider, raw)
	if cached, exists := adapter.quotaCache.load(cacheKey); exists {
		applyCLIProxyAPIQuotaResult(account, cached)
		return
	}

	var result cliProxyAPIQuotaResult
	var ok bool
	switch provider {
	case "codex":
		result, ok = adapter.queryCLIProxyAPICodexQuota(ctx, session, target, managementKey, raw)
	case "gemini-cli":
		result, ok = adapter.queryCLIProxyAPIGeminiQuota(ctx, session, target, managementKey, raw)
	case "antigravity":
		result, ok = adapter.queryCLIProxyAPIAntigravityQuota(ctx, session, target, managementKey, raw)
	}
	if !ok {
		return
	}
	if len(result.Windows) > 0 {
		adapter.quotaCache.store(cacheKey, result)
	}
	applyCLIProxyAPIQuotaResult(account, result)
}

func applyCLIProxyAPIQuotaResult(account *AccountStatus, result cliProxyAPIQuotaResult) {
	if account == nil {
		return
	}
	if planType := safeCLIProxyAPIIdentifier(result.PlanType, 80); planType != "" {
		account.Type = planType
	}
	if len(result.Windows) == 0 {
		return
	}
	account.QuotaWindows = mergeCLIProxyAPIQuotaWindows(account.QuotaWindows, result.Windows)
	account.QuotaState = AccountQuotaStateAvailable
}

func cliProxyAPIQuotaCacheKey(target TargetConfig, managementKey, provider string, raw map[string]any) string {
	authIndex := strings.TrimSpace(stringField(raw, "auth_index", "authIndex"))
	if authIndex == "" {
		return ""
	}
	identityParts := []string{
		strings.TrimSpace(target.ID),
		strings.TrimRight(strings.TrimSpace(target.BaseURL), "/"),
		provider,
		authIndex,
		cliProxyAPICodexAccountID(raw),
		strings.TrimSpace(stringField(raw, "project_id", "projectId")),
		strings.TrimSpace(managementKey),
	}
	sum := sha256.Sum256([]byte(strings.Join(identityParts, "\x00")))
	return fmt.Sprintf("%x", sum)
}

func normalizeCLIProxyAPIProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "gemini", "gemini-cli", "geminicli":
		return "gemini-cli"
	case "anti-gravity", "antigravity":
		return "antigravity"
	case "openai-codex", "codex":
		return "codex"
	default:
		return normalized
	}
}

func cliProxyAPIProviderHasQuotaEndpoint(provider string) bool {
	return provider == "codex" || provider == "gemini-cli" || provider == "antigravity"
}

func (adapter *cliProxyAPIAdapter) queryCLIProxyAPICodexQuota(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	raw map[string]any,
) (cliProxyAPIQuotaResult, bool) {
	authIndex := strings.TrimSpace(stringField(raw, "auth_index", "authIndex"))
	accountID := cliProxyAPICodexAccountID(raw)
	if authIndex == "" || accountID == "" {
		return cliProxyAPIQuotaResult{}, false
	}
	payload, ok := adapter.cliProxyAPIManagementCall(ctx, session, target, managementKey, map[string]any{
		"auth_index": authIndex,
		"method":     http.MethodGet,
		"url":        cliProxyAPICodexUsageURL,
		"header": map[string]string{
			"Authorization":      "Bearer $TOKEN$",
			"Chatgpt-Account-Id": accountID,
			"Content-Type":       "application/json",
			"User-Agent":         "codex_cli_rs/0.76.0",
		},
	})
	if !ok {
		return cliProxyAPIQuotaResult{}, false
	}
	return cliProxyAPIQuotaResult{
		Windows:  parseCLIProxyAPICodexQuotaWindows(payload, time.Now().UTC()),
		PlanType: safeCLIProxyAPIIdentifier(stringField(payload, "plan_type", "planType"), 80),
	}, true
}

func (adapter *cliProxyAPIAdapter) queryCLIProxyAPIGeminiQuota(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	raw map[string]any,
) (cliProxyAPIQuotaResult, bool) {
	authIndex := strings.TrimSpace(stringField(raw, "auth_index", "authIndex"))
	if authIndex == "" {
		return cliProxyAPIQuotaResult{}, false
	}
	projectID := safeCLIProxyAPIIdentifier(stringField(raw, "project_id", "projectId"), 160)
	planType := ""
	if projectID == "" {
		assist, ok := adapter.callCLIProxyAPIGoogleAssist(ctx, session, target, managementKey, authIndex, false, "")
		if !ok {
			return cliProxyAPIQuotaResult{}, false
		}
		projectID = cliProxyAPIGoogleProjectID(assist)
		planType = cliProxyAPIGooglePlanType(assist)
	}
	if projectID == "" {
		return cliProxyAPIQuotaResult{}, false
	}
	body, _ := json.Marshal(map[string]any{"project": projectID})
	payload, ok := adapter.cliProxyAPIManagementCall(ctx, session, target, managementKey, map[string]any{
		"auth_index": authIndex,
		"method":     http.MethodPost,
		"url":        cliProxyAPIGeminiQuotaURL,
		"header":     cliProxyAPIGoogleHeaders(false),
		"data":       string(body),
	})
	if !ok {
		return cliProxyAPIQuotaResult{}, false
	}
	if planType == "" {
		if assist, assistOK := adapter.callCLIProxyAPIGoogleAssist(ctx, session, target, managementKey, authIndex, false, projectID); assistOK {
			planType = cliProxyAPIGooglePlanType(assist)
		}
	}
	return cliProxyAPIQuotaResult{Windows: parseCLIProxyAPIGeminiQuotaWindows(payload), PlanType: planType}, true
}

func (adapter *cliProxyAPIAdapter) queryCLIProxyAPIAntigravityQuota(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	raw map[string]any,
) (cliProxyAPIQuotaResult, bool) {
	authIndex := strings.TrimSpace(stringField(raw, "auth_index", "authIndex"))
	if authIndex == "" {
		return cliProxyAPIQuotaResult{}, false
	}
	projectID := safeCLIProxyAPIIdentifier(stringField(raw, "project_id", "projectId"), 160)
	bodyValue := map[string]any{}
	if projectID != "" {
		bodyValue["project"] = projectID
	}
	body, _ := json.Marshal(bodyValue)

	// 新版优先读取额度摘要；缺少项目标识时跳过，随后回退到旧版模型额度接口。
	if projectID != "" {
		for _, quotaURL := range []string{
			cliProxyAPIAntigravityDailySummaryURL,
			cliProxyAPIAntigravitySandboxSummaryURL,
			cliProxyAPIAntigravitySummaryURL,
		} {
			payload, ok := adapter.cliProxyAPIManagementCall(ctx, session, target, managementKey, map[string]any{
				"auth_index": authIndex,
				"method":     http.MethodPost,
				"url":        quotaURL,
				"header":     cliProxyAPIGoogleHeaders(true),
				"data":       string(body),
			})
			if !ok {
				continue
			}
			windows := parseCLIProxyAPIAntigravityQuotaSummaryWindows(payload)
			if len(windows) == 0 {
				continue
			}
			return adapter.completeCLIProxyAPIAntigravityQuota(
				ctx, session, target, managementKey, authIndex, windows,
			), true
		}
	}

	for _, quotaURL := range []string{
		cliProxyAPIAntigravityModelsURL,
		cliProxyAPIAntigravityDailyURL,
		cliProxyAPIAntigravitySandboxURL,
	} {
		payload, ok := adapter.cliProxyAPIManagementCall(ctx, session, target, managementKey, map[string]any{
			"auth_index": authIndex,
			"method":     http.MethodPost,
			"url":        quotaURL,
			"header":     cliProxyAPIGoogleHeaders(true),
			"data":       string(body),
		})
		if !ok {
			continue
		}
		windows := parseCLIProxyAPIAntigravityQuotaWindows(payload)
		if len(windows) == 0 {
			continue
		}
		return adapter.completeCLIProxyAPIAntigravityQuota(
			ctx, session, target, managementKey, authIndex, windows,
		), true
	}
	return cliProxyAPIQuotaResult{}, false
}

// completeCLIProxyAPIAntigravityQuota 在已经拿到额度后尝试补充套餐，辅助请求失败不影响额度结果。
func (adapter *cliProxyAPIAdapter) completeCLIProxyAPIAntigravityQuota(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	authIndex string,
	windows []AccountQuotaWindow,
) cliProxyAPIQuotaResult {
	result := cliProxyAPIQuotaResult{Windows: windows}
	if assist, ok := adapter.callCLIProxyAPIGoogleAssist(ctx, session, target, managementKey, authIndex, true, ""); ok {
		result.PlanType = cliProxyAPIGooglePlanType(assist)
	}
	return result
}

func (adapter *cliProxyAPIAdapter) callCLIProxyAPIGoogleAssist(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	authIndex string,
	antigravity bool,
	projectID string,
) (map[string]any, bool) {
	metadata := map[string]string{"ideType": "ANTIGRAVITY"}
	if !antigravity {
		metadata = map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		}
	}
	bodyValue := map[string]any{"metadata": metadata}
	if projectID != "" {
		bodyValue["cloudaicompanionProject"] = projectID
	}
	body, _ := json.Marshal(bodyValue)
	return adapter.cliProxyAPIManagementCall(ctx, session, target, managementKey, map[string]any{
		"auth_index": authIndex,
		"method":     http.MethodPost,
		"url":        cliProxyAPIGeminiLoadAssistURL,
		"header":     cliProxyAPIGoogleHeaders(antigravity),
		"data":       string(body),
	})
}

func cliProxyAPIGoogleHeaders(antigravity bool) map[string]string {
	userAgent := "google-api-nodejs-client/9.15.1"
	if antigravity {
		userAgent = "antigravity/1.11.5"
	}
	return map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    userAgent,
	}
}

func (adapter *cliProxyAPIAdapter) cliProxyAPIManagementCall(
	ctx context.Context,
	session *requestSession,
	target TargetConfig,
	managementKey string,
	call map[string]any,
) (map[string]any, bool) {
	endpoint, err := joinTargetURL(target.BaseURL, "/v0/management/api-call")
	if err != nil {
		return nil, false
	}
	body, err := json.Marshal(call)
	if err != nil {
		return nil, false
	}
	headers := make(http.Header)
	setBearer(headers, managementKey)
	headers.Set("Content-Type", "application/json")
	var response map[string]any
	if err := session.doJSON(ctx, http.MethodPost, endpoint, headers, body, &response); err != nil {
		return nil, false
	}
	statusCode := parseStatusCode(firstNonNil(response["status_code"], response["statusCode"]))
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, false
	}
	bodyText, ok := response["body"].(string)
	if !ok || strings.TrimSpace(bodyText) == "" {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(bodyText))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, false
	}
	return payload, true
}

func parseCLIProxyAPICodexQuotaWindows(payload map[string]any, now time.Time) []AccountQuotaWindow {
	rateLimit := mapField(payload, "rate_limit", "rateLimit")
	result := cliProxyAPICodexLimitWindows(rateLimit, "code", "", now, false)
	reviewLimit := mapField(payload, "code_review_rate_limit", "codeReviewRateLimit")
	result = append(result, cliProxyAPICodexLimitWindows(reviewLimit, "review", "代码审查 ", now, true)...)

	additional, _ := firstNonNil(
		payload["additional_rate_limits"], payload["additionalRateLimits"],
		firstNonNilField(rateLimit, "additional_rate_limits", "additionalRateLimits"),
	).([]any)
	for index, item := range additional {
		if len(result) >= cliProxyAPIMaxQuotaWindows {
			break
		}
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		limit := mapField(entry, "rate_limit", "rateLimit")
		label := safeCLIProxyAPIText(stringField(entry, "limit_name", "limitName", "metered_feature", "meteredFeature"), 100)
		if limit == nil || label == "" {
			continue
		}
		result = append(result, cliProxyAPICodexLimitWindows(
			limit, "extra-"+strconv.Itoa(index+1), label+" ", now, false,
		)...)
	}
	return mergeCLIProxyAPIQuotaWindows(nil, result)
}

type cliProxyAPICodexWindowSpec struct {
	fieldNames     []string
	fallbackSuffix string
	fallbackLabel  string
}

// cliProxyAPICodexLimitWindows 根据上游窗口时长生成标签，避免把月额度固定写成周额度。
func cliProxyAPICodexLimitWindows(
	limit map[string]any,
	keyPrefix string,
	labelPrefix string,
	now time.Time,
	neutralPrimaryLabel bool,
) []AccountQuotaWindow {
	if limit == nil {
		return nil
	}
	primaryLabel := "5 小时"
	if neutralPrimaryLabel {
		primaryLabel = "额度"
	}
	specs := []cliProxyAPICodexWindowSpec{
		{fieldNames: []string{"primary_window", "primaryWindow"}, fallbackSuffix: "5h", fallbackLabel: primaryLabel},
		{fieldNames: []string{"secondary_window", "secondaryWindow"}, fallbackSuffix: "7d", fallbackLabel: "7 天"},
		{fieldNames: []string{"monthly_window", "monthlyWindow"}, fallbackSuffix: "30d", fallbackLabel: "30 天"},
	}
	result := make([]AccountQuotaWindow, 0, len(specs))
	for _, spec := range specs {
		window := mapField(limit, spec.fieldNames...)
		if window == nil {
			continue
		}
		suffix, durationLabel := cliProxyAPICodexWindowDescriptor(window, spec.fallbackSuffix, spec.fallbackLabel)
		if parsed, ok := cliProxyAPICodexWindow(keyPrefix+"-"+suffix, labelPrefix+durationLabel, window, limit, now); ok {
			result = append(result, parsed)
		}
	}
	if len(result) == 0 && cliProxyAPICodexDirectWindow(limit) {
		suffix, durationLabel := cliProxyAPICodexWindowDescriptor(limit, "primary", primaryLabel)
		if parsed, ok := cliProxyAPICodexWindow(keyPrefix+"-"+suffix, labelPrefix+durationLabel, limit, limit, now); ok {
			result = append(result, parsed)
		}
	}
	return result
}

func cliProxyAPICodexWindowDescriptor(window map[string]any, fallbackSuffix, fallbackLabel string) (string, string) {
	seconds, ok := decimalFromAny(firstNonNil(window["limit_window_seconds"], window["limitWindowSeconds"]))
	if !ok || !seconds.IsPositive() {
		return fallbackSuffix, fallbackLabel
	}
	wholeSeconds := seconds.IntPart()
	if wholeSeconds <= 0 || wholeSeconds > 366*24*60*60 {
		return fallbackSuffix, fallbackLabel
	}
	switch {
	case wholeSeconds%86400 == 0:
		days := wholeSeconds / 86400
		return fmt.Sprintf("%dd", days), fmt.Sprintf("%d 天", days)
	case wholeSeconds%3600 == 0:
		hours := wholeSeconds / 3600
		return fmt.Sprintf("%dh", hours), fmt.Sprintf("%d 小时", hours)
	case wholeSeconds%60 == 0:
		minutes := wholeSeconds / 60
		return fmt.Sprintf("%dm", minutes), fmt.Sprintf("%d 分钟", minutes)
	default:
		return fmt.Sprintf("%ds", wholeSeconds), fmt.Sprintf("%d 秒", wholeSeconds)
	}
}

func cliProxyAPICodexDirectWindow(value map[string]any) bool {
	if value == nil {
		return false
	}
	if _, ok := decimalFromAny(firstNonNil(value["used_percent"], value["usedPercent"])); ok {
		return true
	}
	return parseCLIProxyAPITime(firstNonNil(value["reset_at"], value["resetAt"])) != ""
}

func firstNonNilField(value map[string]any, names ...string) any {
	if value == nil {
		return nil
	}
	for _, name := range names {
		if item := value[name]; item != nil {
			return item
		}
	}
	return nil
}

func cliProxyAPICodexWindow(key, label string, window, rateLimit map[string]any, now time.Time) (AccountQuotaWindow, bool) {
	if window == nil {
		return AccountQuotaWindow{}, false
	}
	remaining := cliProxyAPIRemainingFromUsedPercent(firstNonNil(window["used_percent"], window["usedPercent"]))
	resetAt := cliProxyAPIResetAt(window, now)
	if remaining == nil {
		limitReached, hasLimitReached := flexibleBool(firstNonNil(rateLimit["limit_reached"], rateLimit["limitReached"]))
		allowed, hasAllowed := flexibleBool(rateLimit["allowed"])
		if (hasLimitReached && limitReached) || (hasAllowed && !allowed) {
			zero := decimal.Zero
			remaining = &zero
		}
	}
	if remaining == nil && resetAt == "" {
		return AccountQuotaWindow{}, false
	}
	return AccountQuotaWindow{Key: key, Label: label, RemainingPercent: remaining, ResetAt: resetAt}, true
}

func parseCLIProxyAPIGeminiQuotaWindows(payload map[string]any) []AccountQuotaWindow {
	buckets, _ := payload["buckets"].([]any)
	result := make([]AccountQuotaWindow, 0, len(buckets))
	for _, item := range buckets {
		if len(result) >= cliProxyAPIMaxQuotaWindows {
			break
		}
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		modelID := safeCLIProxyAPIIdentifier(stringField(entry, "modelId", "model_id"), 120)
		if modelID == "" {
			continue
		}
		remaining := cliProxyAPIRemainingFromFraction(firstNonNil(entry["remainingFraction"], entry["remaining_fraction"]))
		resetAt := parseCLIProxyAPITime(firstNonNil(entry["resetTime"], entry["reset_time"]))
		if remaining == nil && resetAt == "" {
			continue
		}
		result = append(result, AccountQuotaWindow{Key: modelID, Label: modelID, RemainingPercent: remaining, ResetAt: resetAt})
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].Label < result[right].Label })
	return result
}

// parseCLIProxyAPIAntigravityQuotaSummaryWindows 将新版额度摘要的分组窗口展平为账号展示项。
func parseCLIProxyAPIAntigravityQuotaSummaryWindows(payload map[string]any) []AccountQuotaWindow {
	groups, _ := payload["groups"].([]any)
	result := make([]AccountQuotaWindow, 0)
	for groupIndex, item := range groups {
		if len(result) >= cliProxyAPIMaxQuotaWindows {
			break
		}
		group, ok := item.(map[string]any)
		if !ok {
			continue
		}
		groupLabel := safeCLIProxyAPIText(stringField(group, "displayName", "display_name"), 80)
		if groupLabel == "" {
			groupLabel = fmt.Sprintf("额度组 %d", groupIndex+1)
		}
		buckets, _ := group["buckets"].([]any)
		for bucketIndex, bucketItem := range buckets {
			if len(result) >= cliProxyAPIMaxQuotaWindows {
				break
			}
			bucket, ok := bucketItem.(map[string]any)
			if !ok {
				continue
			}
			bucketID := safeCLIProxyAPIIdentifier(stringField(bucket, "bucketId", "bucket_id"), 100)
			if bucketID == "" {
				bucketID = fmt.Sprintf("bucket-%d", bucketIndex+1)
			}
			bucketLabel := safeCLIProxyAPIText(stringField(bucket, "displayName", "display_name"), 80)
			if bucketLabel == "" {
				bucketLabel = safeCLIProxyAPIText(stringField(bucket, "window"), 80)
			}
			if bucketLabel == "" {
				bucketLabel = fmt.Sprintf("额度 %d", bucketIndex+1)
			}
			remaining := cliProxyAPIRemainingFromFraction(firstNonNil(
				bucket["remainingFraction"], bucket["remaining_fraction"],
			))
			resetAt := parseCLIProxyAPITime(firstNonNil(bucket["resetTime"], bucket["reset_time"]))
			if remaining == nil && resetAt == "" {
				continue
			}
			result = append(result, AccountQuotaWindow{
				Key:              fmt.Sprintf("summary-%d-%s", groupIndex+1, bucketID),
				Label:            groupLabel + " · " + bucketLabel,
				RemainingPercent: remaining,
				ResetAt:          resetAt,
			})
		}
	}
	return mergeCLIProxyAPIQuotaWindows(nil, result)
}

func parseCLIProxyAPIAntigravityQuotaWindows(payload map[string]any) []AccountQuotaWindow {
	models := mapField(payload, "models")
	if models == nil {
		return nil
	}
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]AccountQuotaWindow, 0, len(keys))
	for _, modelID := range keys {
		if len(result) >= cliProxyAPIMaxQuotaWindows {
			break
		}
		entry, ok := models[modelID].(map[string]any)
		if !ok {
			continue
		}
		quota := mapField(entry, "quotaInfo", "quota_info")
		if quota == nil {
			continue
		}
		key := safeCLIProxyAPIIdentifier(modelID, 120)
		label := safeCLIProxyAPIText(stringField(entry, "displayName", "display_name"), 120)
		if label == "" {
			label = key
		}
		if key == "" || label == "" {
			continue
		}
		remaining := cliProxyAPIRemainingFromFraction(firstNonNil(quota["remainingFraction"], quota["remaining_fraction"]))
		resetAt := parseCLIProxyAPITime(firstNonNil(quota["resetTime"], quota["reset_time"]))
		if remaining == nil && resetAt == "" {
			continue
		}
		result = append(result, AccountQuotaWindow{Key: key, Label: label, RemainingPercent: remaining, ResetAt: resetAt})
	}
	return result
}

// parseCLIProxyAPIDirectQuotaWindows 兼容部分分支直接在 auth-files 中返回的百分比额度。
// 只接收含义明确的百分比或比例字段，不把调用次数、余额或未知数值换算成额度。
func parseCLIProxyAPIDirectQuotaWindows(raw map[string]any, now time.Time) []AccountQuotaWindow {
	result := make([]AccountQuotaWindow, 0)
	if windows, ok := firstNonNil(raw["quota_windows"], raw["quotaWindows"]).([]any); ok {
		for index, item := range windows {
			if len(result) >= cliProxyAPIMaxQuotaWindows {
				break
			}
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key := safeCLIProxyAPIIdentifier(stringField(entry, "key", "id"), 120)
			if key == "" {
				key = "quota-" + strconv.Itoa(index+1)
			}
			label := safeCLIProxyAPIText(stringField(entry, "label", "name"), 120)
			if label == "" {
				label = "额度"
			}
			remaining := cliProxyAPIPercentFromMap(entry)
			resetAt := cliProxyAPIResetAt(entry, now)
			if remaining == nil && resetAt == "" {
				continue
			}
			result = append(result, AccountQuotaWindow{Key: key, Label: label, RemainingPercent: remaining, ResetAt: resetAt})
		}
	}
	quota := mapField(raw, "quota")
	if quota != nil {
		remaining := cliProxyAPIPercentFromMap(quota)
		resetAt := cliProxyAPIResetAt(quota, now)
		if remaining != nil || resetAt != "" {
			result = append(result, AccountQuotaWindow{Key: "quota", Label: "总额度", RemainingPercent: remaining, ResetAt: resetAt})
		}
	}
	return mergeCLIProxyAPIQuotaWindows(nil, result)
}

func cliProxyAPIPercentFromMap(value map[string]any) *decimal.Decimal {
	if value == nil {
		return nil
	}
	if direct, ok := decimalFromAny(firstNonNil(value["remaining_percent"], value["remainingPercent"])); ok {
		return clampCLIProxyAPIPercent(direct)
	}
	if fraction := cliProxyAPIRemainingFromFraction(firstNonNil(value["remaining_fraction"], value["remainingFraction"])); fraction != nil {
		return fraction
	}
	return cliProxyAPIRemainingFromUsedPercent(firstNonNil(value["used_percent"], value["usedPercent"]))
}

func cliProxyAPIRemainingFromUsedPercent(value any) *decimal.Decimal {
	used, ok := decimalFromAny(value)
	if !ok {
		return nil
	}
	remaining := decimal.NewFromInt(100).Sub(used)
	return clampCLIProxyAPIPercent(remaining)
}

func cliProxyAPIRemainingFromFraction(value any) *decimal.Decimal {
	fraction, ok := decimalFromAny(value)
	if !ok {
		return nil
	}
	return clampCLIProxyAPIPercent(fraction.Mul(decimal.NewFromInt(100)))
}

func clampCLIProxyAPIPercent(value decimal.Decimal) *decimal.Decimal {
	if value.IsNegative() {
		value = decimal.Zero
	}
	if value.GreaterThan(decimal.NewFromInt(100)) {
		value = decimal.NewFromInt(100)
	}
	copyValue := value
	return &copyValue
}

func decimalFromAny(value any) (decimal.Decimal, bool) {
	parsed, err := parseDecimal(value)
	return parsed, err == nil
}

func cliProxyAPIResetAt(value map[string]any, now time.Time) string {
	if value == nil {
		return ""
	}
	if parsed := parseCLIProxyAPITime(firstNonNil(value["reset_at"], value["resetAt"], value["next_recover_at"], value["nextRecoverAt"])); parsed != "" {
		return parsed
	}
	seconds, ok := decimalFromAny(firstNonNil(value["reset_after_seconds"], value["resetAfterSeconds"]))
	if !ok || !seconds.IsPositive() {
		return ""
	}
	return now.Add(time.Duration(seconds.IntPart()) * time.Second).UTC().Format(time.RFC3339Nano)
}

func parseCLIProxyAPITime(value any) string {
	switch typed := value.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" {
			return ""
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, raw); err == nil && !parsed.IsZero() {
				return parsed.UTC().Format(time.RFC3339Nano)
			}
		}
		if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return cliProxyAPIUnixTime(number)
		}
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return cliProxyAPIUnixTime(number)
		}
	case float64:
		return cliProxyAPIUnixTime(int64(typed))
	case int64:
		return cliProxyAPIUnixTime(typed)
	case int:
		return cliProxyAPIUnixTime(int64(typed))
	}
	return ""
}

func cliProxyAPIUnixTime(value int64) string {
	if value <= 0 {
		return ""
	}
	// 13 位时间戳按毫秒处理，避免把真实到期时间解析到遥远未来。
	if value > 10_000_000_000 {
		value /= 1000
	}
	parsed := time.Unix(value, 0).UTC()
	if parsed.Year() < 2000 || parsed.Year() > 2200 {
		return ""
	}
	return parsed.Format(time.RFC3339Nano)
}

func cliProxyAPIPlanType(raw map[string]any) string {
	for _, candidate := range []string{
		stringField(raw, "plan_type", "planType"),
		stringField(mapField(raw, "id_token", "idToken"), "plan_type", "planType", "chatgpt_plan_type"),
	} {
		if plan := safeCLIProxyAPIIdentifier(candidate, 80); plan != "" {
			return plan
		}
	}
	return ""
}

func parseCLIProxyAPISubscriptionExpiry(raw map[string]any) string {
	idToken := mapField(raw, "id_token", "idToken")
	if idToken == nil {
		return ""
	}
	return parseCLIProxyAPITime(firstNonNil(
		idToken["chatgpt_subscription_active_until"],
		idToken["subscription_active_until"],
		idToken["subscription_expires_at"],
	))
}

func cliProxyAPICodexAccountID(raw map[string]any) string {
	idToken := mapField(raw, "id_token", "idToken")
	if idToken == nil {
		return ""
	}
	accountID := stringField(idToken, "chatgpt_account_id", "account_id")
	if accountID == "" {
		accountID = stringField(mapField(idToken, "https://api.openai.com/auth"), "chatgpt_account_id")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len(accountID) > 200 || strings.ContainsAny(accountID, "\r\n\x00") {
		return ""
	}
	return accountID
}

func cliProxyAPIGoogleProjectID(payload map[string]any) string {
	value := firstNonNil(payload["cloudaicompanionProject"], payload["project"])
	if object, ok := value.(map[string]any); ok {
		value = firstNonNil(object["id"], object["projectId"])
	}
	text, _ := value.(string)
	return safeCLIProxyAPIIdentifier(text, 160)
}

func cliProxyAPIGooglePlanType(payload map[string]any) string {
	for _, tier := range []map[string]any{
		mapField(payload, "paidTier", "paid_tier"),
		mapField(payload, "currentTier", "current_tier"),
	} {
		if tier == nil {
			continue
		}
		if plan := safeCLIProxyAPIIdentifier(stringField(tier, "id"), 80); plan != "" {
			return normalizeCLIProxyAPIGooglePlanType(plan)
		}
		if plan := safeCLIProxyAPIText(stringField(tier, "name"), 80); plan != "" {
			return normalizeCLIProxyAPIGooglePlanType(plan)
		}
	}
	return ""
}

// normalizeCLIProxyAPIGooglePlanType 统一常见 Antigravity 套餐标识，方便前端按账号类型筛选。
func normalizeCLIProxyAPIGooglePlanType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "free-tier":
		return "free"
	case "g1-pro-tier":
		return "pro"
	case "g1-ultra-tier":
		return "ultra"
	case "g1-ultra-lite-tier":
		return "ultra-lite"
	default:
		return value
	}
}

func mergeCLIProxyAPIQuotaWindows(current, incoming []AccountQuotaWindow) []AccountQuotaWindow {
	merged := make(map[string]AccountQuotaWindow, len(current)+len(incoming))
	order := make([]string, 0, len(current)+len(incoming))
	for _, collection := range [][]AccountQuotaWindow{current, incoming} {
		for _, window := range collection {
			key := safeCLIProxyAPIIdentifier(window.Key, 120)
			label := safeCLIProxyAPIText(window.Label, 120)
			if key == "" || label == "" {
				continue
			}
			window.Key = key
			window.Label = label
			window.ResetAt = parseCLIProxyAPITime(window.ResetAt)
			if window.RemainingPercent != nil {
				window.RemainingPercent = clampCLIProxyAPIPercent(*window.RemainingPercent)
			}
			if _, exists := merged[key]; !exists {
				order = append(order, key)
			}
			merged[key] = window
			if len(order) >= cliProxyAPIMaxQuotaWindows {
				break
			}
		}
	}
	result := make([]AccountQuotaWindow, 0, len(order))
	for _, key := range order {
		result = append(result, merged[key])
	}
	return result
}

func mapField(object map[string]any, names ...string) map[string]any {
	if object == nil {
		return nil
	}
	for _, name := range names {
		if value, ok := object[name].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func flexibleBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}
