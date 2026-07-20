package monitor

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Detect 使用只读公开端点自动识别内置渠道类型。
func (registry *Registry) Detect(ctx context.Context, baseURL string, allowPrivate bool) (TargetKind, error) {
	if registry == nil || registry.http == nil {
		return "", checkError(ErrorClassConfig, "自动识别渠道", "监控适配器注册器未初始化", 0, nil)
	}
	session := registry.http.newSession(allowPrivate)
	rootURL, err := joinTargetURL(baseURL, "/")
	if err != nil {
		return "", err
	}
	var rootPayload any
	rootErr := session.doJSON(ctx, http.MethodGet, rootURL, nil, nil, &rootPayload)
	if rootErr == nil && isCLIProxyAPIRoot(rootPayload) {
		return TargetKindCLIProxyAPI, nil
	}
	if shouldAbortDetection(rootErr) {
		return "", rootErr
	}

	healthURL, err := joinTargetURL(baseURL, "/health?format=json")
	if err != nil {
		return "", err
	}
	var healthPayload any
	healthErr := session.doJSON(ctx, http.MethodGet, healthURL, nil, nil, &healthPayload)
	if healthErr == nil && isChatGPT2APIHealth(healthPayload) {
		return TargetKindChatGPT2API, nil
	}
	if shouldAbortDetection(healthErr) {
		return "", healthErr
	}
	subHealth := healthErr == nil && isSub2APIHealth(healthPayload)

	statusURL, err := joinTargetURL(baseURL, "/api/status")
	if err != nil {
		return "", err
	}
	var statusPayload any
	statusErr := session.doJSON(ctx, http.MethodGet, statusURL, nil, nil, &statusPayload)
	if statusErr == nil && isNewAPIStatus(statusPayload) {
		return TargetKindNewAPI, nil
	}
	if shouldAbortDetection(statusErr) {
		return "", statusErr
	}

	// 单独的 {"status":"ok"} 太常见，必须再验证一个 Sub2API 专属端点。
	if subHealth {
		setupURL, err := joinTargetURL(baseURL, "/setup/status")
		if err != nil {
			return "", err
		}
		var setupPayload any
		setupErr := session.doJSON(ctx, http.MethodGet, setupURL, nil, nil, &setupPayload)
		if setupErr == nil && isSub2APISetupStatus(setupPayload) {
			return TargetKindSub2API, nil
		}
		if shouldAbortDetection(setupErr) {
			return "", setupErr
		}

		meURL, err := joinTargetURL(baseURL, "/api/v1/auth/me")
		if err != nil {
			return "", err
		}
		var mePayload any
		meErr := session.doJSON(ctx, http.MethodGet, meURL, nil, nil, &mePayload)
		if IsAuthFailure(meErr) || (meErr == nil && isSub2APIAuthEnvelope(mePayload)) {
			return TargetKindSub2API, nil
		}
		if shouldAbortDetection(meErr) {
			return "", meErr
		}
	}

	for _, candidate := range []error{rootErr, healthErr, statusErr} {
		if candidate != nil && ErrorClassOf(candidate) == ErrorClassServer {
			return "", candidate
		}
	}
	return "", checkError(ErrorClassResponse, "自动识别渠道", "无法识别渠道类型，请选择自定义 HTTP", 0, nil)
}

func isCLIProxyAPIRoot(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok || stringField(root, "message") != "CLI Proxy API Server" {
		return false
	}
	endpoints, ok := root["endpoints"].([]any)
	if !ok {
		return false
	}
	foundChat, foundModels := false, false
	for _, endpoint := range endpoints {
		value, ok := endpoint.(string)
		if !ok {
			continue
		}
		switch strings.TrimSpace(value) {
		case "POST /v1/chat/completions":
			foundChat = true
		case "GET /v1/models":
			foundModels = true
		}
	}
	return foundChat && foundModels
}

func isChatGPT2APIHealth(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	accounts, ok := root["accounts"].(map[string]any)
	if !ok {
		return false
	}
	_, activeErr := decimalField(accounts, "active")
	_, quotaErr := decimalField(accounts, "total_quota")
	return activeErr == nil && quotaErr == nil
}

func isNewAPIStatus(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	success, ok := root["success"].(bool)
	if !ok || !success {
		return false
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return false
	}
	quotaPerUnit, err := decimalField(data, "quota_per_unit")
	if err != nil || !quotaPerUnit.IsPositive() {
		return false
	}
	return strings.TrimSpace(stringField(data, "quota_display_type")) != ""
}

func isSub2APIHealth(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok || isChatGPT2APIHealth(payload) {
		return false
	}
	return strings.EqualFold(stringField(root, "status"), "ok")
}

func isSub2APISetupStatus(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok || parseStatusCode(root["code"]) != 0 {
		return false
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return false
	}
	_, exists := boolField(data, "needs_setup")
	return exists && strings.TrimSpace(stringField(data, "step")) != ""
}

func isSub2APIAuthEnvelope(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	code := parseStatusCode(root["code"])
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

func shouldAbortDetection(err error) bool {
	if err == nil {
		return false
	}
	class := ErrorClassOf(err)
	if class == ErrorClassConfig || class == ErrorClassNetwork {
		return true
	}
	var checkErr *CheckError
	return errors.As(err, &checkErr) && checkErr.Message == "渠道响应超过 1 MB 限制"
}
