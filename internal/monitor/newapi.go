package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

type newAPIAdapter struct {
	http     *secureHTTPClient
	mu       sync.Mutex
	sessions map[string]newAPICachedSession
}

type newAPICachedSession struct {
	session *requestSession
	headers http.Header
}

type newAPIQuotaDisplay struct {
	quotaPerUnit decimal.Decimal
	displayType  string
	exchangeRate decimal.Decimal
	unit         string
}

func newNewAPIAdapter(client *secureHTTPClient) *newAPIAdapter {
	return &newAPIAdapter{http: client, sessions: make(map[string]newAPICachedSession)}
}

func (adapter *newAPIAdapter) Kind() TargetKind {
	return TargetKindNewAPI
}

func (adapter *newAPIAdapter) Check(ctx context.Context, target TargetConfig) (Snapshot, error) {
	target = ensureTargetKind(target, adapter.Kind())
	snapshot := newSnapshot(target)
	// 密码登录会复用内存中的 Cookie 会话，避免重复使用已经过期的一次性验证码。
	session, authHeaders, cached := adapter.cachedSession(target)
	if session == nil {
		session = adapter.http.newSession(target.AllowPrivateNetwork)
	}

	statusURL, err := joinTargetURL(target.BaseURL, "/api/status")
	if err != nil {
		return Snapshot{}, err
	}
	var statusPayload any
	if err := session.doJSON(ctx, http.MethodGet, statusURL, nil, nil, &statusPayload); err != nil {
		return Snapshot{}, err
	}
	statusData, err := newAPIEnvelopeObject(statusPayload, false)
	if err != nil {
		return Snapshot{}, err
	}
	display := parseNewAPIQuotaDisplay(statusData)

	// 访问令牌和静态 Cookie 每次直接组装请求头，只有密码登录会写入会话缓存。
	if !cached {
		authHeaders, err = adapter.authenticate(ctx, session, target, statusData)
		if err != nil {
			return Snapshot{}, err
		}
		adapter.storeSession(target, session, authHeaders)
	}
	selfURL, err := joinTargetURL(target.BaseURL, "/api/user/self")
	if err != nil {
		return Snapshot{}, err
	}
	self, err := adapter.readSelf(ctx, session, selfURL, authHeaders)
	if err != nil {
		if !cached || !IsAuthFailure(err) {
			return Snapshot{}, err
		}
		adapter.deleteSession(target)
		// 缓存会话失效时只重新登录一次，后续错误交给注册器统一分类和重试。
		session = adapter.http.newSession(target.AllowPrivateNetwork)
		authHeaders, err = adapter.authenticate(ctx, session, target, statusData)
		if err != nil {
			return Snapshot{}, err
		}
		adapter.storeSession(target, session, authHeaders)
		self, err = adapter.readSelf(ctx, session, selfURL, authHeaders)
		if err != nil {
			return Snapshot{}, err
		}
	}
	rawQuota, err := decimalField(self, "quota", "balance")
	if err != nil {
		return Snapshot{}, checkError(ErrorClassResponse, "读取 New API 余额", "New API 响应缺少余额字段", 0, err)
	}
	balance, unit := display.convert(rawQuota)
	snapshot.Metrics = append(snapshot.Metrics, metricWithThreshold(target, MetricWalletBalance, "钱包余额", balance, unit))
	if !newAPIUserEnabled(self) {
		snapshot.Status = TargetStatusDisabled
		snapshot.Message = "New API 账号已被停用"
	}

	_, hasSubscriptionThreshold := target.Thresholds[MetricSubscriptionBalance]
	// 配置订阅阈值即视为需要读取订阅，无需再暴露一个前端开关。
	if target.NewAPI.IncludeSubscription || hasSubscriptionThreshold {
		subscription, err := adapter.readSubscription(ctx, session, target, authHeaders, display)
		if err != nil {
			if statusCodeOf(err) != http.StatusNotFound {
				return Snapshot{}, err
			}
		} else {
			snapshot.Metrics = append(snapshot.Metrics, subscription)
		}
	}
	return snapshot, nil
}

// ReadGroupMultipliers 读取当前 New API 用户真正可用的分组倍率，不访问管理员渠道配置。
func (adapter *newAPIAdapter) ReadGroupMultipliers(ctx context.Context, target TargetConfig) (GroupMultiplierResult, error) {
	target = ensureTargetKind(target, adapter.Kind())
	session, authHeaders, cached := adapter.cachedSession(target)
	if session == nil {
		session = adapter.http.newSession(target.AllowPrivateNetwork)
	}
	statusURL, err := joinTargetURL(target.BaseURL, "/api/status")
	if err != nil {
		return GroupMultiplierResult{}, err
	}
	var statusPayload any
	if err := session.doJSON(ctx, http.MethodGet, statusURL, nil, nil, &statusPayload); err != nil {
		return GroupMultiplierResult{}, err
	}
	statusData, err := newAPIEnvelopeObject(statusPayload, false)
	if err != nil {
		return GroupMultiplierResult{}, err
	}
	if !cached {
		authHeaders, err = adapter.authenticate(ctx, session, target, statusData)
		if err != nil {
			return GroupMultiplierResult{}, err
		}
		adapter.storeSession(target, session, authHeaders)
	}
	groups, err := adapter.readGroupMultipliers(ctx, session, target, authHeaders)
	if err != nil && cached && IsAuthFailure(err) {
		adapter.deleteSession(target)
		session = adapter.http.newSession(target.AllowPrivateNetwork)
		authHeaders, err = adapter.authenticate(ctx, session, target, statusData)
		if err == nil {
			adapter.storeSession(target, session, authHeaders)
			groups, err = adapter.readGroupMultipliers(ctx, session, target, authHeaders)
		}
	}
	if err != nil {
		return GroupMultiplierResult{}, err
	}
	return GroupMultiplierResult{Groups: groups}, nil
}

func (adapter *newAPIAdapter) readGroupMultipliers(ctx context.Context, session *requestSession, target TargetConfig, headers http.Header) ([]GroupMultiplier, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/user/self/groups")
	if err != nil {
		return nil, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err == nil {
		data, parseErr := newAPIEnvelopeObject(payload, true)
		if parseErr != nil {
			return nil, parseErr
		}
		return parseNewAPIGroupMultipliers(data)
	} else if statusCodeOf(err) != http.StatusNotFound {
		return nil, err
	}

	// 较早版本没有当前用户分组端点时，回退到带登录态的价格页倍率表。
	if strings.TrimSpace(target.Credential.Cookie) == "" && !newAPIUsesPassword(target.Credential) {
		return nil, checkError(ErrorClassResponse, "读取 New API 分组倍率", "当前站点版本不支持访问令牌读取用户分组倍率，请升级站点或改用网页登录", http.StatusNotFound, nil)
	}
	pricingURL, err := joinTargetURL(target.BaseURL, "/api/pricing")
	if err != nil {
		return nil, err
	}
	payload = nil
	if err := session.doJSON(ctx, http.MethodGet, pricingURL, headers, nil, &payload); err != nil {
		// 部分站点会关闭价格导航模块并对价格页返回 403，这不代表登录凭据一定失效。
		if statusCodeOf(err) == http.StatusForbidden {
			return nil, checkError(ErrorClassRemote, "读取 New API 分组倍率", "当前站点未公开价格与旧版分组倍率", http.StatusForbidden, nil)
		}
		return nil, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 New API 分组倍率", "New API 分组倍率响应格式无效", 0, nil)
	}
	if success, exists := object["success"].(bool); exists && !success {
		return nil, checkError(ErrorClassAuth, "读取 New API 分组倍率", "New API 凭据无效或无权读取倍率", 0, nil)
	}
	ratios, ok := object["group_ratio"].(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 New API 分组倍率", "New API 响应缺少分组倍率", 0, nil)
	}
	descriptions, _ := object["usable_group"].(map[string]any)
	normalized := make(map[string]any, len(ratios))
	for key, ratio := range ratios {
		normalized[key] = map[string]any{"ratio": ratio, "desc": stringField(descriptions, key)}
	}
	return parseNewAPIGroupMultipliers(normalized)
}

func parseNewAPIGroupMultipliers(data map[string]any) ([]GroupMultiplier, error) {
	groups := make([]GroupMultiplier, 0, len(data))
	for key, raw := range data {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		ratioValue := raw
		description := ""
		if detail, ok := raw.(map[string]any); ok {
			ratioValue = detail["ratio"]
			description = strings.TrimSpace(stringField(detail, "desc", "description"))
		}
		if newAPIAutomaticGroupRatio(ratioValue) {
			// 自动选择没有固定数值，无法建立稳定的倍率基准。
			continue
		}
		ratio, err := parseGroupMultiplierDecimal(ratioValue)
		if err != nil || ratio.IsNegative() {
			return nil, checkError(ErrorClassResponse, "解析 New API 分组倍率", "New API 分组包含无效倍率", 0, err)
		}
		groups = append(groups, GroupMultiplier{Key: key, Name: key, Description: description, Multiplier: ratio})
	}
	if len(groups) == 0 {
		return nil, checkError(ErrorClassResponse, "解析 New API 分组倍率", "没有读取到可监控的固定分组倍率", 0, nil)
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left].Key < groups[right].Key })
	return groups, nil
}

func newAPIAutomaticGroupRatio(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "自动", "auto", "automatic":
		return true
	default:
		return false
	}
}

func (adapter *newAPIAdapter) readSelf(ctx context.Context, session *requestSession, endpoint string, headers http.Header) (map[string]any, error) {
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return nil, err
	}
	return newAPIEnvelopeObject(payload, true)
}

// VerifyBrowserCredential 使用浏览器会话读取当前用户，并校验会话所属用户与提交的用户 ID 一致。
func (adapter *newAPIAdapter) VerifyBrowserCredential(ctx context.Context, target TargetConfig) (Credential, error) {
	target = ensureTargetKind(target, adapter.Kind())
	cookie := strings.TrimSpace(target.Credential.Cookie)
	userID := strings.TrimSpace(target.Credential.UserID)
	if cookie == "" {
		return Credential{}, checkError(ErrorClassConfig, "校验 New API 网页登录", "网页登录会话不能为空", 0, nil)
	}
	if strings.ContainsAny(cookie, "\r\n") {
		return Credential{}, checkError(ErrorClassConfig, "校验 New API 网页登录", "网页登录会话格式无效", 0, nil)
	}
	providedID := ""
	if userID != "" {
		var err error
		providedID, err = parsePositiveInt64String(userID)
		if err != nil {
			return Credential{}, checkError(ErrorClassConfig, "校验 New API 网页登录", "用户 ID 格式无效", 0, err)
		}
	}

	endpoint, err := joinTargetURL(target.BaseURL, "/api/user/self")
	if err != nil {
		return Credential{}, err
	}
	headers := make(http.Header)
	headers.Set("Cookie", cookie)
	if providedID != "" {
		headers.Set("New-Api-User", providedID)
	}
	self, err := adapter.readSelf(ctx, adapter.http.newSession(target.AllowPrivateNetwork), endpoint, headers)
	if err != nil {
		return Credential{}, err
	}
	remoteIDValue, exists := firstValue(self, "id", "user_id")
	if !exists {
		return Credential{}, checkError(ErrorClassResponse, "校验 New API 网页登录", "渠道响应缺少有效用户 ID", 0, nil)
	}
	remoteID, err := parsePositiveInt64String(remoteIDValue)
	if err != nil {
		return Credential{}, checkError(ErrorClassResponse, "校验 New API 网页登录", "渠道响应缺少有效用户 ID", 0, err)
	}
	if providedID != "" && remoteID != providedID {
		return Credential{}, checkError(ErrorClassAuth, "校验 New API 网页登录", "网页登录会话与用户 ID 不匹配", 0, err)
	}
	return Credential{Cookie: cookie, UserID: remoteID}, nil
}

func (adapter *newAPIAdapter) cachedSession(target TargetConfig) (*requestSession, http.Header, bool) {
	if !newAPIUsesPassword(target.Credential) {
		return nil, nil, false
	}
	adapter.mu.Lock()
	cached, exists := adapter.sessions[newAPISessionKey(target)]
	adapter.mu.Unlock()
	if !exists || cached.session == nil {
		return nil, nil, false
	}
	return cached.session, cached.headers.Clone(), true
}

func (adapter *newAPIAdapter) storeSession(target TargetConfig, session *requestSession, headers http.Header) {
	if !newAPIUsesPassword(target.Credential) || session == nil {
		return
	}
	adapter.mu.Lock()
	adapter.sessions[newAPISessionKey(target)] = newAPICachedSession{session: session, headers: headers.Clone()}
	adapter.mu.Unlock()
}

func (adapter *newAPIAdapter) deleteSession(target TargetConfig) {
	adapter.mu.Lock()
	cached := adapter.sessions[newAPISessionKey(target)]
	delete(adapter.sessions, newAPISessionKey(target))
	adapter.mu.Unlock()
	if cached.session != nil {
		cached.session.client.CloseIdleConnections()
	}
}

func newAPIUsesPassword(credential Credential) bool {
	return strings.TrimSpace(credential.AccessToken) == "" && strings.TrimSpace(credential.Cookie) == "" &&
		(strings.TrimSpace(credential.Username) != "" || strings.TrimSpace(credential.Email) != "") && credential.Password != ""
}

func newAPISessionKey(target TargetConfig) string {
	username := target.Credential.Username
	if strings.TrimSpace(username) == "" {
		username = target.Credential.Email
	}
	return strings.TrimSpace(target.ID) + "|" + strings.TrimSpace(target.BaseURL) + "|" +
		strings.ToLower(strings.TrimSpace(username)) + "|" + credentialFingerprint(target.Credential.Password, target.Credential.TOTPSecret, target.Credential.UserID)
}

func (adapter *newAPIAdapter) authenticate(ctx context.Context, session *requestSession, target TargetConfig, status map[string]any) (http.Header, error) {
	credential := target.Credential
	headers := make(http.Header)
	userID := strings.TrimSpace(credential.UserID)
	if userID != "" {
		parsedUserID, err := parsePositiveInt64String(userID)
		if err != nil {
			return nil, checkError(ErrorClassConfig, "配置 New API 认证", "用户 ID 格式无效", 0, err)
		}
		userID = parsedUserID
	}
	if strings.TrimSpace(credential.AccessToken) != "" {
		if userID == "" {
			// 新版管理令牌使用标准 Bearer 身份，不再要求额外的旧版用户编号。
			setBearer(headers, credential.AccessToken)
			return headers, nil
		}
		headers.Set("Authorization", strings.TrimSpace(credential.AccessToken))
		headers.Set("New-Api-User", userID)
		return headers, nil
	}
	if cookie := strings.TrimSpace(credential.Cookie); cookie != "" {
		if userID == "" {
			return nil, checkError(ErrorClassConfig, "配置 New API 认证", "使用 Cookie 时必须填写用户 ID", 0, nil)
		}
		if strings.ContainsAny(cookie, "\r\n") {
			return nil, checkError(ErrorClassConfig, "配置 New API 认证", "Cookie 格式无效", 0, nil)
		}
		headers.Set("Cookie", cookie)
		headers.Set("New-Api-User", userID)
		return headers, nil
	}

	username := strings.TrimSpace(credential.Username)
	if username == "" {
		username = strings.TrimSpace(credential.Email)
	}
	if username == "" || credential.Password == "" {
		return nil, checkError(ErrorClassConfig, "配置 New API 认证", "请填写网页登录会话、访问令牌或账号密码", 0, nil)
	}
	if enabled, ok := boolField(status, "turnstile_check"); ok && enabled {
		return nil, checkError(ErrorClassAuth, "登录 New API", "站点启用了浏览器验证，请改用网页登录或访问令牌", 0, nil)
	}
	loginURL, err := joinTargetURL(target.BaseURL, "/api/user/login")
	if err != nil {
		return nil, err
	}
	loginBody, _ := json.Marshal(map[string]string{"username": username, "password": credential.Password})
	var loginPayload any
	if err := session.doJSON(ctx, http.MethodPost, loginURL, nil, loginBody, &loginPayload); err != nil {
		return nil, err
	}
	loginData, err := newAPIEnvelopeObject(loginPayload, true)
	if err != nil {
		return nil, err
	}
	if required, _ := boolField(loginData, "require_2fa", "requires_2fa"); required {
		code, err := currentTOTPCode(credential)
		if err != nil {
			return nil, err
		}
		verifyURL, err := joinTargetURL(target.BaseURL, "/api/user/login/2fa")
		if err != nil {
			return nil, err
		}
		verifyBody, _ := json.Marshal(map[string]string{"code": code})
		var verifyPayload any
		if err := session.doJSON(ctx, http.MethodPost, verifyURL, nil, verifyBody, &verifyPayload); err != nil {
			return nil, err
		}
		loginData, err = newAPIEnvelopeObject(verifyPayload, true)
		if err != nil {
			return nil, err
		}
	}
	if userID == "" {
		if id, exists := loginData["id"]; exists {
			parsed, parseErr := parsePositiveInt64String(id)
			if parseErr == nil {
				userID = parsed
			}
		}
	}
	if userID == "" {
		return nil, checkError(ErrorClassResponse, "登录 New API", "登录成功但响应缺少用户 ID", 0, nil)
	}
	headers.Set("New-Api-User", userID)
	return headers, nil
}

func (adapter *newAPIAdapter) readSubscription(ctx context.Context, session *requestSession, target TargetConfig, headers http.Header, display newAPIQuotaDisplay) (Metric, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/subscription/self")
	if err != nil {
		return Metric{}, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return Metric{}, err
	}
	data, err := newAPIEnvelopeObject(payload, true)
	if err != nil {
		return Metric{}, err
	}
	remaining := decimal.Zero
	items, _ := data["subscriptions"].([]any)
	for _, item := range items {
		summary, ok := item.(map[string]any)
		if !ok {
			continue
		}
		subscription, ok := summary["subscription"].(map[string]any)
		if !ok {
			subscription = summary
		}
		if status := strings.ToLower(stringField(subscription, "status")); status != "" && status != "active" {
			continue
		}
		total, totalErr := decimalField(subscription, "amount_total")
		used, usedErr := decimalField(subscription, "amount_used")
		if totalErr != nil || usedErr != nil {
			continue
		}
		available := total.Sub(used)
		if available.IsPositive() {
			remaining = remaining.Add(available)
		}
	}
	value, unit := display.convert(remaining)
	return metricWithThreshold(target, MetricSubscriptionBalance, "订阅余额", value, unit), nil
}

func newAPIEnvelopeObject(payload any, auth bool) (map[string]any, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 New API 响应", "New API 响应格式无效", 0, nil)
	}
	if success, exists := object["success"].(bool); exists && !success {
		class := ErrorClassRemote
		message := "New API 返回了失败状态"
		if auth {
			class = ErrorClassAuth
			message = "New API 凭据无效或登录失败"
		}
		return nil, checkError(class, "解析 New API 响应", message, 0, nil)
	}
	data, ok := object["data"].(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 New API 响应", "New API 响应缺少 data", 0, nil)
	}
	return data, nil
}

func parseNewAPIQuotaDisplay(status map[string]any) newAPIQuotaDisplay {
	quotaPerUnit, err := decimalField(status, "quota_per_unit")
	if err != nil || !quotaPerUnit.IsPositive() {
		quotaPerUnit = decimal.NewFromInt(500000)
	}
	displayType := strings.ToUpper(stringField(status, "quota_display_type"))
	if displayType == "" {
		displayType = "USD"
	}
	result := newAPIQuotaDisplay{quotaPerUnit: quotaPerUnit, displayType: displayType, exchangeRate: decimal.NewFromInt(1), unit: "USD"}
	switch displayType {
	case "TOKENS":
		result.unit = "tokens"
	case "CNY":
		result.unit = "CNY"
		if rate, err := decimalField(status, "usd_exchange_rate"); err == nil && rate.IsPositive() {
			result.exchangeRate = rate
		}
	case "CUSTOM":
		result.unit = stringField(status, "custom_currency_symbol")
		if result.unit == "" {
			result.unit = "CUSTOM"
		}
		if rate, err := decimalField(status, "custom_currency_exchange_rate"); err == nil && rate.IsPositive() {
			result.exchangeRate = rate
		}
	}
	return result
}

func (display newAPIQuotaDisplay) convert(raw decimal.Decimal) (decimal.Decimal, string) {
	if display.displayType == "TOKENS" {
		return raw, display.unit
	}
	return raw.Div(display.quotaPerUnit).Mul(display.exchangeRate), display.unit
}

func newAPIUserEnabled(user map[string]any) bool {
	value, exists := user["status"]
	if !exists {
		return true
	}
	if parsed, err := parseDecimal(value); err == nil {
		return parsed.Equal(decimal.NewFromInt(1))
	}
	status := strings.ToLower(strings.TrimSpace(stringField(user, "status")))
	return status == "" || status == "active" || status == "enabled" || status == "normal" || status == "正常"
}

var _ Adapter = (*newAPIAdapter)(nil)
var _ BrowserCredentialVerifier = (*newAPIAdapter)(nil)
var _ GroupMultiplierReader = (*newAPIAdapter)(nil)
