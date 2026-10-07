package scheduler

import (
	"errors"
	"github.com/shopspring/decimal"
	"poolwatch/internal/alerts"
	"poolwatch/internal/monitor"
	"testing"
)

func priceCatalogFixture(value string) monitor.GroupPriceResult {
	return monitor.GroupPriceResult{Catalog: monitor.GroupPriceCatalog{GroupKey: "default", GroupName: "默认分组", Multiplier: decimal.NewFromInt(1), Models: []monitor.GroupModelPrice{{Name: "模型甲", BillingMode: "token", Prices: []monitor.GroupPriceItem{{Key: "input", Label: "输入", Value: decimal.RequireFromString(value), Unit: "USD/百万令牌"}}}}}}
}

func Test独立模型价格无倍率配置仍检测并保留轮换凭据(t *testing.T) {
	db, vault, target := schedulerFixture(t)
	defer db.Close()
	ctx := t.Context()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认", Multiplier: decimal.NewFromInt(1)}}, priceResult: priceCatalogFixture("2")}
	notifier := &multiplierNotifier{}
	s := NewService(db, vault, runner, alerts.NewEngine(db, notifier), false)
	if _, err := s.DiscoverPriceGroups(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadModelPriceCatalog(ctx, target.ID, "default"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelPriceSelection(ctx, target.ID, "default", []string{"模型甲"}); err != nil {
		t.Fatal(err)
	}
	groups, _ := db.ListGroupMultiplierMonitors(ctx, target.ID)
	if len(groups) != 0 || len(notifier.items) != 0 {
		t.Fatal("价格发现和保存不应创建倍率选择或告警")
	}
	runner.priceResult = priceCatalogFixture("3")
	runner.priceResult.CredentialUpdate = &monitor.Credential{AccessToken: "synthetic-new-token", RefreshToken: "synthetic-refresh-token"}
	if err := s.CheckTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if len(notifier.items) != 1 || notifier.items[0].Type != "price_changed" {
		t.Fatalf("后台价格变化没有通知：%#v", notifier.items)
	}
	current, _ := db.TargetByID(ctx, target.ID)
	config, err := s.runtimeConfig(current)
	if err != nil || config.Credential.AccessToken != "synthetic-new-token" {
		t.Fatal("价格检测应持久化更新凭据")
	}
	if err := s.CheckModelPrices(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if len(notifier.items) != 1 {
		t.Fatal("相同价格重复通知")
	}
	runner.priceError = errors.New("synthetic secret must not leak")
	if err := s.CheckModelPrices(ctx, target.ID); err == nil {
		t.Fatal("应返回价格失败")
	}
	items, _ := db.ListModelPriceMonitors(ctx, target.ID)
	if items[0].Prices[0].Value != "3" || items[0].LastError != "价格检测失败，保留上次有效价格。" {
		t.Fatal("价格失败应脱敏且保留基准")
	}
	if !s.acquireTarget(target.ID) {
		t.Fatal("测试锁失败")
	}
	if _, err := s.ReadModelPriceCatalog(ctx, target.ID, "default"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatal("价格必须防重入")
	}
	s.releaseTarget(target.ID)
}

func Test倍率失败不阻止价格检测且取消互不影响(t *testing.T) {
	db, vault, target := schedulerFixture(t)
	defer db.Close()
	ctx := t.Context()
	runner := &multiplierRunner{groups: []monitor.GroupMultiplier{{Key: "default", Name: "默认", Multiplier: decimal.NewFromInt(1)}}, priceResult: priceCatalogFixture("2")}
	notifier := &multiplierNotifier{}
	s := NewService(db, vault, runner, alerts.NewEngine(db, notifier), false)
	if _, err := s.SaveGroupMultiplierSelection(ctx, target.ID, []string{"default"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelPriceSelection(ctx, target.ID, "default", []string{"模型甲"}); err != nil {
		t.Fatal(err)
	}
	runner.groupError = errors.New("倍率失败")
	runner.regularError = errors.New("钱包失败")
	runner.priceResult = priceCatalogFixture("8")
	_ = s.CheckTarget(ctx, target.ID)
	items, _ := db.ListModelPriceMonitors(ctx, target.ID)
	if items[0].Prices[0].Value != "8" {
		t.Fatal("其他检测失败不能阻断价格")
	}
	if _, err := s.SaveGroupMultiplierSelection(ctx, target.ID, nil); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 {
		t.Fatal("倍率清空不能清空价格")
	}
	if err := s.SaveModelPriceSelection(ctx, target.ID, "default", nil); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 0 {
		t.Fatal("价格清空未生效")
	}
}
