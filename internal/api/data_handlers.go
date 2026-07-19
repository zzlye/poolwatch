package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"poolwatch/internal/monitor"
	"poolwatch/internal/push"
	"poolwatch/internal/store"
)

func (s *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	targets, err := s.dependencies.Store.ListTargets(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取仪表盘失败")
		return
	}
	mappedTargets := make([]targetResponse, 0, len(targets))
	healthy, warning := 0, 0
	for _, target := range targets {
		mapped, err := s.mapTarget(request.Context(), target)
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "读取渠道状态失败")
			return
		}
		mappedTargets = append(mappedTargets, mapped)
		switch mapped.Status {
		case string(monitor.TargetStatusHealthy):
			healthy++
		case string(monitor.TargetStatusWarning), string(monitor.TargetStatusError):
			warning++
		}
	}
	alerts, err := s.dependencies.Store.ListAlerts(request.Context(), "all", 4)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取告警失败")
		return
	}
	mappedAlerts := make([]alertResponse, 0, len(alerts))
	for _, alert := range alerts {
		mappedAlerts = append(mappedAlerts, mapAlert(alert))
	}
	openAlerts, _ := s.dependencies.Store.CountActiveAlerts(request.Context())
	pushDevices, _ := s.dependencies.Store.CountPushSubscriptions(request.Context())
	writeJSON(response, http.StatusOK, map[string]any{
		"summary": map[string]int{
			"totalTargets": len(targets), "healthyTargets": healthy, "warningTargets": warning,
			"openAlerts": openAlerts, "pushDevices": pushDevices,
		},
		"targets": mappedTargets, "alerts": mappedAlerts, "lastUpdatedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) handleAlerts(response http.ResponseWriter, request *http.Request) {
	state := strings.TrimSpace(request.URL.Query().Get("status"))
	alerts, err := s.dependencies.Store.ListAlerts(request.Context(), state, 500)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取告警列表失败")
		return
	}
	result := make([]alertResponse, 0, len(alerts))
	for _, alert := range alerts {
		result = append(result, mapAlert(alert))
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleAcknowledgeAlert(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if body.Status != "acknowledged" {
		writeAPIError(response, http.StatusBadRequest, "告警只能标记为已确认")
		return
	}
	alert, err := s.dependencies.Store.AcknowledgeAlert(request.Context(), request.PathValue("id"))
	if err != nil {
		if store.IsNotFound(err) {
			writeAPIError(response, http.StatusNotFound, "告警不存在或状态不可更新")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "更新告警失败")
		return
	}
	target, err := s.dependencies.Store.TargetByID(request.Context(), alert.TargetID)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取告警渠道失败")
		return
	}
	s.dependencies.Events.Publish("alert", map[string]string{"alertId": alert.ID})
	writeJSON(response, http.StatusOK, mapAlert(store.AlertWithTarget{Alert: alert, TargetName: target.Name}))
}

func (s *Server) handleSettings(response http.ResponseWriter, request *http.Request) {
	settings, err := s.readSettings(request)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取系统设置失败")
		return
	}
	writeJSON(response, http.StatusOK, settings)
}

func (s *Server) handleUpdateSettings(response http.ResponseWriter, request *http.Request) {
	var body struct {
		ProductName                 *string `json:"productName"`
		HistoryRetentionDays        *int    `json:"historyRetentionDays"`
		DefaultCheckIntervalMinutes *int    `json:"defaultCheckIntervalMinutes"`
		AllowPrivateTargets         *bool   `json:"allowPrivateTargets"`
		TOTPEnabled                 *bool   `json:"totpEnabled"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if body.ProductName != nil {
		name := strings.TrimSpace(*body.ProductName)
		if len(name) < 1 || len(name) > 40 {
			writeAPIError(response, http.StatusBadRequest, "产品名称需要 1 至 40 个字符")
			return
		}
		if err := s.dependencies.Store.SetSetting(request.Context(), "product_name", name); err != nil {
			writeAPIError(response, http.StatusInternalServerError, "保存产品名称失败")
			return
		}
	}
	if body.HistoryRetentionDays != nil {
		if *body.HistoryRetentionDays < 1 || *body.HistoryRetentionDays > 365 {
			writeAPIError(response, http.StatusBadRequest, "历史保留期限需要在 1 至 365 天之间")
			return
		}
		if err := s.dependencies.Store.SetSetting(request.Context(), "history_retention_days", strconv.Itoa(*body.HistoryRetentionDays)); err != nil {
			writeAPIError(response, http.StatusInternalServerError, "保存历史保留期限失败")
			return
		}
	}
	if body.DefaultCheckIntervalMinutes != nil {
		if *body.DefaultCheckIntervalMinutes < 1 || *body.DefaultCheckIntervalMinutes > 1440 {
			writeAPIError(response, http.StatusBadRequest, "默认检测间隔需要在 1 至 1440 分钟之间")
			return
		}
		seconds := *body.DefaultCheckIntervalMinutes * 60
		if err := s.dependencies.Store.SetSetting(request.Context(), "default_poll_seconds", strconv.Itoa(seconds)); err != nil {
			writeAPIError(response, http.StatusInternalServerError, "保存默认检测间隔失败")
			return
		}
	}
	s.dependencies.Events.Publish("settings.updated", map[string]bool{"updated": true})
	settings, err := s.readSettings(request)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取系统设置失败")
		return
	}
	writeJSON(response, http.StatusOK, settings)
}

func (s *Server) handlePushInfo(response http.ResponseWriter, request *http.Request) {
	devices, err := s.dependencies.Push.Devices(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取推送设备失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"supported": true, "vapidPublicKey": s.dependencies.Push.PublicKey(), "devices": devices,
	})
}

func (s *Server) handlePushSubscribe(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Endpoint       string `json:"endpoint"`
		ExpirationTime any    `json:"expirationTime"`
		Name           string `json:"name"`
		Keys           struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.dependencies.Push.Subscribe(request.Context(), push.SubscriptionInput{
		Endpoint: body.Endpoint, P256DH: body.Keys.P256DH, Auth: body.Keys.Auth,
		Name: body.Name, UserAgent: request.UserAgent(),
	}); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushDelete(response http.ResponseWriter, request *http.Request) {
	if err := s.dependencies.Push.DeleteDevice(request.Context(), request.PathValue("id")); err != nil {
		if store.IsNotFound(err) {
			writeAPIError(response, http.StatusNotFound, "推送设备不存在")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "删除推送设备失败")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushTest(response http.ResponseWriter, request *http.Request) {
	if err := s.dependencies.Push.SendTest(request.Context()); err != nil {
		writeAPIError(response, http.StatusBadGateway, "测试通知发送失败")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) readSettings(request *http.Request) (settingsResponse, error) {
	productName, err := s.dependencies.Store.GetSetting(request.Context(), "product_name")
	if err != nil {
		return settingsResponse{}, err
	}
	if productName == "" {
		productName = "号池监控"
	}
	retentionText, err := s.dependencies.Store.GetSetting(request.Context(), "history_retention_days")
	if err != nil {
		return settingsResponse{}, err
	}
	retention, _ := strconv.Atoi(retentionText)
	if retention < 1 || retention > 365 {
		retention = 7
	}
	pollText, err := s.dependencies.Store.GetSetting(request.Context(), "default_poll_seconds")
	if err != nil {
		return settingsResponse{}, err
	}
	pollSeconds, _ := strconv.Atoi(pollText)
	if pollSeconds < 60 {
		pollSeconds = 300
	}
	admin, err := s.dependencies.Store.AdminByID(request.Context(), adminFromContext(request.Context()).ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return settingsResponse{}, err
	}
	return settingsResponse{
		ProductName: productName, HistoryRetentionDays: retention, DefaultCheckIntervalMinutes: pollSeconds / 60,
		AllowPrivateTargets: s.dependencies.AllowPrivateTargets, TOTPEnabled: admin.TOTPEnabled,
	}, nil
}

func mapAlert(item store.AlertWithTarget) alertResponse {
	severity := "warning"
	if item.Type == string(monitor.AlertTypeCredentialInvalid) || item.Type == string(monitor.AlertTypeConnectivity) {
		severity = "critical"
	} else if item.Type == string(monitor.AlertTypeRecovered) {
		severity = "info"
	}
	return alertResponse{
		ID: item.ID, TargetID: item.TargetID, TargetName: item.TargetName, Type: item.Type,
		Title: item.Title, Message: item.Message, Severity: severity, Status: item.State,
		CreatedAt: item.OpenedAt, ResolvedAt: item.RecoveredAt,
	}
}
