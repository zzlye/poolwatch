package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/identity"
)

const groupMultiplierColumns = `target_id, group_key, group_name, description, current_multiplier,
	previous_multiplier, missing, last_error, last_checked_at, last_changed_at, created_at, updated_at`

// ListGroupMultiplierMonitors 返回一个渠道已经选择监控的全部分组。
func (s *Store) ListGroupMultiplierMonitors(ctx context.Context, targetID string) ([]GroupMultiplierMonitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupMultiplierColumns+`
		FROM group_multiplier_monitors WHERE target_id = ? ORDER BY group_name, group_key`, targetID)
	if err != nil {
		return nil, fmt.Errorf("读取倍率监控失败: %w", err)
	}
	defer rows.Close()
	items := make([]GroupMultiplierMonitor, 0)
	for rows.Next() {
		item, err := scanGroupMultiplierMonitor(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// HasGroupMultiplierMonitors 判断渠道是否已经选择至少一个倍率分组。
func (s *Store) HasGroupMultiplierMonitors(ctx context.Context, targetID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM group_multiplier_monitors WHERE target_id = ? LIMIT 1
	)`, targetID).Scan(&exists)
	return exists == 1, err
}

// SyncGroupMultiplierSelection 同步用户选择；新分组以本次检测值建立基准，既有分组保留原基准。
func (s *Store) SyncGroupMultiplierSelection(ctx context.Context, targetID string, selectedKeys []string, observations []GroupMultiplierObservation, now time.Time) error {
	if strings.TrimSpace(targetID) == "" {
		return errors.New("渠道标识不能为空")
	}
	if len(selectedKeys) > 500 {
		return errors.New("单个渠道最多监控 500 个分组")
	}
	selected := make(map[string]struct{}, len(selectedKeys))
	for _, key := range selectedKeys {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > 200 {
			return errors.New("分组标识无效")
		}
		if _, exists := selected[key]; exists {
			return errors.New("分组选择存在重复项")
		}
		selected[key] = struct{}{}
	}
	normalized, err := normalizeGroupMultiplierObservations(observations)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := listGroupMultiplierMonitorsTx(ctx, tx, targetID)
	if err != nil {
		return err
	}
	for key := range selected {
		if _, detected := normalized[key]; !detected {
			if _, alreadyMonitored := existing[key]; !alreadyMonitored {
				return fmt.Errorf("分组 %s 已不在本次检测结果中，请重新检测", key)
			}
		}
	}
	for key := range existing {
		if _, keep := selected[key]; keep {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM group_multiplier_monitors WHERE target_id = ? AND group_key = ?`, targetID, key); err != nil {
			return fmt.Errorf("取消倍率监控失败: %w", err)
		}
	}
	nowText := formatTime(now)
	for key := range selected {
		observation, detected := normalized[key]
		if current, exists := existing[key]; exists {
			if !detected {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE group_multiplier_monitors SET
				group_name = ?, description = ?, missing = 0, last_error = '', updated_at = ?
				WHERE target_id = ? AND group_key = ?`, observation.GroupName, observation.Description,
				nowText, targetID, current.GroupKey); err != nil {
				return fmt.Errorf("更新倍率监控分组失败: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_multiplier_monitors(
			target_id, group_key, group_name, description, current_multiplier, previous_multiplier,
			missing, last_error, last_checked_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, '', 0, '', ?, NULL, ?, ?)`, targetID, key, observation.GroupName,
			observation.Description, observation.Multiplier, nowText, nowText, nowText); err != nil {
			return fmt.Errorf("新增倍率监控分组失败: %w", err)
		}
	}
	return tx.Commit()
}

// ApplyGroupMultiplierCheck 更新已监控分组，并返回本轮真实发生的倍率变化。
func (s *Store) ApplyGroupMultiplierCheck(ctx context.Context, targetID string, observations []GroupMultiplierObservation, alertType string, now time.Time) ([]GroupMultiplierChange, *Alert, error) {
	normalized, err := normalizeGroupMultiplierObservations(observations)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	existing, err := listGroupMultiplierMonitorsTx(ctx, tx, targetID)
	if err != nil {
		return nil, nil, err
	}
	changes := make([]GroupMultiplierChange, 0)
	nowText := formatTime(now)
	for key, current := range existing {
		observation, found := normalized[key]
		if !found {
			if _, err := tx.ExecContext(ctx, `UPDATE group_multiplier_monitors SET
				missing = 1, last_error = '', last_checked_at = ?, updated_at = ?
				WHERE target_id = ? AND group_key = ?`, nowText, nowText, targetID, key); err != nil {
				return nil, nil, fmt.Errorf("记录未找到分组失败: %w", err)
			}
			continue
		}
		// 手动更换登录账号后当前基准会被清空，下一次成功读取只建立新账号基准。
		changed := current.CurrentMultiplier != "" && current.CurrentMultiplier != observation.Multiplier
		previous := current.PreviousMultiplier
		var changedAt any
		if changed {
			previous = current.CurrentMultiplier
			changedAt = nowText
			changes = append(changes, GroupMultiplierChange{
				GroupKey: key, GroupName: observation.GroupName,
				PreviousMultiplier: current.CurrentMultiplier, CurrentMultiplier: observation.Multiplier,
			})
		} else {
			changedAt = nullableGroupMultiplierTime(current.LastChangedAt)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE group_multiplier_monitors SET
			group_name = ?, description = ?, current_multiplier = ?, previous_multiplier = ?,
			missing = 0, last_error = '', last_checked_at = ?, last_changed_at = ?, updated_at = ?
			WHERE target_id = ? AND group_key = ?`, observation.GroupName, observation.Description,
			observation.Multiplier, previous, nowText, changedAt, nowText, targetID, key); err != nil {
			return nil, nil, fmt.Errorf("保存倍率检测结果失败: %w", err)
		}
	}
	var changeAlert *Alert
	if len(changes) > 0 {
		sort.Slice(changes, func(left, right int) bool {
			if changes[left].GroupName == changes[right].GroupName {
				return changes[left].GroupKey < changes[right].GroupKey
			}
			return changes[left].GroupName < changes[right].GroupName
		})
		alertID, err := identity.NewID("alert")
		if err != nil {
			return nil, nil, err
		}
		displayCount := len(changes)
		if displayCount > 10 {
			displayCount = 10
		}
		parts := make([]string, 0, displayCount+1)
		for _, change := range changes[:displayCount] {
			parts = append(parts, fmt.Sprintf("%s：%s× → %s×", change.GroupName, change.PreviousMultiplier, change.CurrentMultiplier))
		}
		if len(changes) > displayCount {
			parts = append(parts, fmt.Sprintf("另有 %d 个分组", len(changes)-displayCount))
		}
		message := "检测到分组倍率变化：" + strings.Join(parts, "；") + "。"
		alert := Alert{
			ID: alertID, TargetID: targetID, Type: alertType, State: "resolved",
			Title: "分组倍率已变更", Message: message, CurrentValue: fmt.Sprintf("%d", len(changes)),
			Unit: "个分组", OpenedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO alerts(id, target_id, type, metric_key, state, title, message,
			current_value, threshold_value, unit, opened_at, recovered_at, last_notified_at)
			VALUES (?, ?, ?, '', ?, ?, ?, ?, '', ?, ?, NULL, NULL)`, alert.ID, alert.TargetID, alert.Type,
			alert.State, alert.Title, alert.Message, alert.CurrentValue, alert.Unit, formatTime(alert.OpenedAt)); err != nil {
			return nil, nil, fmt.Errorf("保存倍率变化事件失败: %w", err)
		}
		changeAlert = &alert
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return changes, changeAlert, nil
}

// MarkGroupMultiplierCheckFailure 保存倍率检测的脱敏错误，不影响渠道本身的健康状态。
func (s *Store) MarkGroupMultiplierCheckFailure(ctx context.Context, targetID, message string, now time.Time) error {
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE group_multiplier_monitors SET
		last_error = ?, last_checked_at = ?, updated_at = ? WHERE target_id = ?`,
		message, formatTime(now), formatTime(now), targetID)
	return err
}

func listGroupMultiplierMonitorsTx(ctx context.Context, tx *sql.Tx, targetID string) (map[string]GroupMultiplierMonitor, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+groupMultiplierColumns+`
		FROM group_multiplier_monitors WHERE target_id = ?`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make(map[string]GroupMultiplierMonitor)
	for rows.Next() {
		item, err := scanGroupMultiplierMonitor(rows)
		if err != nil {
			return nil, err
		}
		items[item.GroupKey] = item
	}
	return items, rows.Err()
}

func normalizeGroupMultiplierObservations(observations []GroupMultiplierObservation) (map[string]GroupMultiplierObservation, error) {
	result := make(map[string]GroupMultiplierObservation, len(observations))
	for _, observation := range observations {
		observation.GroupKey = strings.TrimSpace(observation.GroupKey)
		observation.GroupName = strings.TrimSpace(observation.GroupName)
		observation.Description = strings.TrimSpace(observation.Description)
		if observation.GroupKey == "" || len(observation.GroupKey) > 200 || observation.GroupName == "" || len(observation.GroupName) > 200 {
			return nil, errors.New("检测到的分组信息无效")
		}
		if len(observation.Description) > 500 {
			observation.Description = observation.Description[:500]
		}
		rawMultiplier := strings.TrimSpace(observation.Multiplier)
		if len(rawMultiplier) > 100 {
			return nil, errors.New("检测到的分组倍率长度无效")
		}
		value, err := decimal.NewFromString(rawMultiplier)
		// 导出存储入口也必须在 String 展开前限制指数和有效位，不能只依赖适配器校验。
		if err != nil || value.IsNegative() || value.Exponent() < -50 || value.Exponent() > 50 || value.Coefficient().BitLen() > 128 {
			return nil, errors.New("检测到的分组倍率无效")
		}
		observation.Multiplier = value.String()
		if _, exists := result[observation.GroupKey]; exists {
			return nil, errors.New("检测到重复的分组标识")
		}
		result[observation.GroupKey] = observation
	}
	return result, nil
}

func scanGroupMultiplierMonitor(row scanner) (GroupMultiplierMonitor, error) {
	var item GroupMultiplierMonitor
	var missing int
	var checkedAt, changedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&item.TargetID, &item.GroupKey, &item.GroupName, &item.Description,
		&item.CurrentMultiplier, &item.PreviousMultiplier, &missing, &item.LastError,
		&checkedAt, &changedAt, &createdAt, &updatedAt); err != nil {
		return GroupMultiplierMonitor{}, err
	}
	item.Missing = missing == 1
	if checkedAt.Valid {
		item.LastCheckedAt = parseTime(checkedAt.String)
	}
	if changedAt.Valid {
		item.LastChangedAt = parseTime(changedAt.String)
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func nullableGroupMultiplierTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}
