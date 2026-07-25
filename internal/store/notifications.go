package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// NotificationDelivered 判断指定告警通道对具体接收方是否已经完成过投递。
func (s *Store) NotificationDelivered(ctx context.Context, alertID, channel, destinationID string) (bool, error) {
	var deliveredAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT delivered_at FROM alert_notification_deliveries
		WHERE alert_id = ? AND channel = ? AND destination_id = ?`, alertID, channel, destinationID).Scan(&deliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return deliveredAt.Valid && strings.TrimSpace(deliveredAt.String) != "", nil
}

// RecordNotificationAttempt 记录一次接收方投递结果，成功时间一旦写入便保持不变。
func (s *Store) RecordNotificationAttempt(ctx context.Context, alertID, channel, destinationID string, delivered bool, errorText string, attemptedAt time.Time) error {
	if len(errorText) > 500 {
		errorText = errorText[:500]
	}
	var deliveredAt any
	if delivered {
		deliveredAt = formatTime(attemptedAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_notification_deliveries(
		alert_id, channel, destination_id, delivered_at, attempt_count, last_attempt_at, last_error
	) VALUES (?, ?, ?, ?, 1, ?, ?)
	ON CONFLICT(alert_id, channel, destination_id) DO UPDATE SET
		delivered_at = CASE
			WHEN alert_notification_deliveries.delivered_at IS NOT NULL THEN alert_notification_deliveries.delivered_at
			ELSE excluded.delivered_at
		END,
		attempt_count = alert_notification_deliveries.attempt_count + 1,
		last_attempt_at = excluded.last_attempt_at,
		last_error = CASE WHEN excluded.delivered_at IS NOT NULL THEN '' ELSE excluded.last_error END`,
		alertID, channel, destinationID, deliveredAt, formatTime(attemptedAt), strings.TrimSpace(errorText))
	return err
}

// NotificationAttemptCount 返回测试和诊断所需的接收方尝试次数。
func (s *Store) NotificationAttemptCount(ctx context.Context, alertID, channel, destinationID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT attempt_count FROM alert_notification_deliveries
		WHERE alert_id = ? AND channel = ? AND destination_id = ?`, alertID, channel, destinationID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
