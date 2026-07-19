package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreMigrationAndAuthenticationLifecycle(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")
	database, err := Open(dataDir)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	hasAdmin, err := database.HasAdmin(ctx)
	if err != nil || hasAdmin {
		t.Fatalf("初始化状态不正确: %v, %v", hasAdmin, err)
	}

	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	if err := database.CreateAdmin(ctx, "admin", "hash", now); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if err := database.CreateSession(ctx, Session{
		TokenHash: "token-hash",
		AdminID:   1,
		CSRFToken: "csrf-token",
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	session, err := database.SessionByHash(ctx, "token-hash", now)
	if err != nil || session.CSRFToken != "csrf-token" {
		t.Fatalf("读取会话失败: %#v, %v", session, err)
	}
	if _, err := database.SessionByHash(ctx, "token-hash", now.Add(2*time.Hour)); err != sql.ErrNoRows {
		t.Fatalf("过期会话应返回不存在: %v", err)
	}

	retention, err := database.GetSetting(ctx, "history_retention_days")
	if err != nil || retention != "7" {
		t.Fatalf("默认保留期限不正确: %q, %v", retention, err)
	}
	var journalMode string
	if err := database.DB().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil || journalMode != "wal" {
		t.Fatalf("数据库未启用 WAL: %q, %v", journalMode, err)
	}
}

func TestSnapshotLimitKeepsNewestPointsInAscendingOrder(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_history", Name: "历史测试", Kind: "custom", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	for index := 0; index < 4; index++ {
		if err := database.InsertSnapshot(ctx, &Snapshot{
			TargetID: target.ID, ObservedAt: now.Add(time.Duration(index) * time.Second),
			Status: "healthy", MetricsJSON: "[]", DetailJSON: "{}",
		}); err != nil {
			t.Fatalf("保存快照失败: %v", err)
		}
	}
	items, err := database.ListSnapshots(ctx, target.ID, now.Add(-time.Minute), 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("读取受限历史失败: %#v, %v", items, err)
	}
	if !items[0].ObservedAt.Equal(now.Add(2*time.Second)) || !items[1].ObservedAt.Equal(now.Add(3*time.Second)) {
		t.Fatalf("历史应保留最新数据并按时间升序返回: %#v", items)
	}
}
