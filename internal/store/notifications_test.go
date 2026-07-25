package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestNotificationDeliveryStateAndAlertCascade(t *testing.T) {
	dataDirectory := t.TempDir()
	database, err := Open(dataDirectory)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if database != nil {
			_ = database.Close()
		}
	})
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_delivery", Name: "投递测试", Kind: "custom", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "warning", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	alert := Alert{ID: "alert_delivery", TargetID: target.ID, Type: "threshold", State: "open", Title: "余额不足", OpenedAt: now}
	if err := database.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}
	delivered, err := database.NotificationDelivered(ctx, alert.ID, "email", "")
	if err != nil || delivered {
		t.Fatalf("初始投递状态不正确: %v, %v", delivered, err)
	}
	if err := database.RecordNotificationAttempt(ctx, alert.ID, "email", "", false, "临时错误", now); err != nil {
		t.Fatalf("记录失败尝试失败: %v", err)
	}
	if err := database.RecordNotificationAttempt(ctx, alert.ID, "email", "", true, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("记录成功尝试失败: %v", err)
	}
	if err := database.RecordNotificationAttempt(ctx, alert.ID, "email", "", false, "迟到错误", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("记录迟到尝试失败: %v", err)
	}
	if err := database.RecordNotificationAttempt(ctx, alert.ID, "web_push", "device_failed", false, "临时错误", now); err != nil {
		t.Fatalf("记录失败设备失败: %v", err)
	}
	if err := database.RecordNotificationAttempt(ctx, alert.ID, "web_push", "device_ok", true, "", now); err != nil {
		t.Fatalf("记录成功设备失败: %v", err)
	}
	delivered, err = database.NotificationDelivered(ctx, alert.ID, "email", "")
	count, countErr := database.NotificationAttemptCount(ctx, alert.ID, "email", "")
	if err != nil || countErr != nil || !delivered || count != 3 {
		t.Fatalf("最终投递状态不正确: delivered=%v count=%d err=%v/%v", delivered, count, err, countErr)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	database, err = Open(dataDirectory)
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %v", err)
	}
	deliveredFailed, failedErr := database.NotificationDelivered(ctx, alert.ID, "web_push", "device_failed")
	deliveredOK, okErr := database.NotificationDelivered(ctx, alert.ID, "web_push", "device_ok")
	if failedErr != nil || okErr != nil || deliveredFailed || !deliveredOK {
		t.Fatalf("重启后设备独立投递状态不正确: failed=%v ok=%v err=%v/%v", deliveredFailed, deliveredOK, failedErr, okErr)
	}
	if err := database.DeleteTarget(ctx, target.ID); err != nil {
		t.Fatalf("删除渠道失败: %v", err)
	}
	count, err = database.NotificationAttemptCount(ctx, alert.ID, "email", "")
	if err != nil || count != 0 {
		t.Fatalf("告警删除后投递状态没有级联清理: %d, %v", count, err)
	}
}

func TestNotificationDeliveryMigratesLegacyChannelState(t *testing.T) {
	dataDirectory := t.TempDir()
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	database, err := Open(dataDirectory)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	target := Target{
		ID: "target_legacy_delivery", Name: "旧投递状态", Kind: "custom", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "warning", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		database.Close()
		t.Fatalf("创建渠道失败: %v", err)
	}
	alert := Alert{ID: "alert_legacy_delivery", TargetID: target.ID, Type: "threshold", State: "open", Title: "余额不足", OpenedAt: now}
	if err := database.CreateAlert(ctx, alert); err != nil {
		database.Close()
		t.Fatalf("创建告警失败: %v", err)
	}
	if err := database.UpsertPushSubscription(ctx, PushSubscription{
		ID: "device_legacy", Endpoint: "https://push.example.com/legacy", P256DH: "encrypted-key", Auth: "encrypted-auth",
		DeviceName: "旧设备", CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		database.Close()
		t.Fatalf("创建旧推送设备失败: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	legacyDatabase, err := sql.Open("sqlite", filepath.Join(dataDirectory, "poolwatch.db"))
	if err != nil {
		t.Fatalf("打开旧结构数据库失败: %v", err)
	}
	legacyStatements := []string{
		`DROP TABLE alert_notification_deliveries`,
		`CREATE TABLE alert_notification_deliveries (
			alert_id TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
			channel TEXT NOT NULL,
			delivered_at TEXT,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at TEXT,
			last_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(alert_id, channel)
		)`,
		`INSERT INTO alert_notification_deliveries(
			alert_id, channel, delivered_at, attempt_count, last_attempt_at, last_error
		) VALUES ('alert_legacy_delivery', 'email', '2026-07-25T09:00:00Z', 1, '2026-07-25T09:00:00Z', '')`,
		`INSERT INTO alert_notification_deliveries(
			alert_id, channel, delivered_at, attempt_count, last_attempt_at, last_error
		) VALUES ('alert_legacy_delivery', 'web_push', '2026-07-25T09:00:00Z', 1, '2026-07-25T09:00:00Z', '')`,
	}
	for _, statement := range legacyStatements {
		if _, err := legacyDatabase.Exec(statement); err != nil {
			legacyDatabase.Close()
			t.Fatalf("准备旧投递结构失败: %v", err)
		}
	}
	if err := legacyDatabase.Close(); err != nil {
		t.Fatalf("关闭旧结构数据库失败: %v", err)
	}

	database, err = Open(dataDirectory)
	if err != nil {
		t.Fatalf("升级旧投递结构失败: %v", err)
	}
	defer database.Close()
	delivered, err := database.NotificationDelivered(ctx, alert.ID, "email", "")
	if err != nil || !delivered {
		t.Fatalf("旧通道投递状态没有保留: delivered=%v err=%v", delivered, err)
	}
	pushDelivered, err := database.NotificationDelivered(ctx, alert.ID, "web_push", "device_legacy")
	if err != nil || !pushDelivered {
		t.Fatalf("旧整批推送状态没有展开到设备: delivered=%v err=%v", pushDelivered, err)
	}
	for _, deviceID := range []string{"device_one", "device_two"} {
		if err := database.RecordNotificationAttempt(ctx, alert.ID, "web_push", deviceID, true, "", now); err != nil {
			t.Fatalf("升级后写入独立设备失败: %s: %v", deviceID, err)
		}
	}
}
