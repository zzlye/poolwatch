import type {
  Alert,
  BootstrapState,
  DashboardData,
  DetectTargetResult,
  HistoryResult,
  PushInfo,
  SanitizedAccount,
  Settings,
  Target,
  TargetAuthAttempt,
  TargetDraft,
  TargetStatus,
  TestConnectionResult,
  TotpSetup
} from '../types'
import { targetKindLabels } from '../types'

const now = Date.now()
const minutesAgo = (minutes: number) => new Date(now - minutes * 60_000).toISOString()

function makeMockChatAccounts(total: number): SanitizedAccount[] {
  const types = ['free', 'plus', 'team']
  const statuses: TargetStatus[] = ['healthy', 'warning', 'error', 'disabled']
  // 模拟足够多的账号，便于在独立前端验收筛选和分页。
  return Array.from({ length: total }, (_, index) => ({
    id: `account-${index + 1}`,
    email: `demo${String(index + 1).padStart(2, '0')}***@example.com`,
    type: types[index % types.length],
    status: statuses[index % statuses.length],
    imageQuota: String((index * 7) % 45),
    recoveryAt: statuses[index % statuses.length] === 'healthy' ? undefined : new Date(now + (index + 1) * 12 * 60_000).toISOString()
  }))
}

let targets: Target[] = [
  {
    id: 'new-api-main',
    name: '主站额度',
    kind: 'new_api',
    baseUrl: 'https://api.example.com',
    topupUrl: 'https://api.example.com/console/topup',
    status: 'healthy',
    statusText: '运行正常',
    enabled: true,
    checkIntervalMinutes: 5,
    lastCheckedAt: minutesAgo(2),
    nextCheckAt: new Date(now + 3 * 60_000).toISOString(),
    authConfigured: true,
    metrics: [
      { key: 'wallet_balance', label: '钱包余额', value: '126.80', unit: '元', threshold: '30', status: 'healthy' },
      { key: 'subscription_balance', label: '订阅余额', value: '48.20', unit: 'USD', threshold: '10', status: 'healthy' }
    ]
  },
  {
    id: 'sub2api-backup',
    name: '备用订阅站',
    kind: 'sub2api',
    baseUrl: 'https://sub.example.com',
    topupUrl: 'https://sub.example.com/purchase',
    status: 'warning',
    statusText: '余额接近阈值',
    enabled: true,
    checkIntervalMinutes: 10,
    lastCheckedAt: minutesAgo(7),
    nextCheckAt: new Date(now + 3 * 60_000).toISOString(),
    authConfigured: true,
    metrics: [
      { key: 'wallet_balance', label: '钱包余额', value: '18.20', unit: '元', threshold: '20', status: 'warning' }
    ]
  },
  {
    id: 'chat-pool',
    name: 'ChatGPT 号池',
    kind: 'chatgpt2api',
    baseUrl: 'https://pool.example.com',
    status: 'healthy',
    statusText: '账号池稳定',
    enabled: true,
    checkIntervalMinutes: 5,
    lastCheckedAt: minutesAgo(1),
    nextCheckAt: new Date(now + 4 * 60_000).toISOString(),
    authConfigured: true,
    metrics: [
      { key: 'image_quota', label: '图片额度', value: '284', unit: '次', threshold: '80', status: 'healthy' },
      { key: 'healthy_accounts', label: '正常账号', value: '12', unit: '个', status: 'healthy' },
      { key: 'limited_accounts', label: '限流账号', value: '2', unit: '个', status: 'warning' },
      { key: 'error_accounts', label: '异常账号', value: '0', unit: '个', status: 'healthy' }
    ],
    accounts: makeMockChatAccounts(23)
  }
]

let alerts: Alert[] = [
  {
    id: 'alert-1',
    targetId: 'sub2api-backup',
    targetName: '备用订阅站',
    type: 'threshold',
    title: '钱包余额不足',
    message: '当前余额 18.20 元，已低于阈值 20.00 元。',
    severity: 'warning',
    status: 'open',
    createdAt: minutesAgo(23)
  },
  {
    id: 'alert-2',
    targetId: 'new-api-main',
    targetName: '主站额度',
    type: 'recovered',
    title: '连接已恢复',
    message: '连续检测已恢复正常。',
    severity: 'info',
    status: 'resolved',
    createdAt: minutesAgo(180),
    resolvedAt: minutesAgo(174)
  }
]

let settings: Settings = {
  productName: '号池监控',
  historyRetentionDays: 7,
  defaultCheckIntervalMinutes: 5,
  allowPrivateTargets: false,
  totpEnabled: false
}

const targetAuthAttempts = new Map<string, TargetAuthAttempt>()

const pushInfo: PushInfo = {
  supported: true,
  vapidPublicKey: '',
  devices: [
    {
      id: 'device-1',
      name: '当前浏览器',
      userAgent: 'Windows · Edge',
      createdAt: minutesAgo(1440),
      lastSeenAt: minutesAgo(3),
      current: true
    }
  ]
}

function makeHistory(target: Target, metricKey?: string): HistoryResult {
  const metric = target.metrics.find((item) => item.key === metricKey) ?? target.metrics.find((item) => item.threshold) ?? target.metrics[0]
  const baseValue = Number(metric?.value || 0)
  const snapshots = Array.from({ length: 14 }, (_, index) => ({
    id: `${target.id}-${index}`,
    targetId: target.id,
    metricKey: metric?.key ?? 'wallet_balance',
    value: Math.max(0, baseValue + Math.sin(index / 2) * Math.max(baseValue * 0.12, 2) - (13 - index) * 0.35).toFixed(2),
    unit: metric?.unit ?? '元',
    measuredAt: new Date(now - (13 - index) * 6 * 60 * 60_000).toISOString()
  }))
  return { target, snapshots }
}

function targetFromDraft(draft: TargetDraft, id: string = crypto.randomUUID()): Target {
  return {
    id,
    name: draft.name,
    kind: draft.kind,
    baseUrl: draft.baseUrl,
    topupUrl: draft.topupUrl || undefined,
    status: 'unknown',
    statusText: '等待首次检测',
    enabled: draft.enabled,
    checkIntervalMinutes: draft.checkIntervalMinutes,
    authConfigured: Boolean(draft.password || draft.accessToken || draft.cookie || draft.browserAuthAttemptId || draft.adminKey || draft.totpSecret || draft.authType === 'none'),
    credentialMode: draft.credentialMode,
    metrics: draft.thresholds.map((threshold) => ({
      key: threshold.key,
      label: threshold.label,
      value: '0',
      unit: threshold.unit,
      threshold: threshold.value,
      status: 'unknown'
    }))
  }
}

// 模拟层只在显式开启时使用，生产构建不会静默伪造监控结果。
export async function mockRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  await new Promise((resolve) => window.setTimeout(resolve, 80))
  const method = init.method ?? 'GET'
  const body = init.body ? JSON.parse(String(init.body)) : undefined
  const cleanPath = path.split('?')[0]

  if (cleanPath === '/api/bootstrap') return { initialized: true, authenticated: true, productName: settings.productName, totpEnabled: settings.totpEnabled } as T
  if (cleanPath === '/api/setup' || cleanPath === '/api/session') return { ok: true } as T
  if (cleanPath === '/api/dashboard') {
    const data: DashboardData = {
      summary: {
        totalTargets: targets.length,
        healthyTargets: targets.filter((item) => item.status === 'healthy').length,
        warningTargets: targets.filter((item) => item.status === 'warning').length,
        openAlerts: alerts.filter((item) => item.status === 'open').length,
        pushDevices: pushInfo.devices.length
      },
      targets,
      alerts: alerts.slice(0, 4),
      lastUpdatedAt: new Date().toISOString()
    }
    return data as T
  }
  if (cleanPath === '/api/targets' && method === 'GET') return targets as T
  if (cleanPath === '/api/targets' && method === 'POST') {
    const target = targetFromDraft(body)
    targets = [target, ...targets]
    return target as T
  }
  if (cleanPath === '/api/targets/detect') {
    const address = String(body.baseUrl ?? '').toLowerCase()
    const kind = address.includes('sub')
      ? 'sub2api'
      : address.includes('chat') || address.includes('pool')
        ? 'chatgpt2api'
        : address.includes('api')
          ? 'new_api'
          : 'custom'
    const result: DetectTargetResult = { kind, message: `已识别为 ${targetKindLabels[kind]}` }
    return result as T
  }
  if (cleanPath === '/api/target-auth/attempts' && method === 'POST') {
    const id = `auth_${crypto.randomUUID().replace(/-/g, '').slice(0, 32)}`
    const attempt: TargetAuthAttempt = {
      id,
      status: 'waiting',
      loginUrl: String(body.baseUrl ?? ''),
      expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
      message: '请在渠道页面完成登录。'
    }
    targetAuthAttempts.set(id, attempt)
    return attempt as T
  }
  if (cleanPath.startsWith('/api/target-auth/attempts/')) {
    const id = decodeURIComponent(cleanPath.split('/')[4] ?? '')
    const attempt = targetAuthAttempts.get(id)
    if (!attempt) throw new Error('网页登录任务不存在或已经过期')
    if (method === 'DELETE') {
      targetAuthAttempts.set(id, { ...attempt, status: 'cancelled', message: '网页登录已取消。' })
      return { ok: true } as T
    }
    return attempt as T
  }
  if (cleanPath === '/api/targets/test') {
    const result: TestConnectionResult = {
      ok: true,
      detectedKind: body.kind === 'custom' ? undefined : body.kind,
      message: '连接成功，已读取可用指标。',
      sample: { data: { balance: '86.50', status: 'active', quota: 240 } },
      metrics: [{ key: 'wallet_balance', label: '钱包余额', value: '86.50', unit: '元', status: 'healthy' }]
    }
    return result as T
  }
  if (cleanPath === '/api/checks') return { ok: true } as T
  if (cleanPath.startsWith('/api/targets/')) {
    const parts = cleanPath.split('/')
    const id = decodeURIComponent(parts[3] ?? '')
    const target = targets.find((item) => item.id === id)
    if (!target) throw new Error('未找到渠道')
    if (parts[4] === 'history') {
      const metric = new URL(path, window.location.origin).searchParams.get('metric') ?? undefined
      return makeHistory(target, metric) as T
    }
    if (parts[4] === 'check') return { ok: true } as T
    if (method === 'PUT') {
      const next = targetFromDraft(body, id)
      targets = targets.map((item) => (item.id === id ? next : item))
      return next as T
    }
    if (method === 'DELETE') {
      targets = targets.filter((item) => item.id !== id)
      return { ok: true } as T
    }
    return target as T
  }
  if (cleanPath === '/api/alerts') return alerts as T
  if (cleanPath.startsWith('/api/alerts/') && method === 'PATCH') {
    const id = cleanPath.split('/')[3]
    alerts = alerts.map((item) => (item.id === id ? { ...item, status: 'acknowledged' } : item))
    return alerts.find((item) => item.id === id) as T
  }
  if (cleanPath === '/api/settings' && method === 'GET') return settings as T
  if (cleanPath === '/api/settings' && method === 'PUT') {
    settings = { ...settings, ...body }
    return settings as T
  }
  if (cleanPath === '/api/push') return pushInfo as T
  if (cleanPath.startsWith('/api/push/')) return { ok: true } as T
  if (cleanPath === '/api/security/totp/start') {
    const result: TotpSetup = { secret: 'JBSWY3DPEHPK3PXP', otpauthUrl: 'otpauth://totp/pool-monitor', recoveryCodes: ['K4R9-N2VT-7QPA', 'B7Q3-X8CW-4MTR', 'M5PA-Y6DF-2KZH'] }
    return result as T
  }
  if (cleanPath === '/api/security/totp/confirm') {
    settings = { ...settings, totpEnabled: true }
    return { recoveryCodes: body.recoveryCodes ?? [] } as T
  }
	if (cleanPath === '/api/security/totp' && method === 'DELETE') {
		settings = { ...settings, totpEnabled: false }
		return { ok: true } as T
	}
  throw new Error(`模拟接口尚未实现：${method} ${cleanPath}`)
}

export const mockBootstrap: BootstrapState = {
  initialized: true,
  authenticated: true,
  productName: '号池监控',
  totpEnabled: false
}
