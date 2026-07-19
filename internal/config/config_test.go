package config

import (
	"encoding/base64"
	"path/filepath"
	"testing"
)

func TestLoadValidatesEncryptionKeyAndPublicAddress(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("APP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("PUBLIC_BASE_URL", "https://monitor.example.com/")
	t.Setenv("TZ", "UTC")
	configuration, err := Load()
	if err != nil {
		t.Fatalf("读取有效配置失败: %v", err)
	}
	if configuration.PublicBaseURL != "https://monitor.example.com" || configuration.DataDir == "" {
		t.Fatalf("配置归一化结果不正确: %#v", configuration)
	}

	t.Setenv("PUBLIC_BASE_URL", "https://user:pass@monitor.example.com/path")
	if _, err := Load(); err == nil {
		t.Fatal("包含账号和路径的公开地址应被拒绝")
	}
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("APP_ENCRYPTION_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("无效加密密钥应被拒绝")
	}
}
