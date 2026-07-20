package monitor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// TargetKind 表示受监控渠道的类型。
type TargetKind string

const (
	TargetKindNewAPI      TargetKind = "new_api"
	TargetKindSub2API     TargetKind = "sub2api"
	TargetKindChatGPT2API TargetKind = "chatgpt2api"
	TargetKindCLIProxyAPI TargetKind = "cliproxyapi"
	TargetKindCustom      TargetKind = "custom"
	TargetKindCustomHTTP  TargetKind = TargetKindCustom
)

// ThresholdComparison 表示指标触发告警时使用的比较方向。
type ThresholdComparison string

const (
	ThresholdComparisonLTE ThresholdComparison = "lte"
	ThresholdComparisonGTE ThresholdComparison = "gte"
)

// MetricKey 表示不同渠道之间可统一识别的指标。
type MetricKey string

const (
	MetricWalletBalance       MetricKey = "wallet_balance"
	MetricSubscriptionBalance MetricKey = "subscription_balance"
	MetricImageQuota          MetricKey = "image_quota"
	MetricAccountTotal        MetricKey = "account_total"
	MetricHealthyAccounts     MetricKey = "healthy_accounts"
	MetricLimitedAccounts     MetricKey = "limited_accounts"
	MetricErrorAccounts       MetricKey = "error_accounts"
	MetricDisabledAccounts    MetricKey = "disabled_accounts"
	// 账号累计统计来自 chatgpt2api 的只读健康端点。
	MetricAccountSuccess  MetricKey = "account_success"
	MetricAccountFail     MetricKey = "account_fail"
	MetricCustomValue     MetricKey = "custom_value"
	MetricAccountActive   MetricKey = MetricHealthyAccounts
	MetricAccountLimited  MetricKey = MetricLimitedAccounts
	MetricAccountAbnormal MetricKey = MetricErrorAccounts
	MetricAccountDisabled MetricKey = MetricDisabledAccounts
)

// TargetStatus 表示一次检测得到的渠道状态。
type TargetStatus string

const (
	TargetStatusHealthy  TargetStatus = "healthy"
	TargetStatusWarning  TargetStatus = "warning"
	TargetStatusDegraded TargetStatus = TargetStatusWarning
	TargetStatusError    TargetStatus = "error"
	TargetStatusDisabled TargetStatus = "disabled"
	TargetStatusUnknown  TargetStatus = "unknown"
)

// AlertType 表示上层告警状态机使用的事件类型。
type AlertType string

const (
	AlertTypeQuotaLow          AlertType = "threshold"
	AlertTypeCredentialInvalid AlertType = "credential"
	AlertTypeConnectivity      AlertType = "unreachable"
	AlertTypeRecovered         AlertType = "recovered"
)

// AuthMode 表示自定义 HTTP 渠道的认证方式。
type AuthMode string

const (
	AuthModeNone   AuthMode = "none"
	AuthModeBearer AuthMode = "bearer"
	AuthModeBasic  AuthMode = "basic"
	AuthModeHeader AuthMode = "header"
)

// Credential 保存适配器运行时需要的凭据，调用方必须加密持久化。
type Credential struct {
	Username      string            `json:"username,omitempty"`
	Email         string            `json:"email,omitempty"`
	Password      string            `json:"password,omitempty"`
	TOTPCode      string            `json:"totp_code,omitempty"`
	TOTPSecret    string            `json:"totp_secret,omitempty"`
	AccessToken   string            `json:"access_token,omitempty"`
	RefreshToken  string            `json:"refresh_token,omitempty"`
	UserID        string            `json:"user_id,omitempty"`
	Cookie        string            `json:"cookie,omitempty"`
	AdminKey      string            `json:"admin_key,omitempty"`
	BearerToken   string            `json:"bearer_token,omitempty"`
	BasicUsername string            `json:"basic_username,omitempty"`
	BasicPassword string            `json:"basic_password,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
}

// NewAPIConfig 保存 New API 专属检测选项。
type NewAPIConfig struct {
	IncludeSubscription bool `json:"include_subscription"`
}

// ChatGPT2APIConfig 保存 chatgpt2api 专属检测选项。
type ChatGPT2APIConfig struct {
	IncludeAccounts bool `json:"include_accounts"`
}

// CustomMetricMapping 描述一个自定义 JSON 数值字段。
type CustomMetricMapping struct {
	Key     MetricKey `json:"key"`
	Label   string    `json:"label"`
	Pointer string    `json:"pointer"`
	Unit    string    `json:"unit"`
}

// CustomHTTPConfig 保存自定义 HTTP 请求和字段映射。
type CustomHTTPConfig struct {
	Method        string                `json:"method"`
	ConfirmPOST   bool                  `json:"confirm_post"`
	Body          json.RawMessage       `json:"body,omitempty"`
	AuthMode      AuthMode              `json:"auth_mode"`
	Metrics       []CustomMetricMapping `json:"metrics"`
	StatusPointer string                `json:"status_pointer,omitempty"`
	HealthyValues []string              `json:"healthy_values,omitempty"`
}

// TargetConfig 是适配器检测单个渠道所需的完整配置。
type TargetConfig struct {
	ID                   string                            `json:"id"`
	Name                 string                            `json:"name"`
	Kind                 TargetKind                        `json:"kind"`
	BaseURL              string                            `json:"base_url"`
	AllowPrivateNetwork  bool                              `json:"allow_private_network"`
	Thresholds           map[MetricKey]decimal.Decimal     `json:"thresholds,omitempty"`
	ThresholdComparisons map[MetricKey]ThresholdComparison `json:"threshold_comparisons,omitempty"`
	Credential           Credential                        `json:"credential"`
	NewAPI               NewAPIConfig                      `json:"new_api,omitempty"`
	ChatGPT2API          ChatGPT2APIConfig                 `json:"chatgpt2api,omitempty"`
	Custom               CustomHTTPConfig                  `json:"custom,omitempty"`
}

// Metric 是以十进制字符串序列化的单项监控值。
type Metric struct {
	Key        MetricKey           `json:"key"`
	Label      string              `json:"label"`
	Value      decimal.Decimal     `json:"value"`
	Unit       string              `json:"unit"`
	Threshold  *decimal.Decimal    `json:"threshold,omitempty"`
	Comparison ThresholdComparison `json:"comparison,omitempty"`
}

// AccountStatus 是号池账号明细的严格白名单视图。
type AccountStatus struct {
	ExternalID  string          `json:"-"`
	DisplayName string          `json:"display_name,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	Email       string          `json:"email,omitempty"`
	Type        string          `json:"type,omitempty"`
	Status      string          `json:"status"`
	StatusText  string          `json:"status_text,omitempty"`
	Quota       decimal.Decimal `json:"quota"`
	RecoveryAt  string          `json:"recovery_at,omitempty"`
	// RestoreAt 只为兼容旧适配器和历史测试保留，新代码统一使用 RecoveryAt。
	RestoreAt     string `json:"-"`
	Success       int64  `json:"success"`
	Fail          int64  `json:"fail"`
	ImageInflight int64  `json:"image_inflight"`
}

// Snapshot 表示一次只读检测结果，不包含任何凭据或原始响应。
type Snapshot struct {
	TargetID         string          `json:"target_id"`
	Kind             TargetKind      `json:"kind"`
	Status           TargetStatus    `json:"status"`
	ObservedAt       time.Time       `json:"observed_at"`
	Metrics          []Metric        `json:"metrics"`
	Accounts         []AccountStatus `json:"accounts,omitempty"`
	Message          string          `json:"message,omitempty"`
	CredentialUpdate *Credential     `json:"-"`
}

// Adapter 定义所有渠道适配器统一的只读检测接口。
type Adapter interface {
	Kind() TargetKind
	Check(ctx context.Context, target TargetConfig) (Snapshot, error)
}

// TargetInput 是主线调度器传给检测器的输入类型。
type TargetInput = TargetConfig

// Result 是主线调度器接收的一次检测结果。
type Result = Snapshot

// Runner 是主线调度器使用的最小检测接口。
type Runner interface {
	Run(ctx context.Context, target TargetInput) (Result, error)
}

// Prober 是连接测试接口使用的临时响应探测契约。
type Prober interface {
	Probe(ctx context.Context, target TargetInput) (Result, any, error)
}

// BrowserCredentialVerifier 校验浏览器授权流程捕获的渠道凭据，并返回可持久化的规范化凭据。
type BrowserCredentialVerifier interface {
	VerifyBrowserCredential(ctx context.Context, target TargetInput) (Credential, error)
}

// Detector 根据只读公开端点识别渠道类型。
type Detector interface {
	Detect(ctx context.Context, baseURL string, allowPrivate bool) (TargetKind, error)
}

func metricWithThreshold(target TargetConfig, key MetricKey, label string, value decimal.Decimal, unit string) Metric {
	metric := Metric{Key: key, Label: label, Value: value, Unit: unit}
	if threshold, ok := target.Thresholds[key]; ok {
		copyValue := threshold
		metric.Threshold = &copyValue
		metric.Comparison = NormalizeThresholdComparison(target.ThresholdComparisons[key])
	}
	return metric
}

// NormalizeThresholdComparison 兼容旧配置，空值和未知值都按低于等于阈值处理。
func NormalizeThresholdComparison(comparison ThresholdComparison) ThresholdComparison {
	if comparison == ThresholdComparisonGTE {
		return ThresholdComparisonGTE
	}
	return ThresholdComparisonLTE
}

// ThresholdBreached 判断指标是否已经按配置方向达到告警阈值。
func ThresholdBreached(metric Metric) bool {
	if metric.Threshold == nil {
		return false
	}
	if NormalizeThresholdComparison(metric.Comparison) == ThresholdComparisonGTE {
		return metric.Value.GreaterThanOrEqual(*metric.Threshold)
	}
	return metric.Value.LessThanOrEqual(*metric.Threshold)
}

func newSnapshot(target TargetConfig) Snapshot {
	return Snapshot{
		TargetID:   target.ID,
		Kind:       target.Kind,
		Status:     TargetStatusHealthy,
		ObservedAt: time.Now().UTC(),
		Metrics:    make([]Metric, 0),
	}
}
