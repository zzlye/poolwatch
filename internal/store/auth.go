package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// HasAdmin 判断首次初始化是否已经完成。
func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return false, fmt.Errorf("读取管理员状态失败: %w", err)
	}
	return count > 0, nil
}

// CreateAdmin 创建系统中唯一的管理员。
func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admins(id, username, password_hash, created_at, password_set_at) VALUES (1, ?, ?, ?, ?)`,
		username, passwordHash, formatTime(now), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("创建管理员失败: %w", err)
	}
	return nil
}

// AdminByUsername 按登录名读取管理员。
func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	var admin Admin
	var enabled int
	var createdAt, passwordSetAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, totp_enabled, totp_secret_enc, created_at, password_set_at FROM admins WHERE username = ?`,
		username,
	).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &enabled, &admin.TOTPSecretEnc, &createdAt, &passwordSetAt)
	if err != nil {
		return Admin{}, err
	}
	admin.TOTPEnabled = enabled != 0
	admin.CreatedAt = parseTime(createdAt)
	admin.PasswordSetAt = parseTime(passwordSetAt)
	return admin, nil
}

// AdminByID 按主键读取管理员。
func (s *Store) AdminByID(ctx context.Context, id int64) (Admin, error) {
	var username string
	err := s.db.QueryRowContext(ctx, `SELECT username FROM admins WHERE id = ?`, id).Scan(&username)
	if err != nil {
		return Admin{}, err
	}
	return s.AdminByUsername(ctx, username)
}

// SetAdminTOTP 原子更新动态验证码密钥与恢复码。
func (s *Store) SetAdminTOTP(ctx context.Context, secretEnc string, enabled bool, codeHashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE admins SET totp_secret_enc = ?, totp_enabled = ? WHERE id = 1`, secretEnc, boolToInt(enabled)); err != nil {
		return fmt.Errorf("更新动态验证码失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE admin_id = 1`); err != nil {
		return err
	}
	for _, hash := range codeHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes(admin_id, code_hash) VALUES (1, ?)`, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ConsumeRecoveryCode 一次性消费匹配的恢复码。
func (s *Store) ConsumeRecoveryCode(ctx context.Context, hash string, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE recovery_codes SET used_at = ? WHERE admin_id = 1 AND code_hash = ? AND used_at IS NULL`,
		formatTime(now), hash,
	)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

// CreateSession 保存登录会话的哈希摘要。
func (s *Store) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token_hash, admin_id, csrf_token, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		session.TokenHash, session.AdminID, session.CSRFToken, formatTime(session.ExpiresAt), formatTime(session.CreatedAt),
	)
	return err
}

// SessionByHash 读取仍在有效期内的会话。
func (s *Store) SessionByHash(ctx context.Context, tokenHash string, now time.Time) (Session, error) {
	var session Session
	var expiresAt, createdAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, admin_id, csrf_token, expires_at, created_at FROM sessions WHERE token_hash = ?`,
		tokenHash,
	).Scan(&session.TokenHash, &session.AdminID, &session.CSRFToken, &expiresAt, &createdAt)
	if err != nil {
		return Session{}, err
	}
	session.ExpiresAt = parseTime(expiresAt)
	session.CreatedAt = parseTime(createdAt)
	if !session.ExpiresAt.After(now) {
		_ = s.DeleteSession(ctx, tokenHash)
		return Session{}, sql.ErrNoRows
	}
	return session, nil
}

// DeleteSession 注销一个会话。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteAdminSessions 注销管理员的全部设备。
func (s *Store) DeleteAdminSessions(ctx context.Context, adminID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ?`, adminID)
	return err
}

// CleanupSessions 清除所有过期会话。
func (s *Store) CleanupSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(now))
	return err
}

// GetSetting 读取一个系统设置，不存在时返回空字符串。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetSetting 新增或覆盖一个系统设置。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
