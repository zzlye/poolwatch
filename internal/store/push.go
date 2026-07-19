package store

import (
	"context"
	"fmt"
	"time"
)

// UpsertPushSubscription 按浏览器端点新增或更新推送设备。
func (s *Store) UpsertPushSubscription(ctx context.Context, subscription PushSubscription) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions(
		id, endpoint, p256dh, auth, device_name, user_agent, created_at, last_used_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(endpoint) DO UPDATE SET
		p256dh = excluded.p256dh, auth = excluded.auth, device_name = excluded.device_name,
		user_agent = excluded.user_agent, last_used_at = excluded.last_used_at`,
		subscription.ID, subscription.Endpoint, subscription.P256DH, subscription.Auth, subscription.DeviceName,
		subscription.UserAgent, formatTime(subscription.CreatedAt), formatTime(subscription.LastUsedAt))
	if err != nil {
		return fmt.Errorf("保存推送设备失败: %w", err)
	}
	return nil
}

// ListPushSubscriptions 返回全部推送设备，包括发送通知所需的加密字段。
func (s *Store) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, endpoint, p256dh, auth, device_name, user_agent, created_at, last_used_at
		FROM push_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subscriptions := make([]PushSubscription, 0)
	for rows.Next() {
		var subscription PushSubscription
		var createdAt, lastUsedAt string
		if err := rows.Scan(&subscription.ID, &subscription.Endpoint, &subscription.P256DH, &subscription.Auth,
			&subscription.DeviceName, &subscription.UserAgent, &createdAt, &lastUsedAt); err != nil {
			return nil, err
		}
		subscription.CreatedAt = parseTime(createdAt)
		subscription.LastUsedAt = parseTime(lastUsedAt)
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

// DeletePushSubscription 删除一个推送设备。
func (s *Store) DeletePushSubscription(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireAffected(result, "推送设备不存在")
}

// DeletePushSubscriptionByEndpoint 清除浏览器推送服务判定失效的端点。
func (s *Store) DeletePushSubscriptionByEndpoint(ctx context.Context, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// TouchPushSubscription 更新设备最近成功使用时间。
func (s *Store) TouchPushSubscription(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_subscriptions SET last_used_at = ? WHERE id = ?`, formatTime(now), id)
	return err
}

// CountPushSubscriptions 返回已订阅设备数量。
func (s *Store) CountPushSubscriptions(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&count)
	return count, err
}
