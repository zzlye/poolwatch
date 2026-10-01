package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"poolwatch/internal/store"
)

// 指标配置与采集结果分离，未获取的额度不得被写成真实零值。
func TestMissingMetricValueAndAccountWarning(t *testing.T) {
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	config, _ := json.Marshal(storedTargetConfig{ThresholdMeta: []thresholdDraft{{Key: "image_quota", Label: "图片额度", Unit: "次", Value: "100"}}})
	now := time.Now().UTC()
	target := store.Target{ID: "chat-partial", Name: "测试号池", Kind: "chatgpt2api", BaseURL: "https://pool.example", Enabled: true, PollIntervalSeconds: 300, Status: "error", ConfigJSON: string(config), CreatedAt: now, UpdatedAt: now}
	if err := database.CreateTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	server := &Server{dependencies: Dependencies{Store: database}}
	result, err := server.mapTarget(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Metrics) != 1 || result.Metrics[0].Value != "" || result.Metrics[0].Status != "unknown" {
		t.Fatal("未采集的指标必须使用未知状态与空值")
	}
	detail, _ := json.Marshal(map[string]string{"accountsWarning": "账号明细读取失败，汇总已更新"})
	if err := database.InsertSnapshot(context.Background(), &store.Snapshot{TargetID: target.ID, ObservedAt: now, Status: "warning", MetricsJSON: "[]", DetailJSON: string(detail)}); err != nil {
		t.Fatal(err)
	}
	result, err = server.mapTarget(context.Background(), target)
	if err != nil || result.AccountsWarning != "账号明细读取失败，汇总已更新" {
		t.Fatal("明细提示未传递给前端")
	}
}
