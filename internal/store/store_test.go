package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationAddsGeneralAccountColumns(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("创建旧数据库目录失败: %v", err)
	}
	legacy, err := sql.Open("sqlite", filepath.Join(dataDir, "poolwatch.db"))
	if err != nil {
		t.Fatalf("打开旧数据库失败: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE chat_accounts (
		target_id TEXT NOT NULL,
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
	)`)
	if closeErr := legacy.Close(); err != nil || closeErr != nil {
		t.Fatalf("准备旧账号表失败: %v, 关闭错误: %v", err, closeErr)
	}

	database, err := Open(dataDir)
	if err != nil {
		t.Fatalf("升级旧数据库失败: %v", err)
	}
	defer database.Close()
	columns := map[string]bool{}
	rows, err := database.DB().Query(`PRAGMA table_info(chat_accounts)`)
	if err != nil {
		t.Fatalf("读取升级后字段失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, fieldType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("读取升级字段失败: %v", err)
		}
		columns[name] = true
	}
	for _, name := range []string{
		"display_name", "provider", "status_text", "quota_state", "quota_windows_json", "subscription_expires_at",
	} {
		if !columns[name] {
			t.Fatalf("旧账号表升级后缺少字段：%s", name)
		}
	}
}

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

func TestChatAccounts支持泛化安全字段(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_cli_accounts", Name: "CLIProxyAPI", Kind: "cliproxyapi", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "healthy", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	if err := database.ReplaceChatAccounts(ctx, target.ID, []ChatAccount{{
		TargetID: target.ID, ExternalID: "hashed-id", DisplayName: "主账号", Provider: "codex",
		Email: "pr***@example.com", Type: "plus", Status: "warning", StatusText: "额度冷却中",
		QuotaState: "available", QuotaWindows: []AccountQuotaWindow{{
			Key: "code-5h", Label: "5 小时", RemainingPercent: "42.5", ResetAt: "2026-07-20T09:00:00Z",
		}},
		SubscriptionExpiresAt: "2026-08-20T08:00:00Z",
		RestoreAt:             "2026-07-21T00:00:00Z", Success: 8, Fail: 2, ObservedAt: now,
	}}); err != nil {
		t.Fatalf("保存泛化账号失败: %v", err)
	}
	accounts, err := database.ListChatAccounts(ctx, target.ID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("读取泛化账号失败：%#v, %v", accounts, err)
	}
	account := accounts[0]
	if account.DisplayName != "主账号" || account.Provider != "codex" || account.StatusText != "额度冷却中" ||
		account.Success != 8 || account.Fail != 2 {
		t.Fatalf("泛化账号字段不完整：%#v", account)
	}
	if account.QuotaState != "available" || account.SubscriptionExpiresAt != "2026-08-20T08:00:00Z" ||
		len(account.QuotaWindows) != 1 || account.QuotaWindows[0].RemainingPercent != "42.5" {
		t.Fatalf("泛化账号额度字段不完整：%#v", account)
	}

	columns := map[string]bool{}
	rows, err := database.DB().QueryContext(ctx, `PRAGMA table_info(chat_accounts)`)
	if err != nil {
		t.Fatalf("读取账号表结构失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, fieldType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("读取账号表字段失败: %v", err)
		}
		columns[name] = true
	}
	for _, name := range []string{
		"display_name", "provider", "status_text", "quota_state", "quota_windows_json", "subscription_expires_at",
	} {
		if !columns[name] {
			t.Fatalf("账号表缺少迁移字段：%s", name)
		}
	}
}

func TestRefreshTargetMonitoringConfig保留历史并清理取消指标(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_refresh_config", Name: "配置更新测试", Kind: "new_api", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "warning", FailureCount: 2,
		LastError: "旧错误", LastCheckedAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	if err := database.InsertSnapshot(ctx, &Snapshot{
		TargetID: target.ID, ObservedAt: now.Add(-time.Minute), Status: "warning", MetricsJSON: "[]", DetailJSON: "{}",
	}); err != nil {
		t.Fatalf("保存历史快照失败: %v", err)
	}
	for _, alert := range []Alert{
		{ID: "alert_subscription", TargetID: target.ID, Type: "threshold", MetricKey: "subscription_balance", State: "open", Title: "订阅余额不足", OpenedAt: now.Add(-time.Minute)},
		{ID: "alert_wallet", TargetID: target.ID, Type: "threshold", MetricKey: "wallet_balance", State: "open", Title: "钱包余额不足", OpenedAt: now.Add(-time.Minute)},
		{ID: "alert_recovered", TargetID: target.ID, Type: "recovered", MetricKey: "subscription_balance", State: "resolved", Title: "订阅余额已恢复", OpenedAt: now.Add(-30 * time.Second)},
	} {
		if err := database.CreateAlert(ctx, alert); err != nil {
			t.Fatalf("创建告警失败: %v", err)
		}
	}

	if err := database.RefreshTargetMonitoringConfig(ctx, target.ID, []string{"subscription_balance"}, now); err != nil {
		t.Fatalf("刷新渠道监控配置失败: %v", err)
	}
	updated, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("读取更新后的渠道失败: %v", err)
	}
	if updated.Status != "unknown" || updated.FailureCount != 0 || updated.LastError != "" || !updated.LastCheckedAt.IsZero() {
		t.Fatalf("渠道应进入待检测状态: %#v", updated)
	}
	if _, err := database.LatestSnapshot(ctx, target.ID); err != nil {
		t.Fatalf("配置更新不应删除历史快照: %v", err)
	}
	if _, err := database.ActiveAlert(ctx, target.ID, "threshold", "subscription_balance"); err != sql.ErrNoRows {
		t.Fatalf("已取消指标的活跃告警应被清理: %v", err)
	}
	if _, err := database.ActiveAlert(ctx, target.ID, "threshold", "wallet_balance"); err != nil {
		t.Fatalf("仍在监控的指标告警应继续保留: %v", err)
	}
	resolved, err := database.AlertByID(ctx, "alert_subscription")
	if err != nil || resolved.LastNotifiedAt == nil {
		t.Fatalf("关闭指标后应终止旧告警的待发送状态: %#v, %v", resolved, err)
	}
	pending, err := database.ListUnnotifiedAlerts(ctx, 10)
	if err != nil {
		t.Fatalf("读取待发送告警失败: %v", err)
	}
	pendingIDs := make(map[string]bool, len(pending))
	for _, item := range pending {
		pendingIDs[item.ID] = true
	}
	if pendingIDs["alert_subscription"] || !pendingIDs["alert_wallet"] || !pendingIDs["alert_recovered"] {
		t.Fatalf("关闭指标后待发送事件不正确: %#v", pendingIDs)
	}
}

func TestUpdateTargetAndMonitoring任一步失败都会回滚(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_atomic_update", Name: "原名称", Kind: "new_api", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "healthy", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(ctx, target); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	target.Name = "不应保存的新名称"
	target.UpdatedAt = now.Add(time.Minute)
	if err := database.UpdateTargetAndMonitoring(ctx, target, TargetMonitoringUpdateMode("invalid"), nil); err == nil {
		t.Fatal("无效监控更新模式应返回错误")
	}
	stored, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("读取回滚后的渠道失败: %v", err)
	}
	if stored.Name != "原名称" || stored.Status != "healthy" {
		t.Fatalf("事务失败后不应留下部分更新: %#v", stored)
	}
}
