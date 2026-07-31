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
)

type multiplierRunner struct {
	mu              sync.Mutex
	groups          []monitor.GroupMultiplier
	groupError      error
	credential      *monitor.Credential
	groupCalls      int
	regularSnapshot monitor.Snapshot
	regularError    error
	priceResult     monitor.GroupPriceResult
	priceError      error
	priceGroupKey   string
	priceCalls      int
}

func (runner *multiplierRunner) ReadGroupPrices(_ context.Context, _ monitor.TargetInput, groupKey string) (monitor.GroupPriceResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.priceCalls++
	runner.priceGroupKey = groupKey
	return runner.priceResult, runner.priceError
}

func (runner *multiplierRunner) Run(_ context.Context, target monitor.TargetInput) (monitor.Result, error) {
	if runner.regularError != nil {
		return monitor.Snapshot{}, runner.regularError
	}
	if runner.regularSnapshot.TargetID != "" {
		return runner.regularSnapshot, nil
	}
	return monitor.Snapshot{
		TargetID: target.ID, Kind: target.Kind, Status: monitor.TargetStatusHealthy, ObservedAt: time.Now().UTC(),
		Metrics: []monitor.Metric{{Key: monitor.MetricWalletBalance, Label: "钱包余额", Value: decimal.NewFromInt(20), Unit: "USD"}},
	}, nil
}

func (runner *multiplierRunner) ReadGroupMultipliers(_ context.Context, _ monitor.TargetInput) (monitor.GroupMultiplierResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.groupCalls++
	if runner.groupError != nil {
		return monitor.GroupMultiplierResult{CredentialUpdate: runner.credential}, runner.groupError
	}
	groups := append([]monitor.GroupMultiplier(nil), runner.groups...)
	return monitor.GroupMultiplierResult{Groups: groups, CredentialUpdate: runner.credential}, nil
}

func (runner *multiplierRunner) setGroups(groups ...monitor.GroupMultiplier) {
	runner.mu.Lock()
	runner.groups = append([]monitor.GroupMultiplier(nil), groups...)
	runner.mu.Unlock()
}

type multiplierNotifier struct {
	mu    sync.Mutex
	items []alerts.Notification
}

func (notifier *multiplierNotifier) Notify(_ context.Context, item alerts.Notification) error {
	notifier.mu.Lock()
	notifier.items = append(notifier.items, item)
	notifier.mu.Unlock()
	return nil
}

func Test倍率选择建立基准且变化只通知一次(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{
		{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)},
		{Key: "vip", Name: "会员组", Multiplier: decimal.RequireFromString("0.5")},
	}}
	notifier := &multiplierNotifier{}
	service := NewService(database, vault, runner, alerts.NewEngine(database, notifier), false)
	ctx := context.Background()

	groups, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default", "vip"})
	if err != nil || len(groups) != 2 {
		t.Fatalf("保存倍率监控选择失败，groups=%#v err=%v", groups, err)
	}
	if len(notifier.items) != 0 {
		t.Fatalf("首次建立基准不应通知：%#v", notifier.items)
	}
	stored, err := database.ListGroupMultiplierMonitors(ctx, target.ID)
	storedByKey := make(map[string]string, len(stored))
	for _, item := range stored {
		storedByKey[item.GroupKey] = item.CurrentMultiplier
	}
	if err != nil || len(stored) != 2 || storedByKey["default"] != "1" || storedByKey["vip"] != "0.5" {
		t.Fatalf("倍率基准保存错误：%#v，%v", stored, err)
	}

	runner.setGroups(
		monitor.GroupMultiplier{Key: "default", Name: "默认组", Multiplier: decimal.RequireFromString("1.2")},
		monitor.GroupMultiplier{Key: "vip", Name: "会员组", Multiplier: decimal.RequireFromString("0.4")},
	)
	if _, err := service.DetectGroupMultipliers(ctx, target.ID); err != nil {
		t.Fatalf("检测倍率变化失败：%v", err)
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != string(monitor.AlertTypeMultiplierChanged) {
		t.Fatalf("同轮多个倍率变化应合并通知一次：%#v", notifier.items)
	}
	if _, err := service.DetectGroupMultipliers(ctx, target.ID); err != nil {
		t.Fatalf("重复检测倍率失败：%v", err)
	}
	if len(notifier.items) != 1 {
		t.Fatalf("倍率未再次变化时不应重复通知：%#v", notifier.items)
	}
	alertsFound, err := database.ListAlerts(ctx, "all", 20)
	if err != nil || len(alertsFound) != 1 || alertsFound[0].State != "resolved" {
		t.Fatalf("倍率点事件保存错误：%#v，%v", alertsFound, err)
	}
}

func Test分组价格只读已监控分组并保存续期凭据(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	rotated := monitor.Credential{AccessToken: "price-access", RefreshToken: "price-refresh"}
	runner := &multiplierRunner{
		groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}},
		priceResult: monitor.GroupPriceResult{
			Catalog: monitor.GroupPriceCatalog{
				GroupKey: "default", GroupName: "默认组", Multiplier: decimal.RequireFromString("0.25"),
				Models: []monitor.GroupModelPrice{{Name: "gpt-price", BillingMode: "token"}},
			},
			CredentialUpdate: &rotated,
		},
	}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	if _, err := service.ReadGroupPrices(ctx, target.ID, "default"); err == nil || !strings.Contains(err.Error(), "尚未加入") {
		t.Fatalf("未监控分组不应读取价格：%v", err)
	}
	if runner.priceCalls != 0 {
		t.Fatalf("未监控分组不应请求上游价格：%d", runner.priceCalls)
	}
	if _, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatalf("建立倍率基准失败：%v", err)
	}
	catalog, err := service.ReadGroupPrices(ctx, target.ID, "default")
	if err != nil || catalog.Multiplier.String() != "0.25" || runner.priceGroupKey != "default" {
		t.Fatalf("读取当前上游价格失败，catalog=%#v err=%v", catalog, err)
	}
	stored, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("重新读取渠道失败：%v", err)
	}
	decrypted, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(decrypted), "price-refresh") {
		t.Fatalf("价格读取轮换的凭据未持久化：%s，%v", decrypted, err)
	}
}

func Test分组价格超时仍先保存续期凭据(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	rotated := monitor.Credential{AccessToken: "timeout-access", RefreshToken: "timeout-refresh"}
	runner := &multiplierRunner{
		groups:      []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}},
		priceResult: monitor.GroupPriceResult{CredentialUpdate: &rotated},
		priceError:  context.DeadlineExceeded,
	}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	if _, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatalf("建立倍率基准失败：%v", err)
	}
	if _, err := service.ReadGroupPrices(ctx, target.ID, "default"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("价格读取必须保留超时错误：%v", err)
	}
	stored, err := database.TargetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("重新读取渠道失败：%v", err)
	}
	plain, err := vault.Decrypt(stored.CredentialsEnc)
	if err != nil || !strings.Contains(string(plain), "timeout-refresh") {
		t.Fatalf("超时前轮换的价格凭据未持久化：%s，%v", plain, err)
	}
}

func Test倍率读取失败不污染渠道健康状态(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}}}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	ctx := context.Background()
	if _, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatalf("保存倍率基准失败：%v", err)
	}
	runner.mu.Lock()
	runner.groupError = &monitor.CheckError{Kind: monitor.ErrorClassNetwork, Message: "连接失败"}
	runner.mu.Unlock()
	if _, err := service.DetectGroupMultipliers(ctx, target.ID); err == nil {
		t.Fatal("倍率网络失败应返回错误")
	}
	current, err := database.TargetByID(ctx, target.ID)
	if err != nil || current.FailureCount != 0 || current.Status != string(monitor.TargetStatusUnknown) {
		t.Fatalf("倍率失败不应污染渠道健康状态：%#v，%v", current, err)
	}
	items, err := database.ListGroupMultiplierMonitors(ctx, target.ID)
	if err != nil || len(items) != 1 || items[0].LastError == "" || items[0].CurrentMultiplier != "1" {
		t.Fatalf("倍率失败状态没有安全保存：%#v，%v", items, err)
	}
}

func Test定时渠道检测会同时检查已选倍率(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}}}
	notifier := &multiplierNotifier{}
	service := NewService(database, vault, runner, alerts.NewEngine(database, notifier), false)
	ctx := context.Background()
	if _, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatalf("保存倍率基准失败：%v", err)
	}
	runner.setGroups(monitor.GroupMultiplier{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(2)})
	if err := service.CheckTarget(ctx, target.ID); err != nil {
		t.Fatalf("渠道和倍率联合检测失败：%v", err)
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != string(monitor.AlertTypeMultiplierChanged) {
		t.Fatalf("渠道定时检测应产生倍率通知：%#v", notifier.items)
	}
}

func Test常规指标失败时仍独立检查已选倍率(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}}}
	notifier := &multiplierNotifier{}
	service := NewService(database, vault, runner, alerts.NewEngine(database, notifier), false)
	ctx := context.Background()
	if _, err := service.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatalf("保存倍率基准失败：%v", err)
	}
	runner.setGroups(monitor.GroupMultiplier{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(2)})
	runner.regularError = &monitor.CheckError{Kind: monitor.ErrorClassResponse, Message: "钱包响应格式错误"}
	if err := service.CheckTarget(ctx, target.ID); err == nil {
		t.Fatal("常规指标失败仍应返回原检测错误")
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != string(monitor.AlertTypeMultiplierChanged) {
		t.Fatalf("常规指标失败不应阻断独立倍率通知：%#v", notifier.items)
	}
}

func Test倍率读取轮换凭据会安全回写(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{
		groups:     []monitor.GroupMultiplier{{Key: "default", Name: "默认组", Multiplier: decimal.NewFromInt(1)}},
		credential: &monitor.Credential{AccessToken: "renewed", RefreshToken: "rotated"},
	}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	if _, err := service.SaveGroupMultiplierSelection(context.Background(), target.ID, []string{"default"}); err != nil {
		t.Fatalf("带令牌轮换的倍率读取失败：%v", err)
	}
	current, err := database.TargetByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("读取渠道失败：%v", err)
	}
	plain, err := vault.Decrypt(current.CredentialsEnc)
	if err != nil {
		t.Fatalf("解密轮换凭据失败：%v", err)
	}
	var credential monitor.Credential
	if err := json.Unmarshal(plain, &credential); err != nil || credential.AccessToken != "renewed" || credential.RefreshToken != "rotated" {
		t.Fatalf("轮换凭据保存错误：%#v，%v", credential, err)
	}
}

func Test倍率读取失败前已轮换的凭据仍会安全回写(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	runner := &multiplierRunner{
		groupError: &monitor.CheckError{Kind: monitor.ErrorClassNetwork, Message: "续期后读取失败"},
		credential: &monitor.Credential{AccessToken: "renewed-before-error", RefreshToken: "rotated-before-error"},
	}
	service := NewService(database, vault, runner, alerts.NewEngine(database, nil), false)
	if _, err := service.DetectGroupMultipliers(context.Background(), target.ID); err == nil {
		t.Fatal("上游读取失败应返回错误")
	}
	current, err := database.TargetByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("读取渠道失败：%v", err)
	}
	plain, err := vault.Decrypt(current.CredentialsEnc)
	if err != nil {
		t.Fatalf("解密轮换凭据失败：%v", err)
	}
	var credential monitor.Credential
	if err := json.Unmarshal(plain, &credential); err != nil || credential.RefreshToken != "rotated-before-error" {
		t.Fatalf("读取失败前的轮换凭据没有保存：%#v，%v", credential, err)
	}
}

func Test请求取消后仍会短暂完成轮换凭据落库(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	service := NewService(database, vault, &multiplierRunner{}, alerts.NewEngine(database, nil), false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	credential := &monitor.Credential{AccessToken: "cancelled-renewed", RefreshToken: "cancelled-rotated"}
	if err := service.persistCredentialUpdate(cancelled, &target, credential); err != nil {
		t.Fatalf("请求取消后轮换凭据仍应完成落库：%v", err)
	}
	current, err := database.TargetByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("读取渠道失败：%v", err)
	}
	plain, err := vault.Decrypt(current.CredentialsEnc)
	if err != nil {
		t.Fatalf("解密轮换凭据失败：%v", err)
	}
	var stored monitor.Credential
	if err := json.Unmarshal(plain, &stored); err != nil || stored.RefreshToken != "cancelled-rotated" {
		t.Fatalf("取消请求后的轮换凭据没有保存：%#v，%v", stored, err)
	}
}

func Test不支持渠道与并发倍率检测会被拒绝(t *testing.T) {
	database, vault, target := schedulerFixture(t)
	defer database.Close()
	target.Kind = string(monitor.TargetKindCustom)
	if err := database.UpdateTarget(context.Background(), target); err != nil {
		t.Fatalf("更新测试渠道失败：%v", err)
	}
	service := NewService(database, vault, &multiplierRunner{}, alerts.NewEngine(database, nil), false)
	if _, err := service.DetectGroupMultipliers(context.Background(), target.ID); err == nil {
		t.Fatal("自定义渠道不应支持倍率检测")
	}

	// 同一渠道锁由普通检测和倍率操作共享，避免保存基准时读取到并发旧配置。
	target.Kind = string(monitor.TargetKindNewAPI)
	if err := database.UpdateTarget(context.Background(), target); err != nil {
		t.Fatalf("恢复测试渠道失败：%v", err)
	}
	unlock, err := service.LockTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("取得测试锁失败：%v", err)
	}
	defer unlock()
	if _, err := service.DetectGroupMultipliers(context.Background(), target.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("并发倍率检测应被拒绝：%v", err)
	}
}
