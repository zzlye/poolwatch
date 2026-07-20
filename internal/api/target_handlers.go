package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/identity"
	"poolwatch/internal/monitor"
	"poolwatch/internal/scheduler"
	"poolwatch/internal/store"
)

func (s *Server) handleTargets(response http.ResponseWriter, request *http.Request) {
	targets, err := s.dependencies.Store.ListTargets(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道列表失败")
		return
	}
	result := make([]targetResponse, 0, len(targets))
	for _, target := range targets {
		mapped, err := s.mapTarget(request.Context(), target)
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "读取渠道状态失败")
			return
		}
		result = append(result, mapped)
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleTarget(response http.ResponseWriter, request *http.Request) {
	target, err := s.dependencies.Store.TargetByID(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道失败")
		return
	}
	mapped, err := s.mapTarget(request.Context(), target)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道状态失败")
		return
	}
	writeJSON(response, http.StatusOK, mapped)
}

func (s *Server) handleCreateTarget(response http.ResponseWriter, request *http.Request) {
	var draft targetDraft
	if err := decodeJSON(response, request, &draft); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	target, _, err := s.buildTarget(request.Context(), draft, nil, false)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.dependencies.Store.CreateTarget(request.Context(), target); err != nil {
		writeAPIError(response, http.StatusInternalServerError, "保存渠道失败")
		return
	}
	s.targetAuth.consume(draft.BrowserAuthAttemptID, adminFromContext(request.Context()).ID)
	_ = s.dependencies.Store.AddAuditEvent(request.Context(), "target.created", target.ID, "创建渠道", time.Now().UTC())
	s.dependencies.Events.Publish("target.updated", map[string]string{"targetId": target.ID})
	mapped, err := s.mapTarget(request.Context(), target)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取新渠道失败")
		return
	}
	writeJSON(response, http.StatusCreated, mapped)
}

func (s *Server) handleUpdateTarget(response http.ResponseWriter, request *http.Request) {
	var draft targetDraft
	if err := decodeJSON(response, request, &draft); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	id := request.PathValue("id")
	unlock, err := s.dependencies.Scheduler.LockTarget(request.Context(), id)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "等待当前渠道检测结束超时")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "锁定渠道失败")
		return
	}
	defer unlock()
	existing, err := s.dependencies.Store.TargetByID(request.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道失败")
		return
	}
	target, _, err := s.buildTarget(request.Context(), draft, &existing, false)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	monitoringIdentityChanged := existing.Kind != target.Kind || existing.BaseURL != target.BaseURL
	monitoringConfigChanged := existing.ConfigJSON != target.ConfigJSON
	monitoringMode := store.TargetMonitoringKeep
	removedMetricKeys := make([]string, 0)
	if monitoringIdentityChanged {
		monitoringMode = store.TargetMonitoringResetHistory
	} else if monitoringConfigChanged {
		monitoringMode = store.TargetMonitoringRefresh
		removedMetricKeys, err = removedThresholdKeys(existing.ConfigJSON, target.ConfigJSON)
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "读取原渠道监控配置失败")
			return
		}
	}
	if err := s.dependencies.Store.UpdateTargetAndMonitoring(request.Context(), target, monitoringMode, removedMetricKeys); err != nil {
		writeAPIError(response, http.StatusInternalServerError, "更新渠道失败")
		return
	}
	s.targetAuth.consume(draft.BrowserAuthAttemptID, adminFromContext(request.Context()).ID)
	_ = s.dependencies.Store.AddAuditEvent(request.Context(), "target.updated", target.ID, "更新渠道", time.Now().UTC())
	s.dependencies.Events.Publish("target.updated", map[string]string{"targetId": target.ID})
	stored, _ := s.dependencies.Store.TargetByID(request.Context(), target.ID)
	mapped, err := s.mapTarget(request.Context(), stored)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道状态失败")
		return
	}
	writeJSON(response, http.StatusOK, mapped)
}

func removedThresholdKeys(previousJSON, currentJSON string) ([]string, error) {
	var previous storedTargetConfig
	if err := json.Unmarshal([]byte(previousJSON), &previous); err != nil {
		return nil, err
	}
	var current storedTargetConfig
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		return nil, err
	}
	removed := make([]string, 0)
	for key := range previous.Thresholds {
		if _, exists := current.Thresholds[key]; !exists {
			removed = append(removed, string(key))
		}
	}
	return removed, nil
}

func (s *Server) handleDeleteTarget(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	unlock, err := s.dependencies.Scheduler.LockTarget(request.Context(), id)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "等待当前渠道检测结束超时")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "锁定渠道失败")
		return
	}
	defer unlock()
	if err := s.dependencies.Store.DeleteTarget(request.Context(), id); err != nil {
		if store.IsNotFound(err) {
			writeAPIError(response, http.StatusNotFound, "渠道不存在")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "删除渠道失败")
		return
	}
	_ = s.dependencies.Store.AddAuditEvent(request.Context(), "target.deleted", id, "删除渠道", time.Now().UTC())
	s.dependencies.Events.Publish("target.updated", map[string]string{"targetId": id})
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestTarget(response http.ResponseWriter, request *http.Request) {
	var draft targetDraft
	if err := decodeJSON(response, request, &draft); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	_, runtimeConfig, err := s.buildTarget(request.Context(), draft, nil, true)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	runtimeConfig.ID = ""
	snapshot, sample, checkErr := s.dependencies.Scheduler.TestConfig(request.Context(), runtimeConfig)
	sample = sanitizeSample(sample)
	metrics := mapMonitorMetrics(snapshot.Metrics, string(snapshot.Status))
	if checkErr != nil {
		if sample != nil {
			writeJSON(response, http.StatusOK, map[string]any{
				"ok": false, "detectedKind": draft.Kind, "message": checkErr.Error(), "sample": sample, "metrics": metrics,
			})
			return
		}
		writeAPIError(response, http.StatusBadGateway, checkErr.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"ok": true, "detectedKind": draft.Kind, "message": "连接成功，已读取可用指标。", "sample": sample, "metrics": metrics,
	})
}

func (s *Server) handleDetectTarget(response http.ResponseWriter, request *http.Request) {
	var body struct {
		BaseURL string `json:"baseUrl"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	baseURL, err := validateHTTPURL(body.BaseURL, true)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "渠道地址无效")
		return
	}
	kind, err := s.dependencies.Scheduler.DetectTarget(request.Context(), baseURL)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{
		"kind": string(kind), "message": "已识别为 " + targetKindLabel(kind),
	})
}

func (s *Server) handleCheckTarget(response http.ResponseWriter, request *http.Request) {
	err := s.dependencies.Scheduler.CheckTarget(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "检测超时")
		return
	}
	if errors.Is(err, scheduler.ErrAlreadyRunning) {
		writeAPIError(response, http.StatusConflict, "该渠道正在检测中")
		return
	}
	// 渠道自身错误已经写入快照和告警，手动请求仍视为完成。
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckAll(response http.ResponseWriter, request *http.Request) {
	err := s.dependencies.Scheduler.CheckAll(request.Context())
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "批量检测超时")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHistory(response http.ResponseWriter, request *http.Request) {
	target, err := s.dependencies.Store.TargetByID(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道失败")
		return
	}
	mappedTarget, err := s.mapTarget(request.Context(), target)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取渠道状态失败")
		return
	}
	metricKey := strings.TrimSpace(request.URL.Query().Get("metric"))
	if metricKey == "" {
		for _, metric := range mappedTarget.Metrics {
			if metric.Threshold != "" {
				metricKey = metric.Key
				break
			}
		}
		if metricKey == "" && len(mappedTarget.Metrics) > 0 {
			metricKey = mappedTarget.Metrics[0].Key
		}
	}
	retention := s.historyRetention(request.Context())
	snapshots, err := s.dependencies.Store.ListSnapshots(request.Context(), target.ID, time.Now().UTC().AddDate(0, 0, -retention), 10000)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取检测历史失败")
		return
	}
	points := make([]snapshotResponse, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var metrics []monitor.Metric
		if json.Unmarshal([]byte(snapshot.MetricsJSON), &metrics) != nil {
			continue
		}
		for _, metric := range metrics {
			if string(metric.Key) != metricKey {
				continue
			}
			points = append(points, snapshotResponse{
				ID: fmt.Sprintf("%d:%s", snapshot.ID, metric.Key), TargetID: target.ID, MetricKey: string(metric.Key),
				Value: metric.Value.String(), Unit: metric.Unit, MeasuredAt: snapshot.ObservedAt.Format(time.RFC3339Nano),
			})
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"target": mappedTarget, "snapshots": points})
}

func (s *Server) buildTarget(ctx context.Context, draft targetDraft, existing *store.Target, testing bool) (store.Target, monitor.TargetConfig, error) {
	draft.Name = strings.TrimSpace(draft.Name)
	if len(draft.Name) < 1 || len(draft.Name) > 100 {
		return store.Target{}, monitor.TargetConfig{}, errors.New("渠道名称需要 1 至 100 个字符")
	}
	kind := monitor.TargetKind(strings.TrimSpace(draft.Kind))
	if kind != monitor.TargetKindNewAPI && kind != monitor.TargetKindSub2API && kind != monitor.TargetKindChatGPT2API &&
		kind != monitor.TargetKindCLIProxyAPI && kind != monitor.TargetKindCustom {
		return store.Target{}, monitor.TargetConfig{}, errors.New("渠道类型不受支持")
	}
	baseURL, err := validateHTTPURL(draft.BaseURL, true)
	if err != nil {
		return store.Target{}, monitor.TargetConfig{}, fmt.Errorf("渠道地址无效: %w", err)
	}
	topupURL, err := validateHTTPURL(draft.TopupURL, false)
	if err != nil {
		return store.Target{}, monitor.TargetConfig{}, fmt.Errorf("充值地址无效: %w", err)
	}
	if topupURL == "" {
		topupURL = defaultTopupURL(baseURL, kind)
	}
	if draft.CheckIntervalMinutes < 1 || draft.CheckIntervalMinutes > 1440 {
		return store.Target{}, monitor.TargetConfig{}, errors.New("检测间隔需要在 1 至 1440 分钟之间")
	}

	config := storedTargetConfig{
		Thresholds:           make(map[monitor.MetricKey]decimal.Decimal),
		ThresholdComparisons: make(map[monitor.MetricKey]monitor.ThresholdComparison),
	}
	seenKeys := make(map[monitor.MetricKey]bool)
	for index, threshold := range draft.Thresholds {
		value, err := decimal.NewFromString(strings.TrimSpace(threshold.Value))
		if err != nil || value.IsNegative() {
			return store.Target{}, monitor.TargetConfig{}, errors.New("告警阈值必须是大于或等于零的十进制数")
		}
		key := monitor.MetricKey(strings.TrimSpace(threshold.Key))
		if kind == monitor.TargetKindCustom {
			if index == 0 {
				key = monitor.MetricCustomValue
			} else {
				key = monitor.MetricKey(fmt.Sprintf("custom_value_%d", index+1))
			}
		}
		if key == "" || seenKeys[key] {
			return store.Target{}, monitor.TargetConfig{}, errors.New("指标键重复或为空")
		}
		seenKeys[key] = true
		config.Thresholds[key] = value
		comparison := monitor.ThresholdComparison(strings.ToLower(strings.TrimSpace(threshold.Comparison)))
		if comparison == "" {
			comparison = monitor.ThresholdComparisonLTE
		}
		if comparison != monitor.ThresholdComparisonLTE && comparison != monitor.ThresholdComparisonGTE {
			return store.Target{}, monitor.TargetConfig{}, errors.New("告警比较方式只支持 lte 或 gte")
		}
		config.ThresholdComparisons[key] = comparison
		meta := thresholdDraft{
			Key: string(key), Label: strings.TrimSpace(threshold.Label), Value: value.String(),
			Unit: strings.TrimSpace(threshold.Unit), Comparison: string(comparison),
		}
		if meta.Label == "" {
			meta.Label = metricLabel(key)
		}
		config.ThresholdMeta = append(config.ThresholdMeta, meta)
	}
	if len(config.Thresholds) == 0 {
		return store.Target{}, monitor.TargetConfig{}, errors.New("至少需要配置一个告警阈值")
	}
	config.NewAPI.IncludeSubscription = seenKeys[monitor.MetricSubscriptionBalance]

	credential, resolvedCredentialMode, err := s.mergeCredential(ctx, draft, existing, kind, baseURL)
	if err != nil {
		return store.Target{}, monitor.TargetConfig{}, err
	}
	config.CredentialMode = resolvedCredentialMode
	if testing {
		credential.TOTPCode = strings.TrimSpace(draft.TOTPCode)
	} else {
		credential.TOTPCode = ""
	}
	if !testing && draft.TOTPCode != "" && credential.TOTPSecret == "" && credential.AccessToken == "" && credential.RefreshToken == "" {
		return store.Target{}, monitor.TargetConfig{}, errors.New("自动检测请填写二步验证密钥或访问令牌，一次性验证码只用于连接测试")
	}
	if kind == monitor.TargetKindChatGPT2API {
		config.ChatGPT2API.IncludeAccounts = strings.TrimSpace(credential.AdminKey) != ""
	}
	if kind == monitor.TargetKindCLIProxyAPI && strings.TrimSpace(credential.AdminKey) == "" {
		return store.Target{}, monitor.TargetConfig{}, errors.New("CLIProxyAPI 需要管理密钥")
	}
	if kind == monitor.TargetKindCustom {
		customConfig, err := buildCustomConfig(draft, config.ThresholdMeta)
		if err != nil {
			return store.Target{}, monitor.TargetConfig{}, err
		}
		config.Custom = customConfig
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return store.Target{}, monitor.TargetConfig{}, err
	}
	credentialsJSON, err := json.Marshal(credential)
	if err != nil {
		return store.Target{}, monitor.TargetConfig{}, err
	}
	credentialsEncrypted := ""
	if !credentialIsEmpty(credential) {
		credentialsEncrypted, err = s.dependencies.Vault.Encrypt(credentialsJSON)
		if err != nil {
			return store.Target{}, monitor.TargetConfig{}, err
		}
	}
	now := time.Now().UTC()
	target := store.Target{
		Name: draft.Name, Kind: string(kind), BaseURL: baseURL, Enabled: draft.Enabled,
		PollIntervalSeconds: draft.CheckIntervalMinutes * 60, RechargeURL: topupURL,
		ConfigJSON: string(configJSON), CredentialsEnc: credentialsEncrypted, HasCredentials: credentialsEncrypted != "",
		Status: string(monitor.TargetStatusUnknown), CreatedAt: now, UpdatedAt: now,
	}
	if existing != nil {
		target.ID = existing.ID
		target.Status = existing.Status
		target.FailureCount = existing.FailureCount
		target.LastError = existing.LastError
		target.LastCheckedAt = existing.LastCheckedAt
		target.CreatedAt = existing.CreatedAt
	} else {
		target.ID, err = identity.NewID("target")
		if err != nil {
			return store.Target{}, monitor.TargetConfig{}, err
		}
	}
	runtimeConfig := monitor.TargetConfig{
		ID: target.ID, Name: target.Name, Kind: kind, BaseURL: target.BaseURL,
		AllowPrivateNetwork: s.dependencies.AllowPrivateTargets, Thresholds: config.Thresholds,
		ThresholdComparisons: config.ThresholdComparisons, Credential: credential,
		NewAPI: config.NewAPI, ChatGPT2API: config.ChatGPT2API, Custom: config.Custom,
	}
	return target, runtimeConfig, nil
}

func (s *Server) mergeCredential(ctx context.Context, draft targetDraft, existing *store.Target, kind monitor.TargetKind, baseURL string) (monitor.Credential, credentialMode, error) {
	credential, existingMode, err := s.existingCredential(existing, kind)
	if err != nil {
		return monitor.Credential{}, "", err
	}
	if kind == monitor.TargetKindNewAPI || kind == monitor.TargetKindSub2API {
		requestedMode := resolveCredentialMode(draft, kind, existingMode)
		if strings.TrimSpace(draft.BrowserAuthAttemptID) != "" {
			if !isBrowserCredentialMode(kind, requestedMode) {
				return monitor.Credential{}, "", errors.New("网页登录尝试只能用于浏览器认证方式")
			}
			attemptBaseURL, err := normalizeTargetBaseURL(baseURL)
			if err != nil {
				return monitor.Credential{}, "", errors.New("渠道地址无效")
			}
			imported, err := s.targetAuth.credential(
				draft.BrowserAuthAttemptID,
				adminFromContext(ctx).ID,
				kind,
				attemptBaseURL,
			)
			if err != nil {
				return monitor.Credential{}, "", err
			}
			return imported, requestedMode, nil
		}
		if requestedMode != existingMode {
			credential = monitor.Credential{}
		}
		switch kind {
		case monitor.TargetKindNewAPI:
			credential, err = mergeNewAPICredential(credential, draft, requestedMode)
		case monitor.TargetKindSub2API:
			credential, err = mergeSub2APICredential(credential, draft, requestedMode)
		}
		if err != nil {
			return monitor.Credential{}, "", err
		}
		return credential, requestedMode, nil
	}

	mergeString(&credential.Username, draft.Username)
	mergeString(&credential.Email, draft.Email)
	mergeString(&credential.Password, draft.Password)
	mergeString(&credential.TOTPSecret, draft.TOTPSecret)
	mergeString(&credential.AccessToken, draft.AccessToken)
	mergeString(&credential.RefreshToken, draft.RefreshToken)
	mergeString(&credential.UserID, draft.UserID)
	mergeString(&credential.AdminKey, draft.AdminKey)
	credential.TOTPCode = ""
	if kind != monitor.TargetKindCustom {
		return credential, "", nil
	}
	switch strings.TrimSpace(draft.AuthType) {
	case "", "none":
		return monitor.Credential{}, "", nil
	case "bearer":
		credential.Headers = nil
		credential.BasicUsername = ""
		credential.BasicPassword = ""
		mergeString(&credential.BearerToken, draft.AccessToken)
	case "basic":
		credential.Headers = nil
		credential.BearerToken = ""
		mergeString(&credential.BasicUsername, draft.Username)
		mergeString(&credential.BasicPassword, draft.Password)
	case "headers":
		credential.BearerToken = ""
		credential.BasicUsername = ""
		credential.BasicPassword = ""
		var headers map[string]string
		if strings.TrimSpace(draft.CustomHeaders) != "" && strings.TrimSpace(draft.CustomHeaders) != "{}" {
			if err := json.Unmarshal([]byte(draft.CustomHeaders), &headers); err != nil {
				return monitor.Credential{}, "", errors.New("自定义请求头必须是字符串键值 JSON 对象")
			}
			credential.Headers = headers
		}
		if len(credential.Headers) == 0 {
			return monitor.Credential{}, "", errors.New("自定义请求头认证至少需要一个请求头")
		}
	default:
		return monitor.Credential{}, "", errors.New("自定义认证方式不受支持")
	}
	return credential, "", nil
}

func (s *Server) existingCredential(existing *store.Target, kind monitor.TargetKind) (monitor.Credential, credentialMode, error) {
	if existing == nil || existing.Kind != string(kind) {
		return monitor.Credential{}, "", nil
	}
	var mode credentialMode
	if existing.ConfigJSON != "" {
		var config storedTargetConfig
		if err := json.Unmarshal([]byte(existing.ConfigJSON), &config); err != nil {
			return monitor.Credential{}, "", errors.New("已有渠道配置格式无效")
		}
		mode = config.CredentialMode
	}
	var credential monitor.Credential
	if existing.CredentialsEnc != "" {
		decoded, err := s.dependencies.Vault.Decrypt(existing.CredentialsEnc)
		if err != nil {
			return monitor.Credential{}, "", errors.New("读取已有渠道凭据失败")
		}
		if json.Unmarshal(decoded, &credential) != nil {
			return monitor.Credential{}, "", errors.New("已有渠道凭据格式无效")
		}
	}
	if mode == "" {
		mode = inferCredentialMode(kind, credential)
	}
	return credential, mode, nil
}

func resolveCredentialMode(draft targetDraft, kind monitor.TargetKind, existing credentialMode) credentialMode {
	if mode := credentialMode(strings.TrimSpace(string(draft.CredentialMode))); mode != "" {
		return mode
	}
	if strings.TrimSpace(draft.BrowserAuthAttemptID) != "" {
		if kind == monitor.TargetKindNewAPI {
			return credentialModeNewAPIBrowserSession
		}
		return credentialModeSub2APIBrowserOAuth
	}
	if kind == monitor.TargetKindNewAPI {
		if strings.TrimSpace(draft.Cookie) != "" {
			return credentialModeNewAPIBrowserSession
		}
		if strings.TrimSpace(draft.AccessToken) != "" {
			return credentialModeNewAPIAccessToken
		}
		if strings.TrimSpace(draft.Password) != "" {
			return credentialModeNewAPIPassword
		}
	} else {
		if strings.TrimSpace(draft.Password) != "" {
			return credentialModeSub2APIPassword
		}
		if strings.TrimSpace(draft.AccessToken) != "" || strings.TrimSpace(draft.RefreshToken) != "" {
			return credentialModeSub2APIAccessToken
		}
	}
	if existing != "" {
		return existing
	}
	if kind == monitor.TargetKindSub2API {
		return credentialModeSub2APIPassword
	}
	return credentialModeNewAPIPassword
}

func inferCredentialMode(kind monitor.TargetKind, credential monitor.Credential) credentialMode {
	if kind == monitor.TargetKindNewAPI {
		if strings.TrimSpace(credential.Cookie) != "" {
			return credentialModeNewAPIBrowserSession
		}
		if strings.TrimSpace(credential.AccessToken) != "" {
			return credentialModeNewAPIAccessToken
		}
		if credential.Password != "" {
			return credentialModeNewAPIPassword
		}
		return ""
	}
	if strings.TrimSpace(credential.AccessToken) != "" || strings.TrimSpace(credential.RefreshToken) != "" {
		return credentialModeSub2APIAccessToken
	}
	if credential.Password != "" {
		return credentialModeSub2APIPassword
	}
	return ""
}

func isBrowserCredentialMode(kind monitor.TargetKind, mode credentialMode) bool {
	return kind == monitor.TargetKindNewAPI && mode == credentialModeNewAPIBrowserSession ||
		kind == monitor.TargetKindSub2API && mode == credentialModeSub2APIBrowserOAuth
}

func mergeNewAPICredential(credential monitor.Credential, draft targetDraft, mode credentialMode) (monitor.Credential, error) {
	switch mode {
	case credentialModeNewAPIPassword:
		credential.AccessToken = ""
		credential.RefreshToken = ""
		credential.UserID = ""
		credential.Cookie = ""
		mergeString(&credential.Username, draft.Username)
		mergeString(&credential.Email, draft.Email)
		mergeString(&credential.Password, draft.Password)
		mergeString(&credential.TOTPSecret, draft.TOTPSecret)
		if (strings.TrimSpace(credential.Username) == "" && strings.TrimSpace(credential.Email) == "") || credential.Password == "" {
			return monitor.Credential{}, errors.New("New API 密码登录需要账号和密码")
		}
	case credentialModeNewAPIAccessToken:
		credential.Username = ""
		credential.Email = ""
		credential.Password = ""
		credential.TOTPSecret = ""
		credential.RefreshToken = ""
		credential.Cookie = ""
		mergeString(&credential.AccessToken, draft.AccessToken)
		mergeString(&credential.UserID, draft.UserID)
		if err := validateImportedToken(credential.AccessToken, "New API 访问令牌"); err != nil {
			return monitor.Credential{}, err
		}
		if strings.TrimSpace(credential.UserID) == "" {
			return monitor.Credential{}, errors.New("New API 访问令牌登录需要用户 ID")
		}
	case credentialModeNewAPIBrowserSession:
		credential.Username = ""
		credential.Email = ""
		credential.Password = ""
		credential.TOTPSecret = ""
		credential.AccessToken = ""
		credential.RefreshToken = ""
		if strings.TrimSpace(draft.Cookie) != "" {
			credential.Cookie = strings.TrimSpace(draft.Cookie)
		}
		mergeString(&credential.UserID, draft.UserID)
		if err := validateImportedCookie(credential.Cookie); err != nil {
			return monitor.Credential{}, err
		}
		if strings.TrimSpace(credential.UserID) == "" {
			return monitor.Credential{}, errors.New("New API 网页登录需要用户 ID")
		}
	default:
		return monitor.Credential{}, errors.New("New API 认证方式不受支持")
	}
	credential.TOTPCode = ""
	return credential, nil
}

func mergeSub2APICredential(credential monitor.Credential, draft targetDraft, mode credentialMode) (monitor.Credential, error) {
	switch mode {
	case credentialModeSub2APIPassword:
		credential.Username = ""
		credential.AccessToken = ""
		credential.RefreshToken = ""
		credential.UserID = ""
		credential.Cookie = ""
		mergeString(&credential.Email, draft.Email)
		mergeString(&credential.Password, draft.Password)
		mergeString(&credential.TOTPSecret, draft.TOTPSecret)
		if strings.TrimSpace(credential.Email) == "" || credential.Password == "" {
			return monitor.Credential{}, errors.New("Sub2API 密码登录需要邮箱和密码")
		}
	case credentialModeSub2APIAccessToken, credentialModeSub2APIBrowserOAuth:
		credential.Username = ""
		credential.Email = ""
		credential.Password = ""
		credential.TOTPSecret = ""
		credential.UserID = ""
		credential.Cookie = ""
		mergeString(&credential.AccessToken, draft.AccessToken)
		mergeString(&credential.RefreshToken, draft.RefreshToken)
		if credential.AccessToken == "" && credential.RefreshToken == "" {
			return monitor.Credential{}, errors.New("Sub2API 令牌登录需要访问令牌或刷新令牌")
		}
		if credential.AccessToken != "" {
			if err := validateImportedToken(credential.AccessToken, "Sub2API 访问令牌"); err != nil {
				return monitor.Credential{}, err
			}
		}
		if credential.RefreshToken != "" {
			if err := validateImportedToken(credential.RefreshToken, "Sub2API 刷新令牌"); err != nil {
				return monitor.Credential{}, err
			}
		}
	default:
		return monitor.Credential{}, errors.New("Sub2API 认证方式不受支持")
	}
	credential.TOTPCode = ""
	return credential, nil
}

func validateImportedCookie(cookie string) error {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return errors.New("New API 网页登录需要会话 Cookie")
	}
	if len(cookie) > maxImportedCookieBytes {
		return errors.New("网页登录会话超过 16 KB 限制")
	}
	if strings.ContainsAny(cookie, "\r\n") {
		return errors.New("网页登录会话格式无效")
	}
	return nil
}

func validateImportedToken(token, label string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("%s不能为空", label)
	}
	if len(token) > maxImportedTokenBytes {
		return fmt.Errorf("%s超过 64 KB 限制", label)
	}
	if strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("%s格式无效", label)
	}
	return nil
}

func buildCustomConfig(draft targetDraft, thresholds []thresholdDraft) (monitor.CustomHTTPConfig, error) {
	method := strings.ToUpper(strings.TrimSpace(draft.RequestMethod))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return monitor.CustomHTTPConfig{}, errors.New("自定义请求只支持 GET 或 POST")
	}
	if method == http.MethodPost && !draft.ConfirmPOST {
		return monitor.CustomHTTPConfig{}, errors.New("使用 POST 检测前必须显式确认请求不会修改远端数据")
	}
	pointer := strings.TrimSpace(draft.JSONPointer)
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return monitor.CustomHTTPConfig{}, errors.New("指标字段必须是以 / 开头的 RFC 6901 JSON Pointer")
	}
	var authMode monitor.AuthMode
	switch strings.TrimSpace(draft.AuthType) {
	case "", "none":
		authMode = monitor.AuthModeNone
	case "bearer":
		authMode = monitor.AuthModeBearer
	case "basic":
		authMode = monitor.AuthModeBasic
	case "headers":
		authMode = monitor.AuthModeHeader
	default:
		return monitor.CustomHTTPConfig{}, errors.New("自定义认证方式不受支持")
	}
	custom := monitor.CustomHTTPConfig{
		Method: method, ConfirmPOST: draft.ConfirmPOST, AuthMode: authMode,
		StatusPointer: strings.TrimSpace(draft.StatusPointer), HealthyValues: []string{"ok", "healthy", "active", "enabled", "normal", "true", "1", "正常"},
	}
	for _, threshold := range thresholds {
		custom.Metrics = append(custom.Metrics, monitor.CustomMetricMapping{
			Key: monitor.MetricKey(threshold.Key), Label: threshold.Label, Pointer: pointer, Unit: threshold.Unit,
		})
	}
	return custom, nil
}

func (s *Server) mapTarget(ctx context.Context, target store.Target) (targetResponse, error) {
	var config storedTargetConfig
	if err := json.Unmarshal([]byte(target.ConfigJSON), &config); err != nil {
		return targetResponse{}, err
	}
	status := target.Status
	if !target.Enabled {
		status = string(monitor.TargetStatusDisabled)
	}
	result := targetResponse{
		ID: target.ID, Name: target.Name, Kind: target.Kind, BaseURL: target.BaseURL, TopupURL: target.RechargeURL,
		Status: status, StatusText: targetStatusText(status), Enabled: target.Enabled,
		CheckIntervalMinutes: target.PollIntervalSeconds / 60, LastError: target.LastError,
		AuthConfigured: target.HasCredentials || (target.Kind == string(monitor.TargetKindCustom) && config.Custom.AuthMode == monitor.AuthModeNone),
		Metrics:        make([]metricResponse, 0),
	}
	if target.Kind == string(monitor.TargetKindNewAPI) || target.Kind == string(monitor.TargetKindSub2API) {
		result.CredentialMode = config.CredentialMode
		if result.CredentialMode == "" {
			_, inferredMode, err := s.existingCredential(&target, monitor.TargetKind(target.Kind))
			if err != nil {
				return targetResponse{}, err
			}
			result.CredentialMode = inferredMode
		}
	}
	if !target.LastCheckedAt.IsZero() {
		checked := target.LastCheckedAt
		next := checked.Add(time.Duration(target.PollIntervalSeconds) * time.Second)
		result.LastCheckedAt = &checked
		if target.Enabled {
			result.NextCheckAt = &next
		}
	}
	if target.Kind == string(monitor.TargetKindCustom) {
		result.AuthType = string(config.Custom.AuthMode)
		if config.Custom.AuthMode == monitor.AuthModeHeader {
			result.AuthType = "headers"
		}
		result.RequestMethod = config.Custom.Method
		result.ConfirmPOST = config.Custom.ConfirmPOST
		result.StatusPointer = config.Custom.StatusPointer
		if len(config.Custom.Metrics) > 0 {
			result.JSONPointer = config.Custom.Metrics[0].Pointer
		}
		result.CustomHeadersSet = config.Custom.AuthMode == monitor.AuthModeHeader && target.HasCredentials
	}

	snapshot, err := s.dependencies.Store.LatestSnapshot(ctx, target.ID)
	if err == nil {
		var metrics []monitor.Metric
		if err := json.Unmarshal([]byte(snapshot.MetricsJSON), &metrics); err != nil {
			return targetResponse{}, err
		}
		visibleMetrics := make([]monitor.Metric, 0, len(metrics))
		for index := range metrics {
			// 快照中的阈值属于历史配置，响应必须以渠道当前配置为准。
			metrics[index].Threshold = nil
			metrics[index].Comparison = ""
			if threshold, exists := config.Thresholds[metrics[index].Key]; exists {
				copyValue := threshold
				metrics[index].Threshold = &copyValue
				metrics[index].Comparison = monitor.NormalizeThresholdComparison(config.ThresholdComparisons[metrics[index].Key])
			}
			if target.Kind == string(monitor.TargetKindNewAPI) && metrics[index].Key == monitor.MetricSubscriptionBalance && !config.NewAPI.IncludeSubscription {
				continue
			}
			visibleMetrics = append(visibleMetrics, metrics[index])
		}
		result.Metrics = mapMonitorMetrics(visibleMetrics, status)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return targetResponse{}, err
	}
	visibleMetricKeys := make(map[string]bool, len(result.Metrics))
	for _, metric := range result.Metrics {
		visibleMetricKeys[metric.Key] = true
	}
	for _, meta := range config.ThresholdMeta {
		if visibleMetricKeys[meta.Key] {
			continue
		}
		// 尚未读取到的已配置指标仍需返回，确保编辑页面不会意外关闭监控项。
		result.Metrics = append(result.Metrics, metricResponse{
			Key: meta.Key, Label: meta.Label, Value: "0", Unit: meta.Unit, Threshold: meta.Value,
			Comparison: string(monitor.NormalizeThresholdComparison(monitor.ThresholdComparison(meta.Comparison))),
			Status:     string(monitor.TargetStatusUnknown),
		})
	}
	if target.Kind == string(monitor.TargetKindChatGPT2API) || target.Kind == string(monitor.TargetKindCLIProxyAPI) {
		accounts, err := s.dependencies.Store.ListChatAccounts(ctx, target.ID)
		if err != nil {
			return targetResponse{}, err
		}
		for _, account := range accounts {
			result.Accounts = append(result.Accounts, mapAccountResponse(target.Kind, account))
		}
	}
	return result, nil
}

func mapAccountResponse(targetKind string, account store.ChatAccount) accountResponse {
	result := accountResponse{
		ID: account.ExternalID, DisplayName: account.DisplayName, Provider: account.Provider,
		Email: account.Email, Type: account.Type, Status: account.Status, StatusText: account.StatusText,
		RecoveryAt: account.RestoreAt, Success: account.Success, Fail: account.Fail,
	}
	// 图片额度只属于 chatgpt2api，CLIProxyAPI 账号响应不携带没有实际含义的零值字段。
	if targetKind == string(monitor.TargetKindChatGPT2API) {
		result.ImageQuota = strconv.FormatInt(account.Quota, 10)
	}
	return result
}

func mapMonitorMetrics(metrics []monitor.Metric, targetStatus string) []metricResponse {
	result := make([]metricResponse, 0, len(metrics))
	for _, metric := range metrics {
		status := string(monitor.TargetStatusHealthy)
		if targetStatus == string(monitor.TargetStatusError) {
			status = targetStatus
		} else if monitor.ThresholdBreached(metric) {
			status = string(monitor.TargetStatusWarning)
		}
		item := metricResponse{Key: string(metric.Key), Label: metric.Label, Value: metric.Value.String(), Unit: metric.Unit, Status: status}
		if metric.Threshold != nil {
			item.Threshold = metric.Threshold.String()
			item.Comparison = string(monitor.NormalizeThresholdComparison(metric.Comparison))
		}
		result = append(result, item)
	}
	return result
}

func validateHTTPURL(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", errors.New("地址不能为空")
		}
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("只允许不含账号信息的 HTTP 或 HTTPS 地址")
	}
	return parsed.String(), nil
}

func defaultTopupURL(baseURL string, kind monitor.TargetKind) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	switch kind {
	case monitor.TargetKindNewAPI:
		parsed.Path, parsed.RawQuery, parsed.Fragment = "/console/topup", "", ""
	case monitor.TargetKindSub2API:
		parsed.Path, parsed.RawQuery, parsed.Fragment = "/purchase", "", ""
	default:
		return ""
	}
	return parsed.String()
}

func credentialIsEmpty(credential monitor.Credential) bool {
	return credential.Username == "" && credential.Email == "" && credential.Password == "" && credential.TOTPSecret == "" &&
		credential.AccessToken == "" && credential.RefreshToken == "" && credential.UserID == "" && credential.Cookie == "" &&
		credential.AdminKey == "" && credential.BearerToken == "" && credential.BasicUsername == "" && credential.BasicPassword == "" && len(credential.Headers) == 0
}

func mergeString(destination *string, value string) {
	if strings.TrimSpace(value) != "" {
		*destination = strings.TrimSpace(value)
	}
}

func metricLabel(key monitor.MetricKey) string {
	labels := map[monitor.MetricKey]string{
		monitor.MetricWalletBalance: "钱包余额", monitor.MetricSubscriptionBalance: "订阅额度",
		monitor.MetricImageQuota: "图片额度", monitor.MetricAccountTotal: "账号总数", monitor.MetricHealthyAccounts: "正常账号",
		monitor.MetricLimitedAccounts: "限流账号", monitor.MetricErrorAccounts: "异常账号",
		monitor.MetricDisabledAccounts: "禁用账号", monitor.MetricCustomValue: "自定义指标",
	}
	if label := labels[key]; label != "" {
		return label
	}
	return string(key)
}

func targetKindLabel(kind monitor.TargetKind) string {
	switch kind {
	case monitor.TargetKindNewAPI:
		return "New API"
	case monitor.TargetKindSub2API:
		return "Sub2API"
	case monitor.TargetKindChatGPT2API:
		return "chatgpt2api"
	case monitor.TargetKindCLIProxyAPI:
		return "CLIProxyAPI"
	default:
		return "自定义 HTTP"
	}
}

func targetStatusText(status string) string {
	switch status {
	case string(monitor.TargetStatusHealthy):
		return "运行正常"
	case string(monitor.TargetStatusWarning):
		return "需要关注"
	case string(monitor.TargetStatusError):
		return "检测失败"
	case string(monitor.TargetStatusDisabled):
		return "已停用"
	default:
		return "等待首次检测"
	}
}

func sanitizeSample(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveSampleKey(key) {
				result[key] = "[已隐藏]"
				continue
			}
			result[key] = sanitizeSample(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = sanitizeSample(child)
		}
		return result
	case string:
		if len(typed) > 2000 {
			return typed[:2000] + "…"
		}
		return typed
	default:
		return value
	}
}

func sensitiveSampleKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	// 统一移除常见分隔符后再判断，覆盖 access_token、accessToken 与 access-token 等写法。
	normalized = strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(normalized)
	for _, marker := range []string{
		"token", "password", "passwd", "secret", "cookie", "authorization", "apikey", "privatekey",
	} {
		// 复合键可能是 clientSecret、authorizationHeader 等，包含敏感片段时一律隐藏其值。
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func (s *Server) historyRetention(ctx context.Context) int {
	value, _ := s.dependencies.Store.GetSetting(ctx, "history_retention_days")
	days, err := strconv.Atoi(value)
	if err != nil || days < 1 || days > 365 {
		return 7
	}
	return days
}
