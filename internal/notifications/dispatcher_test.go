package notifications

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"poolwatch/internal/alerts"
	"poolwatch/internal/mailnotify"
	"poolwatch/internal/push"
	"poolwatch/internal/store"
)

type fakePushSender struct {
	mu        sync.Mutex
	devices   []string
	attempts  map[string]int
	failUntil map[string]int
	failure   error
	onSend    func()
}

func (sender *fakePushSender) DeviceIDs(context.Context) ([]string, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]string(nil), sender.devices...), nil
}

func (sender *fakePushSender) SendDevice(_ context.Context, deviceID string, _ push.Notification) error {
	sender.mu.Lock()
	if sender.attempts == nil {
		sender.attempts = make(map[string]int)
	}
	sender.attempts[deviceID]++
	attempt := sender.attempts[deviceID]
	failUntil := sender.failUntil[deviceID]
	onSend := sender.onSend
	sender.mu.Unlock()
	if onSend != nil {
		onSend()
	}
	if attempt <= failUntil {
		if sender.failure != nil {
			return sender.failure
		}
		return errors.New("临时推送失败")
	}
	return nil
}

func (sender *fakePushSender) attemptCount(deviceID string) int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.attempts[deviceID]
}

type fakeEmailSender struct {
	mu        sync.Mutex
	enabled   bool
	attempts  int
	failUntil int
	messages  []mailnotify.AlertMessage
}

func (sender *fakeEmailSender) Enabled(context.Context) (bool, error) {
	return sender.enabled, nil
}

func (sender *fakeEmailSender) SendAlert(_ context.Context, message mailnotify.AlertMessage) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.attempts++
	sender.messages = append(sender.messages, message)
	if sender.attempts <= sender.failUntil {
		return errors.New("临时邮件失败")
	}
	return nil
}

type fakeEventPublisher struct {
	mu     sync.Mutex
	events int
}

func (publisher *fakeEventPublisher) Publish(_ string, _ any) {
	publisher.mu.Lock()
	publisher.events++
	publisher.mu.Unlock()
}

func TestDispatcher只重试失败的推送设备并保持状态(t *testing.T) {
	database, notification := dispatcherFixture(t)
	defer database.Close()
	pushSender := &fakePushSender{
		devices:   []string{"device_computer", "device_phone"},
		failUntil: map[string]int{"device_phone": 1},
	}
	emailSender := &fakeEmailSender{enabled: true}
	events := &fakeEventPublisher{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	dispatcher := NewDispatcher(database, pushSender, emailSender, events, logger)

	if err := dispatcher.Notify(context.Background(), notification); err == nil {
		t.Fatal("一个设备推送失败时应保留待重试状态")
	}
	// 使用新的分发器验证跳过状态来自数据库，而不是进程内缓存。
	dispatcher = NewDispatcher(database, pushSender, emailSender, events, logger)
	if err := dispatcher.Notify(context.Background(), notification); err != nil {
		t.Fatalf("重试通知失败: %v", err)
	}
	computerAttempts := pushSender.attemptCount("device_computer")
	phoneAttempts := pushSender.attemptCount("device_phone")
	if computerAttempts != 1 || phoneAttempts != 2 || emailSender.attempts != 1 {
		t.Fatalf("成功接收方被重复发送: computer=%d phone=%d email=%d", computerAttempts, phoneAttempts, emailSender.attempts)
	}
	computerStored, computerErr := database.NotificationAttemptCount(
		context.Background(), notification.AlertID, channelWebPush, "device_computer",
	)
	phoneStored, phoneErr := database.NotificationAttemptCount(
		context.Background(), notification.AlertID, channelWebPush, "device_phone",
	)
	if computerErr != nil || phoneErr != nil || computerStored != 1 || phoneStored != 2 {
		t.Fatalf("设备投递状态没有独立持久化: computer=%d phone=%d err=%v/%v", computerStored, phoneStored, computerErr, phoneErr)
	}
	if !strings.Contains(logs.String(), "通知投递失败") || !strings.Contains(logs.String(), "device_phone") {
		t.Fatalf("推送失败没有写入中文警告日志: %s", logs.String())
	}
	if len(emailSender.messages) != 1 || emailSender.messages[0].TargetName != notification.TargetName ||
		!emailSender.messages[0].OccurredAt.Equal(notification.OccurredAt) {
		t.Fatalf("邮箱通知字段缺失: %#v", emailSender.messages)
	}
}

func TestDispatcher只重试失败邮箱(t *testing.T) {
	database, notification := dispatcherFixture(t)
	defer database.Close()
	pushSender := &fakePushSender{devices: []string{"device_one"}}
	emailSender := &fakeEmailSender{enabled: true, failUntil: 1}
	dispatcher := NewDispatcher(database, pushSender, emailSender, nil, nil)

	if err := dispatcher.Notify(context.Background(), notification); err == nil {
		t.Fatal("首次邮件失败时应保留待重试状态")
	}
	if err := dispatcher.Notify(context.Background(), notification); err != nil {
		t.Fatalf("重试通知失败: %v", err)
	}
	if pushSender.attemptCount("device_one") != 1 || emailSender.attempts != 2 {
		t.Fatalf("成功推送被重复发送: push=%d email=%d", pushSender.attemptCount("device_one"), emailSender.attempts)
	}
}

func TestDispatcher跳过停用邮箱并串行化相同告警(t *testing.T) {
	database, notification := dispatcherFixture(t)
	defer database.Close()
	pushSender := &fakePushSender{devices: []string{"device_one"}}
	emailSender := &fakeEmailSender{enabled: false}
	dispatcher := NewDispatcher(database, pushSender, emailSender, nil, nil)

	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- dispatcher.Notify(context.Background(), notification)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("并发通知失败: %v", err)
		}
	}
	if pushSender.attemptCount("device_one") != 1 || emailSender.attempts != 0 {
		t.Fatalf("相同告警被重复投递或停用邮箱被调用: push=%d email=%d", pushSender.attemptCount("device_one"), emailSender.attempts)
	}
}

func TestDispatcher发送成功后使用独立上下文记录(t *testing.T) {
	database, notification := dispatcherFixture(t)
	defer database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	pushSender := &fakePushSender{devices: []string{"device_cancel"}, onSend: cancel}
	dispatcher := NewDispatcher(database, pushSender, nil, nil, nil)

	if err := dispatcher.Notify(ctx, notification); err != nil {
		t.Fatalf("发送成功后不应因原上下文取消而丢失投递状态: %v", err)
	}
	if err := dispatcher.Notify(context.Background(), notification); err != nil {
		t.Fatalf("读取已保存投递状态失败: %v", err)
	}
	if attempts := pushSender.attemptCount("device_cancel"); attempts != 1 {
		t.Fatalf("原上下文取消导致成功设备被重复发送: %d", attempts)
	}
}

func TestDispatcher日志和状态不泄露推送端点(t *testing.T) {
	database, notification := dispatcherFixture(t)
	defer database.Close()
	const secret = "SECRET_ENDPOINT_TOKEN"
	pushSender := &fakePushSender{
		devices:   []string{"device_secret"},
		failUntil: map[string]int{"device_secret": 1},
		failure:   errors.New(`Post "https://push.example.com/subscription/SECRET_ENDPOINT_TOKEN": connection reset`),
	}
	var logs bytes.Buffer
	dispatcher := NewDispatcher(
		database,
		pushSender,
		nil,
		nil,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)

	if err := dispatcher.Notify(context.Background(), notification); err == nil {
		t.Fatal("模拟推送失败没有返回错误")
	}
	var storedError string
	if err := database.DB().QueryRowContext(
		context.Background(),
		`SELECT last_error FROM alert_notification_deliveries
		 WHERE alert_id = ? AND channel = ? AND destination_id = ?`,
		notification.AlertID,
		channelWebPush,
		"device_secret",
	).Scan(&storedError); err != nil {
		t.Fatalf("读取推送失败状态失败: %v", err)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(storedError, secret) {
		t.Fatalf("日志或投递状态泄露了推送端点: log=%s stored=%s", logs.String(), storedError)
	}
	if !strings.Contains(logs.String(), "[地址已隐藏]") || !strings.Contains(storedError, "[地址已隐藏]") {
		t.Fatalf("推送端点没有被脱敏: log=%s stored=%s", logs.String(), storedError)
	}
}

func dispatcherFixture(t *testing.T) (*store.Store, alerts.Notification) {
	t.Helper()
	database, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	target := store.Target{
		ID: "target_notify", Name: "主站", Kind: "custom", BaseURL: "https://example.com",
		Enabled: true, PollIntervalSeconds: 300, ConfigJSON: "{}", Status: "warning", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateTarget(context.Background(), target); err != nil {
		database.Close()
		t.Fatalf("创建渠道失败: %v", err)
	}
	alert := store.Alert{
		ID: "alert_notify", TargetID: target.ID, Type: "threshold", State: "open",
		Title: "钱包余额不足", Message: "当前余额 1 元", OpenedAt: now,
	}
	if err := database.CreateAlert(context.Background(), alert); err != nil {
		database.Close()
		t.Fatalf("创建告警失败: %v", err)
	}
	return database, alerts.Notification{
		AlertID: alert.ID, TargetID: target.ID, TargetName: target.Name, Type: alert.Type,
		Title: alert.Title, Message: alert.Message, Severity: "warning", OccurredAt: now,
	}
}
