package secure

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestVaultRoundTripAndTamper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	vault, err := NewVault(key)
	if err != nil {
		t.Fatalf("创建保险箱失败: %v", err)
	}

	encoded, err := vault.Encrypt([]byte("secret-value"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	decoded, err := vault.Decrypt(encoded)
	if err != nil || string(decoded) != "secret-value" {
		t.Fatalf("解密结果不正确: %q, %v", decoded, err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("解析测试密文失败: %v", err)
	}
	// 直接修改认证标签中的有效字节，避免只改变 Base64 未使用位而仍解码为相同内容。
	payload[len(payload)-1] ^= 1
	_, err = vault.Decrypt(base64.RawURLEncoding.EncodeToString(payload))
	if err == nil {
		t.Fatal("被篡改的密文应当校验失败")
	}
}

func TestPasswordHashAndVerify(t *testing.T) {
	password := "correct-horse-123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码散列失败: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("密码散列格式不正确: %s", hash)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("正确密码验证失败")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("错误密码不应通过验证")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("过短密码应被拒绝")
	}
}
