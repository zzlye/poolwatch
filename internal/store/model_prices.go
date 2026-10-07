package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"poolwatch/internal/identity"
)

// ErrPriceSelection 标记可反馈给用户的模型价格选择错误。
var ErrPriceSelection = errors.New("模型价格选择无效")

// PricePoint 保存独立计费项目；金额和单位原样传输，不跨币种比较大小。
type PricePoint struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Range string `json:"range"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

// ModelPriceObservation 只包含适配器解析后的价格，不含上游原始响应或凭据。
type ModelPriceObservation struct {
	Name   string       `json:"name"`
	Prices []PricePoint `json:"prices"`
}

// ModelPriceMonitor 与倍率监控分表保存，取消任何一方不会修改另一方。
type ModelPriceMonitor struct {
	TargetID      string       `json:"targetId"`
	GroupKey      string       `json:"groupKey"`
	GroupName     string       `json:"groupName"`
	ModelName     string       `json:"modelName"`
	Prices        []PricePoint `json:"prices"`
	Previous      []PricePoint `json:"previous"`
	Missing       bool         `json:"missing"`
	LastError     string       `json:"lastError"`
	LastCheckedAt string       `json:"lastCheckedAt"`
	ChangedAt     string       `json:"changedAt"`
}

type priceQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listModelPrices(ctx context.Context, q priceQuerier, targetID string) ([]ModelPriceMonitor, error) {
	rows, err := q.QueryContext(ctx, `SELECT target_id,group_key,group_name,model_name,current_json,previous_json,missing,last_error,last_checked_at,changed_at FROM model_price_monitors WHERE target_id=? ORDER BY group_name,group_key,model_name`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ModelPriceMonitor, 0)
	for rows.Next() {
		var item ModelPriceMonitor
		var current, previous string
		if err := rows.Scan(&item.TargetID, &item.GroupKey, &item.GroupName, &item.ModelName, &current, &previous, &item.Missing, &item.LastError, &item.LastCheckedAt, &item.ChangedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(current), &item.Prices); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(previous), &item.Previous); err != nil {
			return nil, err
		}
		if item.Prices == nil {
			item.Prices = []PricePoint{}
		}
		if item.Previous == nil {
			item.Previous = []PricePoint{}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListModelPriceMonitors 返回一个渠道的独立模型价格配置和最新结果。
func (s *Store) ListModelPriceMonitors(ctx context.Context, targetID string) ([]ModelPriceMonitor, error) {
	return listModelPrices(ctx, s.db, targetID)
}

func normalizeModelPrices(observations []ModelPriceObservation) (map[string][]PricePoint, error) {
	result := make(map[string][]PricePoint, len(observations))
	total := 0
	for _, observation := range observations {
		if observation.Name == "" || len(observation.Name) > 800 {
			return nil, fmt.Errorf("%w：模型名称无效", ErrPriceSelection)
		}
		if _, exists := result[observation.Name]; exists {
			return nil, fmt.Errorf("%w：模型名称重复", ErrPriceSelection)
		}
		points := append([]PricePoint{}, observation.Prices...)
		keys := make(map[string]bool)
		for i := range points {
			p := &points[i]
			if p.Key == "" || len(p.Key) > 1600 || keys[p.Key] || len(p.Value) > 128 || len(p.Unit) > 200 || len(p.Label) > 400 || len(p.Range) > 1000 {
				return nil, fmt.Errorf("%w：价格项目格式无效", ErrPriceSelection)
			}
			keys[p.Key] = true
			value, err := decimal.NewFromString(p.Value)
			if err != nil || value.IsNegative() || value.Exponent() < -50 || value.Exponent() > 50 || value.Coefficient().BitLen() > 256 {
				return nil, fmt.Errorf("%w：价格数值无效", ErrPriceSelection)
			}
			p.Value = value.String()
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Key < points[j].Key })
		total += len(points)
		if total > 10000 || len(result) >= 500 {
			return nil, fmt.Errorf("%w：价格项目数量超限", ErrPriceSelection)
		}
		result[observation.Name] = points
	}
	return result, nil
}

// SyncModelPriceSelection 保存单个分组的模型选择，首次建立基准不通知，保留已监控模型的原基准。
func (s *Store) SyncModelPriceSelection(ctx context.Context, targetID, groupKey, groupName string, names []string, observations []ModelPriceObservation, now time.Time) error {
	if groupKey == "" || len(groupKey) > 200 || len(names) > 500 {
		return fmt.Errorf("%w：分组或数量无效", ErrPriceSelection)
	}
	values, err := normalizeModelPrices(observations)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || len(name) > 800 || selected[name] {
			return fmt.Errorf("%w：模型选择重复或无效", ErrPriceSelection)
		}
		selected[name] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	items, err := listModelPrices(ctx, tx, targetID)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	groups := make(map[string]bool)
	count := len(selected)
	for _, item := range items {
		if item.GroupKey == groupKey {
			existing[item.ModelName] = true
		} else {
			count++
			groups[item.GroupKey] = true
		}
	}
	if len(names) > 0 {
		groups[groupKey] = true
	}
	if count > 500 || len(groups) > 20 {
		return fmt.Errorf("%w：每个渠道最多监控 20 个分组、500 个模型", ErrPriceSelection)
	}
	for name := range selected {
		if !existing[name] && len(values[name]) == 0 {
			return fmt.Errorf("%w：模型已不存在或未公开固定价格，请重新检测", ErrPriceSelection)
		}
	}
	for name := range existing {
		if !selected[name] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM model_price_monitors WHERE target_id=? AND group_key=? AND model_name=?`, targetID, groupKey, name); err != nil {
				return err
			}
		}
	}
	for name := range selected {
		if existing[name] {
			continue
		}
		encoded, _ := json.Marshal(values[name])
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_price_monitors(target_id,group_key,group_name,model_name,current_json,last_checked_at) VALUES(?,?,?,?,?,?)`, targetID, groupKey, groupName, name, string(encoded), formatTime(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func equalPricePoints(a, b []PricePoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value || a[i].Unit != b[i].Unit {
			return false
		}
	}
	return true
}

func priceChangeSummary(name string, old, current []PricePoint) string {
	previous := map[string]PricePoint{}
	for _, p := range old {
		previous[p.Key] = p
	}
	parts := []string{}
	for _, p := range current {
		before, ok := previous[p.Key]
		delete(previous, p.Key)
		if !ok {
			parts = append(parts, p.Label+" 新增 "+p.Value+" "+p.Unit)
		} else if before.Value != p.Value || before.Unit != p.Unit {
			parts = append(parts, p.Label+" "+before.Value+" "+before.Unit+" → "+p.Value+" "+p.Unit)
		}
	}
	if len(previous) > 0 {
		parts = append(parts, fmt.Sprintf("移除 %d 项计费价格", len(previous)))
	}
	if len(parts) > 3 {
		parts = append(parts[:3], "另有计费项目变化")
	}
	return name + "：" + strings.Join(parts, "，")
}

// ApplyModelPriceCheck 在同一事务内更新价格基准及变化事件，重复采样不产生重复通知。
func (s *Store) ApplyModelPriceCheck(ctx context.Context, targetID, groupKey, groupName string, observations []ModelPriceObservation, now time.Time) (*Alert, error) {
	values, err := normalizeModelPrices(observations)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items, err := listModelPrices(ctx, tx, targetID)
	if err != nil {
		return nil, err
	}
	changes := []string{}
	stamp := formatTime(now)
	for _, item := range items {
		if item.GroupKey != groupKey {
			continue
		}
		points, found := values[item.ModelName]
		if len(points) == 0 {
			message := "模型本次未找到，保留上次价格"
			if found {
				message = "模型本次未公开固定价格，保留上次价格"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE model_price_monitors SET missing=?,last_error=?,last_checked_at=? WHERE target_id=? AND group_key=? AND model_name=?`, !found, message, stamp, targetID, groupKey, item.ModelName); err != nil {
				return nil, err
			}
			continue
		}
		changed := len(item.Prices) > 0 && !equalPricePoints(item.Prices, points)
		previous := item.Previous
		changedAt := item.ChangedAt
		if changed {
			previous = item.Prices
			changedAt = stamp
			changes = append(changes, priceChangeSummary(item.ModelName, item.Prices, points))
		}
		currentJSON, _ := json.Marshal(points)
		previousJSON, _ := json.Marshal(previous)
		if _, err := tx.ExecContext(ctx, `UPDATE model_price_monitors SET group_name=?,current_json=?,previous_json=?,missing=0,last_error='',last_checked_at=?,changed_at=? WHERE target_id=? AND group_key=? AND model_name=?`, groupName, string(currentJSON), string(previousJSON), stamp, changedAt, targetID, groupKey, item.ModelName); err != nil {
			return nil, err
		}
	}
	var alert *Alert
	if len(changes) > 0 {
		id, err := identity.NewID("alert")
		if err != nil {
			return nil, err
		}
		count := len(changes)
		if count > 5 {
			changes = append(changes[:5], fmt.Sprintf("另有 %d 个模型变化", count-5))
		}
		message := groupName + " 模型价格变化：" + strings.Join(changes, "；")
		// 详情保存在独立价格记录中，通知正文限制长度以适配各推送通道。
		runes := []rune(message)
		if len(runes) > 1800 {
			message = string(runes[:1800]) + "…"
		}
		alert = &Alert{ID: id, TargetID: targetID, Type: "price_changed", State: "resolved", Title: "模型价格已变更", Message: message, CurrentValue: fmt.Sprint(count), Unit: "个模型", OpenedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO alerts(id,target_id,type,state,title,message,current_value,unit,opened_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, targetID, alert.Type, alert.State, alert.Title, message, alert.CurrentValue, alert.Unit, stamp); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return alert, nil
}

// MarkModelPriceFailure 仅标记价格监控失败，不覆盖余额状态、倍率基准或上次有效价格。
func (s *Store) MarkModelPriceFailure(ctx context.Context, targetID, groupKey, message string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE model_price_monitors SET last_error=?,last_checked_at=? WHERE target_id=? AND group_key=?`, message, formatTime(now), targetID, groupKey)
	return err
}
