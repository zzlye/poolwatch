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
