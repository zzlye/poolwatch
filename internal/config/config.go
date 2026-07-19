package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 汇总进程启动后不会变化的运行参数。
type Config struct {
	Address             string
	DataDir             string
	PublicBaseURL       string
	SetupToken          string
	EncryptionKey       []byte
	AllowPrivateTargets bool
	SessionLifetime     time.Duration
	Timezone            *time.Location
}

// Load 从环境变量读取配置，并在启动前完成严格校验。
func Load() (Config, error) {
	key, err := decodeEncryptionKey(strings.TrimSpace(os.Getenv("APP_ENCRYPTION_KEY")))
	if err != nil {
		return Config{}, err
	}

	timezoneName := valueOrDefault("TZ", "Asia/Shanghai")
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		return Config{}, fmt.Errorf("时区配置无效: %w", err)
	}

	dataDir, err := filepath.Abs(valueOrDefault("DATA_DIR", "data"))
	if err != nil {
		return Config{}, fmt.Errorf("数据目录无效: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Config{}, fmt.Errorf("创建数据目录失败: %w", err)
	}

	return Config{
		Address:             valueOrDefault("LISTEN_ADDRESS", ":8080"),
		DataDir:             dataDir,
		PublicBaseURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/"),
		SetupToken:          strings.TrimSpace(os.Getenv("SETUP_TOKEN")),
		EncryptionKey:       key,
		AllowPrivateTargets: boolValue("ALLOW_PRIVATE_TARGETS", false),
		SessionLifetime:     7 * 24 * time.Hour,
		Timezone:            location,
	}, nil
}

func decodeEncryptionKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("必须设置 APP_ENCRYPTION_KEY")
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(raw)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("APP_ENCRYPTION_KEY 必须是 Base64 编码的 32 字节密钥")
}

func valueOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func boolValue(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
