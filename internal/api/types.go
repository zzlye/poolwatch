package api

import (
	"time"

	"github.com/shopspring/decimal"

	"poolwatch/internal/monitor"
)

type bootstrapResponse struct {
	Initialized   bool   `json:"initialized"`
	Authenticated bool   `json:"authenticated"`
	ProductName   string `json:"productName"`
	TOTPEnabled   bool   `json:"totpEnabled"`
}

type thresholdDraft struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Value        string `json:"value"`
	Unit         string `json:"unit"`
	Comparison   string `json:"comparison,omitempty"`
	AlertEnabled *bool  `json:"alertEnabled,omitempty"`
}

type credentialMode string

const (
	credentialModeNewAPIPassword       credentialMode = "password"
	credentialModeNewAPIAccessToken    credentialMode = "access_token"
	credentialModeNewAPIBrowserSession credentialMode = "browser_session"
	credentialModeSub2APIPassword      credentialMode = "password"
	credentialModeSub2APIAccessToken   credentialMode = "access_token"
	credentialModeSub2APIBrowserOAuth  credentialMode = "browser_oauth"
)

type targetDraft struct {
	Name                 string           `json:"name"`
	Kind                 string           `json:"kind"`
	BaseURL              string           `json:"baseUrl"`
	TopupURL             string           `json:"topupUrl"`
	Enabled              bool             `json:"enabled"`
	CheckIntervalMinutes int              `json:"checkIntervalMinutes"`
	Username             string           `json:"username"`
	Email                string           `json:"email"`
	Password             string           `json:"password"`
	TOTPCode             string           `json:"totpCode"`
	TOTPSecret           string           `json:"totpSecret"`
	AccessToken          string           `json:"accessToken"`
	RefreshToken         string           `json:"refreshToken"`
	CredentialMode       credentialMode   `json:"credentialMode"`
	Cookie               string           `json:"cookie"`
	BrowserAuthAttemptID string           `json:"browserAuthAttemptId"`
	AdminKey             string           `json:"adminKey"`
	UserID               string           `json:"userId"`
	AuthType             string           `json:"authType"`
	RequestMethod        string           `json:"requestMethod"`
	ConfirmPOST          bool             `json:"confirmPost"`
	CustomHeaders        string           `json:"customHeaders"`
	JSONPointer          string           `json:"jsonPointer"`
	StatusPointer        string           `json:"statusPointer"`
	Thresholds           []thresholdDraft `json:"thresholds"`
}

type metricResponse struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Value          string `json:"value"`
	Unit           string `json:"unit"`
	Threshold      string `json:"threshold,omitempty"`
	AlertThreshold string `json:"alertThreshold,omitempty"`
	AlertEnabled   bool   `json:"alertEnabled"`
	Comparison     string `json:"comparison,omitempty"`
	Status         string `json:"status"`
}

type accountQuotaWindowResponse struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	RemainingPercent string `json:"remainingPercent,omitempty"`
	ResetAt          string `json:"resetAt,omitempty"`
}

type accountResponse struct {
	ID                    string                       `json:"id"`
	DisplayName           string                       `json:"displayName,omitempty"`
	Provider              string                       `json:"provider,omitempty"`
	Email                 string                       `json:"email"`
	Type                  string                       `json:"type"`
	Status                string                       `json:"status"`
	StatusText            string                       `json:"statusText,omitempty"`
	ImageQuota            string                       `json:"imageQuota,omitempty"`
	QuotaState            string                       `json:"quotaState,omitempty"`
	QuotaWindows          []accountQuotaWindowResponse `json:"quotaWindows,omitempty"`
	SubscriptionExpiresAt string                       `json:"subscriptionExpiresAt,omitempty"`
	RecoveryAt            string                       `json:"recoveryAt,omitempty"`
	Success               int64                        `json:"success"`
	Fail                  int64                        `json:"fail"`
}

type accountQuotaRefreshRequest struct {
	AccountIDs []string `json:"accountIds"`
}

type accountQuotaRefreshResponse struct {
	Accounts         []accountResponse `json:"accounts"`
	RefreshedCount   int               `json:"refreshedCount"`
	UnavailableCount int               `json:"unavailableCount"`
	UnsupportedCount int               `json:"unsupportedCount"`
}

type targetResponse struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Kind                 string            `json:"kind"`
	BaseURL              string            `json:"baseUrl"`
	TopupURL             string            `json:"topupUrl,omitempty"`
	Status               string            `json:"status"`
	StatusText           string            `json:"statusText"`
	Enabled              bool              `json:"enabled"`
	CheckIntervalMinutes int               `json:"checkIntervalMinutes"`
	LastCheckedAt        *time.Time        `json:"lastCheckedAt,omitempty"`
	NextCheckAt          *time.Time        `json:"nextCheckAt,omitempty"`
	LastError            string            `json:"lastError,omitempty"`
	AuthConfigured       bool              `json:"authConfigured"`
	CredentialMode       credentialMode    `json:"credentialMode,omitempty"`
	AuthType             string            `json:"authType,omitempty"`
	RequestMethod        string            `json:"requestMethod,omitempty"`
	ConfirmPOST          bool              `json:"confirmPost,omitempty"`
	JSONPointer          string            `json:"jsonPointer,omitempty"`
	StatusPointer        string            `json:"statusPointer,omitempty"`
	CustomHeadersSet     bool              `json:"customHeadersConfigured,omitempty"`
	Metrics              []metricResponse  `json:"metrics"`
	Accounts             []accountResponse `json:"accounts,omitempty"`
}

type alertResponse struct {
	ID         string     `json:"id"`
	TargetID   string     `json:"targetId"`
	TargetName string     `json:"targetName"`
	Type       string     `json:"type"`
	Title      string     `json:"title"`
	Message    string     `json:"message"`
	Severity   string     `json:"severity"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

type snapshotResponse struct {
	ID         string `json:"id"`
	TargetID   string `json:"targetId"`
	MetricKey  string `json:"metricKey"`
	Value      string `json:"value"`
	Unit       string `json:"unit"`
	MeasuredAt string `json:"measuredAt"`
}

type settingsResponse struct {
	ProductName                 string `json:"productName"`
	HistoryRetentionDays        int    `json:"historyRetentionDays"`
	DefaultCheckIntervalMinutes int    `json:"defaultCheckIntervalMinutes"`
	AllowPrivateTargets         bool   `json:"allowPrivateTargets"`
	TOTPEnabled                 bool   `json:"totpEnabled"`
}

type storedTargetConfig struct {
	Thresholds           map[monitor.MetricKey]decimal.Decimal             `json:"thresholds,omitempty"`
	ThresholdComparisons map[monitor.MetricKey]monitor.ThresholdComparison `json:"threshold_comparisons,omitempty"`
	ThresholdMeta        []thresholdDraft                                  `json:"threshold_meta,omitempty"`
	CredentialMode       credentialMode                                    `json:"credential_mode,omitempty"`
	NewAPI               monitor.NewAPIConfig                              `json:"new_api,omitempty"`
	ChatGPT2API          monitor.ChatGPT2APIConfig                         `json:"chatgpt2api,omitempty"`
	Custom               monitor.CustomHTTPConfig                          `json:"custom,omitempty"`
}
