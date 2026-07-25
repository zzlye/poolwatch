package notifications

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"poolwatch/internal/alerts"
	"poolwatch/internal/mailnotify"
	"poolwatch/internal/push"
	"poolwatch/internal/store"
)

const (
	channelWebPush = "web_push"
	channelEmail   = "email"
)

var deliveryURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

type pushSender interface {
	DeviceIDs(context.Context) ([]string, error)
	SendDevice(context.Context, string, push.Notification) error
}

type emailSender interface {
	Enabled(context.Context) (bool, error)
	SendAlert(context.Context, mailnotify.AlertMessage) error
}

type eventPublisher interface {
	Publish(string, any)
}

// Dispatcher 将告警分发给 SSE、Web Push 和邮箱，并记录各可靠通道的独立投递状态。
type Dispatcher struct {
	store  *store.Store
	push   pushSender
	email  emailSender
	events eventPublisher
	logger *slog.Logger
	now    func() time.Time
	locks  [64]sync.Mutex
}

// NewDispatcher 创建多通道通知分发器。
func NewDispatcher(database *store.Store, pushService pushSender, emailService emailSender, events eventPublisher, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Dispatcher{
		store: database, push: pushService, email: emailService, events: events, logger: logger,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Notify 实现告警通知器；已成功的可靠通道在后续重试中会被跳过。
func (d *Dispatcher) Notify(ctx context.Context, notification alerts.Notification) error {
	lock := d.lockFor(notification.AlertID)
	lock.Lock()
	defer lock.Unlock()

	if d.events != nil {
		// SSE 只负责通知在线页面刷新数据，不参与可靠投递完成判定。
		d.events.Publish("alert", notification)
	}
	var failures []error
	if d.push != nil {
		deviceIDs, err := d.push.DeviceIDs(ctx)
		if err != nil {
			d.logWarning("读取浏览器推送设备失败", notification.AlertID, channelWebPush, "", err)
			failures = append(failures, fmt.Errorf("读取浏览器推送设备失败: %w", err))
		} else if err := d.deliverPush(ctx, notification, deviceIDs); err != nil {
			failures = append(failures, err)
		}
	}
	if d.email != nil {
		enabled, err := d.email.Enabled(ctx)
		if err != nil {
			d.logWarning("读取邮箱提醒状态失败", notification.AlertID, channelEmail, "", err)
			failures = append(failures, fmt.Errorf("读取邮箱提醒状态失败: %w", err))
		} else if enabled {
			if err := d.deliver(ctx, notification.AlertID, channelEmail, "", func(channelContext context.Context) error {
				return d.email.SendAlert(channelContext, mailnotify.AlertMessage{
					AlertID: notification.AlertID, TargetName: notification.TargetName, Title: notification.Title,
					Message: notification.Message, Severity: notification.Severity, Recovered: notification.Recovered,
					OccurredAt: notification.OccurredAt,
				})
			}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (d *Dispatcher) deliverPush(ctx context.Context, notification alerts.Notification, deviceIDs []string) error {
	payload := push.Notification{
		Title: notification.Title, Body: notification.Message,
		URL: "/alerts?focus=" + notification.AlertID, Tag: notification.AlertID, Severity: notification.Severity,
	}
	uniqueDevices := make(map[string]struct{}, len(deviceIDs))
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(deviceIDs))
	for _, rawDeviceID := range deviceIDs {
		deviceID := strings.TrimSpace(rawDeviceID)
		if deviceID == "" {
			continue
		}
		if _, exists := uniqueDevices[deviceID]; exists {
			continue
		}
		uniqueDevices[deviceID] = struct{}{}
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := d.deliver(ctx, notification.AlertID, channelWebPush, deviceID, func(channelContext context.Context) error {
				return d.push.SendDevice(channelContext, deviceID, payload)
			}); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	failures := make([]error, 0, len(errorsFound))
	for err := range errorsFound {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (d *Dispatcher) deliver(ctx context.Context, alertID, channel, destinationID string, send func(context.Context) error) error {
	delivered, err := d.store.NotificationDelivered(ctx, alertID, channel, destinationID)
	if err != nil {
		d.logWarning("读取通知投递状态失败", alertID, channel, destinationID, err)
		return fmt.Errorf("读取%s投递状态失败: %w", channelLabel(channel), err)
	}
	if delivered {
		return nil
	}
	channelContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sendErr := send(channelContext)
	now := d.now()
	if sendErr != nil {
		d.logWarning("通知投递失败", alertID, channel, destinationID, sendErr)
		recordErr := d.recordAttempt(ctx, alertID, channel, destinationID, false, safeDeliveryError(sendErr), now)
		if recordErr != nil {
			d.logWarning("通知投递状态落库失败", alertID, channel, destinationID, recordErr)
			return errors.Join(
				fmt.Errorf("%s发送失败: %w", channelLabel(channel), sendErr),
				fmt.Errorf("记录%s投递结果失败: %w", channelLabel(channel), recordErr),
			)
		}
		return fmt.Errorf("%s发送失败: %w", channelLabel(channel), sendErr)
	}
	if err := d.recordAttempt(ctx, alertID, channel, destinationID, true, "", now); err != nil {
		d.logWarning("通知投递状态落库失败", alertID, channel, destinationID, err)
		return fmt.Errorf("记录%s投递结果失败: %w", channelLabel(channel), err)
	}
	return nil
}

func (d *Dispatcher) recordAttempt(ctx context.Context, alertID, channel, destinationID string, delivered bool, errorText string, attemptedAt time.Time) error {
	// 发送完成后使用独立短上下文保存结果，避免调用方取消导致已经送达的通知再次发送。
	persistenceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return d.store.RecordNotificationAttempt(
		persistenceContext, alertID, channel, destinationID, delivered, errorText, attemptedAt,
	)
}

func (d *Dispatcher) logWarning(message, alertID, channel, destinationID string, err error) {
	attributes := []any{"告警ID", alertID, "通道", channelLabel(channel)}
	if destinationID != "" {
		attributes = append(attributes, "设备ID", destinationID)
	}
	attributes = append(attributes, "错误", safeDeliveryError(err))
	d.logger.Warn(message, attributes...)
}

func (d *Dispatcher) lockFor(alertID string) *sync.Mutex {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(alertID))
	return &d.locks[hasher.Sum32()%uint32(len(d.locks))]
}

func channelLabel(channel string) string {
	if channel == channelEmail {
		return "邮箱提醒"
	}
	return "浏览器推送"
}

func safeDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(err.Error()))
	// 通知服务错误可能携带含令牌的完整地址，日志和持久化状态只保留脱敏占位。
	value = deliveryURLPattern.ReplaceAllString(value, "[地址已隐藏]")
	characters := []rune(value)
	if len(characters) > 300 {
		value = string(characters[:300])
	}
	return value
}
