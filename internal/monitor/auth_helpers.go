package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
)

func currentTOTPCode(credential Credential) (string, error) {
	if code := strings.TrimSpace(credential.TOTPCode); code != "" {
		return code, nil
	}
	secret := strings.TrimSpace(credential.TOTPSecret)
	if secret == "" {
		return "", checkError(ErrorClassAuth, "生成两步验证码", "渠道需要两步验证码", 0, nil)
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		return "", checkError(ErrorClassAuth, "生成两步验证码", "两步验证密钥无效", 0, nil)
	}
	return code, nil
}

func ensureTargetKind(target TargetConfig, kind TargetKind) TargetConfig {
	if target.Kind == "" {
		target.Kind = kind
	}
	return target
}

func credentialFingerprint(values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:8])
}
