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
	dsn := databasePath + "?_pragma=journal_mode%28WAL%29&_pragma=foreign_keys%281%29&_pragma=busy_timeout%285000%29"
	db, err := sql.Open("sqlite", dsn)
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
		`INSERT OR IGNORE INTO settings(key, value) VALUES ('product_name', '号池监控')`,
		`INSERT OR IGNORE INTO settings(key, value) VALUES ('default_poll_seconds', '300')`,
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
		`DROP INDEX IF EXISTS idx_alerts_open_incident`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_alerts_open_incident ON alerts(target_id, type, metric_key) WHERE state IN ('open', 'acknowledged')`,
		`CREATE TABLE IF NOT EXISTS alert_notification_deliveries (
			alert_id TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
			channel TEXT NOT NULL,
			destination_id TEXT NOT NULL DEFAULT '',
			delivered_at TEXT,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at TEXT,
			last_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(alert_id, channel, destination_id)
		)`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			id TEXT PRIMARY KEY,
			endpoint TEXT NOT NULL UNIQUE,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			device_name TEXT NOT NULL,
			user_agent TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS chat_accounts (
			target_id TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
			external_id TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			status_text TEXT NOT NULL DEFAULT '',
			quota INTEGER NOT NULL DEFAULT 0,
			quota_state TEXT NOT NULL DEFAULT '',
			quota_windows_json TEXT NOT NULL DEFAULT '[]',
			subscription_expires_at TEXT NOT NULL DEFAULT '',
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
	if err := s.ensureNotificationDeliveryDestination(ctx); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "push_subscriptions", "user_agent", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "display_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "provider", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "status_text", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "quota_state", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "quota_windows_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "subscription_expires_at", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "chat_accounts", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

// ensureNotificationDeliveryDestination 将早期按通道记录的投递状态升级为按具体接收方记录。
func (s *Store) ensureNotificationDeliveryDestination(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(alert_notification_deliveries)`)
	if err != nil {
		return err
	}
	destinationFound := false
	primaryColumns := map[string]int{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, fieldType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "destination_id" {
			destinationFound = true
		}
		if primaryKey > 0 {
			primaryColumns[name] = primaryKey
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if destinationFound && primaryColumns["alert_id"] == 1 && primaryColumns["channel"] == 2 && primaryColumns["destination_id"] == 3 {
		return nil
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DROP TABLE IF EXISTS alert_notification_deliveries_upgrade`); err != nil {
		return fmt.Errorf("清理通知投递升级临时表失败: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE alert_notification_deliveries_upgrade (
		alert_id TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
		channel TEXT NOT NULL,
		destination_id TEXT NOT NULL DEFAULT '',
		delivered_at TEXT,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_attempt_at TEXT,
		last_error TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(alert_id, channel, destination_id)
	)`); err != nil {
		return fmt.Errorf("创建通知投递升级表失败: %w", err)
	}
	if destinationFound {
		if _, err := transaction.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_deliveries_upgrade(
			alert_id, channel, destination_id, delivered_at, attempt_count, last_attempt_at, last_error
		) SELECT alert_id, channel, destination_id, delivered_at, attempt_count, last_attempt_at, last_error
		FROM alert_notification_deliveries`); err != nil {
			return fmt.Errorf("迁移通知投递状态失败: %w", err)
		}
	} else {
		if _, err := transaction.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_deliveries_upgrade(
			alert_id, channel, destination_id, delivered_at, attempt_count, last_attempt_at, last_error
		) SELECT alert_id, channel, '', delivered_at, attempt_count, last_attempt_at, last_error
		FROM alert_notification_deliveries WHERE channel <> 'web_push'`); err != nil {
			return fmt.Errorf("迁移通知通道投递状态失败: %w", err)
		}
		// 旧版只有整批推送状态；整批成功时可安全展开到当时保存的全部设备，避免邮箱重试造成重复推送。
		if _, err := transaction.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_deliveries_upgrade(
			alert_id, channel, destination_id, delivered_at, attempt_count, last_attempt_at, last_error
		) SELECT deliveries.alert_id, deliveries.channel, subscriptions.id, deliveries.delivered_at,
			deliveries.attempt_count, deliveries.last_attempt_at, deliveries.last_error
		FROM alert_notification_deliveries AS deliveries
		CROSS JOIN push_subscriptions AS subscriptions
		WHERE deliveries.channel = 'web_push'`); err != nil {
			return fmt.Errorf("迁移浏览器设备投递状态失败: %w", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `DROP TABLE alert_notification_deliveries`); err != nil {
		return fmt.Errorf("替换通知投递旧表失败: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE alert_notification_deliveries_upgrade RENAME TO alert_notification_deliveries`); err != nil {
		return fmt.Errorf("启用通知投递新表失败: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交通知投递升级失败: %w", err)
	}
	return nil
}

// ensureColumn 为早期数据库补充后来新增的简单字段。
func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, fieldType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition); err != nil {
		return fmt.Errorf("升级数据库字段失败: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
