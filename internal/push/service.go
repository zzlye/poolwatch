package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"poolwatch/internal/identity"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

const (
	vapidPublicSetting  = "vapid_public_key"
	vapidPrivateSetting = "vapid_private_key_enc"
)

// Notification 是 Service Worker 可以直接展示的通知载荷。
type Notification struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	URL      string `json:"url"`
	Tag      string `json:"tag"`
	Severity string `json:"severity"`
}

// Device 是可返回前端的推送设备脱敏信息。
type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	Current    bool      `json:"current"`
}

// SubscriptionInput 接收浏览器 PushManager 生成的订阅。
type SubscriptionInput struct {
	Endpoint  string
	P256DH    string
	Auth      string
	Name      string
	UserAgent string
}

type sendFunc func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error)

// resolver 允许测试替换域名解析，同时确保订阅校验和实际连接使用同一套解析边界。
type resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Service 管理 VAPID 密钥、设备订阅和通知发送。
type Service struct {
	store      *store.Store
	vault      *secure.Vault
	subscriber string
	publicKey  string
	privateKey string
	send       sendFunc
	resolver   resolver
	now        func() time.Time
	mu         sync.RWMutex
}

// NewService 创建推送服务，调用 EnsureKeys 后即可对外提供公钥。
func NewService(database *store.Store, vault *secure.Vault, publicBaseURL string) *Service {
	subscriber := "mailto:admin@localhost"
	if parsed, err := url.Parse(publicBaseURL); err == nil && parsed.Scheme == "https" && parsed.Host != "" {
		subscriber = parsed.Scheme + "://" + parsed.Host
	}
	return &Service{
		store:      database,
		vault:      vault,
		subscriber: subscriber,
		send:       webpush.SendNotificationWithContext,
		resolver:   net.DefaultResolver,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// EnsureKeys 读取或首次生成系统自有的 VAPID 密钥对。
func (s *Service) EnsureKeys(ctx context.Context) error {
	publicKey, err := s.store.GetSetting(ctx, vapidPublicSetting)
	if err != nil {
		return err
	}
	privateEncrypted, err := s.store.GetSetting(ctx, vapidPrivateSetting)
	if err != nil {
		return err
	}
	if publicKey == "" || privateEncrypted == "" {
		privateKey, generatedPublic, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return fmt.Errorf("生成浏览器推送密钥失败: %w", err)
		}
		privateEncrypted, err = s.vault.Encrypt([]byte(privateKey))
		if err != nil {
			return err
		}
		if err := s.store.SetSetting(ctx, vapidPrivateSetting, privateEncrypted); err != nil {
			return err
		}
		if err := s.store.SetSetting(ctx, vapidPublicSetting, generatedPublic); err != nil {
			return err
		}
		publicKey = generatedPublic
	}
	privateKey, err := s.vault.Decrypt(privateEncrypted)
	if err != nil {
		return fmt.Errorf("读取浏览器推送密钥失败: %w", err)
	}
	s.mu.Lock()
	s.publicKey = publicKey
	s.privateKey = string(privateKey)
	s.mu.Unlock()
	return nil
}

// PublicKey 返回浏览器订阅时需要的 VAPID 公钥。
func (s *Service) PublicKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicKey
}

// Subscribe 加密订阅认证字段并保存设备。
func (s *Service) Subscribe(ctx context.Context, input SubscriptionInput) error {
	endpoint, err := url.Parse(strings.TrimSpace(input.Endpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("推送订阅地址无效")
	}
	if err := validatePushEndpoint(endpoint); err != nil {
		return err
	}
	if len(input.Endpoint) > 4096 || len(input.P256DH) > 1024 || len(input.Auth) > 1024 {
		return errors.New("推送订阅数据过长")
	}
	if input.P256DH == "" || input.Auth == "" {
		return errors.New("推送订阅缺少浏览器密钥")
	}
	p256dhEncrypted, err := s.vault.Encrypt([]byte(input.P256DH))
	if err != nil {
		return err
	}
	authEncrypted, err := s.vault.Encrypt([]byte(input.Auth))
	if err != nil {
		return err
	}
	id, err := identity.NewID("push")
	if err != nil {
		return err
	}
	now := s.now()
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "未命名设备"
	}
	return s.store.UpsertPushSubscription(ctx, store.PushSubscription{
		ID: id, Endpoint: input.Endpoint, P256DH: p256dhEncrypted, Auth: authEncrypted,
		DeviceName: truncate(name, 80), UserAgent: truncate(input.UserAgent, 300), CreatedAt: now, LastUsedAt: now,
	})
}

// Devices 返回不含端点和密钥的设备列表。
func (s *Service) Devices(ctx context.Context) ([]Device, error) {
	subscriptions, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		devices = append(devices, Device{
			ID: subscription.ID, Name: subscription.DeviceName, UserAgent: subscription.UserAgent,
			CreatedAt: subscription.CreatedAt, LastSeenAt: subscription.LastUsedAt,
		})
	}
	return devices, nil
}

// DeleteDevice 取消一个已保存的浏览器订阅。
func (s *Service) DeleteDevice(ctx context.Context, id string) error {
	return s.store.DeletePushSubscription(ctx, id)
}

// Send 向所有设备各发送一次通知，并自动移除已失效端点。
func (s *Service) Send(ctx context.Context, notification Notification) error {
	payload, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	subscriptions, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return err
	}
	s.mu.RLock()
	publicKey, privateKey := s.publicKey, s.privateKey
	s.mu.RUnlock()
	if publicKey == "" || privateKey == "" {
		return errors.New("浏览器推送密钥尚未初始化")
	}

	var wait sync.WaitGroup
	var errorMu sync.Mutex
	errorsFound := make([]error, 0)
	pushClient := newPushHTTPClient(s.resolver)
	for _, saved := range subscriptions {
		saved := saved
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := s.sendOne(ctx, saved, payload, publicKey, privateKey, pushClient); err != nil {
				errorMu.Lock()
				errorsFound = append(errorsFound, err)
				errorMu.Unlock()
			}
		}()
	}
	wait.Wait()
	return errors.Join(errorsFound...)
}

// SendTest 发送一条可辨识的测试通知。
func (s *Service) SendTest(ctx context.Context) error {
	return s.Send(ctx, Notification{
		Title: "号池监控测试", Body: "这台设备已经可以接收额度和账号状态通知。",
		URL: "/settings", Tag: "push-test", Severity: "info",
	})
}

func (s *Service) sendOne(ctx context.Context, saved store.PushSubscription, payload []byte, publicKey, privateKey string, pushClient *http.Client) error {
	p256dh, err := s.vault.Decrypt(saved.P256DH)
	if err != nil {
		return fmt.Errorf("读取推送设备密钥失败: %w", err)
	}
	auth, err := s.vault.Decrypt(saved.Auth)
	if err != nil {
		return fmt.Errorf("读取推送设备认证信息失败: %w", err)
	}
	subscription := &webpush.Subscription{Endpoint: saved.Endpoint}
	subscription.Keys.P256dh = string(p256dh)
	subscription.Keys.Auth = string(auth)
	response, err := s.send(ctx, payload, subscription, &webpush.Options{
		Subscriber: s.subscriber, VAPIDPublicKey: publicKey, VAPIDPrivateKey: privateKey, TTL: 60, HTTPClient: pushClient,
	})
	if err != nil {
		return fmt.Errorf("发送浏览器通知失败: %w", err)
	}
	if response != nil {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
			_ = s.store.DeletePushSubscriptionByEndpoint(ctx, saved.Endpoint)
			return nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("浏览器推送服务返回状态 %d", response.StatusCode)
		}
	}
	return s.store.TouchPushSubscription(ctx, saved.ID, s.now())
}

func validatePushEndpoint(endpoint *url.URL) error {
	if endpoint == nil {
		return errors.New("推送订阅地址无效")
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("推送订阅地址必须使用公网服务")
	}
	if parsed := net.ParseIP(host); parsed != nil && !isPublicPushIP(parsed) {
		return errors.New("推送订阅地址必须使用公网服务")
	}
	return nil
}

func newPushHTTPClient(lookup resolver) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 推送端点不使用环境代理，连接前重新解析并直接拨号已校验的公网 IP，阻止 DNS 重绑定。
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("推送服务地址格式无效")
		}
		addresses, err := lookup.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("推送服务地址解析失败")
		}
		for _, resolved := range addresses {
			if !isPublicPushIP(resolved.IP) {
				return nil, errors.New("推送服务地址不是公网地址")
			}
		}
		var lastErr error
		for _, resolved := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("推送服务没有可连接的公网地址")
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("推送服务不允许重定向")
		},
	}
}

func isPublicPushIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range blockedPushPrefixes() {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func blockedPushPrefixes() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32",
		"2002::/16", "fc00::/7", "fe80::/10", "fec0::/10",
	}
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
