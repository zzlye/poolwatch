package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"poolwatch/internal/identity"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

const pendingTOTPSetting = "totp_pending_enc"

var (
	ErrUnauthorized       = errors.New("账号、密码或验证码不正确")
	ErrAlreadyInitialized = errors.New("系统已经完成初始化")
	ErrSetupToken         = errors.New("初始化口令不正确")
	ErrSecondFactor       = errors.New("请输入有效的动态验证码或恢复码")
	ErrTOTPAlreadyEnabled = errors.New("动态验证码已经启用，请先验证并关闭现有配置")
)

// Service 负责唯一管理员、登录会话和动态验证码生命周期。
type Service struct {
	store           *store.Store
	vault           *secure.Vault
	setupToken      string
	sessionLifetime time.Duration
	now             func() time.Time
}

// SessionResult 包含只会返回给当前浏览器的原始会话令牌。
type SessionResult struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

// TOTPSetup 是启用动态验证码前展示给管理员的资料。
type TOTPSetup struct {
	Secret        string   `json:"secret"`
	OTPAuthURL    string   `json:"otpauthUrl"`
	RecoveryCodes []string `json:"recoveryCodes"`
}

type pendingTOTP struct {
	Secret        string   `json:"secret"`
	OTPAuthURL    string   `json:"otpauth_url"`
	RecoveryCodes []string `json:"recovery_codes"`
}

// NewService 创建认证服务。
func NewService(database *store.Store, vault *secure.Vault, setupToken string, sessionLifetime time.Duration) *Service {
	return &Service{
		store:           database,
		vault:           vault,
		setupToken:      strings.TrimSpace(setupToken),
		sessionLifetime: sessionLifetime,
		now:             func() time.Time { return time.Now().UTC() },
	}
}

// Initialized 判断唯一管理员是否已经创建。
func (s *Service) Initialized(ctx context.Context) (bool, error) {
	return s.store.HasAdmin(ctx)
}

// Setup 使用部署口令创建唯一管理员并直接签发首个会话。
func (s *Service) Setup(ctx context.Context, providedToken, username, password string) (SessionResult, error) {
	initialized, err := s.store.HasAdmin(ctx)
	if err != nil {
		return SessionResult{}, err
	}
	if initialized {
		return SessionResult{}, ErrAlreadyInitialized
	}
	if s.setupToken == "" || !constantTimeEqual(s.setupToken, strings.TrimSpace(providedToken)) {
		return SessionResult{}, ErrSetupToken
	}
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 {
		return SessionResult{}, errors.New("管理员名称需要 3 至 64 个字符")
	}
	passwordHash, err := secure.HashPassword(password)
	if err != nil {
		return SessionResult{}, err
	}
	now := s.now()
	if err := s.store.CreateAdmin(ctx, username, passwordHash, now); err != nil {
		return SessionResult{}, err
	}
	return s.createSession(ctx, 1, now)
}

// Login 验证密码和可选的第二因素后创建会话。
func (s *Service) Login(ctx context.Context, username, password, secondFactor string) (SessionResult, error) {
	admin, err := s.store.AdminByUsername(ctx, strings.TrimSpace(username))
	if err != nil || !secure.VerifyPassword(admin.PasswordHash, password) {
		return SessionResult{}, ErrUnauthorized
	}
	if admin.TOTPEnabled {
		valid, err := s.verifySecondFactor(ctx, admin, secondFactor, true)
		if err != nil || !valid {
			return SessionResult{}, ErrSecondFactor
		}
	}
	return s.createSession(ctx, admin.ID, s.now())
}

// Authenticate 校验浏览器会话并返回管理员资料。
func (s *Service) Authenticate(ctx context.Context, token string) (store.Admin, store.Session, error) {
	if token == "" {
		return store.Admin{}, store.Session{}, ErrUnauthorized
	}
	session, err := s.store.SessionByHash(ctx, identity.HashToken(token), s.now())
	if err != nil {
		return store.Admin{}, store.Session{}, ErrUnauthorized
	}
	admin, err := s.store.AdminByID(ctx, session.AdminID)
	if err != nil {
		return store.Admin{}, store.Session{}, ErrUnauthorized
	}
	return admin, session, nil
}

// Logout 删除当前浏览器会话。
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, identity.HashToken(token))
}

// StartTOTP 生成待确认的密钥和一次性恢复码。
func (s *Service) StartTOTP(ctx context.Context, admin store.Admin) (TOTPSetup, error) {
	current, err := s.store.AdminByID(ctx, admin.ID)
	if err != nil {
		return TOTPSetup{}, err
	}
	if current.TOTPEnabled {
		return TOTPSetup{}, ErrTOTPAlreadyEnabled
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "号池监控",
		AccountName: admin.Username,
		Period:      30,
		SecretSize:  20,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("生成动态验证码密钥失败: %w", err)
	}
	codes, err := generateRecoveryCodes(10)
	if err != nil {
		return TOTPSetup{}, err
	}
	pending := pendingTOTP{Secret: key.Secret(), OTPAuthURL: key.URL(), RecoveryCodes: codes}
	encoded, err := json.Marshal(pending)
	if err != nil {
		return TOTPSetup{}, err
	}
	encrypted, err := s.vault.Encrypt(encoded)
	if err != nil {
		return TOTPSetup{}, err
	}
	if err := s.store.SetSetting(ctx, pendingTOTPSetting, encrypted); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{Secret: pending.Secret, OTPAuthURL: pending.OTPAuthURL, RecoveryCodes: pending.RecoveryCodes}, nil
}

// ConfirmTOTP 验证首个动态验证码后正式启用第二因素。
func (s *Service) ConfirmTOTP(ctx context.Context, code string) ([]string, error) {
	admin, err := s.store.AdminByID(ctx, 1)
	if err != nil {
		return nil, err
	}
	if admin.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	pending, err := s.loadPendingTOTP(ctx)
	if err != nil {
		return nil, err
	}
	if !validateTOTP(strings.TrimSpace(code), pending.Secret, s.now()) {
		return nil, ErrSecondFactor
	}
	secretEncrypted, err := s.vault.Encrypt([]byte(pending.Secret))
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(pending.RecoveryCodes))
	for _, recoveryCode := range pending.RecoveryCodes {
		hashes = append(hashes, recoveryCodeHash(recoveryCode))
	}
	if err := s.store.SetAdminTOTP(ctx, secretEncrypted, true, hashes); err != nil {
		return nil, err
	}
	if err := s.store.SetSetting(ctx, pendingTOTPSetting, ""); err != nil {
		return nil, err
	}
	return append([]string(nil), pending.RecoveryCodes...), nil
}

// DisableTOTP 校验当前第二因素后关闭动态验证码并清除恢复码。
func (s *Service) DisableTOTP(ctx context.Context, admin store.Admin, code string) error {
	if !admin.TOTPEnabled {
		return nil
	}
	valid, err := s.verifySecondFactor(ctx, admin, code, true)
	if err != nil || !valid {
		return ErrSecondFactor
	}
	return s.store.SetAdminTOTP(ctx, "", false, nil)
}

// ChangePassword 校验原密码和第二因素后替换密码并注销其他会话。
func (s *Service) ChangePassword(ctx context.Context, admin store.Admin, currentPassword, newPassword, secondFactor string) error {
	if !secure.VerifyPassword(admin.PasswordHash, currentPassword) {
		return ErrUnauthorized
	}
	if admin.TOTPEnabled {
		valid, err := s.verifySecondFactor(ctx, admin, secondFactor, true)
		if err != nil || !valid {
			return ErrSecondFactor
		}
	}
	hash, err := secure.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.store.UpdateAdminPassword(ctx, admin.ID, hash, s.now())
}

func (s *Service) createSession(ctx context.Context, adminID int64, now time.Time) (SessionResult, error) {
	token, err := identity.RandomToken(32)
	if err != nil {
		return SessionResult{}, err
	}
	csrfToken, err := identity.RandomToken(16)
	if err != nil {
		return SessionResult{}, err
	}
	expiresAt := now.Add(s.sessionLifetime)
	if err := s.store.CreateSession(ctx, store.Session{
		TokenHash: identity.HashToken(token), AdminID: adminID, CSRFToken: csrfToken,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return SessionResult{}, err
	}
	return SessionResult{Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (s *Service) verifySecondFactor(ctx context.Context, admin store.Admin, code string, consumeRecovery bool) (bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return false, nil
	}
	secret, err := s.vault.Decrypt(admin.TOTPSecretEnc)
	if err != nil {
		return false, err
	}
	if validateTOTP(code, string(secret), s.now()) {
		return true, nil
	}
	if !looksLikeRecoveryCode(code) {
		return false, nil
	}
	if !consumeRecovery {
		return true, nil
	}
	return s.store.ConsumeRecoveryCode(ctx, recoveryCodeHash(code), s.now())
}

func (s *Service) loadPendingTOTP(ctx context.Context) (pendingTOTP, error) {
	encrypted, err := s.store.GetSetting(ctx, pendingTOTPSetting)
	if err != nil {
		return pendingTOTP{}, err
	}
	if encrypted == "" {
		return pendingTOTP{}, errors.New("请先开始配置动态验证码")
	}
	decoded, err := s.vault.Decrypt(encrypted)
	if err != nil {
		return pendingTOTP{}, err
	}
	var pending pendingTOTP
	if err := json.Unmarshal(decoded, &pending); err != nil {
		return pendingTOTP{}, errors.New("待确认的动态验证码配置损坏")
	}
	return pending, nil
}

func validateTOTP(code, secret string, now time.Time) bool {
	valid, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && valid
}

func generateRecoveryCodes(count int) ([]string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codes := make([]string, 0, count)
	for len(codes) < count {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		encoded := make([]byte, len(random))
		for index := range random {
			encoded[index] = alphabet[int(random[index])%len(alphabet)]
		}
		code := string(encoded[:4]) + "-" + string(encoded[4:8]) + "-" + string(encoded[8:12])
		codes = append(codes, code)
	}
	return codes, nil
}

func recoveryCodeHash(code string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	return identity.HashToken(normalized)
}

func looksLikeRecoveryCode(code string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(code), "-", "")
	return len(normalized) == 12
}

func constantTimeEqual(first, second string) bool {
	firstHash := sha256.Sum256([]byte(first))
	secondHash := sha256.Sum256([]byte(second))
	return subtle.ConstantTimeCompare(firstHash[:], secondHash[:]) == 1
}

// SanitizeOTPAuthURL 确保返回前的二维码地址仍是标准 otpauth 地址。
func SanitizeOTPAuthURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "otpauth" {
		return ""
	}
	return parsed.String()
}
