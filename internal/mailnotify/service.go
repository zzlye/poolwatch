package mailnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

const smtpConfigSetting = "smtp_config_enc"

// ErrInvalidConfig 标识可安全返回给管理员的邮箱表单校验错误。
var ErrInvalidConfig = errors.New("邮箱配置无效")

// Config 保存发送邮件所需的完整配置，密码只会出现在服务端内存和加密存储中。
type Config struct {
	Enabled     bool     `json:"enabled"`
	Provider    string   `json:"provider"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Security    string   `json:"security"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	FromName    string   `json:"fromName"`
	FromAddress string   `json:"fromAddress"`
	Recipients  []string `json:"recipients"`
}

// PublicConfig 是管理接口可返回的邮箱配置，其中不包含 SMTP 密码。
type PublicConfig struct {
	Enabled            bool     `json:"enabled"`
	Provider           string   `json:"provider"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Security           string   `json:"security"`
	Username           string   `json:"username"`
	FromName           string   `json:"fromName"`
	FromAddress        string   `json:"fromAddress"`
	Recipients         []string `json:"recipients"`
	PasswordConfigured bool     `json:"passwordConfigured"`
}

// Message 表示一封已经完成业务内容组装的纯文本邮件。
type Message struct {
	Subject string
	Body    string
}

// AlertMessage 包含生成告警邮件所需的脱敏字段。
type AlertMessage struct {
	AlertID    string
	TargetName string
	Title      string
	Message    string
	Severity   string
	Recovered  bool
	OccurredAt time.Time
}

type messageSender interface {
	Send(context.Context, Config, Message) error
}

// Service 管理加密邮箱配置并发送测试或告警邮件。
type Service struct {
	store         *store.Store
	vault         *secure.Vault
	publicBaseURL string
	sender        messageSender
}

// NewService 创建邮箱通知服务。
func NewService(database *store.Store, vault *secure.Vault, publicBaseURL string, allowPrivateTargets bool) *Service {
	return &Service{
		store: database, vault: vault, publicBaseURL: strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		sender: newSMTPSender(20*time.Second, allowPrivateTargets, nil),
	}
}

// Settings 返回不含密码的当前邮箱配置。
func (s *Service) Settings(ctx context.Context) (PublicConfig, error) {
	config, found, err := s.load(ctx)
	if err != nil {
		return PublicConfig{}, err
	}
	if !found {
		return publicConfig(defaultConfig()), nil
	}
	return publicConfig(config), nil
}

// Save 校验并原子保存邮箱配置，认证身份未变时空密码会沿用原有密码。
func (s *Service) Save(ctx context.Context, input Config) (PublicConfig, error) {
	merged, err := s.mergeWithStoredPassword(ctx, input)
	if err != nil {
		return PublicConfig{}, err
	}
	if err := validateConfig(merged, true); err != nil {
		return PublicConfig{}, err
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return PublicConfig{}, errors.New("邮箱配置格式无效")
	}
	encrypted, err := s.vault.Encrypt(encoded)
	if err != nil {
		return PublicConfig{}, fmt.Errorf("加密邮箱配置失败: %w", err)
	}
	if err := s.store.SetSetting(ctx, smtpConfigSetting, encrypted); err != nil {
		return PublicConfig{}, fmt.Errorf("保存邮箱配置失败: %w", err)
	}
	return publicConfig(merged), nil
}

// Clear 删除完整邮箱配置和加密凭据，并返回首次配置时的默认状态。
func (s *Service) Clear(ctx context.Context) (PublicConfig, error) {
	if err := s.store.DeleteSetting(ctx, smtpConfigSetting); err != nil {
		return PublicConfig{}, fmt.Errorf("清除邮箱配置失败: %w", err)
	}
	return publicConfig(defaultConfig()), nil
}

// Test 使用当前表单配置发送测试邮件，配置与密码都不会在测试过程中落库。
func (s *Service) Test(ctx context.Context, input Config) error {
	merged, err := s.mergeWithStoredPassword(ctx, input)
	if err != nil {
		return err
	}
	if err := validateConfig(merged, false); err != nil {
		return err
	}
	productName := s.productName(ctx)
	return s.sender.Send(ctx, merged, Message{
		Subject: "[" + productName + "] 测试邮件",
		Body:    productName + " 已经成功连接发送邮箱。\n\n后续额度、凭据和连接状态告警会发送到这里。",
	})
}

// Enabled 返回邮箱提醒当前是否启用，供多通道通知分发器决定投递范围。
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	config, found, err := s.load(ctx)
	if err != nil || !found {
		return false, err
	}
	return config.Enabled, nil
}

// SendAlert 按当前已保存配置发送一封告警或恢复邮件。
func (s *Service) SendAlert(ctx context.Context, alert AlertMessage) error {
	config, found, err := s.load(ctx)
	if err != nil {
		return err
	}
	if !found || !config.Enabled {
		return nil
	}
	if err := validateConfig(config, false); err != nil {
		return err
	}
	productName := s.productName(ctx)
	state := "告警"
	if alert.Recovered {
		state = "恢复"
	} else if strings.EqualFold(alert.Severity, "critical") {
		state = "紧急"
	}
	targetName := cleanLine(alert.TargetName, 120)
	if targetName == "" {
		targetName = "未命名渠道"
	}
	title := cleanLine(alert.Title, 200)
	detail := cleanText(alert.Message, 2000)
	occurredAt := alert.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	link := s.alertLink(alert.AlertID)
	body := strings.Join([]string{
		productName + "通知", "", "渠道：" + targetName, "状态：" + state, "标题：" + title,
		"详情：" + detail, "时间：" + occurredAt.Format(time.RFC3339),
	}, "\n")
	if link != "" {
		body += "\n查看详情：" + link
	}
	return s.sender.Send(ctx, config, Message{
		Subject: fmt.Sprintf("[%s][%s] %s：%s", productName, state, targetName, title),
		Body:    body,
	})
}

func (s *Service) mergeWithStoredPassword(ctx context.Context, input Config) (Config, error) {
	input = normalizeConfig(input)
	if strings.TrimSpace(input.Password) != "" {
		return input, nil
	}
	stored, found, err := s.load(ctx)
	if err != nil {
		return Config{}, err
	}
	if found && sameCredentialIdentity(input, stored) {
		input.Password = stored.Password
	} else if found {
		return Config{}, configError("发件服务商、服务器或账号已变更，请重新填写授权码或应用密码")
	}
	return input, nil
}

// sameCredentialIdentity 判断已保存密码是否仍属于当前发信认证身份。
func sameCredentialIdentity(left, right Config) bool {
	left = normalizeConfig(left)
	right = normalizeConfig(right)
	return left.Provider == right.Provider && left.Host == right.Host && left.Port == right.Port &&
		left.Security == right.Security && left.Username == right.Username
}

func (s *Service) load(ctx context.Context) (Config, bool, error) {
	encrypted, err := s.store.GetSetting(ctx, smtpConfigSetting)
	if err != nil {
		return Config{}, false, fmt.Errorf("读取邮箱配置失败: %w", err)
	}
	if strings.TrimSpace(encrypted) == "" {
		return Config{}, false, nil
	}
	decoded, err := s.vault.Decrypt(encrypted)
	if err != nil {
		return Config{}, false, fmt.Errorf("解密邮箱配置失败: %w", err)
	}
	var config Config
	if err := json.Unmarshal(decoded, &config); err != nil {
		return Config{}, false, errors.New("邮箱配置数据已经损坏")
	}
	return normalizeConfig(config), true, nil
}

func (s *Service) productName(ctx context.Context) string {
	name, err := s.store.GetSetting(ctx, "product_name")
	if err != nil || strings.TrimSpace(name) == "" {
		return "号池监控"
	}
	return cleanLine(name, 40)
}

func (s *Service) alertLink(alertID string) string {
	if s.publicBaseURL == "" || strings.TrimSpace(alertID) == "" {
		return ""
	}
	return s.publicBaseURL + "/alerts?focus=" + url.QueryEscape(strings.TrimSpace(alertID))
}

func defaultConfig() Config {
	return Config{
		Provider: "qq", Host: "smtp.qq.com", Port: 465, Security: "tls",
		FromName: "号池监控", Recipients: []string{},
	}
}

func publicConfig(config Config) PublicConfig {
	return PublicConfig{
		Enabled: config.Enabled, Provider: config.Provider, Host: config.Host, Port: config.Port,
		Security: config.Security, Username: config.Username, FromName: config.FromName,
		FromAddress: config.FromAddress, Recipients: append([]string{}, config.Recipients...),
		PasswordConfigured: strings.TrimSpace(config.Password) != "",
	}
}

func normalizeConfig(config Config) Config {
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	if config.Provider == "" {
		config.Provider = "custom"
	}
	config.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(config.Host)), ".")
	config.Security = strings.ToLower(strings.TrimSpace(config.Security))
	config.Username = strings.TrimSpace(config.Username)
	config.FromName = strings.TrimSpace(config.FromName)
	config.FromAddress = strings.ToLower(strings.TrimSpace(config.FromAddress))
	recipients := make([]string, 0, len(config.Recipients))
	seen := make(map[string]struct{}, len(config.Recipients))
	for _, value := range config.Recipients {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		recipients = append(recipients, value)
	}
	sort.Strings(recipients)
	config.Recipients = recipients
	return config
}

func validateConfig(config Config, allowEmptyDisabled bool) error {
	if allowEmptyDisabled && !config.Enabled && config.Username == "" &&
		config.FromAddress == "" && len(config.Recipients) == 0 && strings.TrimSpace(config.Password) == "" {
		return nil
	}
	switch config.Provider {
	case "qq", "163", "gmail", "outlook", "custom":
	default:
		return configError("邮箱服务商类型无效")
	}
	if err := validateSMTPHost(config.Host); err != nil {
		return err
	}
	if config.Port < 1 || config.Port > 65535 {
		return configError("SMTP 端口需要在 1 至 65535 之间")
	}
	if config.Security != "tls" && config.Security != "starttls" {
		return configError("SMTP 加密方式仅支持 TLS 或 STARTTLS")
	}
	if config.Username == "" || len(config.Username) > 320 || strings.ContainsAny(config.Username, "\r\n\x00") {
		return configError("SMTP 账号格式无效")
	}
	if strings.TrimSpace(config.Password) == "" || len(config.Password) > 2048 || strings.ContainsRune(config.Password, '\x00') {
		return configError("请填写 SMTP 授权码或密码")
	}
	if len([]rune(config.FromName)) > 80 || strings.ContainsAny(config.FromName, "\r\n\x00") {
		return configError("发件人名称格式无效")
	}
	if _, err := parseMailbox(config.FromAddress); err != nil {
		return configError("发件邮箱地址格式无效")
	}
	if len(config.Recipients) < 1 || len(config.Recipients) > 10 {
		return configError("接收邮箱数量需要在 1 至 10 个之间")
	}
	for _, recipient := range config.Recipients {
		if _, err := parseMailbox(recipient); err != nil {
			return configError("接收邮箱地址格式无效")
		}
	}
	return nil
}

func validateSMTPHost(host string) error {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\@?#:\r\n\x00 ") {
		return configError("SMTP 服务器地址格式无效")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return configError("SMTP 服务器地址格式无效")
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return configError("SMTP 服务器地址格式无效")
		}
	}
	return nil
}

func parseMailbox(value string) (string, error) {
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("邮箱地址格式无效")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || !strings.EqualFold(parsed.Address, value) {
		return "", errors.New("邮箱地址格式无效")
	}
	return parsed.Address, nil
}

func cleanLine(value string, maximum int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(value))
	characters := []rune(value)
	if len(characters) > maximum {
		value = string(characters[:maximum])
	}
	return value
}

func cleanText(value string, maximum int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	characters := []rune(value)
	if len(characters) > maximum {
		value = string(characters[:maximum])
	}
	return value
}

func configError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, message)
}

// ConfigErrorMessage 提取管理员可读的邮箱配置校验提示。
func ConfigErrorMessage(err error) string {
	if !errors.Is(err, ErrInvalidConfig) {
		return "邮箱配置无效"
	}
	return strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrInvalidConfig.Error()+":"))
}
