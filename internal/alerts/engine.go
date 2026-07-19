package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"poolwatch/internal/identity"
	"poolwatch/internal/monitor"
	"poolwatch/internal/store"
)

// Notification 是告警状态机产生的一次通知事件。
type Notification struct {
	AlertID   string `json:"alertId"`
	TargetID  string `json:"targetId"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
	Recovered bool   `json:"recovered"`
}

// Notifier 向 SSE 与浏览器推送转发新事件。
type Notifier interface {
	Notify(context.Context, Notification) error
}

// NotifierFunc 让普通函数可以作为通知器使用。
type NotifierFunc func(context.Context, Notification) error

// Notify 调用包装的通知函数。
func (function NotifierFunc) Notify(ctx context.Context, notification Notification) error {
	return function(ctx, notification)
}

// Engine 持久化快照并驱动告警的开启、确认和恢复。
type Engine struct {
	store    *store.Store
	notifier Notifier
	now      func() time.Time
}

// NewEngine 创建告警状态机。
func NewEngine(database *store.Store, notifier Notifier) *Engine {
	return &Engine{store: database, notifier: notifier, now: func() time.Time { return time.Now().UTC() }}
}

// HandleSuccess 保存成功快照、检查独立指标阈值并恢复故障告警。
func (e *Engine) HandleSuccess(ctx context.Context, target store.Target, snapshot monitor.Snapshot) error {
	observedAt := snapshot.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = e.now()
	}
	status := snapshot.Status
	if status == "" {
		status = monitor.TargetStatusHealthy
	}
	thresholdWarning := false
	for _, metric := range snapshot.Metrics {
		if metric.Threshold != nil && shouldCompareThreshold(target.Kind, metric.Key) && metric.Value.LessThanOrEqual(*metric.Threshold) {
			thresholdWarning = true
		}
	}
	if thresholdWarning && status == monitor.TargetStatusHealthy {
		status = monitor.TargetStatusWarning
	}
	metricsJSON, err := json.Marshal(snapshot.Metrics)
	if err != nil {
		return err
	}
	detailJSON, err := json.Marshal(map[string]string{"message": truncate(snapshot.Message, 500)})
	if err != nil {
		return err
	}
	storedSnapshot := &store.Snapshot{
		TargetID: target.ID, ObservedAt: observedAt, Status: string(status),
		MetricsJSON: string(metricsJSON), DetailJSON: string(detailJSON),
	}
	if err := e.store.InsertSnapshot(ctx, storedSnapshot); err != nil {
		return err
	}
	if target.Kind == string(monitor.TargetKindChatGPT2API) {
		if err := e.store.ReplaceChatAccounts(ctx, target.ID, sanitizedAccounts(target.ID, snapshot.Accounts, observedAt)); err != nil {
			return err
		}
	}

	for _, metric := range snapshot.Metrics {
		if metric.Threshold == nil || !shouldCompareThreshold(target.Kind, metric.Key) {
			continue
		}
		isLow := metric.Value.LessThanOrEqual(*metric.Threshold)
		if isLow {
			if err := e.openThreshold(ctx, target, metric, observedAt); err != nil {
				return err
			}
			continue
		}
		if err := e.recoverIncident(ctx, target, string(monitor.AlertTypeQuotaLow), string(metric.Key),
			metric.Label+"已恢复", fmt.Sprintf("当前%s为 %s %s，已经高于阈值 %s %s。", metric.Label,
				metric.Value.String(), metric.Unit, metric.Threshold.String(), metric.Unit), observedAt); err != nil {
			return err
		}
	}
	if status == monitor.TargetStatusDisabled {
		message := strings.TrimSpace(snapshot.Message)
		if message == "" {
			message = "渠道账号当前处于停用或不可用状态。"
		}
		if err := e.openIncident(ctx, target, string(monitor.AlertTypeCredentialInvalid), "", "渠道账号已停用", message, "", "", "", "critical", observedAt); err != nil {
			return err
		}
	} else if err := e.recoverIncident(ctx, target, string(monitor.AlertTypeCredentialInvalid), "", "渠道账号已恢复", "渠道账号和凭据检测已经恢复正常。", observedAt); err != nil {
		return err
	}
	if err := e.recoverIncident(ctx, target, string(monitor.AlertTypeConnectivity), "", "渠道连接已恢复", "渠道连接检测已经恢复正常。", observedAt); err != nil {
		return err
	}
	return e.store.UpdateTargetCheck(ctx, target.ID, string(status), 0, "", observedAt)
}

// HandleFailure 保存安全错误摘要，并按错误类型和连续次数开启告警。
func (e *Engine) HandleFailure(ctx context.Context, target store.Target, checkError error) error {
	now := e.now()
	errorMessage := sanitizeError(checkError)
	failureCount := target.FailureCount + 1
	metricsJSON := "[]"
	detailJSON, _ := json.Marshal(map[string]string{"message": errorMessage})
	if err := e.store.InsertSnapshot(ctx, &store.Snapshot{
		TargetID: target.ID, ObservedAt: now, Status: string(monitor.TargetStatusError),
		MetricsJSON: metricsJSON, DetailJSON: string(detailJSON),
	}); err != nil {
		return err
	}
	if err := e.store.UpdateTargetCheck(ctx, target.ID, string(monitor.TargetStatusError), failureCount, errorMessage, now); err != nil {
		return err
	}

	class := monitor.ErrorClassOf(checkError)
	alertType := string(monitor.AlertTypeConnectivity)
	title := "渠道连续检测失败"
	message := fmt.Sprintf("渠道已连续 %d 次检测失败：%s", failureCount, errorMessage)
	shouldAlert := failureCount >= 3
	if class == monitor.ErrorClassAuth {
		alertType = string(monitor.AlertTypeCredentialInvalid)
		title = "渠道凭据已经失效"
		message = "登录凭据被渠道拒绝，请更新访问令牌或登录信息。"
		shouldAlert = true
	} else if class == monitor.ErrorClassConfig {
		title = "渠道配置需要处理"
		message = errorMessage
		shouldAlert = true
	}
	if !shouldAlert {
		return nil
	}
	otherType := string(monitor.AlertTypeCredentialInvalid)
	if alertType == otherType {
		otherType = string(monitor.AlertTypeConnectivity)
	}
	if active, err := e.store.ActiveAlert(ctx, target.ID, otherType, ""); err == nil {
		_ = e.store.ResolveAlert(ctx, active.ID, now)
	}
	return e.openIncident(ctx, target, alertType, "", title, message, "", "", "", "critical", now)
}

// RetryPending 再次发送尚未成功交给通知器的事件。
func (e *Engine) RetryPending(ctx context.Context, limit int) error {
	items, err := e.store.ListUnnotifiedAlerts(ctx, limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		severity := "warning"
		if item.Type == string(monitor.AlertTypeCredentialInvalid) || item.Type == string(monitor.AlertTypeConnectivity) {
			severity = "critical"
		} else if item.Type == string(monitor.AlertTypeRecovered) {
			severity = "info"
		}
		e.notify(ctx, item.Alert, severity, item.Type == string(monitor.AlertTypeRecovered))
	}
	return nil
}

func (e *Engine) openThreshold(ctx context.Context, target store.Target, metric monitor.Metric, now time.Time) error {
	title := metric.Label + "不足"
	message := fmt.Sprintf("当前%s为 %s %s，已达到或低于阈值 %s %s。", metric.Label,
		metric.Value.String(), metric.Unit, metric.Threshold.String(), metric.Unit)
	return e.openIncident(ctx, target, string(monitor.AlertTypeQuotaLow), string(metric.Key), title, message,
		metric.Value.String(), metric.Threshold.String(), metric.Unit, "warning", now)
}

func (e *Engine) openIncident(ctx context.Context, target store.Target, alertType, metricKey, title, message,
	currentValue, threshold, unit, severity string, now time.Time) error {
	if _, err := e.store.ActiveAlert(ctx, target.ID, alertType, metricKey); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id, err := identity.NewID("alert")
	if err != nil {
		return err
	}
	alert := store.Alert{
		ID: id, TargetID: target.ID, Type: alertType, MetricKey: metricKey, State: "open",
		Title: title, Message: message, CurrentValue: currentValue, Threshold: threshold, Unit: unit, OpenedAt: now,
	}
	if err := e.store.CreateAlert(ctx, alert); err != nil {
		return err
	}
	e.notify(ctx, alert, severity, false)
	return nil
}

func (e *Engine) recoverIncident(ctx context.Context, target store.Target, alertType, metricKey, title, message string, now time.Time) error {
	active, err := e.store.ActiveAlert(ctx, target.ID, alertType, metricKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := e.store.ResolveAlert(ctx, active.ID, now); err != nil {
		return err
	}
	recoveryID, err := identity.NewID("alert")
	if err != nil {
		return err
	}
	recoveryTime := now
	recovery := store.Alert{
		ID: recoveryID, TargetID: target.ID, Type: string(monitor.AlertTypeRecovered), MetricKey: metricKey,
		State: "resolved", Title: title, Message: message, OpenedAt: now, RecoveredAt: &recoveryTime,
	}
	if err := e.store.CreateAlert(ctx, recovery); err != nil {
		return err
	}
	e.notify(ctx, recovery, "info", true)
	return nil
}

func (e *Engine) notify(ctx context.Context, alert store.Alert, severity string, recovered bool) {
	if e.notifier != nil {
		if err := e.notifier.Notify(ctx, Notification{
			AlertID: alert.ID, TargetID: alert.TargetID, Type: alert.Type, Title: alert.Title,
			Message: alert.Message, Severity: severity, Recovered: recovered,
		}); err != nil {
			return
		}
	}
	_ = e.store.MarkAlertNotified(ctx, alert.ID, e.now())
}

func shouldCompareThreshold(targetKind string, metricKey monitor.MetricKey) bool {
	return targetKind != string(monitor.TargetKindChatGPT2API) || metricKey == monitor.MetricImageQuota
}

func sanitizedAccounts(targetID string, accounts []monitor.AccountStatus, observedAt time.Time) []store.ChatAccount {
	result := make([]store.ChatAccount, 0, len(accounts))
	for index, account := range accounts {
		stableValue := strings.ToLower(strings.TrimSpace(account.Email)) + "|" + account.Type + fmt.Sprintf("|%d", index)
		externalID := identity.HashToken(stableValue)[:24]
		maskedEmail := maskEmail(account.Email)
		// 不接受非邮箱值，避免上游将令牌伪装在账号字段中写入数据库或接口响应。
		if maskedEmail == "" {
			continue
		}
		result = append(result, store.ChatAccount{
			TargetID: targetID, ExternalID: externalID, Email: maskedEmail, Type: sanitizeAccountText(account.Type, 80),
			Status: normalizeAccountStatus(account.Status), Quota: account.Quota.IntPart(), RestoreAt: sanitizeAccountText(account.RestoreAt, 100),
			Success: account.Success, Fail: account.Fail, ObservedAt: observedAt,
		})
	}
	return result
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || !isDisplayEmail(parts[0], parts[1]) {
		return ""
	}
	visible := []rune(parts[0])
	prefix := string(visible[:1])
	if len(visible) > 2 {
		prefix = string(visible[:2])
	}
	return prefix + "***@" + parts[1]
}

func isDisplayEmail(local, domain string) bool {
	if len(local) == 0 || len(local) > 64 || len(domain) == 0 || len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, character := range local {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", character)) {
			return false
		}
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

func sanitizeAccountText(value string, maximum int) string {
	value = truncate(value, maximum)
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(value))
	for _, marker := range []string{"token", "secret", "password", "cookie", "authorization", "apikey", "bearer"} {
		// 账号类型和恢复时间只是展示辅助信息，疑似凭据时宁可留空也不能持久化。
		if strings.Contains(normalized, marker) {
			return ""
		}
	}
	return value
}

func normalizeAccountStatus(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "正常", "active", "healthy", "ok":
		return string(monitor.TargetStatusHealthy)
	case "限流", "limited", "warning", "rate_limited":
		return string(monitor.TargetStatusWarning)
	case "禁用", "disabled":
		return string(monitor.TargetStatusDisabled)
	case "异常", "error", "abnormal", "invalid":
		return string(monitor.TargetStatusError)
	default:
		return string(monitor.TargetStatusUnknown)
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return "渠道检测失败"
	}
	return truncate(strings.ReplaceAll(err.Error(), "\n", " "), 500)
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
