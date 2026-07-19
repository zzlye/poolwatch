package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Vault 使用部署主密钥加密需要持久化的敏感内容。
type Vault struct {
	aead cipher.AEAD
}

// NewVault 使用 32 字节部署密钥创建保险箱。
func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("保险箱密钥长度必须为 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建加密器失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	return &Vault{aead: aead}, nil
}

// Encrypt 使用独立随机数加密一段敏感数据。
func (v *Vault) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	sealed := v.aead.Seal(nil, nonce, plaintext, nil)
	payload := append([]byte{1}, nonce...)
	payload = append(payload, sealed...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decrypt 校验密文版本与完整性后返回明文。
func (v *Vault) Decrypt(encoded string) ([]byte, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("密文格式无效")
	}
	minimum := 1 + v.aead.NonceSize() + v.aead.Overhead()
	if len(payload) < minimum || payload[0] != 1 {
		return nil, errors.New("密文版本或长度无效")
	}
	nonceEnd := 1 + v.aead.NonceSize()
	plaintext, err := v.aead.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], nil)
	if err != nil {
		return nil, errors.New("密文校验失败，请检查 APP_ENCRYPTION_KEY")
	}
	return plaintext, nil
}
