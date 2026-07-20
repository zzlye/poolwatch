package store

import "time"

// Admin 表示系统中唯一的管理员账号。
type Admin struct {
	ID            int64
	Username      string
	PasswordHash  string
	TOTPEnabled   bool
	TOTPSecretEnc string
	CreatedAt     time.Time
	PasswordSetAt time.Time
}

// Session 表示经过哈希后持久化的登录会话。
type Session struct {
	TokenHash string
	AdminID   int64
	CSRFToken string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Target 表示一个需要定时检测的渠道。
type Target struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Kind                string    `json:"kind"`
	BaseURL             string    `json:"base_url"`
	Enabled             bool      `json:"enabled"`
	PollIntervalSeconds int       `json:"poll_interval_seconds"`
	RechargeURL         string    `json:"recharge_url,omitempty"`
	ConfigJSON          string    `json:"config_json"`
	CredentialsEnc      string    `json:"-"`
	HasCredentials      bool      `json:"has_credentials"`
	Status              string    `json:"status"`
	FailureCount        int       `json:"failure_count"`
	LastError           string    `json:"last_error,omitempty"`
	LastCheckedAt       time.Time `json:"last_checked_at,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Snapshot 保存某次检测得到的指标和脱敏详情。
type Snapshot struct {
	ID          int64     `json:"id"`
	TargetID    string    `json:"target_id"`
	ObservedAt  time.Time `json:"observed_at"`
	Status      string    `json:"status"`
	MetricsJSON string    `json:"metrics_json"`
	DetailJSON  string    `json:"detail_json,omitempty"`
}

// Alert 表示额度、凭据或可用性事件。
type Alert struct {
	ID             string     `json:"id"`
	TargetID       string     `json:"target_id"`
	Type           string     `json:"type"`
	MetricKey      string     `json:"metric_key,omitempty"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	Message        string     `json:"message"`
	CurrentValue   string     `json:"current_value,omitempty"`
	Threshold      string     `json:"threshold,omitempty"`
	Unit           string     `json:"unit,omitempty"`
	OpenedAt       time.Time  `json:"opened_at"`
	RecoveredAt    *time.Time `json:"recovered_at,omitempty"`
	LastNotifiedAt *time.Time `json:"last_notified_at,omitempty"`
}

// PushSubscription 保存浏览器推送订阅所需的公开信息。
type PushSubscription struct {
	ID         string    `json:"id"`
	Endpoint   string    `json:"-"`
	P256DH     string    `json:"-"`
	Auth       string    `json:"-"`
	DeviceName string    `json:"device_name"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// AccountQuotaWindow 是持久化后的账号额度白名单视图。
type AccountQuotaWindow struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	RemainingPercent string `json:"remaining_percent,omitempty"`
	ResetAt          string `json:"reset_at,omitempty"`
}

// ChatAccount 是号池账号的只读脱敏视图。
type ChatAccount struct {
	TargetID              string               `json:"target_id"`
	ExternalID            string               `json:"id"`
	DisplayName           string               `json:"display_name,omitempty"`
	Provider              string               `json:"provider,omitempty"`
	Email                 string               `json:"email,omitempty"`
	Type                  string               `json:"type,omitempty"`
	Status                string               `json:"status"`
	StatusText            string               `json:"status_text,omitempty"`
	Quota                 int64                `json:"quota"`
	QuotaState            string               `json:"quota_state,omitempty"`
	QuotaWindows          []AccountQuotaWindow `json:"quota_windows,omitempty"`
	SubscriptionExpiresAt string               `json:"subscription_expires_at,omitempty"`
	RestoreAt             string               `json:"restore_at,omitempty"`
	Success               int64                `json:"success"`
	Fail                  int64                `json:"fail"`
	ObservedAt            time.Time            `json:"observed_at"`
}

// AlertWithTarget 为告警列表附加渠道显示名称。
type AlertWithTarget struct {
	Alert
	TargetName string `json:"target_name"`
}
