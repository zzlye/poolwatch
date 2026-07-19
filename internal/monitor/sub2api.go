package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type sub2APIAdapter struct {
	http   *secureHTTPClient
	mu     sync.Mutex
	tokens map[string]sub2APIToken
}

type sub2APIToken struct {
	accessToken  string
	refreshToken string
	validUntil   time.Time
}

func newSub2APIAdapter(client *secureHTTPClient) *sub2APIAdapter {
	return &sub2APIAdapter{http: client, tokens: make(map[string]sub2APIToken)}
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
		if token.refreshToken != "" {
			token, err = adapter.refresh(ctx, session, target, token.refreshToken)
		} else if target.Credential.Email != "" && target.Credential.Password != "" {
			token, err = adapter.login(ctx, session, target)
		}
		if err == nil {
			me, err = adapter.readCurrentUser(ctx, session, target, token.accessToken)
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
			return adapter.refresh(ctx, session, target, cached.refreshToken)
		}
	}
	credential := target.Credential
	if strings.TrimSpace(credential.AccessToken) != "" {
		return sub2APIToken{accessToken: strings.TrimSpace(credential.AccessToken), refreshToken: strings.TrimSpace(credential.RefreshToken)}, nil
	}
	if strings.TrimSpace(credential.RefreshToken) != "" {
		return adapter.refresh(ctx, session, target, credential.RefreshToken)
	}
	return adapter.login(ctx, session, target)
}

func (adapter *sub2APIAdapter) login(ctx context.Context, session *requestSession, target TargetConfig) (sub2APIToken, error) {
	credential := target.Credential
	if strings.TrimSpace(credential.Email) == "" || credential.Password == "" {
		return sub2APIToken{}, checkError(ErrorClassConfig, "配置 Sub2API 认证", "请填写访问令牌、刷新令牌或邮箱密码", 0, nil)
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
		if typed, ok := data.(map[string]any); ok {
			return typed, nil
		}
		return nil, checkError(ErrorClassResponse, "解析 Sub2API 响应", "Sub2API data 格式无效", 0, nil)
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
