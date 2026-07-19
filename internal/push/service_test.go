package push

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

func TestPushKeysSubscriptionAndExpiredEndpoint(t *testing.T) {
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("创建保险箱失败: %v", err)
	}
	ctx := context.Background()
	service := NewService(database, vault, "https://monitor.example.com")
	service.resolver = staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	if err := service.EnsureKeys(ctx); err != nil || service.PublicKey() == "" {
		t.Fatalf("初始化推送密钥失败: %v", err)
	}
	publicKey := service.PublicKey()

	if err := service.Subscribe(ctx, SubscriptionInput{
		Endpoint: "https://push.example.com/subscription/one", P256DH: "browser-public-key", Auth: "browser-auth-secret",
		Name: "我的电脑", UserAgent: "Edge on Windows",
	}); err != nil {
		t.Fatalf("保存推送订阅失败: %v", err)
	}
	saved, err := database.ListPushSubscriptions(ctx)
	if err != nil || len(saved) != 1 {
		t.Fatalf("读取推送订阅失败: %#v, %v", saved, err)
	}
	if saved[0].P256DH == "browser-public-key" || saved[0].Auth == "browser-auth-secret" {
		t.Fatal("浏览器推送认证字段未加密")
	}

	// 使用精确函数签名替换发送器，验证成功发送会更新设备。
	service.send = func(_ context.Context, payload []byte, _ *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
		if !strings.Contains(string(payload), "余额不足") {
			t.Fatalf("通知载荷不正确: %s", payload)
		}
		if options.HTTPClient == nil {
			t.Fatal("推送请求未使用受限公网客户端")
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err := service.Send(ctx, Notification{Title: "余额不足", Body: "当前余额 2 元", URL: "/alerts/1", Tag: "alert-1"}); err != nil {
		t.Fatalf("发送通知失败: %v", err)
	}

	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusGone, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err := service.Send(ctx, Notification{Title: "测试", Tag: "test"}); err != nil {
		t.Fatalf("清理失效订阅失败: %v", err)
	}
	count, err := database.CountPushSubscriptions(ctx)
	if err != nil || count != 0 {
		t.Fatalf("失效订阅未删除: %d, %v", count, err)
	}

	reloaded := NewService(database, vault, "https://monitor.example.com")
	if err := reloaded.EnsureKeys(ctx); err != nil || reloaded.PublicKey() != publicKey {
		t.Fatalf("重启后推送密钥未保持: %v", err)
	}
}

func TestPushSubscription拒绝非公网地址(t *testing.T) {
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	vault, err := secure.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("创建保险箱失败: %v", err)
	}
	service := NewService(database, vault, "https://monitor.example.com")
	err = service.Subscribe(context.Background(), SubscriptionInput{
		Endpoint: "https://127.0.0.1/subscription/one", P256DH: "browser-public-key", Auth: "browser-auth-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "公网") {
		t.Fatalf("非公网推送地址未被拒绝: %v", err)
	}
}

type staticResolver struct {
	addresses []net.IPAddr
	err       error
}

func (resolver staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return resolver.addresses, resolver.err
}
