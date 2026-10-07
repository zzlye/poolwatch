package store

import (
	"errors"
	"testing"
	"time"
)

func priceObservations(value, unit string) []ModelPriceObservation {
	return []ModelPriceObservation{{Name: "模型甲", Prices: []PricePoint{{Key: "input", Label: "输入", Range: "通用价格", Value: value, Unit: unit}, {Key: "output", Label: "输出", Range: "通用价格", Value: "10", Unit: unit}}}}
}

func Test模型价格独立基准变化去重及通知重试(t *testing.T) {
	db, target, now := multiplierStoreFixture(t)
	ctx := t.Context()
	if err := db.SyncGroupMultiplierSelection(ctx, target.ID, []string{"default"}, multiplierObservations("1", "2"), now); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"模型甲"}, priceObservations("2.00", "USD/百万令牌"), now); err != nil {
		t.Fatal(err)
	}
	items, _ := db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 || items[0].Prices[0].Value != "2" {
		t.Fatalf("价格基准未规范化：%#v", items)
	}
	for i, value := range []string{"2.0", "3", "3.00", "2"} {
		alert, err := db.ApplyModelPriceCheck(ctx, target.ID, "default", "默认", priceObservations(value, "USD/百万令牌"), now.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if (alert != nil) != (i == 1 || i == 3) {
			t.Fatalf("变化去重错误：%d %#v", i, alert)
		}
	}
	alerts, err := db.ListUnnotifiedAlerts(ctx, 20)
	if err != nil || len(alerts) != 2 || alerts[0].Type != "price_changed" {
		t.Fatalf("价格事件未进入通知重试：%#v %v", alerts, err)
	}
	if err := db.SyncGroupMultiplierSelection(ctx, target.ID, nil, nil, now); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 {
		t.Fatal("取消倍率不应取消价格")
	}
	if err := db.SyncGroupMultiplierSelection(ctx, target.ID, []string{"default"}, multiplierObservations("1", "2"), now); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	multipliers, _ := db.ListGroupMultiplierMonitors(ctx, target.ID)
	if len(multipliers) != 1 {
		t.Fatal("取消价格不应取消倍率")
	}
}

func Test模型价格丢失保留旧值恢复及单位变化(t *testing.T) {
	db, target, now := multiplierStoreFixture(t)
	ctx := t.Context()
	if err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"模型甲"}, priceObservations("0", "USD/次"), now); err != nil {
		t.Fatal(err)
	}
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "default", "默认", nil, now.Add(time.Minute)); a != nil || e != nil {
		t.Fatalf("缺失不能当零价变化：%v %v", a, e)
	}
	items, _ := db.ListModelPriceMonitors(ctx, target.ID)
	if !items[0].Missing || items[0].Prices[0].Value != "0" {
		t.Fatal("真实零价应保留")
	}
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "default", "默认", priceObservations("0", "USD/次"), now.Add(2*time.Minute)); a != nil || e != nil {
		t.Fatalf("同价恢复不通知：%v %v", a, e)
	}
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "default", "默认", priceObservations("0", "元/次"), now.Add(3*time.Minute)); a == nil || e != nil {
		t.Fatalf("单位变化必须记录：%v %v", a, e)
	}
	if err := db.MarkModelPriceFailure(ctx, target.ID, "default", "读取失败", now); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if items[0].LastError != "读取失败" || items[0].Prices[0].Unit != "元/次" {
		t.Fatal("失败不能清空价格")
	}
	if err := db.DeleteTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 0 {
		t.Fatal("渠道删除应级联清理")
	}
}

func Test模型价格无效选择回滚且不重置既有基准(t *testing.T) {
	db, target, now := multiplierStoreFixture(t)
	ctx := t.Context()
	if err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"模型甲"}, priceObservations("2", "USD"), now); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"模型甲"}, priceObservations("9", "USD"), now); err != nil {
		t.Fatal(err)
	}
	items, _ := db.ListModelPriceMonitors(ctx, target.ID)
	if items[0].Prices[0].Value != "2" {
		t.Fatal("重复保存不应吞掉尚未检测的价格变化")
	}
	err := db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"不存在"}, nil, now)
	if !errors.Is(err, ErrPriceSelection) {
		t.Fatal("应拒绝上游不存在的模型")
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 {
		t.Fatal("无效保存应回滚")
	}
	err = db.SyncModelPriceSelection(ctx, target.ID, "default", "默认", []string{"模型甲"}, priceObservations("1e999999", "USD"), now)
	if !errors.Is(err, ErrPriceSelection) {
		t.Fatal("应拒绝异常指数")
	}
	// 手动更换身份后保留选择，但下一次价格只建立新基准。
	target.UpdatedAt = now.Add(time.Minute)
	if err := db.UpdateTargetAndMonitoring(ctx, target, TargetMonitoringKeep, nil, true); err != nil {
		t.Fatal(err)
	}
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "default", "默认", priceObservations("20", "USD"), now); a != nil || e != nil {
		t.Fatalf("身份更换不应跨账号误报：%v %v", a, e)
	}
	if err := db.ResetTargetMonitoring(ctx, target.ID, now); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 0 {
		t.Fatal("更换站点应清理价格配置")
	}
}

func Test模型价格重启持久化分组隔离和历史清理(t *testing.T) {
	directory := t.TempDir()
	db, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	target := Target{ID: "price-persist", Name: "价格持久化", Kind: "sub2api", BaseURL: "https://example.com", Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "unknown", CreatedAt: now, UpdatedAt: now}
	ctx := t.Context()
	if err := db.CreateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"1", "2"} {
		if err := db.SyncModelPriceSelection(ctx, target.ID, key, key, []string{"模型甲"}, priceObservations("2", "USD"), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items, err := db.ListModelPriceMonitors(ctx, target.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("重启未保留配置：%v %v", items, err)
	}
	observations := priceObservations("2.00", "USD")
	// 上游字段顺序变化不能引发价格告警。
	observations[0].Prices[0], observations[0].Prices[1] = observations[0].Prices[1], observations[0].Prices[0]
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "1", "一组", observations, now.Add(time.Minute)); e != nil || a != nil {
		t.Fatalf("顺序变化误报：%v %v", a, e)
	}
	if a, e := db.ApplyModelPriceCheck(ctx, target.ID, "1", "一组", priceObservations("5", "USD"), now.Add(time.Hour)); e != nil || a == nil {
		t.Fatalf("价格变化未记录：%v %v", a, e)
	}
	if err := db.SyncModelPriceSelection(ctx, target.ID, "1", "一组", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 || items[0].GroupKey != "2" {
		t.Fatal("取消一个分组不能删除另一个分组")
	}
	if err := db.CleanupHistory(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	alerts, _ := db.ListAlerts(ctx, "all", 20)
	if len(alerts) != 0 {
		t.Fatal("历史保留期应清除过期价格事件")
	}
	items, _ = db.ListModelPriceMonitors(ctx, target.ID)
	if len(items) != 1 {
		t.Fatal("历史清理不能删除当前价格基准")
	}
}
