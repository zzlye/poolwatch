package alerts

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"poolwatch/internal/monitor"
)

// 明细部分失败必须保留上次账号；明确的空列表仍可正确删除旧记录。
func TestChatPartialResponsePreservesAccountsAndQuotaAlerts(t *testing.T) {
	database, target := createTargetForTest(t, string(monitor.TargetKindChatGPT2API))
	defer database.Close()
	engine := NewEngine(database, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	snapshot := monitor.Snapshot{TargetID: target.ID, Kind: monitor.TargetKindChatGPT2API, ObservedAt: now,
		Accounts: []monitor.AccountStatus{{Email: "first@example.com", Status: "正常", Quota: decimal.NewFromInt(123)}}}
	if err := engine.HandleSuccess(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	threshold := decimal.NewFromInt(100)
	snapshot.ObservedAt = now.Add(time.Second)
	snapshot.Accounts = nil
	snapshot.Status = monitor.TargetStatusWarning
	snapshot.AccountsWarning = "账号明细超限，保留旧列表"
	snapshot.Metrics = []monitor.Metric{{Key: monitor.MetricImageQuota, Label: "图片额度", Unit: "次", Value: decimal.NewFromInt(80), Threshold: &threshold}}
	if err := engine.HandleSuccess(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	accounts, err := database.ListChatAccounts(ctx, target.ID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("部分失败丢失账号：%v", err)
	}
	stored, err := database.LatestSnapshot(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]string
	if json.Unmarshal([]byte(stored.DetailJSON), &details) != nil || details["accountsWarning"] != snapshot.AccountsWarning {
		t.Fatal("明细诊断没有持久化")
	}
	if _, err := database.ActiveAlert(ctx, target.ID, string(monitor.AlertTypeQuotaLow), string(monitor.MetricImageQuota)); err != nil {
		t.Fatalf("明细部分失败不应阻止真实低额度告警：%v", err)
	}
	snapshot.ObservedAt = now.Add(2 * time.Second)
	snapshot.Status = monitor.TargetStatusHealthy
	snapshot.AccountsWarning = ""
	snapshot.Accounts = []monitor.AccountStatus{}
	snapshot.Metrics[0].Value = decimal.NewFromInt(123)
	if err := engine.HandleSuccess(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	accounts, err = database.ListChatAccounts(ctx, target.ID)
	if err != nil || len(accounts) != 0 {
		t.Fatal("成功的空列表未替换旧账号")
	}
}
