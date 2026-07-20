package alerts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/monitor"
	"poolwatch/internal/store"
)

type recordingNotifier struct {
	items []Notification
}

type flakyNotifier struct {
	attempts int
}

func (notifier *flakyNotifier) Notify(_ context.Context, _ Notification) error {
	notifier.attempts++
	if notifier.attempts == 1 {
		return errors.New("临时推送失败")
	}
	return nil
}

func (notifier *recordingNotifier) Notify(_ context.Context, notification Notification) error {
	notifier.items = append(notifier.items, notification)
	return nil
}

func TestThresholdAlertOnlyOnceAndRearmsAfterRecovery(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindNewAPI))
	defer database.Close()
	notifier := &recordingNotifier{}
	engine := NewEngine(database, notifier)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	threshold := decimal.NewFromInt(10)

	low := monitor.Snapshot{TargetID: target.ID, Kind: monitor.TargetKindNewAPI, ObservedAt: now, Metrics: []monitor.Metric{{
		Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(10), Unit: "元", Threshold: &threshold,
	}}}
	if err := engine.HandleSuccess(context.Background(), target, low); err != nil {
		t.Fatalf("处理低额度失败: %v", err)
	}
	if err := engine.HandleSuccess(context.Background(), target, low); err != nil {
		t.Fatalf("重复处理低额度失败: %v", err)
	}
	if len(notifier.items) != 1 {
		t.Fatalf("同一事件应只通知一次，实际 %d 次", len(notifier.items))
	}
	latest, err := database.LatestSnapshot(context.Background(), target.ID)
	if err != nil || latest.Status != string(monitor.TargetStatusWarning) {
		t.Fatalf("低额度快照状态不正确: %#v, %v", latest, err)
	}

	now = now.Add(time.Minute)
	healthy := low
	healthy.ObservedAt = now
	healthy.Metrics[0].Value = decimal.NewFromInt(11)
	if err := engine.HandleSuccess(context.Background(), target, healthy); err != nil {
		t.Fatalf("恢复额度失败: %v", err)
	}
	if len(notifier.items) != 2 || !notifier.items[1].Recovered {
		t.Fatalf("恢复通知不正确: %#v", notifier.items)
	}
	if _, err := database.ActiveAlert(context.Background(), target.ID, string(monitor.AlertTypeQuotaLow), string(monitor.MetricWalletBalance)); err != sql.ErrNoRows {
		t.Fatalf("恢复后不应保留活跃阈值告警: %v", err)
	}

	now = now.Add(time.Minute)
	low.ObservedAt = now
	low.Metrics[0].Value = decimal.NewFromInt(10)
	if err := engine.HandleSuccess(context.Background(), target, low); err != nil {
		t.Fatalf("重新触发低额度失败: %v", err)
	}
	if len(notifier.items) != 3 {
		t.Fatalf("恢复后应重新允许告警，实际通知 %d 次", len(notifier.items))
	}
}

func TestFailureThresholdAndCredentialAlert(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindSub2API))
	defer database.Close()
	notifier := &recordingNotifier{}
	engine := NewEngine(database, notifier)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	ctx := context.Background()

	for attempt := 1; attempt <= 3; attempt++ {
		current, err := database.TargetByID(ctx, target.ID)
		if err != nil {
			t.Fatalf("读取渠道失败: %v", err)
		}
		if err := engine.HandleFailure(ctx, current, &monitor.CheckError{Kind: monitor.ErrorClassNetwork, Message: "连接超时"}); err != nil {
			t.Fatalf("处理第 %d 次失败出错: %v", attempt, err)
		}
		now = now.Add(time.Minute)
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != string(monitor.AlertTypeConnectivity) {
		t.Fatalf("连续三次失败告警不正确: %#v", notifier.items)
	}

	current, _ := database.TargetByID(ctx, target.ID)
	if err := engine.HandleSuccess(ctx, current, monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindSub2API, Status: monitor.TargetStatusHealthy, ObservedAt: now,
	}); err != nil {
		t.Fatalf("处理连接恢复失败: %v", err)
	}
	if len(notifier.items) != 2 || !notifier.items[1].Recovered {
		t.Fatalf("连接恢复通知不正确: %#v", notifier.items)
	}

	current, _ = database.TargetByID(ctx, target.ID)
	if err := engine.HandleFailure(ctx, current, &monitor.CheckError{Kind: monitor.ErrorClassAuth, Message: "访问被拒绝"}); err != nil {
		t.Fatalf("处理凭据失效失败: %v", err)
	}
	if len(notifier.items) != 3 || notifier.items[2].Type != string(monitor.AlertTypeCredentialInvalid) {
		t.Fatalf("凭据失效应立即告警: %#v", notifier.items)
	}
}

func TestChatPoolOnlyComparesImageQuota(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindChatGPT2API))
	defer database.Close()
	notifier := &recordingNotifier{}
	engine := NewEngine(database, notifier)
	threshold := decimal.NewFromInt(10)
	now := time.Now().UTC()
	snapshot := monitor.Snapshot{TargetID: target.ID, Kind: monitor.TargetKindChatGPT2API, ObservedAt: now, Metrics: []monitor.Metric{
		{Key: monitor.MetricHealthyAccounts, Label: "正常账号", Value: decimal.Zero, Unit: "个", Threshold: &threshold},
		{Key: monitor.MetricImageQuota, Label: "图片额度", Value: decimal.NewFromInt(20), Unit: "次", Threshold: &threshold},
	}, Accounts: []monitor.AccountStatus{{Email: "private@example.com", Type: "plus", Status: "正常", Quota: decimal.NewFromInt(20)}}}
	if err := engine.HandleSuccess(context.Background(), target, snapshot); err != nil {
		t.Fatalf("处理号池快照失败: %v", err)
	}
	if len(notifier.items) != 0 {
		t.Fatalf("账号数量阈值不应触发号池告警: %#v", notifier.items)
	}
	accounts, err := database.ListChatAccounts(context.Background(), target.ID)
	if err != nil || len(accounts) != 1 || accounts[0].Email == "private@example.com" {
		t.Fatalf("账号明细未正确脱敏: %#v, %v", accounts, err)
	}
}

func TestChatPoolDropsNonEmailAccountToPreventSecretPersistence(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindChatGPT2API))
	defer database.Close()
	engine := NewEngine(database, nil)
	secret := "Bearer private-token-value"
	if err := engine.HandleSuccess(context.Background(), target, monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindChatGPT2API, Status: monitor.TargetStatusHealthy,
		ObservedAt: time.Now().UTC(), Accounts: []monitor.AccountStatus{{
			Email: secret, Type: "unknown", Status: "active", Quota: decimal.NewFromInt(1),
		}},
	}); err != nil {
		t.Fatalf("处理异常账号明细失败: %v", err)
	}
	accounts, err := database.ListChatAccounts(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("读取账号明细失败: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("非邮箱账号不应写入明细：%#v", accounts)
	}
}

func TestMaskEmailRejectsNonStandardAddress(t *testing.T) {
	if actual := maskEmail("private-token@internal"); actual != "" {
		t.Fatalf("非标准邮箱不应产生脱敏值：%q", actual)
	}
	if actual := maskEmail("private@example.com"); actual != "pr***@example.com" {
		t.Fatalf("标准邮箱脱敏结果不正确：%q", actual)
	}
}

func TestChatPoolRemovesSensitiveMetadata(t *testing.T) {
	accounts := sanitizedAccounts(string(monitor.TargetKindChatGPT2API), "target_test", []monitor.AccountStatus{{
		Email: "private@example.com", Type: "Bearer private-token", RestoreAt: "client_secret_value",
	}}, time.Now().UTC())
	if len(accounts) != 1 {
		t.Fatalf("标准邮箱账号应保留：%#v", accounts)
	}
	if accounts[0].Type != "" || accounts[0].RestoreAt != "" {
		t.Fatalf("疑似凭据的账号元数据不应被保存：%#v", accounts[0])
	}
}

func TestGreaterThanThresholdAlertAndRecovery(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindCLIProxyAPI))
	defer database.Close()
	notifier := &recordingNotifier{}
	engine := NewEngine(database, notifier)
	now := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	threshold := decimal.NewFromInt(1)
	snapshot := monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindCLIProxyAPI, ObservedAt: now,
		Metrics: []monitor.Metric{{
			Key: monitor.MetricErrorAccounts, Label: "异常账号", Value: decimal.NewFromInt(1), Unit: "个",
			Threshold: &threshold, Comparison: monitor.ThresholdComparisonGTE,
		}},
	}
	if err := engine.HandleSuccess(context.Background(), target, snapshot); err != nil {
		t.Fatalf("处理异常账号阈值失败: %v", err)
	}
	if len(notifier.items) != 1 || notifier.items[0].Title != "异常账号过高" {
		t.Fatalf("高于等于阈值应发送过高告警：%#v", notifier.items)
	}
	latest, err := database.LatestSnapshot(context.Background(), target.ID)
	if err != nil || latest.Status != string(monitor.TargetStatusWarning) {
		t.Fatalf("高向阈值快照状态不正确：%#v, %v", latest, err)
	}

	snapshot.ObservedAt = now.Add(time.Minute)
	snapshot.Metrics[0].Value = decimal.Zero
	if err := engine.HandleSuccess(context.Background(), target, snapshot); err != nil {
		t.Fatalf("处理异常账号恢复失败: %v", err)
	}
	if len(notifier.items) != 2 || !notifier.items[1].Recovered || !strings.Contains(notifier.items[1].Message, "已经低于阈值") {
		t.Fatalf("高向阈值恢复通知不正确：%#v", notifier.items)
	}
}

func TestCLIProxyAccountsAreHashedAndSanitized(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindCLIProxyAPI))
	defer database.Close()
	engine := NewEngine(database, nil)
	rawID := "upstream-account-secret-id"
	if err := engine.HandleSuccess(context.Background(), target, monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindCLIProxyAPI, ObservedAt: time.Now().UTC(),
		Accounts: []monitor.AccountStatus{{
			ExternalID: rawID, DisplayName: "主账号", Provider: "codex", Email: "private@example.com",
			Type: "plus", Status: string(monitor.TargetStatusWarning), StatusText: "额度冷却中",
			RecoveryAt: "2026-07-21T00:00:00Z", Success: 12, Fail: 3,
		}},
	}); err != nil {
		t.Fatalf("保存 CLIProxyAPI 账号失败: %v", err)
	}
	accounts, err := database.ListChatAccounts(context.Background(), target.ID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("读取 CLIProxyAPI 账号失败：%#v, %v", accounts, err)
	}
	account := accounts[0]
	if account.ExternalID == rawID || len(account.ExternalID) != 24 || account.Email == "private@example.com" {
		t.Fatalf("账号标识或邮箱未脱敏：%#v", account)
	}
	if account.DisplayName != "主账号" || account.Provider != "codex" || account.StatusText != "额度冷却中" || account.Success != 12 || account.Fail != 3 {
		t.Fatalf("安全账号字段保存不完整：%#v", account)
	}
}

func TestCLIProxyAccountLabelMasksEmailAndDropsSecrets(t *testing.T) {
	accounts := sanitizedAccounts(string(monitor.TargetKindCLIProxyAPI), "target_cli", []monitor.AccountStatus{
		{ExternalID: "email-label", DisplayName: "user@example.com", Status: string(monitor.TargetStatusHealthy)},
		{ExternalID: "secret-label", DisplayName: "sk-abcdefghijklmnopqrstuvwxyz0123456789", Status: string(monitor.TargetStatusHealthy)},
	}, time.Now().UTC())
	if len(accounts) != 2 {
		t.Fatalf("CLIProxyAPI 安全账号应被保留：%#v", accounts)
	}
	if accounts[0].DisplayName != "us***@example.com" || accounts[0].DisplayName == "user@example.com" {
		t.Fatalf("邮箱形式的账号名称未脱敏：%#v", accounts[0])
	}
	if accounts[1].DisplayName != "" {
		t.Fatalf("疑似密钥的账号名称应清空：%#v", accounts[1])
	}
}

func TestDisabledRemoteAccountAlertsImmediately(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindSub2API))
	defer database.Close()
	notifier := &recordingNotifier{}
	engine := NewEngine(database, notifier)
	now := time.Now().UTC()
	if err := engine.HandleSuccess(context.Background(), target, monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindSub2API, Status: monitor.TargetStatusDisabled,
		ObservedAt: now, Message: "账号已被停用",
	}); err != nil {
		t.Fatalf("处理停用账号失败: %v", err)
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != string(monitor.AlertTypeCredentialInvalid) {
		t.Fatalf("停用账号应立即通知: %#v", notifier.items)
	}
	if err := engine.HandleSuccess(context.Background(), target, monitor.Snapshot{
		TargetID: target.ID, Kind: monitor.TargetKindSub2API, Status: monitor.TargetStatusHealthy, ObservedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("处理账号恢复失败: %v", err)
	}
	if len(notifier.items) != 2 || !notifier.items[1].Recovered {
		t.Fatalf("账号恢复通知不正确: %#v", notifier.items)
	}
}

func TestFailedNotificationRemainsPendingUntilRetrySucceeds(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindNewAPI))
	defer database.Close()
	notifier := &flakyNotifier{}
	engine := NewEngine(database, notifier)
	threshold := decimal.NewFromInt(10)
	snapshot := monitor.Snapshot{TargetID: target.ID, Kind: monitor.TargetKindNewAPI, ObservedAt: time.Now().UTC(), Metrics: []monitor.Metric{{
		Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(1), Unit: "元", Threshold: &threshold,
	}}}
	if err := engine.HandleSuccess(context.Background(), target, snapshot); err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}
	active, err := database.ActiveAlert(context.Background(), target.ID, string(monitor.AlertTypeQuotaLow), string(monitor.MetricWalletBalance))
	if err != nil || active.LastNotifiedAt != nil {
		t.Fatalf("失败通知应保持待发送: %#v, %v", active, err)
	}
	if err := engine.RetryPending(context.Background(), 10); err != nil {
		t.Fatalf("重试待发送通知失败: %v", err)
	}
	active, err = database.ActiveAlert(context.Background(), target.ID, string(monitor.AlertTypeQuotaLow), string(monitor.MetricWalletBalance))
	if err != nil || active.LastNotifiedAt == nil || notifier.attempts != 2 {
		t.Fatalf("通知重试状态不正确: %#v, 尝试=%d, 错误=%v", active, notifier.attempts, err)
	}
}

func createTargetForTest(t *testing.T, kind string) (*store.Store, store.Target) {
	t.Helper()
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	now := time.Now().UTC()
	target := store.Target{
		ID: "target_test", Name: "测试渠道", Kind: kind, BaseURL: "https://example.com", Enabled: true,
		PollIntervalSeconds: 300, ConfigJSON: "{}", Status: string(monitor.TargetStatusUnknown), CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(context.Background(), target); err != nil {
		database.Close()
		t.Fatalf("创建测试渠道失败: %v", err)
	}
	return database, target
}
