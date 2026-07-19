package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const targetColumns = `id, name, kind, base_url, enabled, poll_interval_seconds, recharge_url,
	config_json, credentials_enc, status, failure_count, last_error, last_checked_at, created_at, updated_at`

// CreateTarget 新增一个渠道，敏感凭据必须在调用前完成加密。
func (s *Store) CreateTarget(ctx context.Context, target Target) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO targets(
		id, name, kind, base_url, enabled, poll_interval_seconds, recharge_url, config_json,
		credentials_enc, status, failure_count, last_error, last_checked_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		target.ID, target.Name, target.Kind, target.BaseURL, boolToInt(target.Enabled),
		target.PollIntervalSeconds, target.RechargeURL, target.ConfigJSON, target.CredentialsEnc,
		target.Status, target.FailureCount, target.LastError, nullableTime(target.LastCheckedAt),
		formatTime(target.CreatedAt), formatTime(target.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("创建渠道失败: %w", err)
	}
	return nil
}

// UpdateTarget 更新渠道配置，检测状态字段保持由调度器管理。
func (s *Store) UpdateTarget(ctx context.Context, target Target) error {
	result, err := s.db.ExecContext(ctx, `UPDATE targets SET
		name = ?, kind = ?, base_url = ?, enabled = ?, poll_interval_seconds = ?, recharge_url = ?,
		config_json = ?, credentials_enc = ?, updated_at = ?
		WHERE id = ?`,
		target.Name, target.Kind, target.BaseURL, boolToInt(target.Enabled), target.PollIntervalSeconds,
		target.RechargeURL, target.ConfigJSON, target.CredentialsEnc, formatTime(target.UpdatedAt), target.ID,
	)
	if err != nil {
		return fmt.Errorf("更新渠道失败: %w", err)
	}
	return requireAffected(result, "渠道不存在")
}

// TargetByID 读取单个渠道。
func (s *Store) TargetByID(ctx context.Context, id string) (Target, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+targetColumns+` FROM targets WHERE id = ?`, id)
	return scanTarget(row)
}

// ListTargets 按创建时间倒序返回全部渠道。
func (s *Store) ListTargets(ctx context.Context) ([]Target, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+targetColumns+` FROM targets ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("读取渠道列表失败: %w", err)
	}
	defer rows.Close()

	targets := make([]Target, 0)
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// DueTargets 返回已经到达检测时间的启用渠道。
func (s *Store) DueTargets(ctx context.Context, now time.Time) ([]Target, error) {
	targets, err := s.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	due := make([]Target, 0, len(targets))
	for _, target := range targets {
		if !target.Enabled {
			continue
		}
		if target.LastCheckedAt.IsZero() || !target.LastCheckedAt.Add(time.Duration(target.PollIntervalSeconds)*time.Second).After(now) {
			due = append(due, target)
		}
	}
	return due, nil
}

// UpdateTargetCheck 保存一次检测的最终状态与连续失败次数。
func (s *Store) UpdateTargetCheck(ctx context.Context, id, status string, failureCount int, lastError string, checkedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE targets SET
		status = ?, failure_count = ?, last_error = ?, last_checked_at = ?, updated_at = ? WHERE id = ?`,
		status, failureCount, lastError, formatTime(checkedAt), formatTime(checkedAt), id,
	)
	if err != nil {
		return fmt.Errorf("保存渠道检测状态失败: %w", err)
	}
	return requireAffected(result, "渠道不存在")
}

// UpdateTargetCredentials 保存适配器续期后的加密凭据。
func (s *Store) UpdateTargetCredentials(ctx context.Context, id, credentialsEncrypted string, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE targets SET credentials_enc = ?, updated_at = ? WHERE id = ?`,
		credentialsEncrypted, formatTime(updatedAt), id)
	if err != nil {
		return fmt.Errorf("保存续期凭据失败: %w", err)
	}
	return requireAffected(result, "渠道不存在")
}

// DeleteTarget 删除渠道以及由外键关联的历史数据。
func (s *Store) DeleteTarget(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除渠道失败: %w", err)
	}
	return requireAffected(result, "渠道不存在")
}

// ResetTargetMonitoring 在渠道类型或地址变化后清除不再可比的检测数据。
func (s *Store) ResetTargetMonitoring(ctx context.Context, id string, updatedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM snapshots WHERE target_id = ?`,
		`DELETE FROM alerts WHERE target_id = ?`,
		`DELETE FROM chat_accounts WHERE target_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("重置渠道历史失败: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE targets SET status = 'unknown', failure_count = 0,
		last_error = '', last_checked_at = NULL, updated_at = ? WHERE id = ?`, formatTime(updatedAt), id)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "渠道不存在"); err != nil {
		return err
	}
	return tx.Commit()
}

// CountTargetsByStatus 汇总渠道状态数量。
func (s *Store) CountTargetsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM targets GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTarget(row scanner) (Target, error) {
	var target Target
	var enabled int
	var lastCheckedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&target.ID, &target.Name, &target.Kind, &target.BaseURL, &enabled,
		&target.PollIntervalSeconds, &target.RechargeURL, &target.ConfigJSON,
		&target.CredentialsEnc, &target.Status, &target.FailureCount, &target.LastError,
		&lastCheckedAt, &createdAt, &updatedAt,
	); err != nil {
		return Target{}, err
	}
	target.Enabled = enabled != 0
	target.HasCredentials = target.CredentialsEnc != ""
	if lastCheckedAt.Valid {
		target.LastCheckedAt = parseTime(lastCheckedAt.String)
	}
	target.CreatedAt = parseTime(createdAt)
	target.UpdatedAt = parseTime(updatedAt)
	return target, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func requireAffected(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%s", message)
	}
	return nil
}
