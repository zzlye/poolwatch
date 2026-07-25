package mailnotify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/netip"
	"net/smtp"
	"sort"
	"strings"
	"time"
)

type smtpResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type smtpSender struct {
	timeout      time.Duration
	allowPrivate bool
	resolver     smtpResolver
}

// newSMTPSender 创建只通过安全连接发送邮件的 SMTP 客户端。
func newSMTPSender(timeout time.Duration, allowPrivate bool, resolver smtpResolver) *smtpSender {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &smtpSender{timeout: timeout, allowPrivate: allowPrivate, resolver: resolver}
}

// Send 连接 SMTP 服务器并完成一次带认证的纯文本邮件投递。
func (s *smtpSender) Send(ctx context.Context, config Config, message Message) error {
	ctx, cancel := withDefaultTimeout(ctx, s.timeout)
	defer cancel()
	connection, err := s.dial(ctx, config)
	if err != nil {
		return err
	}
	defer connection.Close()
	stopCancellation := closeConnectionOnCancel(ctx, connection)
	defer stopCancellation()

	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		return errors.New("SMTP 服务器握手失败")
	}
	defer client.Close()
	tlsConfig := &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12}
	if config.Security == "starttls" {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return errors.New("SMTP 服务器没有提供 STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP 安全连接失败")
		}
	}
	if err := authenticateSMTP(client, config); err != nil {
		return err
	}
	if err := client.Mail(config.FromAddress); err != nil {
		return errors.New("SMTP 服务器拒绝发件地址")
	}
	for _, recipient := range config.Recipients {
		if err := client.Rcpt(recipient); err != nil {
			return errors.New("SMTP 服务器拒绝接收邮箱")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP 服务器拒绝邮件内容")
	}
	payload, err := buildMessage(config, message)
	if err != nil {
		_ = writer.Close()
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return errors.New("发送邮件内容时连接中断")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP 服务器没有接受邮件")
	}
	if err := client.Quit(); err != nil {
		return errors.New("SMTP 会话结束异常")
	}
	return nil
}

func (s *smtpSender) dial(ctx context.Context, config Config) (net.Conn, error) {
	addresses, err := s.resolver.LookupIPAddr(ctx, config.Host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("SMTP 服务器域名解析失败")
	}
	allowed := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if err := validateSMTPIP(address.IP, s.allowPrivate); err != nil {
			return nil, err
		}
		allowed = append(allowed, address.IP)
	}
	// 固定拨号已经校验的解析结果，避免校验后再次解析带来的地址切换。
	dialer := &net.Dialer{Timeout: s.timeout, KeepAlive: 30 * time.Second}
	var connection net.Conn
	for _, address := range allowed {
		connection, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), fmt.Sprintf("%d", config.Port)))
		if err == nil {
			break
		}
	}
	if err != nil || connection == nil {
		return nil, errors.New("连接 SMTP 服务器失败")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if config.Security != "tls" {
		return connection, nil
	}
	tlsConnection := tls.Client(connection, &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		_ = connection.Close()
		return nil, errors.New("SMTP 安全连接失败")
	}
	return tlsConnection, nil
}

func authenticateSMTP(client *smtp.Client, config Config) error {
	supported, mechanisms := client.Extension("AUTH")
	if !supported {
		return errors.New("SMTP 服务器没有提供账号认证")
	}
	available := make(map[string]bool)
	for _, mechanism := range strings.Fields(strings.ToUpper(mechanisms)) {
		available[mechanism] = true
	}
	var authentication smtp.Auth
	if available["PLAIN"] {
		authentication = smtp.PlainAuth("", config.Username, config.Password, config.Host)
	} else if available["LOGIN"] {
		authentication = &loginAuth{username: config.Username, password: config.Password}
	} else {
		return errors.New("SMTP 服务器认证方式暂不受支持")
	}
	if err := client.Auth(authentication); err != nil {
		return errors.New("SMTP 账号或授权码校验失败")
	}
	return nil
}

type loginAuth struct {
	username string
	password string
	step     int
}

// Start 开始 SMTP LOGIN 认证，凭据只会在 TLS 连接建立后提交。
func (authentication *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("SMTP LOGIN 认证需要安全连接")
	}
	authentication.step = 0
	return "LOGIN", nil, nil
}

// Next 根据服务器质询依次返回账号和授权码。
func (authentication *loginAuth) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(challenge))
	if strings.Contains(prompt, "user") || authentication.step == 0 {
		authentication.step = 1
		return []byte(authentication.username), nil
	}
	if strings.Contains(prompt, "pass") || authentication.step == 1 {
		authentication.step = 2
		return []byte(authentication.password), nil
	}
	return nil, errors.New("SMTP LOGIN 认证质询无效")
}

func buildMessage(config Config, message Message) ([]byte, error) {
	subject := cleanLine(message.Subject, 300)
	body := cleanText(message.Body, 10000)
	if subject == "" || body == "" {
		return nil, errors.New("邮件内容为空")
	}
	from := (&mail.Address{Name: cleanLine(config.FromName, 80), Address: config.FromAddress}).String()
	recipients := make([]string, 0, len(config.Recipients))
	for _, recipient := range config.Recipients {
		recipients = append(recipients, (&mail.Address{Address: recipient}).String())
	}
	sort.Strings(recipients)

	var encodedBody bytes.Buffer
	quoted := quotedprintable.NewWriter(&encodedBody)
	if _, err := quoted.Write([]byte(normalizeCRLF(body))); err != nil {
		return nil, errors.New("编码邮件内容失败")
	}
	if err := quoted.Close(); err != nil {
		return nil, errors.New("编码邮件内容失败")
	}
	headers := []string{
		"From: " + from,
		"To: " + strings.Join(recipients, ", "),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + encodedBody.String()), nil
}

func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func withDefaultTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, exists := ctx.Deadline(); exists {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func closeConnectionOnCancel(ctx context.Context, connection net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func validateSMTPIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return errors.New("SMTP 服务器解析结果无效")
	}
	address, valid := netip.AddrFromSlice(ip)
	if !valid {
		return errors.New("SMTP 服务器解析结果无效")
	}
	address = address.Unmap()
	if isSMTPMetadataIP(address) {
		return errors.New("SMTP 服务器地址属于云元数据网络")
	}
	if address.IsLoopback() {
		return errors.New("SMTP 服务器地址属于回环网络")
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return errors.New("SMTP 服务器地址属于链路本地或无效网络")
	}
	// 检查常见 IPv4 转换地址内嵌的真实目标，避免通过 NAT64 或旧兼容格式绕过回环和元数据限制。
	if embedded, found := embeddedSMTPIPv4(address); found {
		if err := validateSMTPAddress(embedded, allowPrivate); err != nil {
			return errors.New("SMTP 服务器转换地址指向受限网络")
		}
	}
	for _, prefix := range permanentlyBlockedSMTPPrefixes() {
		if prefix.Contains(address) {
			return errors.New("SMTP 服务器地址属于保留网络")
		}
	}
	if !allowPrivate && (!address.IsGlobalUnicast() || address.IsPrivate()) {
		return errors.New("SMTP 服务器默认只允许公网地址")
	}
	return nil
}

func validateSMTPAddress(address netip.Addr, allowPrivate bool) error {
	bytes := address.AsSlice()
	return validateSMTPIP(net.IP(bytes), allowPrivate)
}

// embeddedSMTPIPv4 提取标准 NAT64 与旧 IPv4 兼容地址中的 IPv4 目标。
func embeddedSMTPIPv4(address netip.Addr) (netip.Addr, bool) {
	if !address.Is6() {
		return netip.Addr{}, false
	}
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
	} {
		if !prefix.Contains(address) {
			continue
		}
		bytes := address.As16()
		return netip.AddrFrom4([4]byte{bytes[12], bytes[13], bytes[14], bytes[15]}), true
	}
	return netip.Addr{}, false
}

func isSMTPMetadataIP(address netip.Addr) bool {
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254"} {
		if address == netip.MustParseAddr(raw) {
			return true
		}
	}
	return false
}

func permanentlyBlockedSMTPPrefixes() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "100::/64",
		"64:ff9b:1::/48", "2001::/32", "2001:2::/48", "2001:db8::/32", "2002::/16", "fec0::/10",
	}
	result := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}
