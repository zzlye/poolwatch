package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type sub2APIAdapter struct {
	http   *secureHTTPClient
	mu     sync.Mutex
	tokens map[string]sub2APIToken
	now    func() time.Time
}

type sub2APIToken struct {
	accessToken  string
	refreshToken string
	validUntil   time.Time
}

func newSub2APIAdapter(client *secureHTTPClient) *sub2APIAdapter {
	return &sub2APIAdapter{http: client, tokens: make(map[string]sub2APIToken), now: time.Now}
}

func (adapter *sub2APIAdapter) Kind() TargetKind {
	return TargetKindSub2API
}

func (adapter *sub2APIAdapter) Check(ctx context.Context, target TargetConfig) (Snapshot, error) {
	target = ensureTargetKind(target, adapter.Kind())
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	// 优先使用尚未过期的内存令牌，避免每轮检测都触发登录限流。
	token, err := adapter.resolveToken(ctx, session, target)
	if err != nil {
		return Snapshot{}, err
	}
	me, err := adapter.readCurrentUser(ctx, session, target, token.accessToken)
	if err != nil && IsAuthFailure(err) {
		// 只有明确的认证失败才刷新或重新登录，网络错误由 Registry 统一重试。
		if token.refreshToken != "" || sub2APIUsesPassword(target.Credential) {
			token, err = adapter.renewToken(ctx, session, target, token.refreshToken)
			if err == nil {
				me, err = adapter.readCurrentUser(ctx, session, target, token.accessToken)
			}
		}
	}
	if err != nil {
		return Snapshot{}, err
	}

	balance, err := decimalField(me, "balance", "wallet_balance", "remaining_balance", "credit", "quota")
	if err != nil {
		return Snapshot{}, checkError(ErrorClassResponse, "读取 Sub2API 余额", "Sub2API 响应缺少余额字段", 0, err)
	}
	snapshot := newSnapshot(target)
	snapshot.Metrics = append(snapshot.Metrics, metricWithThreshold(target, MetricWalletBalance, "钱包余额", balance, "USD"))
	updatedCredential := target.Credential
	updatedCredential.AccessToken = token.accessToken
	if token.refreshToken != "" {
		updatedCredential.RefreshToken = token.refreshToken
	}
	updatedCredential.TOTPCode = ""
	snapshot.CredentialUpdate = &updatedCredential
	if !sub2APIUserEnabled(me) {
		snapshot.Status = TargetStatusDisabled
		snapshot.Message = "Sub2API 账号状态异常"
	}
	return snapshot, nil
}

// ReadGroupMultipliers 合并 Sub2API 分组默认倍率与当前用户专属倍率。
func (adapter *sub2APIAdapter) ReadGroupMultipliers(ctx context.Context, target TargetConfig) (GroupMultiplierResult, error) {
	target = ensureTargetKind(target, adapter.Kind())
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	token, err := adapter.resolveToken(ctx, session, target)
	if err != nil {
		return GroupMultiplierResult{}, err
	}
	result := GroupMultiplierResult{CredentialUpdate: sub2APIUpdatedCredential(target.Credential, token)}
	groups, err := adapter.readGroupMultipliers(ctx, session, target, token.accessToken)
	if err != nil && IsAuthFailure(err) && (token.refreshToken != "" || sub2APIUsesPassword(target.Credential)) {
		token, err = adapter.renewToken(ctx, session, target, token.refreshToken)
		if err == nil {
			result.CredentialUpdate = sub2APIUpdatedCredential(target.Credential, token)
			groups, err = adapter.readGroupMultipliers(ctx, session, target, token.accessToken)
		}
	}
	if err != nil {
		return result, err
	}
	result.Groups = groups
	return result, nil
}

func sub2APIUpdatedCredential(current Credential, token sub2APIToken) *Credential {
	current.AccessToken = token.accessToken
	if token.refreshToken != "" {
		current.RefreshToken = token.refreshToken
	}
	current.TOTPCode = ""
	return &current
}

func (adapter *sub2APIAdapter) readGroupMultipliers(ctx context.Context, session *requestSession, target TargetConfig, accessToken string) ([]GroupMultiplier, error) {
	headers := make(http.Header)
	setBearer(headers, accessToken)
	availableURL, err := joinTargetURL(target.BaseURL, "/api/v1/groups/available")
	if err != nil {
		return nil, err
	}
	var availablePayload any
	if err := session.doJSON(ctx, http.MethodGet, availableURL, headers, nil, &availablePayload); err != nil {
		return nil, err
	}
	availableData, err := sub2APIEnvelopeValue(availablePayload, true)
	if err != nil {
		return nil, err
	}
	items, ok := availableData.([]any)
	if !ok {
		if object, objectOK := availableData.(map[string]any); objectOK {
			items, ok = object["groups"].([]any)
		}
	}
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 可用分组格式无效", 0, nil)
	}

	var serverLocation *time.Location
	if sub2APIHasPeakRate(items) {
		serverLocation, err = adapter.readServerLocation(ctx, session, target)
		if err != nil {
			return nil, err
		}
	}

	overrides := make(map[string]any)
	ratesURL, err := joinTargetURL(target.BaseURL, "/api/v1/groups/rates")
	if err != nil {
		return nil, err
	}
	var ratesPayload any
	if err := session.doJSON(ctx, http.MethodGet, ratesURL, headers, nil, &ratesPayload); err != nil {
		// 兼容尚未提供用户专属倍率端点的旧版 Sub2API。
		if statusCodeOf(err) != http.StatusNotFound {
			return nil, err
		}
	} else {
		ratesData, parseErr := sub2APIEnvelopeValue(ratesPayload, true)
		if parseErr != nil {
			return nil, parseErr
		}
		if ratesData == nil {
			// 未配置任何用户专属倍率时部分版本会返回 data: null。
			overrides = make(map[string]any)
		} else if typed, typedOK := ratesData.(map[string]any); typedOK {
			overrides = typed
			if nested, nestedOK := typed["rates"].(map[string]any); nestedOK {
				overrides = nested
			}
		} else {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 专属倍率格式无效", 0, nil)
		}
	}

	groups := make([]GroupMultiplier, 0, len(items))
	for _, raw := range items {
		item, itemOK := raw.(map[string]any)
		if !itemOK {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 分组条目格式无效", 0, nil)
		}
		key, idErr := sub2APIGroupKey(item)
		if idErr != nil {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 分组标识无效", 0, idErr)
		}
		ratioValue, exists := firstValue(item, "rate_multiplier", "multiplier")
		if !exists {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 分组默认倍率无效", 0, nil)
		}
		ratio, ratioErr := parseGroupMultiplierDecimal(ratioValue)
		if ratioErr != nil || !ratio.IsPositive() {
			return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 分组默认倍率无效", 0, ratioErr)
		}
		if override, exists := overrides[key]; exists {
			parsed, parseErr := parseGroupMultiplierDecimal(override)
			if parseErr != nil || !parsed.IsPositive() {
				return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 用户专属倍率无效", 0, parseErr)
			}
			ratio = parsed
		}
		if serverLocation != nil {
			peak, peakErr := sub2APIPeakMultiplier(item, adapter.now().In(serverLocation))
			if peakErr != nil {
				return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 高峰倍率无效", 0, peakErr)
			}
			ratio = ratio.Mul(peak)
			if _, rangeErr := parseGroupMultiplierDecimal(ratio); rangeErr != nil {
				return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "Sub2API 实际倍率超出安全范围", 0, rangeErr)
			}
		}
		name := strings.TrimSpace(stringField(item, "name"))
		if name == "" {
			name = "分组 " + key
		}
		groups = append(groups, GroupMultiplier{
			Key: key, Name: name, Description: strings.TrimSpace(stringField(item, "description")), Multiplier: ratio,
		})
	}
	if len(groups) == 0 {
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 分组倍率", "没有读取到可监控的分组倍率", 0, nil)
	}
	sort.Slice(groups, func(left, right int) bool {
		if groups[left].Name == groups[right].Name {
			return groups[left].Key < groups[right].Key
		}
		return groups[left].Name < groups[right].Name
	})
	return groups, nil
}

func sub2APIHasPeakRate(items []any) bool {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		enabled, _ := boolField(item, "peak_rate_enabled")
		if enabled && strings.EqualFold(strings.TrimSpace(stringField(item, "subscription_type")), "subscription") {
			return true
		}
	}
	return false
}

func (adapter *sub2APIAdapter) readServerLocation(ctx context.Context, session *requestSession, target TargetConfig) (*time.Location, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/settings/public")
	if err != nil {
		return nil, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, nil, nil, &payload); err != nil {
		return nil, err
	}
	settings, err := sub2APIEnvelopeObject(payload, false)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(stringField(settings, "server_timezone"))
	offset := strings.TrimSpace(stringField(settings, "server_utc_offset"))
	seconds, err := sub2APIUTCOffsetSeconds(offset)
	if err != nil {
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 服务器时区", "Sub2API 未返回有效的服务器时区", 0, err)
	}
	if name == "" {
		name = offset
	}
	// 只判断本次检测时刻是否处于高峰，使用服务端公开的当前偏移可避免依赖本机时区数据。
	return time.FixedZone(name, seconds), nil
}

func sub2APIUTCOffsetSeconds(value string) (int, error) {
	if len(value) != 6 || (value[0] != '+' && value[0] != '-') || value[3] != ':' {
		return 0, errors.New("服务器 UTC 偏移格式无效")
	}
	hour, hourErr := strconv.Atoi(value[1:3])
	minute, minuteErr := strconv.Atoi(value[4:6])
	if hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
		return 0, errors.New("服务器 UTC 偏移格式无效")
	}
	seconds := hour*60*60 + minute*60
	if value[0] == '-' {
		seconds = -seconds
	}
	return seconds, nil
}

func sub2APIGroupKey(item map[string]any) (string, error) {
	raw, exists := firstValue(item, "id", "group_id")
	if !exists {
		return "", errors.New("缺少分组标识")
	}
	return parsePositiveInt64String(raw)
}

func sub2APIPeakMultiplier(item map[string]any, now time.Time) (decimal.Decimal, error) {
	enabled, _ := boolField(item, "peak_rate_enabled")
	if !enabled || !strings.EqualFold(strings.TrimSpace(stringField(item, "subscription_type")), "subscription") {
		return decimal.NewFromInt(1), nil
	}
	start, startOK := sub2APIParseMinutes(stringField(item, "peak_start"))
	end, endOK := sub2APIParseMinutes(stringField(item, "peak_end"))
	if !startOK || !endOK || start >= end {
		// 与 Sub2API 官方计费行为一致：高峰窗口无效时安全降级为一倍。
		return decimal.NewFromInt(1), nil
	}
	current := now.Hour()*60 + now.Minute()
	if current < start || current >= end {
		return decimal.NewFromInt(1), nil
	}
	raw, exists := firstValue(item, "peak_rate_multiplier")
	if !exists {
		return decimal.Zero, errors.New("缺少高峰倍率")
	}
	peak, err := parseGroupMultiplierDecimal(raw)
	if err != nil || peak.IsNegative() {
		return decimal.Zero, errors.New("高峰倍率无效")
	}
	return peak, nil
}

func sub2APIParseMinutes(value string) (int, bool) {
	value = strings.TrimSpace(value)
	colon := strings.IndexByte(value, ':')
	if (colon != 1 && colon != 2) || len(value)-colon-1 != 2 {
		return 0, false
	}
	hour, hourErr := strconv.Atoi(value[:colon])
	minute, minuteErr := strconv.Atoi(value[colon+1:])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

// VerifyBrowserCredential 校验网页登录返回的令牌，并保留服务端轮换后的最新令牌。
func (adapter *sub2APIAdapter) VerifyBrowserCredential(ctx context.Context, target TargetConfig) (Credential, error) {
	target = ensureTargetKind(target, adapter.Kind())
	if strings.TrimSpace(target.Credential.AccessToken) == "" && strings.TrimSpace(target.Credential.RefreshToken) == "" {
		return Credential{}, checkError(ErrorClassConfig, "校验 Sub2API 网页登录", "网页登录返回的访问令牌和刷新令牌均为空", 0, nil)
	}
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	token, err := adapter.resolveToken(ctx, session, target)
	if err != nil {
		return Credential{}, err
	}
	_, err = adapter.readCurrentUser(ctx, session, target, token.accessToken)
	if err != nil && IsAuthFailure(err) && token.refreshToken != "" {
		token, err = adapter.refresh(ctx, session, target, token.refreshToken)
		if err == nil {
			_, err = adapter.readCurrentUser(ctx, session, target, token.accessToken)
		}
	}
	if err != nil {
		return Credential{}, err
	}
	return Credential{AccessToken: token.accessToken, RefreshToken: token.refreshToken}, nil
}

func (adapter *sub2APIAdapter) resolveToken(ctx context.Context, session *requestSession, target TargetConfig) (sub2APIToken, error) {
	cacheKey := sub2APICacheKey(target)
	adapter.mu.Lock()
	cached, exists := adapter.tokens[cacheKey]
	adapter.mu.Unlock()
	if exists {
		if cached.accessToken != "" && time.Now().Before(cached.validUntil) {
			return cached, nil
		}
		if cached.refreshToken != "" {
			return adapter.renewToken(ctx, session, target, cached.refreshToken)
		}
	}
	credential := target.Credential
	if strings.TrimSpace(credential.AccessToken) != "" {
		return sub2APIToken{accessToken: strings.TrimSpace(credential.AccessToken), refreshToken: strings.TrimSpace(credential.RefreshToken)}, nil
	}
	if strings.TrimSpace(credential.RefreshToken) != "" {
		return adapter.renewToken(ctx, session, target, credential.RefreshToken)
	}
	return adapter.login(ctx, session, target)
}

// renewToken 优先续期令牌；密码登录渠道的刷新令牌失效时自动重新登录。
func (adapter *sub2APIAdapter) renewToken(ctx context.Context, session *requestSession, target TargetConfig, refreshToken string) (sub2APIToken, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken != "" {
		token, err := adapter.refresh(ctx, session, target, refreshToken)
		if err == nil {
			return token, nil
		}
		if !IsAuthFailure(err) || !sub2APIUsesPassword(target.Credential) {
			return sub2APIToken{}, err
		}
	}
	return adapter.login(ctx, session, target)
}

func sub2APIUsesPassword(credential Credential) bool {
	return strings.TrimSpace(credential.Email) != "" && credential.Password != ""
}

func (adapter *sub2APIAdapter) login(ctx context.Context, session *requestSession, target TargetConfig) (sub2APIToken, error) {
	credential := target.Credential
	if strings.TrimSpace(credential.Email) == "" || credential.Password == "" {
		return sub2APIToken{}, checkError(ErrorClassConfig, "配置 Sub2API 认证", "请使用网页登录，或填写访问令牌、刷新令牌或邮箱密码", 0, nil)
	}
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/auth/login")
	if err != nil {
		return sub2APIToken{}, err
	}
	body, _ := json.Marshal(map[string]string{"email": strings.TrimSpace(credential.Email), "password": credential.Password})
	var payload any
	if err := session.doJSON(ctx, http.MethodPost, endpoint, nil, body, &payload); err != nil {
		return sub2APIToken{}, err
	}
	data, err := sub2APIEnvelopeObject(payload, true)
	if err != nil {
		return sub2APIToken{}, err
	}
	if required, _ := boolField(data, "requires_2fa", "require_2fa"); required {
		code, err := currentTOTPCode(credential)
		if err != nil {
			return sub2APIToken{}, err
		}
		tempToken := stringField(data, "temp_token")
		if tempToken == "" {
			return sub2APIToken{}, checkError(ErrorClassResponse, "登录 Sub2API", "Sub2API 两步验证响应缺少临时令牌", 0, nil)
		}
		verifyURL, err := joinTargetURL(target.BaseURL, "/api/v1/auth/login/2fa")
		if err != nil {
			return sub2APIToken{}, err
		}
		verifyBody, _ := json.Marshal(map[string]string{"temp_token": tempToken, "totp_code": code})
		var verifyPayload any
		if err := session.doJSON(ctx, http.MethodPost, verifyURL, nil, verifyBody, &verifyPayload); err != nil {
			return sub2APIToken{}, err
		}
		data, err = sub2APIEnvelopeObject(verifyPayload, true)
		if err != nil {
			return sub2APIToken{}, err
		}
	}
	return adapter.storeToken(target, data)
}

func (adapter *sub2APIAdapter) refresh(ctx context.Context, session *requestSession, target TargetConfig, refreshToken string) (sub2APIToken, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return sub2APIToken{}, checkError(ErrorClassConfig, "刷新 Sub2API 令牌", "刷新令牌为空", 0, nil)
	}
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/auth/refresh")
	if err != nil {
		return sub2APIToken{}, err
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	var payload any
	if err := session.doJSON(ctx, http.MethodPost, endpoint, nil, body, &payload); err != nil {
		return sub2APIToken{}, err
	}
	data, err := sub2APIEnvelopeObject(payload, true)
	if err != nil {
		return sub2APIToken{}, err
	}
	if stringField(data, "refresh_token") == "" {
		data["refresh_token"] = refreshToken
	}
	return adapter.storeToken(target, data)
}

func (adapter *sub2APIAdapter) storeToken(target TargetConfig, data map[string]any) (sub2APIToken, error) {
	accessToken := stringField(data, "access_token", "accessToken", "token")
	if accessToken == "" {
		return sub2APIToken{}, checkError(ErrorClassResponse, "读取 Sub2API 令牌", "Sub2API 响应缺少访问令牌", 0, nil)
	}
	expiresIn := int64Field(data, "expires_in")
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	validFor := time.Duration(expiresIn) * time.Second
	// 提前五分钟视为过期，避免检测过程中令牌刚好失效。
	validUntil := time.Now().Add(validFor - 5*time.Minute)
	if !validUntil.After(time.Now()) {
		validUntil = time.Now().Add(validFor / 2)
	}
	token := sub2APIToken{accessToken: accessToken, refreshToken: stringField(data, "refresh_token"), validUntil: validUntil}
	adapter.mu.Lock()
	adapter.tokens[sub2APICacheKey(target)] = token
	adapter.mu.Unlock()
	return token, nil
}

func (adapter *sub2APIAdapter) readCurrentUser(ctx context.Context, session *requestSession, target TargetConfig, accessToken string) (map[string]any, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/auth/me")
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	setBearer(headers, accessToken)
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		return nil, err
	}
	return sub2APIEnvelopeObject(payload, true)
}

func sub2APIEnvelopeObject(payload any, auth bool) (map[string]any, error) {
	value, err := sub2APIEnvelopeValue(payload, auth)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 响应", "Sub2API data 格式无效", 0, nil)
	}
	return object, nil
}

func sub2APIEnvelopeValue(payload any, auth bool) (any, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 响应", "Sub2API 响应格式无效", 0, nil)
	}
	if code, exists := object["code"]; exists && parseStatusCode(code) != 0 {
		class := ErrorClassRemote
		message := "Sub2API 返回了失败状态"
		if auth {
			class = ErrorClassAuth
			message = "Sub2API 凭据无效或登录失败"
		}
		return nil, checkError(class, "解析 Sub2API 响应", message, 0, nil)
	}
	if data, exists := object["data"]; exists {
		return data, nil
	}
	return object, nil
}

func sub2APICacheKey(target TargetConfig) string {
	identity := strings.TrimSpace(target.ID)
	if identity == "" {
		identity = strings.TrimSpace(target.BaseURL) + "|" + strings.ToLower(strings.TrimSpace(target.Credential.Email))
	}
	credential := target.Credential
	return identity + "|" + credentialFingerprint(credential.Email, credential.Password, credential.AccessToken, credential.RefreshToken, credential.TOTPSecret)
}

func sub2APIUserEnabled(user map[string]any) bool {
	if active, exists := boolField(user, "active", "is_active", "enabled"); exists {
		return active
	}
	status := strings.ToLower(stringField(user, "status"))
	return status == "" || status == "active" || status == "enabled" || status == "normal" || status == "正常"
}

var _ Adapter = (*sub2APIAdapter)(nil)
var _ BrowserCredentialVerifier = (*sub2APIAdapter)(nil)
var _ GroupMultiplierReader = (*sub2APIAdapter)(nil)
