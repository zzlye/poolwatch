package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// NewID 生成带业务前缀的随机标识。
func NewID(prefix string) (string, error) {
	value, err := RandomToken(16)
	if err != nil {
		return "", err
	}
	return prefix + "_" + value, nil
}

// RandomToken 生成指定随机字节数的十六进制令牌。
func RandomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成随机标识失败: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// HashToken 生成适合持久化和比较的令牌摘要。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
