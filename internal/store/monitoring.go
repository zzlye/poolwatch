package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InsertSnapshot 保存一次检测快照并回填数据库标识。
func (s *Store) InsertSnapshot(ctx context.Context, snapshot *Snapshot) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO snapshots(target_id, observed_at, status, metrics_json, detail_json)
		VALUES (?, ?, ?, ?, ?)`, snapshot.TargetID, formatTime(snapshot.ObservedAt), snapshot.Status, snapshot.MetricsJSON, snapshot.DetailJSON)
	if err != nil {
		return fmt.Errorf("保存检测快照失败: %w", err)
	}
	snapshot.ID, err = result.LastInsertId()
	return err
}

// LatestSnapshot 返回渠道最近一次检测快照。
func (s *Store) LatestSnapshot(ctx context.Context, targetID string) (Snapshot, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, target_id, observed_at, status, metrics_json, detail_json
		FROM snapshots WHERE target_id = ? ORDER BY observed_at DESC LIMIT 1`, targetID)
	return scanSnapshot(row)
}

// ListSnapshots 返回时间范围内的历史快照。
func (s *Store) ListSnapshots(ctx context.Context, targetID string, since time.Time, limit int) ([]Snapshot, error) {
	if limit < 1 || limit > 10000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, target_id, observed_at, status, metrics_json, detail_json
		FROM snapshots WHERE target_id = ? AND observed_at >= ? ORDER BY observed_at DESC LIMIT ?`,
		targetID, formatTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("读取检测历史失败: %w", err)
	}
	defer rows.Close()
	snapshots := make([]Snapshot, 0)
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(snapshots)-1; left < right; left, right = left+1, right-1 {
		snapshots[left], snapshots[right] = snapshots[right], snapshots[left]
	}
	return snapshots, nil
}

// CleanupHistory 清除保留期限之前的快照、已恢复告警和审计记录。
func (s *Store) CleanupHistory(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := formatTime(before)
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM snapshots WHERE observed_at < ?`, []any{cutoff}},
		{`DELETE FROM alerts WHERE state = 'resolved' AND COALESCE(recovered_at, opened_at) < ?`, []any{cutoff}},
		{`DELETE FROM audit_events WHERE created_at < ?`, []any{cutoff}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("清理历史数据失败: %w", err)
		}
	}
	return tx.Commit()
}

// ReplaceChatAccounts 原子替换一个号池的只读脱敏账号列表。
func (s *Store) ReplaceChatAccounts(ctx context.Context, targetID string, accounts []ChatAccount) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_accounts WHERE target_id = ?`, targetID); err != nil {
		return err
	}
	for _, account := range accounts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_accounts(
			target_id, external_id, email, type, status, quota, restore_at, success, fail, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, targetID, account.ExternalID, account.Email, account.Type,
			account.Status, account.Quota, account.RestoreAt, account.Success, account.Fail, formatTime(account.ObservedAt)); err != nil {
			return fmt.Errorf("保存脱敏号池账号失败: %w", err)
		}
	}
	return tx.Commit()
}

// ListChatAccounts 返回一个渠道的脱敏账号列表。
func (s *Store) ListChatAccounts(ctx context.Context, targetID string) ([]ChatAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_id, external_id, email, type, status, quota, restore_at, success, fail, observed_at
		FROM chat_accounts WHERE target_id = ? ORDER BY status, email`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]ChatAccount, 0)
	for rows.Next() {
		var account ChatAccount
		var observedAt string
		if err := rows.Scan(&account.TargetID, &account.ExternalID, &account.Email, &account.Type, &account.Status,
			&account.Quota, &account.RestoreAt, &account.Success, &account.Fail, &observedAt); err != nil {
			return nil, err
		}
		account.ObservedAt = parseTime(observedAt)
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// ActiveAlert 查找尚未恢复的同类事件。
func (s *Store) ActiveAlert(ctx context.Context, targetID, alertType, metricKey string) (Alert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, target_id, type, metric_key, state, title, message,
		current_value, threshold_value, unit, opened_at, recovered_at, last_notified_at
		FROM alerts WHERE target_id = ? AND type = ? AND metric_key = ? AND state IN ('open', 'acknowledged') LIMIT 1`,
		targetID, alertType, metricKey)
	return scanAlert(row)
}

// CreateAlert 创建一个新的活跃告警。
func (s *Store) CreateAlert(ctx context.Context, alert Alert) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alerts(id, target_id, type, metric_key, state, title, message,
		current_value, threshold_value, unit, opened_at, recovered_at, last_notified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, alert.ID, alert.TargetID, alert.Type, alert.MetricKey,
		alert.State, alert.Title, alert.Message, alert.CurrentValue, alert.Threshold, alert.Unit,
		formatTime(alert.OpenedAt), nullableTimePointer(alert.RecoveredAt), nullableTimePointer(alert.LastNotifiedAt))
	if err != nil {
		return fmt.Errorf("创建告警失败: %w", err)
	}
	return nil
}

// ResolveAlert 将一个告警标记为已恢复。
func (s *Store) ResolveAlert(ctx context.Context, id string, recoveredAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE alerts SET state = 'resolved', recovered_at = ? WHERE id = ? AND state IN ('open', 'acknowledged')`,
		formatTime(recoveredAt), id)
	if err != nil {
		return err
	}
	return requireAffected(result, "告警不存在或已经恢复")
}

// AcknowledgeAlert 标记告警已读，但仍保留其活跃事件身份。
func (s *Store) AcknowledgeAlert(ctx context.Context, id string) (Alert, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE alerts SET state = 'acknowledged' WHERE id = ? AND state = 'open'`, id)
	if err != nil {
		return Alert{}, err
	}
	if err := requireAffected(result, "告警不存在或状态不可更新"); err != nil {
		return Alert{}, err
	}
	return s.AlertByID(ctx, id)
}

// MarkAlertNotified 记录事件最近一次发送通知的时间。
func (s *Store) MarkAlertNotified(ctx context.Context, id string, notifiedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET last_notified_at = ? WHERE id = ?`, formatTime(notifiedAt), id)
	return err
}

// AlertByID 读取一个告警。
func (s *Store) AlertByID(ctx context.Context, id string) (Alert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, target_id, type, metric_key, state, title, message,
		current_value, threshold_value, unit, opened_at, recovered_at, last_notified_at FROM alerts WHERE id = ?`, id)
	return scanAlert(row)
}

// ListAlerts 按状态返回告警及渠道名称。
func (s *Store) ListAlerts(ctx context.Context, state string, limit int) ([]AlertWithTarget, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	query := `SELECT a.id, a.target_id, a.type, a.metric_key, a.state, a.title, a.message,
		a.current_value, a.threshold_value, a.unit, a.opened_at, a.recovered_at, a.last_notified_at, t.name
		FROM alerts a JOIN targets t ON t.id = a.target_id`
	args := make([]any, 0, 2)
	if state != "" && state != "all" {
		if state == "active" {
			query += ` WHERE a.state IN ('open', 'acknowledged')`
		} else {
			query += ` WHERE a.state = ?`
			args = append(args, state)
		}
	}
	query += ` ORDER BY a.opened_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := make([]AlertWithTarget, 0)
	for rows.Next() {
		alert, err := scanAlertWithTarget(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

// CountActiveAlerts 返回尚未恢复的告警数量。
func (s *Store) CountActiveAlerts(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE state IN ('open', 'acknowledged')`).Scan(&count)
	return count, err
}

// ListUnnotifiedAlerts 返回尚未成功交给通知器的事件。
func (s *Store) ListUnnotifiedAlerts(ctx context.Context, limit int) ([]AlertWithTarget, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.target_id, a.type, a.metric_key, a.state, a.title, a.message,
		a.current_value, a.threshold_value, a.unit, a.opened_at, a.recovered_at, a.last_notified_at, t.name
		FROM alerts a JOIN targets t ON t.id = a.target_id
		WHERE a.last_notified_at IS NULL ORDER BY a.opened_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AlertWithTarget, 0)
	for rows.Next() {
		item, err := scanAlertWithTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AddAuditEvent 保存不含敏感值的操作记录。
func (s *Store) AddAuditEvent(ctx context.Context, eventType, targetID, detail string, now time.Time) error {
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(event_type, target_id, detail, created_at) VALUES (?, ?, ?, ?)`,
		eventType, targetID, detail, formatTime(now))
	return err
}

func scanSnapshot(row scanner) (Snapshot, error) {
	var snapshot Snapshot
	var observedAt string
	if err := row.Scan(&snapshot.ID, &snapshot.TargetID, &observedAt, &snapshot.Status, &snapshot.MetricsJSON, &snapshot.DetailJSON); err != nil {
		return Snapshot{}, err
	}
	snapshot.ObservedAt = parseTime(observedAt)
	return snapshot, nil
}

func scanAlert(row scanner) (Alert, error) {
	var alert Alert
	var openedAt string
	var recoveredAt, notifiedAt sql.NullString
	if err := row.Scan(&alert.ID, &alert.TargetID, &alert.Type, &alert.MetricKey, &alert.State, &alert.Title,
		&alert.Message, &alert.CurrentValue, &alert.Threshold, &alert.Unit, &openedAt, &recoveredAt, &notifiedAt); err != nil {
		return Alert{}, err
	}
	alert.OpenedAt = parseTime(openedAt)
	if recoveredAt.Valid {
		value := parseTime(recoveredAt.String)
		alert.RecoveredAt = &value
	}
	if notifiedAt.Valid {
		value := parseTime(notifiedAt.String)
		alert.LastNotifiedAt = &value
	}
	return alert, nil
}

func scanAlertWithTarget(row scanner) (AlertWithTarget, error) {
	var item AlertWithTarget
	var openedAt string
	var recoveredAt, notifiedAt sql.NullString
	if err := row.Scan(&item.ID, &item.TargetID, &item.Type, &item.MetricKey, &item.State, &item.Title,
		&item.Message, &item.CurrentValue, &item.Threshold, &item.Unit, &openedAt, &recoveredAt,
		&notifiedAt, &item.TargetName); err != nil {
		return AlertWithTarget{}, err
	}
	item.OpenedAt = parseTime(openedAt)
	if recoveredAt.Valid {
		value := parseTime(recoveredAt.String)
		item.RecoveredAt = &value
	}
	if notifiedAt.Valid {
		value := parseTime(notifiedAt.String)
		item.LastNotifiedAt = &value
	}
	return item, nil
}

func nullableTimePointer(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatTime(*value)
}

// IsNotFound 统一判断数据库查询未找到。
func IsNotFound(err error) bool {
	return err == sql.ErrNoRows || strings.Contains(errString(err), "不存在")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
