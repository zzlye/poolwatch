import type {
  AccountQuotaRefreshResult,
  Alert,
  BootstrapState,
  DashboardData,
  DetectTargetResult,
  EmailSettings,
  GroupMultiplier,
  GroupPriceResult,
  HistoryResult,
  PushInfo,
  SanitizedAccount,
  Settings,
  Target,
  TargetAuthAttempt,
  TargetDraft,
  TargetMultiplierState,
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

function makeMockCLIProxyAccounts(total: number): SanitizedAccount[] {
  const providers = ['OpenAI', 'Anthropic', 'Gemini']
  const types = ['OAuth', 'API Key']
  const statuses: TargetStatus[] = ['healthy', 'warning', 'error', 'disabled']
  // 同时覆盖额度已获取、暂未获取和提供商不支持三种状态，便于跨端验收。
  return Array.from({ length: total }, (_, index) => {
    const provider = providers[index % providers.length]
    const type = types[index % types.length]
    const status = statuses[index % statuses.length]
    const quotaState = provider === 'Anthropic' && type === 'API Key' ? 'unsupported' : index % 5 === 2 ? 'unavailable' : 'available'
    return {
      id: `cli-account-${index + 1}`,
      displayName: `代理账号 ${index + 1}`,
      email: `proxy${String(index + 1).padStart(2, '0')}***@example.com`,
      provider,
      type,
      status,
      statusText: status === 'warning' ? '限流' : undefined,
      quotaState,
      quotaWindows: quotaState === 'available' ? [
        { key: 'short', label: provider === 'Gemini' ? 'Gemini 2.5 Pro' : provider === 'Anthropic' ? '5 小时' : '5 小时额度', remainingPercent: String(Math.max(4, 96 - index * 4)), resetAt: new Date(now + (index + 1) * 30 * 60_000).toISOString() },
        { key: 'weekly', label: '每周额度', remainingPercent: String(Math.max(8, 88 - index * 3)), resetAt: new Date(now + (index + 1) * 24 * 60 * 60_000).toISOString() },
        ...(index === 0 ? [{ key: 'review', label: '代码审查额度', remainingPercent: '67.5', resetAt: new Date(now + 2 * 24 * 60 * 60_000).toISOString() }] : [])
      ] : undefined,
      subscriptionExpiresAt: provider === 'OpenAI' ? new Date(now + 30 * 24 * 60 * 60_000).toISOString() : undefined,
      recoveryAt: status === 'warning' ? new Date(now + (index + 1) * 10 * 60_000).toISOString() : undefined,
      success: 100 + index * 7,
      fail: index % 5
    }
  })
}

function makeMockSub2APIAccounts(total: number): SanitizedAccount[] {
  const statuses: TargetStatus[] = ['healthy', 'warning', 'error', 'disabled']
  // 覆盖订阅被动额度、内部金额额度和不支持额度三类 Sub2API 账号。
  return Array.from({ length: total }, (_, index) => {
    const subscriptionAccount = index % 3 === 0
    const internalBudgetAccount = index % 3 === 1
    const status = statuses[index % statuses.length]
    const quotaState = subscriptionAccount || internalBudgetAccount ? (index % 7 === 3 ? 'unavailable' : 'available') : 'unsupported'
    return {
      id: `sub2-account-${index + 1}`,
      displayName: `上游账号 ${index + 1}`,
      provider: subscriptionAccount || internalBudgetAccount ? 'Anthropic' : 'OpenAI',
      type: subscriptionAccount ? (index % 2 === 0 ? 'OAuth' : 'Setup Token') : internalBudgetAccount ? (index % 2 === 0 ? 'API Key' : 'Bedrock') : 'OAuth',
      status,
      statusText: status === 'warning' ? '限流或冷却中' : undefined,
      quotaState,
      quotaWindows: quotaState !== 'available' ? undefined : subscriptionAccount ? [
        { key: 'five-hour', label: '5 小时', remainingPercent: String(Math.max(5, 92 - index * 4)), resetAt: new Date(now + (index + 1) * 30 * 60_000).toISOString() },
        { key: 'seven-day', label: '7 天', remainingPercent: String(Math.max(8, 86 - index * 3)), resetAt: new Date(now + 7 * 24 * 60 * 60_000).toISOString() }
      ] : internalBudgetAccount ? [
        { key: 'internal-daily', label: '内部日额度', remainingPercent: '75', remainingValue: String(15 + index), limitValue: String(20 + index), unit: 'USD', resetAt: new Date(now + 12 * 60 * 60_000).toISOString() }
      ] : undefined,
      recoveryAt: status === 'warning' ? new Date(now + (index + 1) * 10 * 60_000).toISOString() : undefined
    }
  })
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
      { key: 'wallet_balance', label: '钱包余额', value: '18.20', unit: '元', threshold: '20', status: 'warning' },
      { key: 'healthy_accounts', label: '可用账号', value: '4', unit: '个', status: 'healthy' },
      { key: 'account_total', label: '账号总数', value: '16', unit: '个', status: 'healthy' }
    ],
    accounts: makeMockSub2APIAccounts(16)
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
  },
  {
    id: 'cli-proxy-pool',
    name: 'CLIProxyAPI 号池',
    kind: 'cliproxyapi',
    baseUrl: 'https://cli-proxy.example.com',
    status: 'warning',
    statusText: '存在限流账号',
    enabled: true,
    checkIntervalMinutes: 5,
    lastCheckedAt: minutesAgo(3),
    nextCheckAt: new Date(now + 2 * 60_000).toISOString(),
    authConfigured: true,
    metrics: [
      { key: 'account_total', label: '账号总数', value: '11', unit: '个', status: 'healthy' },
      { key: 'healthy_accounts', label: '可用账号', value: '8', unit: '个', threshold: '0', comparison: 'lte', status: 'healthy' },
      { key: 'limited_accounts', label: '限流账号', value: '2', unit: '个', threshold: '1', comparison: 'gte', status: 'warning' },
      { key: 'error_accounts', label: '异常账号', value: '0', unit: '个', threshold: '1', comparison: 'gte', status: 'healthy' },
      { key: 'disabled_accounts', label: '禁用账号', value: '1', unit: '个', status: 'disabled' }
    ],
    accounts: makeMockCLIProxyAccounts(24)
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
  },
  {
    id: 'alert-multiplier',
    targetId: 'new-api-main',
    targetName: '主站额度',
    type: 'multiplier_changed',
    title: '分组倍率已变更',
    message: '检测到分组倍率变化：会员分组：0.5× → 0.333333×。',
    severity: 'warning',
    status: 'resolved',
    createdAt: minutesAgo(12)
  }
]

const multiplierCatalog = new Map<string, GroupMultiplier[]>([
  ['new-api-main', [
    { key: 'default', name: '默认分组', description: '默认可用分组', multiplier: '1', monitored: false, status: 'unknown' },
    { key: 'vip', name: '会员分组', description: '会员专属价格', multiplier: '0.333333', monitored: false, status: 'unknown' },
    { key: 'high', name: '高性能分组', multiplier: '1.5', monitored: false, status: 'unknown' }
  ]],
  ['sub2api-backup', [
    { key: '10', name: '基础组', description: '公开分组', multiplier: '1.25', monitored: false, status: 'unknown' },
    { key: '11', name: '订阅组', multiplier: '0.8', monitored: false, status: 'unknown' }
  ]]
])

const multiplierSelections = new Map<string, Set<string>>([
  ['new-api-main', new Set(['default'])]
])

const multiplierCheckedAt = new Map<string, string>([
  ['new-api-main', minutesAgo(2)]
])

function mockMultiplierState(target: Target, includeDetected: boolean): TargetMultiplierState {
  const selected = multiplierSelections.get(target.id) ?? new Set<string>()
  const catalog = multiplierCatalog.get(target.id) ?? []
  const checkedAt = multiplierCheckedAt.get(target.id)
  const groups = catalog
    .filter((group) => includeDetected || selected.has(group.key))
    .map((group) => ({
      ...group,
      monitored: selected.has(group.key),
      status: selected.has(group.key) ? 'stable' as const : 'unknown' as const,
      lastCheckedAt: selected.has(group.key) ? checkedAt : undefined
    }))
  return {
    targetId: target.id,
    targetName: target.name,
    targetKind: target.kind as TargetMultiplierState['targetKind'],
    enabled: target.enabled,
    groups,
    lastCheckedAt: selected.size > 0 ? checkedAt : undefined
  }
}

function mockGroupPrices(target: Target, groupKey: string): GroupPriceResult {
  const group = (multiplierCatalog.get(target.id) ?? []).find((item) => item.key === groupKey)
  const selected = multiplierSelections.get(target.id) ?? new Set<string>()
  if (!group || !selected.has(groupKey)) throw new Error('该分组尚未选择监控，请先在渠道详情完成配置')
  const isSub2API = target.kind === 'sub2api'
  return {
    targetId: target.id,
    groupKey: group.key,
    groupName: group.name,
    multiplier: group.multiplier,
    notice: '模拟价格按渠道当前公开数据展示，实际扣费规则以渠道结算记录为准。',
    models: [
      {
        name: isSub2API ? 'claude-sonnet-4' : 'gpt-4.1',
        billingMode: 'token',
        prices: [
          { key: 'input', label: '输入', value: isSub2API ? '3' : '2', unit: '元/百万 Token' },
          { key: 'output', label: '输出', value: isSub2API ? '15' : '8', unit: '元/百万 Token' },
          { key: 'cached', label: '缓存输入', value: isSub2API ? '0.3' : '0.5', unit: '元/百万 Token' }
        ],
        note: '支持文本与工具调用'
      },
      {
        name: isSub2API ? 'gemini-2.5-pro' : 'gpt-4.1-long',
        billingMode: 'tiered',
        prices: [],
        intervals: [
          {
            label: '标准上下文', minTokens: '0', maxTokens: '200000', prices: [
              { key: 'input', label: '输入', value: '1.25', unit: '元/百万 Token' },
              { key: 'output', label: '输出', value: '10', unit: '元/百万 Token' }
            ]
          },
          {
            label: '长上下文', minTokens: '200001', prices: [
              { key: 'input', label: '输入', value: '2.5', unit: '元/百万 Token' },
              { key: 'output', label: '输出', value: '15', unit: '元/百万 Token' }
            ]
          }
        ]
      }
    ]
  }
}

let settings: Settings = {
  productName: '号池监控',
  historyRetentionDays: 7,
  defaultCheckIntervalMinutes: 5,
  allowPrivateTargets: false,
  totpEnabled: false
}

function defaultEmailSettings(): EmailSettings {
  return {
    enabled: false,
    provider: 'qq',
    host: 'smtp.qq.com',
    port: 465,
    security: 'tls',
    username: '',
    fromName: '号池监控',
    fromAddress: '',
    recipients: [],
    passwordConfigured: false
  }
}

let emailSettings: EmailSettings = defaultEmailSettings()

// 模拟环境同样只允许同一发信认证身份沿用已保存的授权码。
function hasSameMockEmailIdentity(input: Record<string, unknown>): boolean {
  return input.provider === emailSettings.provider && input.host === emailSettings.host &&
    input.port === emailSettings.port && input.security === emailSettings.security &&
    input.username === emailSettings.username
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
      threshold: threshold.alertEnabled ? threshold.value : undefined,
      alertThreshold: threshold.value,
      alertEnabled: threshold.alertEnabled,
      comparison: threshold.comparison,
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
    if (target.kind === 'new_api' || target.kind === 'sub2api') {
      multiplierCatalog.set(target.id, [])
    }
    return target as T
  }
  if (cleanPath === '/api/targets/detect') {
    const address = String(body.baseUrl ?? '').toLowerCase()
    const kind = address.includes('sub')
      ? 'sub2api'
      : address.includes('cliproxy') || address.includes('cli-proxy') || address.includes('router-for-me')
        ? 'cliproxyapi'
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
    if (parts[4] === 'accounts' && parts[5] === 'quota' && parts[6] === 'refresh' && method === 'POST') {
      const accountIds = Array.isArray(body?.accountIds) ? body.accountIds.map(String) : []
      const selectedAccounts = (target.accounts ?? []).filter((account) => accountIds.includes(account.id))
      const result: AccountQuotaRefreshResult = {
        accounts: accountIds.flatMap((accountId: string) => selectedAccounts.filter((account) => account.id === accountId)),
        refreshedCount: selectedAccounts.filter((account) => account.quotaState === 'available').length,
        unavailableCount: selectedAccounts.filter((account) => account.quotaState === 'unavailable').length,
        unsupportedCount: selectedAccounts.filter((account) => account.quotaState === 'unsupported').length
      }
      return result as T
    }
    if (parts[4] === 'group-prices' && method === 'GET') {
      const groupKey = new URL(path, window.location.origin).searchParams.get('groupKey') ?? ''
      return mockGroupPrices(target, groupKey) as T
    }
    if (parts[4] === 'group-multipliers') {
      if (target.kind !== 'new_api' && target.kind !== 'sub2api') throw new Error('该渠道不支持倍率监控')
      if (method === 'GET') return mockMultiplierState(target, false) as T
      if (method === 'PUT') {
        const groupKeys = Array.isArray(body?.groupKeys) ? body.groupKeys.map(String) : []
        const catalogKeys = new Set((multiplierCatalog.get(id) ?? []).map((group) => group.key))
        if (groupKeys.length > 500 || new Set(groupKeys).size !== groupKeys.length || groupKeys.some((key: string) => !catalogKeys.has(key))) {
          throw new Error('分组列表已经变化，请重新检测后再保存')
        }
        multiplierSelections.set(id, new Set(groupKeys))
        multiplierCheckedAt.set(id, new Date().toISOString())
        return mockMultiplierState(target, true) as T
      }
      if (method === 'POST' && (parts[5] === 'detect' || parts[5] === 'check')) {
        multiplierCheckedAt.set(id, new Date().toISOString())
        return mockMultiplierState(target, true) as T
      }
    }
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
      multiplierCatalog.delete(id)
      multiplierSelections.delete(id)
      multiplierCheckedAt.delete(id)
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
  if (cleanPath === '/api/email' && method === 'GET') return emailSettings as T
  if (cleanPath === '/api/email' && method === 'PUT') {
    if (emailSettings.passwordConfigured && !body.password && !hasSameMockEmailIdentity(body)) {
      throw new Error('发件服务商、服务器或账号已变更，请重新填写授权码或应用密码')
    }
    const passwordConfigured = emailSettings.passwordConfigured || Boolean(body.password)
    emailSettings = { ...emailSettings, ...body, passwordConfigured }
    delete (emailSettings as EmailSettings & { password?: string }).password
    return emailSettings as T
  }
  if (cleanPath === '/api/email' && method === 'DELETE') {
    emailSettings = defaultEmailSettings()
    return emailSettings as T
  }
  if (cleanPath === '/api/email/test' && method === 'POST') return { ok: true } as T
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
