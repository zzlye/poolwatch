package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open 打开数据库并确保所有表结构已经就绪。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	databasePath := filepath.Join(dataDir, "poolwatch.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close 释放数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// DB 返回底层连接，供健康检查和事务型服务使用。
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS admins (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			totp_enabled INTEGER NOT NULL DEFAULT 0,
			totp_secret_enc TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			password_set_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS recovery_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			code_hash TEXT NOT NULL UNIQUE,
			used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			csrf_token TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT OR IGNORE INTO settings(key, value) VALUES ('history_retention_days', '7')`,
		`CREATE TABLE IF NOT EXISTS targets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			base_url TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			poll_interval_seconds INTEGER NOT NULL DEFAULT 300,
			recharge_url TEXT NOT NULL DEFAULT '',
			config_json TEXT NOT NULL DEFAULT '{}',
			credentials_enc TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'unknown',
			failure_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			last_checked_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target_id TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
			observed_at TEXT NOT NULL,
			status TEXT NOT NULL,
			metrics_json TEXT NOT NULL,
			detail_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_target_time ON snapshots(target_id, observed_at DESC)`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id TEXT PRIMARY KEY,
			target_id TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			metric_key TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			title TEXT NOT NULL,
			message TEXT NOT NULL,
			current_value TEXT NOT NULL DEFAULT '',
			threshold_value TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL DEFAULT '',
			opened_at TEXT NOT NULL,
			recovered_at TEXT,
			last_notified_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_state_time ON alerts(state, opened_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_alerts_open_incident ON alerts(target_id, type, metric_key) WHERE state = 'open'`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			id TEXT PRIMARY KEY,
			endpoint TEXT NOT NULL UNIQUE,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			device_name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS chat_accounts (
			target_id TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
			external_id TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			quota INTEGER NOT NULL DEFAULT 0,
			restore_at TEXT NOT NULL DEFAULT '',
			success INTEGER NOT NULL DEFAULT 0,
			fail INTEGER NOT NULL DEFAULT 0,
			observed_at TEXT NOT NULL,
			PRIMARY KEY(target_id, external_id)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			target_id TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行数据库迁移失败: %w", err)
		}
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
