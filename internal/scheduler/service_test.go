package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/alerts"
	"poolwatch/internal/monitor"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

type blockingRunner struct {
	started chan monitor.TargetConfig
	release chan struct{}
	once    sync.Once
}

type cancelRunner struct {
	started chan struct{}
}

type quotaRunner struct {
	requested []string
	accounts  []monitor.AccountStatus
}

func (runner *quotaRunner) Run(_ context.Context, target monitor.TargetInput) (monitor.Result, error) {
	return monitor.Snapshot{TargetID: target.ID, Kind: target.Kind, Status: monitor.TargetStatusHealthy}, nil
}

func (runner *quotaRunner) RefreshAccountQuotas(_ context.Context, _ monitor.TargetInput, accountIDs []string) ([]monitor.AccountStatus, error) {
	runner.requested = append([]string(nil), accountIDs...)
	return append([]monitor.AccountStatus(nil), runner.accounts...), nil
}

func (runner cancelRunner) Run(ctx context.Context, _ monitor.TargetInput) (monitor.Result, error) {
	close(runner.started)
	<-ctx.Done()
	return monitor.Result{}, ctx.Err()
}

func (runner *blockingRunner) Run(ctx context.Context, target monitor.TargetInput) (monitor.Result, error) {
	runner.once.Do(func() { runner.started <- target })
	select {
	case <-runner.release:
		updatedCredential := target.Credential
		updatedCredential.AccessToken = "renewed-access-token"
		updatedCredential.RefreshToken = "rotated-refresh-token"
		return monitor.Snapshot{
			TargetID: target.ID, Kind: target.Kind, Status: monitor.TargetStatusHealthy, ObservedAt: time.Now().UTC(),
			Metrics:          []monitor.Metric{{Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(20), Unit: "元"}},
			CredentialUpdate: &updatedCredential,
		}, nil
	case <-ctx.Done():
		return monitor.Snapshot{}, ctx.Err()
	}
}

func TestSingleTargetCannotReenterAndCredentialsDecrypt(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &blockingRunner{started: make(chan monitor.TargetConfig, 1), release: make(chan struct{})}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- service.CheckTarget(ctx, target.ID) }()

	var received monitor.TargetConfig
	select {
	case received = <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("检测器未启动")
	}
	if received.Credential.AccessToken != "secret-token" || received.AllowPrivateNetwork {
		t.Fatalf("运行配置解密或网络策略不正确: %#v", received)
	}
	if err := service.CheckTarget(ctx, target.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("渠道重入未被阻止: %v", err)
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("首次检测失败: %v", err)
	}
	stored, err := database.TargetByID(ctx, target.ID)
	if err != nil || stored.Status != string(monitor.TargetStatusHealthy) || stored.LastCheckedAt.IsZero() {
		t.Fatalf("检测结果未保存: %#v, %v", stored, err)
	}
	decrypted, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(decrypted), "rotated-refresh-token") || strings.Contains(string(decrypted), "totp_code") {
		t.Fatalf("续期凭据未安全保存: %s, %v", decrypted, err)
	}
}

func TestHistoryCleanupUsesRetentionSetting(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	service := NewService(database, vault, &blockingRunner{}, alerts.NewEngine(database, nil), false)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	ctx := context.Background()
	if err := database.SetSetting(ctx, "history_retention_days", "1"); err != nil {
		t.Fatalf("保存保留期限失败: %v", err)
	}
	for _, observedAt := range []time.Time{now.Add(-48 * time.Hour), now.Add(-12 * time.Hour)} {
		if err := database.InsertSnapshot(ctx, &store.Snapshot{
			TargetID: target.ID, ObservedAt: observedAt, Status: "healthy", MetricsJSON: "[]", DetailJSON: "{}",
		}); err != nil {
			t.Fatalf("保存测试快照失败: %v", err)
		}
	}
	if err := service.Cleanup(ctx); err != nil {
		t.Fatalf("清理历史失败: %v", err)
	}
	var count int
	if err := database.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshots WHERE target_id = ?`, target.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("历史清理结果不正确: %d, %v", count, err)
	}
}

func TestCallerCancellationDoesNotCountAsChannelFailure(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := cancelRunner{started: make(chan struct{})}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.CheckTarget(ctx, target.ID) }()
	<-runner.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("调用方取消应原样返回: %v", err)
	}
	stored, err := database.TargetByID(context.Background(), target.ID)
	if err != nil || stored.FailureCount != 0 || !stored.LastCheckedAt.IsZero() {
		t.Fatalf("调用方取消不应污染渠道失败状态: %#v, %v", stored, err)
	}
	var snapshots int
	if err := database.DB().QueryRow(`SELECT COUNT(*) FROM snapshots WHERE target_id = ?`, target.ID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("调用方取消不应保存失败快照: %d, %v", snapshots, err)
	}
}

func TestConfigurationUpdateDiscardsRunningCheckResult(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &blockingRunner{started: make(chan monitor.TargetConfig, 1), release: make(chan struct{})}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- service.CheckTarget(ctx, target.ID) }()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("检测器未启动")
	}
	updated, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	updated.ConfigJSON = `{"thresholds":{"wallet_balance":"5"}}`
	updated.UpdatedAt = time.Now().UTC().Add(time.Second)
	if err := database.UpdateTarget(ctx, updated); err != nil {
		t.Fatalf("更新检测中的渠道配置失败: %v", err)
	}
	if err := database.RefreshTargetMonitoringConfig(ctx, target.ID, nil, updated.UpdatedAt); err != nil {
		t.Fatalf("安排新配置重新检测失败: %v", err)
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("旧检测结果应被安静丢弃: %v", err)
	}

	stored, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("读取更新后的渠道失败: %v", err)
	}
	if stored.Status != string(monitor.TargetStatusUnknown) || !stored.LastCheckedAt.IsZero() {
		t.Fatalf("旧检测不应覆盖待重检状态: %#v", stored)
	}
	var snapshotCount int
	if err := database.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshots WHERE target_id = ?`, target.ID).Scan(&snapshotCount); err != nil || snapshotCount != 0 {
		t.Fatalf("旧检测不应保存快照: %d, %v", snapshotCount, err)
	}
	decrypted, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || strings.Contains(string(decrypted), "renewed-access-token") {
		t.Fatalf("旧检测不应覆盖渠道凭据: %s, %v", decrypted, err)
	}
}

func TestTargetLockWaitsUntilRunningCheckFinishes(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &blockingRunner{started: make(chan monitor.TargetConfig, 1), release: make(chan struct{})}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	checkDone := make(chan error, 1)
	go func() { checkDone <- service.CheckTarget(ctx, target.ID) }()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("检测器未启动")
	}

	type lockResult struct {
		unlock func()
		err    error
	}
	locked := make(chan lockResult, 1)
	go func() {
		unlock, err := service.LockTarget(ctx, target.ID)
		locked <- lockResult{unlock: unlock, err: err}
	}()
	select {
	case result := <-locked:
		if result.unlock != nil {
			result.unlock()
		}
		t.Fatalf("检测尚未结束时不应取得配置锁: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(runner.release)
	if err := <-checkDone; err != nil {
		t.Fatalf("检测失败: %v", err)
	}
	select {
	case result := <-locked:
		if result.err != nil {
			t.Fatalf("等待配置锁失败: %v", result.err)
		}
		if _, err := database.LatestSnapshot(ctx, target.ID); err != nil {
			result.unlock()
			t.Fatalf("取得配置锁前检测结果应已完整保存: %v", err)
		}
		result.unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("检测结束后未取得配置锁")
	}
}

func TestRefreshAccountQuotasOnlyUpdatesSelectedAccounts(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	credentialJSON, _ := json.Marshal(monitor.Credential{AdminKey: "management-secret"})
	credentialEncrypted, err := vault.Encrypt(credentialJSON)
	if err != nil {
		t.Fatalf("加密 CLIProxyAPI 管理密钥失败: %v", err)
	}
	target.Kind = string(monitor.TargetKindCLIProxyAPI)
	target.CredentialsEnc = credentialEncrypted
	target.ConfigJSON = `{}`
	if err := database.UpdateTarget(context.Background(), target); err != nil {
		t.Fatalf("更新测试渠道类型失败: %v", err)
	}
	firstID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "auth-first")
	secondID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, "auth-second")
	if err := database.ReplaceChatAccounts(context.Background(), target.ID, []store.ChatAccount{
		{
			TargetID: target.ID, ExternalID: firstID, Provider: "codex", Type: "plus", Status: string(monitor.TargetStatusHealthy),
			StatusText: "可用", QuotaState: monitor.AccountQuotaStateAvailable,
			QuotaWindows: []store.AccountQuotaWindow{{Key: "code-5h", Label: "5 小时", RemainingPercent: "80"}},
		},
		{
			TargetID: target.ID, ExternalID: secondID, Provider: "codex", Type: "free", Status: string(monitor.TargetStatusWarning),
			StatusText: "参数警告", QuotaState: monitor.AccountQuotaStateAvailable,
			QuotaWindows: []store.AccountQuotaWindow{{Key: "code-5h", Label: "5 小时", RemainingPercent: "60"}},
		},
	}); err != nil {
		t.Fatalf("保存测试账号失败: %v", err)
	}
	runner := &quotaRunner{accounts: []monitor.AccountStatus{{
		ExternalID: "auth-first", Provider: "codex", Type: "free", Status: string(monitor.TargetStatusError),
		StatusText: "不应覆盖", QuotaState: monitor.AccountQuotaStateUnavailable,
	}}}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	updated, err := service.RefreshAccountQuotas(context.Background(), target.ID, []string{firstID})
	if err != nil {
		t.Fatalf("刷新当前页额度失败: %v", err)
	}
	if len(runner.requested) != 1 || runner.requested[0] != firstID || len(updated) != 1 || updated[0].ExternalID != firstID {
		t.Fatalf("调度器提交的账号范围不正确：请求=%#v 返回=%#v", runner.requested, updated)
	}
	stored, err := database.ListChatAccounts(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("读取额度刷新结果失败: %v", err)
	}
	byID := make(map[string]store.ChatAccount, len(stored))
	for _, account := range stored {
		byID[account.ExternalID] = account
	}
	if byID[firstID].Status != string(monitor.TargetStatusHealthy) || byID[firstID].StatusText != "可用" || byID[firstID].Type != "plus" ||
		byID[firstID].QuotaState != monitor.AccountQuotaStateUnavailable || len(byID[firstID].QuotaWindows) != 0 {
		t.Fatalf("已选账号只应更新额度字段：%#v", byID[firstID])
	}
	if byID[secondID].Status != string(monitor.TargetStatusWarning) || byID[secondID].QuotaState != monitor.AccountQuotaStateAvailable ||
		len(byID[secondID].QuotaWindows) != 1 || byID[secondID].QuotaWindows[0].RemainingPercent != "60" {
		t.Fatalf("其他页面账号额度应保持原值：%#v", byID[secondID])
	}
}

func TestRefreshAccountQuotasRejectsOtherTargetKinds(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	service := NewService(database, vault, &quotaRunner{}, alerts.NewEngine(database, nil), false)
	_, err := service.RefreshAccountQuotas(context.Background(), target.ID, []string{"0123456789abcdef01234567"})
	if err == nil || !strings.Contains(err.Error(), "只有 CLIProxyAPI") {
		t.Fatalf("其他渠道类型应被拒绝: %v", err)
	}
}

func schedulerFixture(t *testing.T) (*store.Store, *secure.Vault, store.Target) {
	t.Helper()
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		database.Close()
		t.Fatalf("创建保险箱失败: %v", err)
	}
	credentialJSON, _ := json.Marshal(monitor.Credential{AccessToken: "secret-token", UserID: "1"})
	credentialEncrypted, err := vault.Encrypt(credentialJSON)
	if err != nil {
		database.Close()
		t.Fatalf("加密凭据失败: %v", err)
	}
	configJSON, _ := json.Marshal(monitor.TargetConfig{Thresholds: map[monitor.MetricKey]decimal.Decimal{
		monitor.MetricWalletBalance: decimal.NewFromInt(10),
	}})
	now := time.Now().UTC()
	target := store.Target{
		ID: "target_scheduler", Name: "调度测试", Kind: string(monitor.TargetKindNewAPI), BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: string(configJSON), CredentialsEnc: credentialEncrypted,
		Status: string(monitor.TargetStatusUnknown), CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(context.Background(), target); err != nil {
		database.Close()
		t.Fatalf("创建测试渠道失败: %v", err)
	}
	return database, vault, target
}
