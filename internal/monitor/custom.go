package monitor

import (
	"context"
	"net/http"
	"strings"
)

type customHTTPAdapter struct {
	http *secureHTTPClient
}

func newCustomHTTPAdapter(client *secureHTTPClient) *customHTTPAdapter {
	return &customHTTPAdapter{http: client}
}

func (adapter *customHTTPAdapter) Kind() TargetKind {
	return TargetKindCustom
}

func (adapter *customHTTPAdapter) Check(ctx context.Context, target TargetConfig) (Snapshot, error) {
	snapshot, _, err := adapter.Probe(ctx, target)
	return snapshot, err
}

// Probe 在字段映射前保留临时 JSON sample，供连接测试界面选择 Pointer。
func (adapter *customHTTPAdapter) Probe(ctx context.Context, target TargetConfig) (Snapshot, any, error) {
	target = ensureTargetKind(target, adapter.Kind())
	payload, err := adapter.fetch(ctx, target)
	if err != nil {
		return Snapshot{}, nil, err
	}
	snapshot, err := buildCustomSnapshot(target, payload)
	return snapshot, payload, err
}

func (adapter *customHTTPAdapter) fetch(ctx context.Context, target TargetConfig) (any, error) {
	method := strings.ToUpper(strings.TrimSpace(target.Custom.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, checkError(ErrorClassConfig, "配置自定义请求", "自定义渠道仅支持 GET 或 POST", 0, nil)
	}
	if method == http.MethodPost && !target.Custom.ConfirmPOST {
		return nil, checkError(ErrorClassConfig, "配置自定义请求", "使用 POST 前必须显式确认", 0, nil)
	}
	if len(target.Custom.Body) > int(defaultBodyLimit) {
		return nil, checkError(ErrorClassConfig, "配置自定义请求", "自定义请求正文超过 1 MB 限制", 0, nil)
	}
	headers, err := customAuthHeaders(target.Custom.AuthMode, target.Credential)
	if err != nil {
		return nil, err
	}
	var body []byte
	if method == http.MethodPost {
		body = target.Custom.Body
		if len(body) == 0 {
			body = []byte("{}")
		}
	}
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	var payload any
	if err := session.doJSON(ctx, method, target.BaseURL, headers, body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func buildCustomSnapshot(target TargetConfig, payload any) (Snapshot, error) {
	snapshot := newSnapshot(target)
	if len(target.Custom.Metrics) == 0 {
		return snapshot, checkError(ErrorClassConfig, "配置自定义字段", "至少需要配置一个指标字段", 0, nil)
	}
	for _, mapping := range target.Custom.Metrics {
		value, err := ResolveJSONPointer(payload, mapping.Pointer)
		if err != nil {
			return snapshot, checkError(ErrorClassResponse, "读取自定义指标", "自定义指标字段不存在", 0, err)
		}
		parsed, err := parseDecimal(value)
		if err != nil {
			return snapshot, checkError(ErrorClassResponse, "读取自定义指标", "自定义指标不是有效数字", 0, err)
		}
		key := mapping.Key
		if key == "" {
			key = MetricCustomValue
		}
		label := strings.TrimSpace(mapping.Label)
		if label == "" {
			label = "自定义指标"
		}
		snapshot.Metrics = append(snapshot.Metrics, metricWithThreshold(target, key, label, parsed, mapping.Unit))
	}
	if target.Custom.StatusPointer != "" {
		statusValue, err := ResolveJSONPointer(payload, target.Custom.StatusPointer)
		if err != nil {
			return snapshot, checkError(ErrorClassResponse, "读取自定义状态", "自定义状态字段不存在", 0, err)
		}
		if !customStatusHealthy(statusValue, target.Custom.HealthyValues) {
			snapshot.Status = TargetStatusDegraded
			snapshot.Message = "自定义状态字段表示渠道异常"
		}
	}
	return snapshot, nil
}

func customAuthHeaders(mode AuthMode, credential Credential) (http.Header, error) {
	headers := make(http.Header)
	switch mode {
	case "", AuthModeNone:
		return headers, nil
	case AuthModeBearer:
		token := credential.BearerToken
		if strings.TrimSpace(token) == "" {
			token = credential.AccessToken
		}
		if strings.TrimSpace(token) == "" {
			return nil, checkError(ErrorClassConfig, "配置自定义认证", "Bearer 令牌为空", 0, nil)
		}
		setBearer(headers, token)
	case AuthModeBasic:
		username := credential.BasicUsername
		password := credential.BasicPassword
		if username == "" {
			username = credential.Username
			password = credential.Password
		}
		if username == "" {
			return nil, checkError(ErrorClassConfig, "配置自定义认证", "Basic 用户名为空", 0, nil)
		}
		setBasic(headers, username, password)
	case AuthModeHeader:
		if len(credential.Headers) == 0 {
			return nil, checkError(ErrorClassConfig, "配置自定义认证", "自定义请求头为空", 0, nil)
		}
		for name, value := range credential.Headers {
			if !validCustomHeaderName(name) {
				return nil, checkError(ErrorClassConfig, "配置自定义认证", "自定义请求头名称无效或不安全", 0, nil)
			}
			if strings.ContainsAny(value, "\r\n") {
				return nil, checkError(ErrorClassConfig, "配置自定义认证", "自定义请求头值无效", 0, nil)
			}
			headers.Set(name, value)
		}
	default:
		return nil, checkError(ErrorClassConfig, "配置自定义认证", "不支持的自定义认证方式", 0, nil)
	}
	return headers, nil
}

func validCustomHeaderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	blocked := map[string]struct{}{
		"host": {}, "content-length": {}, "transfer-encoding": {}, "connection": {}, "cookie": {},
	}
	if _, exists := blocked[strings.ToLower(name)]; exists {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))) {
			return false
		}
	}
	return true
}

func customStatusHealthy(value any, healthyValues []string) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	text := strings.ToLower(strings.TrimSpace(stringValue(value)))
	if len(healthyValues) == 0 {
		healthyValues = []string{"ok", "healthy", "active", "enabled", "normal", "true", "1", "正常"}
	}
	for _, candidate := range healthyValues {
		if text == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	if number, err := parseDecimal(value); err == nil {
		return !number.IsZero()
	}
	return false
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

var _ Adapter = (*customHTTPAdapter)(nil)
var _ Prober = (*customHTTPAdapter)(nil)
