package mailnotify

import (
	"context"
	"errors"
	"net"
	"net/smtp"
	"reflect"
	"strings"
	"testing"
	"time"

	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

type recordingSender struct {
	configs  []Config
	messages []Message
	err      error
}

func (sender *recordingSender) Send(_ context.Context, config Config, message Message) error {
	sender.configs = append(sender.configs, config)
	sender.messages = append(sender.messages, message)
	return sender.err
}

func TestEmailConfigEncryptedAndBlankPasswordOnlyPreservedForSameIdentity(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "https://monitor.example.com", false)
	recorder := &recordingSender{}
	service.sender = recorder
	ctx := context.Background()

	initial := validConfig()
	public, err := service.Save(ctx, initial)
	if err != nil {
		t.Fatalf("保存邮箱配置失败: %v", err)
	}
	if !public.PasswordConfigured || public.Enabled != initial.Enabled {
		t.Fatalf("公开配置状态不正确: %#v", public)
	}
	encrypted, err := database.GetSetting(ctx, smtpConfigSetting)
	if err != nil {
		t.Fatalf("读取加密配置失败: %v", err)
	}
	for _, secret := range []string{initial.Password, initial.Username, initial.FromAddress, initial.Recipients[0]} {
		if strings.Contains(encrypted, secret) {
			t.Fatalf("加密配置泄露了明文字段: %s", secret)
		}
	}
	loaded, err := service.Settings(ctx)
	if err != nil || !loaded.PasswordConfigured || loaded.Host != initial.Host {
		t.Fatalf("读取公开配置失败: %#v, %v", loaded, err)
	}

	changed := initial
	changed.FromName = "新的发件人名称"
	changed.Password = ""
	if _, err := service.Save(ctx, changed); err != nil {
		t.Fatalf("空密码沿用保存失败: %v", err)
	}
	if err := service.Test(ctx, changed); err != nil {
		t.Fatalf("使用已有密码测试失败: %v", err)
	}
	if len(recorder.configs) != 1 || recorder.configs[0].Password != initial.Password || recorder.configs[0].Host != changed.Host {
		t.Fatalf("测试邮件没有沿用已保存密码: %#v", recorder.configs)
	}

	identityChanges := []struct {
		name   string
		change func(*Config)
	}{
		{name: "服务商", change: func(config *Config) { config.Provider = "gmail" }},
		{name: "服务器", change: func(config *Config) { config.Host = "smtp2.example.com" }},
		{name: "端口", change: func(config *Config) { config.Port = 587 }},
		{name: "加密方式", change: func(config *Config) { config.Security = "starttls" }},
		{name: "发件账号", change: func(config *Config) { config.Username = "other@example.com" }},
	}
	for _, test := range identityChanges {
		t.Run("变更"+test.name+"需要新授权码", func(t *testing.T) {
			draft := changed
			test.change(&draft)
			if _, err := service.Save(ctx, draft); err == nil || !errors.Is(err, ErrInvalidConfig) ||
				!strings.Contains(ConfigErrorMessage(err), "重新填写授权码") {
				t.Fatalf("认证身份变化后应返回明确提示: %v", err)
			}
		})
	}

	replacement := changed
	replacement.Host = "smtp2.example.com"
	replacement.Password = "replacement-secret"
	if public, err := service.Save(ctx, replacement); err != nil || !public.PasswordConfigured {
		t.Fatalf("填写新授权码后没有保存新认证身份: %#v, %v", public, err)
	}
}

func TestEmailConfigCanBeClearedToQQDefault(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "", false)
	ctx := context.Background()
	if _, err := service.Save(ctx, validConfig()); err != nil {
		t.Fatalf("准备邮箱配置失败: %v", err)
	}

	cleared, err := service.Clear(ctx)
	if err != nil {
		t.Fatalf("清除邮箱配置失败: %v", err)
	}
	if cleared.Enabled || cleared.PasswordConfigured || cleared.Provider != "qq" || cleared.Host != "smtp.qq.com" ||
		cleared.Port != 465 || cleared.Security != "tls" {
		t.Fatalf("清除后没有恢复 QQ 首次配置状态: %#v", cleared)
	}
	encrypted, err := database.GetSetting(ctx, smtpConfigSetting)
	if err != nil || encrypted != "" {
		t.Fatalf("清除后仍残留加密邮箱配置: %q, %v", encrypted, err)
	}
	loaded, err := service.Settings(ctx)
	if err != nil || !reflect.DeepEqual(loaded, cleared) {
		t.Fatalf("清除后重新读取的默认状态不一致: %#v, %#v, %v", loaded, cleared, err)
	}
}

func TestEmailTestDoesNotPersistDraft(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "", false)
	recorder := &recordingSender{}
	service.sender = recorder
	ctx := context.Background()
	stored := validConfig()
	if _, err := service.Save(ctx, stored); err != nil {
		t.Fatalf("保存邮箱配置失败: %v", err)
	}
	draft := stored
	draft.Host = "draft.example.com"
	draft.Password = "new-draft-secret"
	if err := service.Test(ctx, draft); err != nil {
		t.Fatalf("发送草稿测试邮件失败: %v", err)
	}
	settings, err := service.Settings(ctx)
	if err != nil || settings.Host != stored.Host {
		t.Fatalf("测试草稿被写入了配置: %#v, %v", settings, err)
	}
}

func TestEmailAlertFormattingAndDisabledState(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	ctx := context.Background()
	if err := database.SetSetting(ctx, "product_name", "测试监控"); err != nil {
		t.Fatalf("保存产品名失败: %v", err)
	}
	service := NewService(database, vault, "https://monitor.example.com", false)
	recorder := &recordingSender{}
	service.sender = recorder
	config := validConfig()
	config.Enabled = false
	if _, err := service.Save(ctx, config); err != nil {
		t.Fatalf("保存停用配置失败: %v", err)
	}
	alert := AlertMessage{
		AlertID: "alert_1", TargetName: "主站", Title: "钱包余额不足", Message: "当前余额为 1 元。",
		Severity: "warning", OccurredAt: time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC),
	}
	if err := service.SendAlert(ctx, alert); err != nil || len(recorder.messages) != 0 {
		t.Fatalf("停用邮箱时仍触发了发送: %v, %#v", err, recorder.messages)
	}
	config.Enabled = true
	config.Password = ""
	if _, err := service.Save(ctx, config); err != nil {
		t.Fatalf("启用邮箱配置失败: %v", err)
	}
	if err := service.SendAlert(ctx, alert); err != nil {
		t.Fatalf("发送告警邮件失败: %v", err)
	}
	if len(recorder.messages) != 1 || !strings.Contains(recorder.messages[0].Subject, "[测试监控][告警] 主站") ||
		!strings.Contains(recorder.messages[0].Body, "https://monitor.example.com/alerts?focus=alert_1") {
		t.Fatalf("告警邮件内容不正确: %#v", recorder.messages)
	}
	alert.Recovered = true
	alert.Title = "钱包余额已恢复"
	if err := service.SendAlert(ctx, alert); err != nil || !strings.Contains(recorder.messages[1].Subject, "[恢复]") {
		t.Fatalf("恢复邮件内容不正确: %#v, %v", recorder.messages, err)
	}
}

func TestEmailConfigValidation(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "", false)
	ctx := context.Background()
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "主机带协议", change: func(config *Config) { config.Host = "https://smtp.example.com" }},
		{name: "明文连接", change: func(config *Config) { config.Security = "plain" }},
		{name: "端口越界", change: func(config *Config) { config.Port = 70000 }},
		{name: "发件地址注入", change: func(config *Config) { config.FromAddress = "a@example.com\r\nBcc:x@example.com" }},
		{name: "没有接收地址", change: func(config *Config) { config.Recipients = nil }},
		{name: "没有授权码", change: func(config *Config) { config.Password = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.change(&config)
			if _, err := service.Save(ctx, config); err == nil {
				t.Fatal("无效邮箱配置应被拒绝")
			}
		})
	}
}

func TestEmailDefaultDisabledConfigCanBeSaved(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "", false)

	settings, err := service.Save(context.Background(), defaultConfig())
	if err != nil {
		t.Fatalf("保存默认停用邮箱配置失败: %v", err)
	}
	if settings.Enabled || settings.Provider != "qq" || settings.Host != "smtp.qq.com" || settings.PasswordConfigured {
		t.Fatalf("默认停用邮箱配置不正确: %#v", settings)
	}
}

func TestSMTPNetworkBoundaryAndMessageEncoding(t *testing.T) {
	if err := validateSMTPIP(net.ParseIP("8.8.8.8"), false); err != nil {
		t.Fatalf("公网地址被拒绝: %v", err)
	}
	if err := validateSMTPIP(net.ParseIP("10.0.0.2"), false); err == nil {
		t.Fatal("默认策略应拒绝私网地址")
	}
	if err := validateSMTPIP(net.ParseIP("10.0.0.2"), true); err != nil {
		t.Fatalf("显式允许后私网地址仍被拒绝: %v", err)
	}
	if err := validateSMTPIP(net.ParseIP("169.254.169.254"), true); err == nil {
		t.Fatal("云元数据地址应始终被拒绝")
	}
	for _, address := range []string{"127.0.0.1", "127.10.20.30", "::1", "::ffff:127.0.0.1"} {
		if err := validateSMTPIP(net.ParseIP(address), true); err == nil || !strings.Contains(err.Error(), "回环网络") {
			t.Fatalf("允许私网时仍应永久拒绝回环地址 %s: %v", address, err)
		}
	}
	for _, address := range []string{"::127.0.0.1", "64:ff9b::127.0.0.1", "64:ff9b::169.254.169.254"} {
		if err := validateSMTPIP(net.ParseIP(address), true); err == nil || !strings.Contains(err.Error(), "转换地址") {
			t.Fatalf("IPv4 转换地址不应绕过永久限制 %s: %v", address, err)
		}
	}
	if err := validateSMTPIP(net.ParseIP("64:ff9b::8.8.8.8"), false); err != nil {
		t.Fatalf("指向公网的标准 NAT64 地址被错误拒绝: %v", err)
	}
	for _, address := range []string{"64:ff9b:1::1", "2001::1", "2002:0808:0808::1", "fec0::1"} {
		if err := validateSMTPIP(net.ParseIP(address), true); err == nil {
			t.Fatalf("允许私网时仍应拒绝 IPv6 特殊转换网络 %s", address)
		}
	}
	payload, err := buildMessage(validConfig(), Message{Subject: "余额不足\r\nBcc: x@example.com", Body: "中文正文"})
	if err != nil {
		t.Fatalf("构建邮件失败: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, "\r\nBcc:") || !strings.Contains(text, "charset=UTF-8") {
		t.Fatalf("邮件头编码或注入防护不正确: %s", text)
	}
}

func TestLoginAuthRequiresTLSAndAnswersChallenges(t *testing.T) {
	authentication := &loginAuth{username: "sender@example.com", password: "secret"}
	if _, _, err := authentication.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("LOGIN 认证应要求安全连接")
	}
	mechanism, initial, err := authentication.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || mechanism != "LOGIN" || initial != nil {
		t.Fatalf("LOGIN 初始化失败: %q, %q, %v", mechanism, initial, err)
	}
	username, err := authentication.Next([]byte("Username:"), true)
	if err != nil || string(username) != "sender@example.com" {
		t.Fatalf("LOGIN 账号质询失败: %q, %v", username, err)
	}
	password, err := authentication.Next([]byte("Password:"), true)
	if err != nil || string(password) != "secret" {
		t.Fatalf("LOGIN 密码质询失败: %q, %v", password, err)
	}
}

func TestEmailSenderErrorIsReturned(t *testing.T) {
	database, vault := emailFixture(t)
	defer database.Close()
	service := NewService(database, vault, "", false)
	service.sender = &recordingSender{err: errors.New("测试发送失败")}
	if err := service.Test(context.Background(), validConfig()); err == nil || err.Error() != "测试发送失败" {
		t.Fatalf("发送错误没有返回: %v", err)
	}
}

func emailFixture(t *testing.T) (*store.Store, *secure.Vault) {
	t.Helper()
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		database.Close()
		t.Fatalf("创建测试保险箱失败: %v", err)
	}
	return database, vault
}

func validConfig() Config {
	return Config{
		Enabled: true, Provider: "custom", Host: "smtp.example.com", Port: 465, Security: "tls",
		Username: "sender@example.com", Password: "smtp-secret", FromName: "号池监控",
		FromAddress: "sender@example.com", Recipients: []string{"receiver@example.com"},
	}
}
