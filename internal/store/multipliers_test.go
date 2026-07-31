package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func multiplierStoreFixture(t *testing.T) (*Store, Target, time.Time) {
	t.Helper()
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开倍率测试数据库失败：%v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	target := Target{
		ID: "target_multiplier_store", Name: "倍率测试渠道", Kind: "new_api", BaseURL: "https://api.example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(context.Background(), target); err != nil {
		t.Fatalf("创建倍率测试渠道失败：%v", err)
	}
	return database, target, now
}

func multiplierObservations(defaultValue, vipValue string) []GroupMultiplierObservation {
	return []GroupMultiplierObservation{
		{GroupKey: "default", GroupName: "默认分组", Description: "默认价格", Multiplier: defaultValue},
		{GroupKey: "vip", GroupName: "会员分组", Description: "会员价格", Multiplier: vipValue},
	}
}

func Test倍率基准变化聚合与未通知重试(t *testing.T) {
	database, target, now := multiplierStoreFixture(t)
	ctx := context.Background()
	if err := database.SyncGroupMultiplierSelection(ctx, target.ID, []string{"default", "vip"}, multiplierObservations("1.0", "0.5000"), now); err != nil {
		t.Fatalf("建立倍率基准失败：%v", err)
	}
	items, err := database.ListGroupMultiplierMonitors(ctx, target.ID)
	baselineByKey := make(map[string]string, len(items))
	for _, item := range items {
		baselineByKey[item.GroupKey] = item.CurrentMultiplier
	}
	if err != nil || len(items) != 2 || baselineByKey["default"] != "1" || baselineByKey["vip"] != "0.5" {
		t.Fatalf("首次基准没有规范保存：%#v，%v", items, err)
	}
	alertsFound, err := database.ListAlerts(ctx, "all", 20)
	if err != nil || len(alertsFound) != 0 {
		t.Fatalf("首次建立基准不应产生告警：%#v，%v", alertsFound, err)
	}

	changes, alert, err := database.ApplyGroupMultiplierCheck(
		ctx, target.ID, multiplierObservations("1.2", "0.4"), "multiplier_changed", now.Add(time.Minute),
	)
	if err != nil || len(changes) != 2 || alert == nil {
		t.Fatalf("倍率变化没有正确落库：changes=%#v alert=%#v err=%v", changes, alert, err)
	}
	if alert.State != "resolved" || alert.CurrentValue != "2" || !strings.Contains(alert.Message, "默认分组：1× → 1.2×") || !strings.Contains(alert.Message, "会员分组：0.5× → 0.4×") {
		t.Fatalf("同轮多个变化没有聚合成一条点事件：%#v", alert)
	}
	pending, err := database.ListUnnotifiedAlerts(ctx, 20)
	if err != nil || len(pending) != 1 || pending[0].ID != alert.ID {
		t.Fatalf("未发送的倍率点事件应进入重试队列：%#v，%v", pending, err)
	}

	changes, repeatedAlert, err := database.ApplyGroupMultiplierCheck(
		ctx, target.ID, multiplierObservations("1.2000", "0.400"), "multiplier_changed", now.Add(2*time.Minute),
	)
	if err != nil || len(changes) != 0 || repeatedAlert != nil {
		t.Fatalf("相同十进制值不应重复告警：changes=%#v alert=%#v err=%v", changes, repeatedAlert, err)
	}
	changes, secondAlert, err := database.ApplyGroupMultiplierCheck(
		ctx, target.ID, multiplierObservations("1", "0.4"), "multiplier_changed", now.Add(3*time.Minute),
	)
	if err != nil || len(changes) != 1 || secondAlert == nil || changes[0].PreviousMultiplier != "1.2" || changes[0].CurrentMultiplier != "1" {
		t.Fatalf("再次发生真实变化时应产生新事件：changes=%#v alert=%#v err=%v", changes, secondAlert, err)
	}
	alertsFound, err = database.ListAlerts(ctx, "all", 20)
	if err != nil || len(alertsFound) != 2 {
		t.Fatalf("两次真实变化应保留两条历史事件：%#v，%v", alertsFound, err)
	}
}

func Test倍率缺失重置基准与渠道删除级联(t *testing.T) {
	database, target, now := multiplierStoreFixture(t)
	ctx := context.Background()
	if err := database.SyncGroupMultiplierSelection(ctx, target.ID, []string{"default", "vip"}, multiplierObservations("1", "0.5"), now); err != nil {
		t.Fatalf("建立倍率基准失败：%v", err)
	}
	_, alert, err := database.ApplyGroupMultiplierCheck(ctx, target.ID, []GroupMultiplierObservation{
		{GroupKey: "default", GroupName: "默认分组", Multiplier: "1"},
	}, "multiplier_changed", now.Add(time.Minute))
	if err != nil || alert != nil {
		t.Fatalf("分组暂时缺失不应被当成零倍率变化：alert=%#v err=%v", alert, err)
	}
	items, err := database.ListGroupMultiplierMonitors(ctx, target.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("读取缺失状态失败：%#v，%v", items, err)
	}
	byKey := make(map[string]GroupMultiplierMonitor, len(items))
	for _, item := range items {
		byKey[item.GroupKey] = item
	}
	if !byKey["vip"].Missing || byKey["vip"].CurrentMultiplier != "0.5" {
		t.Fatalf("缺失分组应保留原基准：%#v", byKey["vip"])
	}

	target.UpdatedAt = now.Add(2 * time.Minute)
	if err := database.UpdateTargetAndMonitoring(ctx, target, TargetMonitoringKeep, nil, true); err != nil {
		t.Fatalf("更换登录信息后重置倍率基准失败：%v", err)
	}
	items, err = database.ListGroupMultiplierMonitors(ctx, target.ID)
	if err != nil || len(items) != 2 || items[0].CurrentMultiplier != "" || items[1].CurrentMultiplier != "" {
		t.Fatalf("重置后应保留选择但清空旧账号基准：%#v，%v", items, err)
	}
	changes, baselineAlert, err := database.ApplyGroupMultiplierCheck(
		ctx, target.ID, multiplierObservations("3", "2"), "multiplier_changed", now.Add(3*time.Minute),
	)
	if err != nil || len(changes) != 0 || baselineAlert != nil {
		t.Fatalf("新登录账号第一次检测只应重建基准：changes=%#v alert=%#v err=%v", changes, baselineAlert, err)
	}

	if err := database.DeleteTarget(ctx, target.ID); err != nil {
		t.Fatalf("删除倍率测试渠道失败：%v", err)
	}
	items, err = database.ListGroupMultiplierMonitors(ctx, target.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("删除渠道后倍率选择应级联删除：%#v，%v", items, err)
	}
}

func Test倍率存储入口拒绝巨大科学计数法(t *testing.T) {
	database, target, now := multiplierStoreFixture(t)
	err := database.SyncGroupMultiplierSelection(context.Background(), target.ID, []string{"default"}, []GroupMultiplierObservation{
		{GroupKey: "default", GroupName: "默认分组", Multiplier: "1e100000000"},
	}, now)
	if err == nil {
		t.Fatal("存储入口必须在展开前拒绝巨大科学计数倍率")
	}
}
