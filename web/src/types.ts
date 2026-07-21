export type TargetKind = 'new_api' | 'sub2api' | 'chatgpt2api' | 'cliproxyapi' | 'custom'

export type MetricKey =
  | 'wallet_balance'
  | 'subscription_balance'
  | 'image_quota'
  | 'account_total'
  | 'healthy_accounts'
  | 'limited_accounts'
  | 'error_accounts'
  | 'disabled_accounts'

export type TargetStatus = 'healthy' | 'warning' | 'error' | 'disabled' | 'unknown'

export type AccountQuotaState = 'available' | 'unavailable' | 'unsupported'

export type AlertType = 'threshold' | 'credential' | 'unreachable' | 'recovered'

export type ThemePreference = 'system' | 'light' | 'dark'

export type CredentialMode = 'password' | 'access_token' | 'browser_session' | 'browser_oauth'

export type ThresholdComparison = 'lte' | 'gte'

export interface MetricValue {
  key: MetricKey
  label: string
  value: string
  unit: string
  threshold?: string
  alertThreshold?: string
  alertEnabled?: boolean
  comparison?: ThresholdComparison
  status: TargetStatus
}

export interface Snapshot {
  id: string
  targetId: string
  metricKey: MetricKey
  value: string
  unit: string
  measuredAt: string
}

export interface AccountQuotaWindow {
  key: string
  label: string
  remainingPercent?: string
  resetAt?: string
}

export interface SanitizedAccount {
  id: string
  displayName?: string
  email?: string
  provider?: string
  type?: string
  status: TargetStatus
  statusText?: string
  imageQuota?: string
  quotaState?: AccountQuotaState
  quotaWindows?: AccountQuotaWindow[]
  subscriptionExpiresAt?: string
  recoveryAt?: string
  success?: number
  fail?: number
}

export interface AccountQuotaRefreshResult {
  accounts: SanitizedAccount[]
  refreshedCount: number
  unavailableCount: number
  unsupportedCount: number
}

export interface Target {
  id: string
  name: string
  kind: TargetKind
  baseUrl: string
  topupUrl?: string
  status: TargetStatus
  statusText: string
  enabled: boolean
  checkIntervalMinutes: number
  lastCheckedAt?: string
  nextCheckAt?: string
  lastError?: string
  authConfigured: boolean
  credentialMode?: CredentialMode
  authType?: 'none' | 'bearer' | 'basic' | 'headers'
  requestMethod?: 'GET' | 'POST'
  confirmPost?: boolean
  jsonPointer?: string
  statusPointer?: string
  customHeadersConfigured?: boolean
  metrics: MetricValue[]
  accounts?: SanitizedAccount[]
}

export interface ThresholdDraft {
  key: MetricKey
  label: string
  value: string
  unit: string
  comparison: ThresholdComparison
  alertEnabled: boolean
}

export interface TargetDraft {
  name: string
  kind: TargetKind
  baseUrl: string
  topupUrl: string
  enabled: boolean
  checkIntervalMinutes: number
  username: string
  email: string
  password: string
  totpSecret: string
  totpCode: string
  accessToken: string
  refreshToken: string
  adminKey: string
  userId: string
  credentialMode: CredentialMode
  cookie: string
  browserAuthAttemptId: string
  authType: 'none' | 'bearer' | 'basic' | 'headers'
  requestMethod: 'GET' | 'POST'
  confirmPost: boolean
  customHeaders: string
  jsonPointer: string
  statusPointer: string
  thresholds: ThresholdDraft[]
}

export interface Alert {
  id: string
  targetId: string
  targetName: string
  type: AlertType
  title: string
  message: string
  severity: 'info' | 'warning' | 'critical'
  status: 'open' | 'acknowledged' | 'resolved'
  createdAt: string
  resolvedAt?: string
}

export interface DashboardData {
  summary: {
    totalTargets: number
    healthyTargets: number
    warningTargets: number
    openAlerts: number
    pushDevices: number
  }
  targets: Target[]
  alerts: Alert[]
  lastUpdatedAt: string
}

export interface BootstrapState {
  initialized: boolean
  authenticated: boolean
  productName: string
  totpEnabled: boolean
}

export interface PushDevice {
  id: string
  name: string
  userAgent: string
  createdAt: string
  lastSeenAt?: string
  current: boolean
}

export interface PushInfo {
  supported: boolean
  vapidPublicKey: string
  devices: PushDevice[]
}

export interface Settings {
  productName: string
  historyRetentionDays: number
  defaultCheckIntervalMinutes: number
  allowPrivateTargets: boolean
  totpEnabled: boolean
}

export interface TotpSetup {
  secret: string
  otpauthUrl: string
  recoveryCodes: string[]
}

export interface TestConnectionResult {
  ok: boolean
  detectedKind?: TargetKind
  message: string
  sample?: unknown
  metrics?: MetricValue[]
}

export interface DetectTargetResult {
  kind: TargetKind
  message: string
}

export interface TargetAuthAttempt {
  id: string
  status: 'waiting' | 'ready' | 'expired' | 'cancelled'
  loginUrl: string
  expiresAt: string
  userId?: string
  message?: string
}

export interface HistoryResult {
  target: Target
  snapshots: Snapshot[]
}

export const targetKindLabels: Record<TargetKind, string> = {
  new_api: 'New API',
  sub2api: 'Sub2API',
  chatgpt2api: 'chatgpt2api',
  cliproxyapi: 'CLIProxyAPI',
  custom: '自定义 HTTP'
}

export const metricLabels: Record<MetricKey, string> = {
  wallet_balance: '钱包余额',
  subscription_balance: '订阅额度',
  image_quota: '图片额度',
  account_total: '账号总数',
  healthy_accounts: '正常账号',
  limited_accounts: '限流账号',
  error_accounts: '异常账号',
  disabled_accounts: '禁用账号'
}
