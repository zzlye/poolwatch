package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

func TestSetupLoginAndTOTPRecovery(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("创建保险箱失败: %v", err)
	}
	service := NewService(database, vault, "setup-secret", time.Hour)
	fixedNow := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedNow }
	ctx := context.Background()

	if _, err := service.Setup(ctx, "wrong", "admin", "long-password-123"); !errors.Is(err, ErrSetupToken) {
		t.Fatalf("错误初始化口令未被拒绝: %v", err)
	}
	session, err := service.Setup(ctx, "setup-secret", "admin", "long-password-123")
	if err != nil || session.Token == "" {
		t.Fatalf("初始化失败: %#v, %v", session, err)
	}
	if _, _, err := service.Authenticate(ctx, session.Token); err != nil {
		t.Fatalf("首个会话验证失败: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "wrong-password", ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("错误密码未被拒绝: %v", err)
	}

	admin, err := database.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("读取管理员失败: %v", err)
	}
	setup, err := service.StartTOTP(ctx, admin)
	if err != nil || len(setup.RecoveryCodes) != 10 || SanitizeOTPAuthURL(setup.OTPAuthURL) == "" {
		t.Fatalf("生成动态验证码资料失败: %#v, %v", setup, err)
	}
	code, err := totp.GenerateCode(setup.Secret, fixedNow)
	if err != nil {
		t.Fatalf("生成测试验证码失败: %v", err)
	}
	recoveryCodes, err := service.ConfirmTOTP(ctx, code)
	if err != nil || len(recoveryCodes) != 10 {
		t.Fatalf("启用动态验证码失败: %v", err)
	}
	admin, _ = database.AdminByUsername(ctx, "admin")
	if _, err := service.StartTOTP(ctx, admin); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("已启用时不应覆盖动态验证码: %v", err)
	}
	if _, err := service.ConfirmTOTP(ctx, code); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("已启用时不应重复确认动态验证码: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "long-password-123", "000000"); !errors.Is(err, ErrSecondFactor) {
		t.Fatalf("错误动态验证码未被拒绝: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "long-password-123", recoveryCodes[0]); err != nil {
		t.Fatalf("恢复码登录失败: %v", err)
	}
	if _, err := service.Login(ctx, "admin", "long-password-123", recoveryCodes[0]); !errors.Is(err, ErrSecondFactor) {
		t.Fatalf("已使用恢复码不应再次生效: %v", err)
	}
}

func TestGenerateRecoveryCodesAreUnique(t *testing.T) {
	codes, err := generateRecoveryCodes(100)
	if err != nil {
		t.Fatalf("生成恢复码失败: %v", err)
	}
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if seen[code] || !looksLikeRecoveryCode(code) {
			t.Fatalf("恢复码重复或格式错误: %q", code)
		}
		seen[code] = true
	}
}
