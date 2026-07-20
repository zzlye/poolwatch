package monitor

import (
	"context"
	"encoding/json"
	"net/http"
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
	providedID := decimal.Zero
	if userID != "" {
		var err error
		providedID, err = decimal.NewFromString(userID)
		if err != nil || !providedID.IsPositive() || !providedID.Equal(providedID.Truncate(0)) {
			return Credential{}, checkError(ErrorClassConfig, "校验 New API 网页登录", "用户 ID 格式无效", 0, err)
		}
	}

	endpoint, err := joinTargetURL(target.BaseURL, "/api/user/self")
	if err != nil {
		return Credential{}, err
	}
	headers := make(http.Header)
	headers.Set("Cookie", cookie)
	if providedID.IsPositive() {
		headers.Set("New-Api-User", providedID.StringFixed(0))
	}
	self, err := adapter.readSelf(ctx, adapter.http.newSession(target.AllowPrivateNetwork), endpoint, headers)
	if err != nil {
		return Credential{}, err
	}
	remoteID, err := decimalField(self, "id", "user_id")
	if err != nil || !remoteID.IsPositive() || !remoteID.Equal(remoteID.Truncate(0)) {
		return Credential{}, checkError(ErrorClassResponse, "校验 New API 网页登录", "渠道响应缺少有效用户 ID", 0, err)
	}
	if providedID.IsPositive() && !remoteID.Equal(providedID) {
		return Credential{}, checkError(ErrorClassAuth, "校验 New API 网页登录", "网页登录会话与用户 ID 不匹配", 0, err)
	}
	return Credential{Cookie: cookie, UserID: remoteID.StringFixed(0)}, nil
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
	if strings.TrimSpace(credential.AccessToken) != "" {
		if userID == "" {
			return nil, checkError(ErrorClassConfig, "配置 New API 认证", "使用管理访问令牌时必须填写用户 ID", 0, nil)
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
			parsed, parseErr := parseDecimal(id)
			if parseErr == nil {
				userID = parsed.StringFixed(0)
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
