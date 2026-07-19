package alerts

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/monitor"
	"poolwatch/internal/store"
)

type recordingNotifier struct {
	items []Notification
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
