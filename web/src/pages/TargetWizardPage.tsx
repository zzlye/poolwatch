import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Check, Download, ExternalLink, Eye, EyeOff, FlaskConical, Globe, KeyRound, LoaderCircle, Lock, Search, X } from 'lucide-react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { BrowserAuthRecovery, requiresBrowserAuthorization } from '../components/BrowserAuthRecovery'
import type { CredentialMode, MetricValue, Target, TargetAuthAttempt, TargetDraft, TargetKind, TestConnectionResult, ThresholdDraft } from '../types'
import { metricLabels, targetKindLabels } from '../types'

const steps = ['基本信息', '登录方式', '指标阈值', '检测与保存']
// 浏览器助手压缩包随页面一起发布，桌面端安装后即可读取当前填写站点的会话。
const browserHelperDownloadURL = '/downloads/poolwatch-browser-helper-v1.1.0.zip'
const minimumBrowserHelperVersion = [1, 1, 0] as const
const browserHelperInstallMessage = '安装浏览器助手并刷新本页后，即可一键读取已登录站点。'
const browserHelperUpdateMessage = '当前浏览器助手版本较旧，请下载新版并在扩展页面重新加载。'
const androidHTTPSMessage = '安卓端网页登录仅支持 HTTPS 渠道地址。请返回上一步填写 HTTPS 地址，或使用桌面端浏览器助手导入当前登录状态。'

interface BrowserHelperResult {
  source: 'poolwatch-extension'
  type: 'POOLWATCH_IMPORT_RESULT'
  requestId: string
  attemptId?: string
  ok: boolean
  code?: string
  message?: string
}

interface BrowserHelperReady {
  source: 'poolwatch-extension'
  type: 'POOLWATCH_BROWSER_HELPER_READY'
  version?: string
  capabilities?: string[]
}

export function supportsBrowserHelper(kind: TargetKind, version: string, capabilities: string[]): boolean {
  // 两类渠道都要求助手明确声明当前能力，避免旧助手被误判为仅支持其中一种渠道。
  const parts = version.split('.').map((part) => Number(part))
  if (parts.length < 3 || parts.some((part) => !Number.isInteger(part) || part < 0)) return false
  for (let index = 0; index < minimumBrowserHelperVersion.length; index += 1) {
    if (parts[index] > minimumBrowserHelperVersion[index]) break
    if (parts[index] < minimumBrowserHelperVersion[index]) return false
  }
  return capabilities.includes(kind)
}

function isHTTPSAddress(value: string): boolean {
  try {
    return new URL(value).protocol === 'https:'
  } catch {
    return false
  }
}

const newAPISubscriptionThreshold: ThresholdDraft = {
  key: 'subscription_balance',
  label: '订阅余额',
  value: '20',
  unit: '站点单位',
  comparison: 'lte',
  alertEnabled: true
}

const thresholdsByKind: Record<TargetKind, ThresholdDraft[]> = {
  new_api: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: '站点单位', comparison: 'lte', alertEnabled: true }],
  sub2api: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: 'USD', comparison: 'lte', alertEnabled: true }],
  chatgpt2api: [{ key: 'image_quota', label: '图片额度总和', value: '80', unit: '次', comparison: 'lte', alertEnabled: true }],
  cliproxyapi: [
    { key: 'healthy_accounts', label: '可用账号', value: '0', unit: '个', comparison: 'lte', alertEnabled: true },
    { key: 'limited_accounts', label: '警告账号', value: '1', unit: '个', comparison: 'gte', alertEnabled: true },
    { key: 'error_accounts', label: '异常账号', value: '1', unit: '个', comparison: 'gte', alertEnabled: true }
  ],
  custom: [{ key: 'wallet_balance', label: '自定义指标', value: '10', unit: '个', comparison: 'lte', alertEnabled: true }]
}

function defaultCredentialMode(kind: TargetKind): CredentialMode {
  if (kind === 'new_api') return 'browser_session'
  if (kind === 'sub2api') return 'browser_oauth'
  return 'access_token'
}

function makeDraft(kind: TargetKind = 'new_api', checkIntervalMinutes = 5): TargetDraft {
  return {
    name: '',
    kind,
    baseUrl: '',
    topupUrl: '',
    enabled: true,
    checkIntervalMinutes,
    username: '',
    email: '',
    password: '',
    totpSecret: '',
    totpCode: '',
    accessToken: '',
    refreshToken: '',
    adminKey: '',
    userId: '',
    credentialMode: defaultCredentialMode(kind),
    cookie: '',
    browserAuthAttemptId: '',
    authType: kind === 'custom' ? 'none' : 'bearer',
    requestMethod: 'GET',
    confirmPost: false,
    customHeaders: '{}',
    jsonPointer: '/data/balance',
    statusPointer: '/data/status',
    thresholds: thresholdsByKind[kind].map((item) => ({ ...item }))
  }
}

// 接口会同时返回采集到的指标和已配置指标，只有带告警配置的额外指标才应在编辑时写回。
function isConfiguredMetric(metric: MetricValue): boolean {
  return metric.alertThreshold !== undefined || metric.threshold !== undefined || metric.alertEnabled === true
}

// 将接口指标恢复为表单阈值，并兼容尚未返回告警开关的旧版数据。
function metricToThreshold(metric: MetricValue, fallback?: ThresholdDraft): ThresholdDraft {
  return {
    key: metric.key,
    label: metric.label || fallback?.label || metricLabels[metric.key],
    value: metric.alertThreshold ?? metric.threshold ?? fallback?.value ?? '0',
    unit: metric.unit || fallback?.unit || '',
    comparison: metric.comparison ?? fallback?.comparison ?? 'lte',
    alertEnabled: metric.alertEnabled ?? metric.threshold !== undefined
  }
}

export function targetToDraft(target: Target): TargetDraft {
  const draft = makeDraft(target.kind)
  const configuredMetrics = new Map(target.metrics.map((item) => [item.key, item]))
  let thresholdDefaults = thresholdsByKind[target.kind]
  const subscriptionMetric = configuredMetrics.get('subscription_balance')
  if (target.kind === 'new_api' && subscriptionMetric && isConfiguredMetric(subscriptionMetric)) {
    thresholdDefaults = [...thresholdDefaults, newAPISubscriptionThreshold]
  }
  const defaultMetricKeys = new Set(thresholdDefaults.map((item) => item.key))
  const thresholds = target.kind === 'custom'
    ? target.metrics.map((item) => metricToThreshold(item, thresholdsByKind.custom[0]))
    : [
        ...thresholdDefaults.map((fallback) => {
          const metric = configuredMetrics.get(fallback.key)
          return metric ? metricToThreshold(metric, fallback) : { ...fallback }
        }),
        ...target.metrics
          .filter((metric) => !defaultMetricKeys.has(metric.key) && isConfiguredMetric(metric))
          .map((metric) => metricToThreshold(metric))
      ]
  return {
    ...draft,
    name: target.name,
    baseUrl: target.baseUrl,
    topupUrl: target.topupUrl ?? '',
    enabled: target.enabled,
    checkIntervalMinutes: target.checkIntervalMinutes,
    credentialMode: target.credentialMode ?? defaultCredentialMode(target.kind),
    authType: target.authType ?? draft.authType,
    requestMethod: target.requestMethod ?? draft.requestMethod,
    confirmPost: target.confirmPost ?? draft.confirmPost,
    jsonPointer: target.jsonPointer ?? draft.jsonPointer,
    statusPointer: target.statusPointer ?? draft.statusPointer,
    // 自定义请求头可能包含秘密，接口只返回是否已配置；编辑时留空表示沿用原值。
    customHeaders: target.customHeadersConfigured ? '' : draft.customHeaders,
    thresholds
  }
}

export function parseSub2APIOAuthCallback(value: string, expectedBaseUrl = ''): { accessToken: string; refreshToken: string } {
  const parsed = new URL(value.trim())
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') throw new Error('OAuth 回调地址必须使用 HTTP 或 HTTPS。')
  if (expectedBaseUrl) {
    const expected = new URL(expectedBaseUrl)
    if (parsed.origin !== expected.origin) throw new Error('OAuth 回调地址与当前渠道不是同一来源。')
  }
  const fragment = new URLSearchParams(parsed.hash.replace(/^#/, ''))
  const accessToken = fragment.get('access_token')?.trim() ?? ''
  const refreshToken = fragment.get('refresh_token')?.trim() ?? ''
  if (!accessToken && !refreshToken) throw new Error('回调地址的 fragment 中没有访问令牌或刷新令牌。')
  if (accessToken.length > 65536 || refreshToken.length > 65536 || /[\r\n]/.test(accessToken + refreshToken)) {
    throw new Error('OAuth 回调中的令牌格式无效。')
  }
  return { accessToken, refreshToken }
}

function getTopupCandidate(baseUrl: string, kind: TargetKind): string {
  try {
    const url = new URL(baseUrl)
    if (kind === 'new_api') return new URL('/console/topup', url).toString()
    if (kind === 'sub2api') return new URL('/purchase', url).toString()
  } catch {
    return ''
  }
  return ''
}

function validateUrl(value: string, required = true): boolean {
  if (!value) return !required
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

function targetOriginChanged(currentValue: string, nextValue: string): boolean {
  try {
    const current = new URL(currentValue)
    const next = new URL(nextValue)
    return current.origin !== next.origin
  } catch {
    // 用户仍在输入地址时暂不清空，形成有效的新来源后再执行隔离。
    return false
  }
}

function clearedCredentialFields(): Partial<TargetDraft> {
  // 渠道来源或类型变化后，所有与旧来源绑定的身份和秘密都必须重新确认。
  return {
    username: '', email: '', password: '', totpSecret: '', totpCode: '',
    accessToken: '', refreshToken: '', adminKey: '', userId: '', cookie: '', customHeaders: ''
  }
}

function collectPointers(value: unknown, base = ''): string[] {
  if (value === null || typeof value !== 'object') return base ? [base] : []
  return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) => {
    // RFC 6901 要求对路径片段中的波浪线和斜杠进行转义。
    const escaped = key.replace(/~/g, '~0').replace(/\//g, '~1')
    return collectPointers(child, `${base}/${escaped}`)
  })
}

function JsonPointerPicker({ sample, value, onChange }: { sample: unknown; value: string; onChange: (value: string) => void }) {
  const pointers = useMemo(() => collectPointers(sample), [sample])
  return (
    <div className="pointer-picker">
      <strong>响应字段</strong>
      <p>选择包含额度数值的字段，路径会按 JSON Pointer 保存。</p>
      <div className="pointer-list">
        {pointers.map((pointer) => (
          <button type="button" key={pointer} className={value === pointer ? 'pointer-option selected' : 'pointer-option'} onClick={() => onChange(pointer)}>
            <code>{pointer}</code>{value === pointer ? <Check aria-hidden="true" size={16} /> : null}
          </button>
        ))}
      </div>
    </div>
  )
}

const credentialModeOptions: Record<'new_api' | 'sub2api', Array<{ mode: CredentialMode; title: string; description: string }>> = {
  new_api: [
    { mode: 'browser_session', title: '网页授权', description: '支持 Linux.do、GitHub 等站点网页登录。' },
    { mode: 'access_token', title: '访问令牌', description: '填写管理访问令牌和用户 ID。' },
    { mode: 'password', title: '账号密码', description: '使用站点账号、密码和可选二步验证。' }
  ],
  sub2api: [
    { mode: 'browser_oauth', title: '网页授权', description: '在浏览器完成 OAuth 登录并导入结果。' },
    { mode: 'access_token', title: '访问令牌', description: '填写访问令牌和可选刷新令牌。' },
    { mode: 'password', title: '账号密码', description: '使用邮箱、密码和可选二步验证。' }
  ]
}

function credentialModeIcon(mode: CredentialMode) {
  if (mode === 'browser_session' || mode === 'browser_oauth') return <Globe aria-hidden="true" size={20} />
  if (mode === 'access_token') return <KeyRound aria-hidden="true" size={20} />
  return <Lock aria-hidden="true" size={20} />
}

function BrowserAuthorizationFields({
  draft,
  update,
  editing,
  configured,
  attempt,
  setAttempt,
  isCurrentAuthTarget
}: {
  draft: TargetDraft
  update: (patch: Partial<TargetDraft>) => void
  editing: boolean
  configured: boolean
  attempt: TargetAuthAttempt | null
  setAttempt: (attempt: TargetAuthAttempt | null) => void
  isCurrentAuthTarget: (kind: TargetKind, baseUrl: string) => boolean
}) {
  const isAndroidApp = typeof navigator !== 'undefined' && navigator.userAgent.includes('PoolWatchAndroid/')
  const [pollError, setPollError] = useState('')
  const [callbackUrl, setCallbackUrl] = useState('')
  const [callbackError, setCallbackError] = useState('')
  const [callbackImported, setCallbackImported] = useState(false)
  const [browserHelperCapabilities, setBrowserHelperCapabilities] = useState<string[] | null>(null)
  const [browserHelperVersion, setBrowserHelperVersion] = useState('')
  const [browserHelperMessage, setBrowserHelperMessage] = useState('')
  const [browserHelperImporting, setBrowserHelperImporting] = useState(false)
  const [showBrowserHelperInstall, setShowBrowserHelperInstall] = useState(false)
  const helperRequestId = useRef('')
  const helperAttemptId = useRef('')
  const helperAfterCreate = useRef(false)
  const helperTimeout = useRef(0)
  const browserHelperDetected = browserHelperCapabilities !== null
  const detectedCapabilities = browserHelperCapabilities ?? []
  const browserHelperReady = browserHelperDetected && supportsBrowserHelper(draft.kind, browserHelperVersion, detectedCapabilities)
  const androidRequiresHTTPS = isAndroidApp && !isHTTPSAddress(draft.baseUrl)

  const applyAttempt = (next: TargetAuthAttempt) => {
    if (!isCurrentAuthTarget(draft.kind, draft.baseUrl)) return false
    setAttempt(next)
    if (next.status === 'ready') {
      update({ browserAuthAttemptId: next.id, userId: next.userId || draft.userId })
      setPollError('')
    } else if (next.status === 'expired' || next.status === 'cancelled') {
      update({ browserAuthAttemptId: '' })
    }
    return true
  }

  const requestBrowserHelper = (next: TargetAuthAttempt) => {
    // 页面只把当前授权任务编号交给助手，渠道地址由助手向服务器读取，避免传入批量渠道数据。
    const requestId = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(16).slice(2)}`
    helperRequestId.current = requestId
    helperAttemptId.current = next.id
    setBrowserHelperImporting(true)
    setBrowserHelperMessage('正在读取已登录站点，请保持当前页面打开。')
    window.clearTimeout(helperTimeout.current)
    helperTimeout.current = window.setTimeout(() => {
      if (helperRequestId.current !== requestId) return
      helperRequestId.current = ''
      helperAttemptId.current = ''
      setBrowserHelperImporting(false)
      setBrowserHelperMessage('浏览器助手响应超时，请刷新页面后重试。')
    }, 30000)
    window.postMessage({
      source: 'poolwatch-page',
      type: draft.kind === 'sub2api' ? 'POOLWATCH_IMPORT_SUB2_API' : 'POOLWATCH_IMPORT_NEW_API',
      requestId,
      attemptId: next.id
    }, window.location.origin)
  }

  const createMutation = useMutation({
    mutationFn: () => api.createTargetAuthAttempt({ kind: draft.kind, baseUrl: draft.baseUrl }),
    onSuccess: (next) => {
      if (!applyAttempt(next)) {
        helperAfterCreate.current = false
        return
      }
      if (helperAfterCreate.current) {
        helperAfterCreate.current = false
        requestBrowserHelper(next)
      }
    },
    onError: () => {
      helperAfterCreate.current = false
    }
  })
  const cancelMutation = useMutation({
    mutationFn: () => api.cancelTargetAuthAttempt(attempt!.id),
    onSuccess: () => {
      if (attempt) applyAttempt({ ...attempt, status: 'cancelled', message: '网页登录已取消。' })
    }
  })

  useEffect(() => {
    if (!attempt || attempt.status !== 'waiting') return
    let stopped = false
    let timer = 0
    const poll = async () => {
      try {
        const next = await api.targetAuthAttempt(attempt.id)
        if (stopped) return
        applyAttempt(next)
        if (next.status === 'waiting') timer = window.setTimeout(poll, 1000)
      } catch (error) {
        if (stopped) return
        setPollError(error instanceof Error ? error.message : '读取网页登录状态失败。')
        timer = window.setTimeout(poll, 1000)
      }
    }
    timer = window.setTimeout(poll, 1000)
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [attempt?.id, attempt?.status])

  useEffect(() => {
    if (isAndroidApp) return
    setBrowserHelperCapabilities(null)
    setBrowserHelperVersion('')
    const announce = () => window.postMessage({ source: 'poolwatch-page', type: 'POOLWATCH_BROWSER_HELPER_PING' }, window.location.origin)
    const handleMessage = (event: MessageEvent) => {
      if (event.source !== window || event.origin !== window.location.origin) return
      const message = event.data
      if (message?.source !== 'poolwatch-extension') return
      if (message.type === 'POOLWATCH_BROWSER_HELPER_READY') {
        const ready = message as BrowserHelperReady
        const capabilities = Array.isArray(ready.capabilities)
          ? [...new Set(ready.capabilities.filter((item): item is string => typeof item === 'string'))]
          : []
        const version = typeof ready.version === 'string' ? ready.version.trim() : ''
        const readyForCurrentKind = supportsBrowserHelper(draft.kind, version, capabilities)
        setBrowserHelperCapabilities(capabilities)
        setBrowserHelperVersion(version)
        if (readyForCurrentKind) {
          setShowBrowserHelperInstall(false)
          // 心跳只清理安装类提示，保留读取中、成功和失败结果供用户查看。
          setBrowserHelperMessage((current) => current === browserHelperInstallMessage || current === browserHelperUpdateMessage ? '' : current)
        } else {
          setShowBrowserHelperInstall(true)
          setBrowserHelperMessage(browserHelperUpdateMessage)
        }
        return
      }
      if (message.type !== 'POOLWATCH_IMPORT_RESULT' || message.requestId !== helperRequestId.current) return
      const result = message as BrowserHelperResult
      if (result.ok && result.attemptId !== helperAttemptId.current) return
      window.clearTimeout(helperTimeout.current)
      helperRequestId.current = ''
      helperAttemptId.current = ''
      setBrowserHelperImporting(false)
      setBrowserHelperMessage(result.message || (result.ok ? '已读取登录会话。' : '读取登录会话失败。'))
      if (result.ok && result.attemptId) {
        void api.targetAuthAttempt(result.attemptId).then(applyAttempt).catch((error) => {
          setBrowserHelperMessage(error instanceof Error ? error.message : '读取网页登录状态失败。')
        })
      }
    }
    window.addEventListener('message', handleMessage)
    announce()
    const timer = window.setInterval(announce, 3000)
    return () => {
      window.removeEventListener('message', handleMessage)
      window.clearInterval(timer)
      window.clearTimeout(helperTimeout.current)
    }
  }, [draft.kind, isAndroidApp])

  const prepareLogin = () => {
    setPollError('')
    setCallbackError('')
    update({ browserAuthAttemptId: '' })
    createMutation.mutate()
  }

  const openLoginWindow = () => {
    if (!attempt) return
    if (isAndroidApp) {
      // 自定义协议必须在用户点击事件中同步触发，安卓 WebView 才会认可这次导航手势。
      window.location.href = `poolwatch-auth://start/${encodeURIComponent(attempt.id)}`
      return
    }
    window.open(attempt.loginUrl, '_blank', 'noopener,noreferrer')
  }

  const quickImport = () => {
    setPollError('')
    setCallbackError('')
    if (!browserHelperReady) {
      setShowBrowserHelperInstall(true)
      setBrowserHelperMessage(browserHelperDetected
        ? browserHelperUpdateMessage
        : browserHelperInstallMessage)
      return
    }
    setShowBrowserHelperInstall(false)
    update({ browserAuthAttemptId: '' })
    if (attempt?.status === 'waiting') {
      requestBrowserHelper(attempt)
      return
    }
    helperAfterCreate.current = true
    createMutation.mutate()
  }

  const importSub2APICallback = () => {
    setCallbackError('')
    try {
      const tokens = parseSub2APIOAuthCallback(callbackUrl, draft.baseUrl)
      update({ accessToken: tokens.accessToken, refreshToken: tokens.refreshToken, browserAuthAttemptId: '' })
      // 完整回调地址含有秘密，解析后立即从输入状态中移除。
      setCallbackUrl('')
      setCallbackImported(true)
    } catch (error) {
      setCallbackImported(false)
      setCallbackError(error instanceof Error ? error.message : 'OAuth 回调地址格式无效。')
    }
  }

  const statusMessage = attempt?.message || (attempt?.status === 'waiting'
    ? '授权任务已经准备好，请打开授权窗口并完成登录。'
    : attempt?.status === 'ready'
      ? '网页登录成功，凭据将在保存时由服务器加密接管。'
      : attempt?.status === 'expired'
        ? '网页登录任务已经过期，请重新准备。'
        : attempt?.status === 'cancelled'
          ? '网页登录已取消。'
          : '')

  return (
    <div className="browser-auth-panel span-2">
      <div className="browser-auth-heading">
        <div><strong>渠道网页登录</strong><small>登录页面由渠道站点提供，号池监控不会接触第三方账号密码。</small></div>
        {attempt?.status === 'ready' || (configured && !attempt) ? <span className="configured-badge"><Check aria-hidden="true" size={15} />已配置</span> : null}
      </div>
      {!isAndroidApp ? (
        <div className="browser-helper-card">
          <div className="browser-helper-title">
            <div>
              <strong>读取当前填写地址</strong>
              <small>{draft.kind === 'sub2api'
                ? '只读取第一步填写的渠道地址，从当前浏览器中取得该站点的访问令牌和刷新令牌。'
                : '只读取第一步填写的渠道地址，从当前浏览器中取得该站点的会话和用户 ID。'}</small>
            </div>
            <span className={browserHelperReady ? 'configured-badge' : 'helper-status-badge'}>
              {browserHelperReady
                ? <><Check aria-hidden="true" size={15} />已连接</>
                : browserHelperDetected ? '需更新' : '待安装'}
            </span>
          </div>
          <div className="browser-auth-actions">
            <button className="button primary" type="button" disabled={createMutation.isPending || browserHelperImporting} onClick={quickImport}>
              {createMutation.isPending || browserHelperImporting ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Globe aria-hidden="true" size={18} />}
              {createMutation.isPending
                ? '正在准备'
                : browserHelperImporting
                  ? '正在读取'
                  : browserHelperReady
                    ? '一键读取当前地址'
                    : browserHelperDetected ? '更新浏览器助手' : '启用一键读取'}
            </button>
            <a className="button secondary" href={browserHelperDownloadURL} download><Download aria-hidden="true" size={18} />{browserHelperDetected && !browserHelperReady ? '更新浏览器助手' : '下载浏览器助手'}</a>
            {attempt?.status === 'waiting' ? <button className="button ghost" type="button" onClick={openLoginWindow}><ExternalLink aria-hidden="true" size={18} />打开渠道站点</button> : null}
          </div>
          {showBrowserHelperInstall ? (
            <ol className="browser-helper-steps">
              <li>下载并解压浏览器助手。</li>
              <li>在 Chrome 或 Edge 扩展页面开启开发者模式，选择“加载已解压的扩展程序”。</li>
              <li>{browserHelperDetected ? '在扩展页面重新加载新版助手，再刷新当前页面。' : '刷新当前页面，再点击“一键读取当前地址”。'}</li>
            </ol>
          ) : null}
          {browserHelperMessage ? <InlineMessage tone={browserHelperMessage.includes('已读取') ? 'success' : 'info'}>{browserHelperMessage}</InlineMessage> : null}
        </div>
      ) : androidRequiresHTTPS ? (
        <InlineMessage tone="danger">{androidHTTPSMessage}</InlineMessage>
      ) : (
        <div className="browser-auth-actions">
          <button className="button secondary" type="button" disabled={createMutation.isPending || attempt?.status === 'waiting'} onClick={prepareLogin}>
            {createMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Globe aria-hidden="true" size={18} />}
            {createMutation.isPending ? '正在准备' : attempt ? '重新准备网页登录' : '准备网页登录'}
          </button>
          {attempt?.status === 'waiting' ? <button className="button primary" type="button" onClick={openLoginWindow}><ExternalLink aria-hidden="true" size={18} />打开授权窗口</button> : null}
          {attempt?.status === 'waiting' ? <button className="button ghost" type="button" disabled={cancelMutation.isPending} onClick={() => cancelMutation.mutate()}><X aria-hidden="true" size={18} />取消</button> : null}
        </div>
      )}
      {statusMessage ? <InlineMessage tone={attempt?.status === 'ready' ? 'success' : attempt?.status === 'expired' || attempt?.status === 'cancelled' ? 'danger' : 'info'}>{statusMessage}</InlineMessage> : null}
      {createMutation.error ? <InlineMessage tone="danger">{createMutation.error.message}</InlineMessage> : null}
      {cancelMutation.error ? <InlineMessage tone="danger">{cancelMutation.error.message}</InlineMessage> : null}
      {pollError ? <InlineMessage tone="danger">网页登录状态更新失败：{pollError}</InlineMessage> : null}

      {!isAndroidApp && draft.kind === 'new_api' ? (
        <details className="manual-auth-details">
          <summary>手工填写 Cookie 和用户 ID</summary>
          <div className="manual-auth-import">
            <div><strong>备用导入方式</strong><p>需要手工处理时，再填写该站点的 Cookie 和用户 ID。</p></div>
            <div className="form-grid">
              <SecretField label="登录 Cookie" value={draft.cookie} show={false} editing={editing} onChange={(cookie) => update({ cookie, browserAuthAttemptId: '' })} onToggle={() => undefined} hideToggle />
              <label className="field"><span>用户 ID</span><input value={draft.userId} onChange={(event) => update({ userId: event.target.value, browserAuthAttemptId: '' })} inputMode="numeric" placeholder={editing ? '留空表示保持不变' : ''} /></label>
            </div>
            {draft.cookie && draft.userId ? <InlineMessage tone="success">Cookie 和用户 ID 已填写，保存前可在最后一步测试连接。</InlineMessage> : null}
          </div>
        </details>
      ) : null}

      {!isAndroidApp && draft.kind === 'sub2api' ? (
        <div className="manual-auth-import">
          <div><strong>导入 OAuth 回调</strong><p>完成授权后复制浏览器地址栏中的完整回调地址，仅在当前页面解析其中的令牌。</p></div>
          <label className="field"><span>OAuth 回调完整地址</span><input type="password" value={callbackUrl} onChange={(event) => { setCallbackUrl(event.target.value); setCallbackError(''); setCallbackImported(false) }} autoComplete="off" spellCheck={false} placeholder="SCHEME://CALLBACK#access_token=...&refresh_token=..." /></label>
          <button className="button secondary" type="button" disabled={!callbackUrl.trim()} onClick={importSub2APICallback}><KeyRound aria-hidden="true" size={18} />解析并导入令牌</button>
          {callbackImported ? <InlineMessage tone="success">OAuth 令牌已导入当前表单，完整回调地址已经清除。</InlineMessage> : null}
          {callbackError ? <InlineMessage tone="danger">{callbackError}</InlineMessage> : null}
        </div>
      ) : null}
    </div>
  )
}

function AuthenticationFields({
  draft,
  update,
  existing,
  attempt,
  setAttempt,
  isCurrentAuthTarget
}: {
  draft: TargetDraft
  update: (patch: Partial<TargetDraft>) => void
  existing?: Target
  attempt: TargetAuthAttempt | null
  setAttempt: (attempt: TargetAuthAttempt | null) => void
  isCurrentAuthTarget: (kind: TargetKind, baseUrl: string) => boolean
}) {
  const [showSecret, setShowSecret] = useState(false)
  const editing = Boolean(existing)
  if (draft.kind === 'custom') {
    return (
      <>
        <label className="field"><span>认证方式</span><select value={draft.authType} onChange={(event) => update({ authType: event.target.value as TargetDraft['authType'] })}><option value="none">无需认证</option><option value="bearer">Bearer 令牌</option><option value="basic">Basic 账号密码</option><option value="headers">自定义请求头</option></select></label>
        {draft.authType === 'bearer' ? <SecretField label="Bearer 令牌" value={draft.accessToken} show={showSecret} editing={editing} onChange={(value) => update({ accessToken: value })} onToggle={() => setShowSecret((value) => !value)} /> : null}
        {draft.authType === 'basic' ? <><label className="field"><span>账号</span><input value={draft.username} onChange={(event) => update({ username: event.target.value })} autoComplete="username" /></label><SecretField label="密码" value={draft.password} show={showSecret} editing={editing} onChange={(value) => update({ password: value })} onToggle={() => setShowSecret((value) => !value)} /></> : null}
        {draft.authType === 'headers' ? <label className="field span-2"><span>自定义请求头（JSON）</span><textarea rows={5} value={draft.customHeaders} onChange={(event) => update({ customHeaders: event.target.value })} spellCheck={false} placeholder={editing ? '留空表示保持已配置的请求头不变' : '{}'} /><small>{editing ? '页面不会读取原请求头；留空会沿用服务器中的加密配置。' : `例如 {"X-API-Key":"密钥"}，密钥会由服务器加密保存。`}</small></label> : null}
      </>
    )
  }

  if (draft.kind === 'chatgpt2api' || draft.kind === 'cliproxyapi') {
    const isCLIProxyAPI = draft.kind === 'cliproxyapi'
    return (
      <>
        <SecretField label={isCLIProxyAPI ? '管理密钥' : '管理员密钥'} value={draft.adminKey} show={showSecret} editing={editing} onChange={(adminKey) => update({ adminKey })} onToggle={() => setShowSecret((value) => !value)} optional={!isCLIProxyAPI} />
        <div className="inline-message tone-info span-2">{isCLIProxyAPI ? '管理密钥仅用于只读查询账号状态与统计，不会启停、重置或删除账号。' : '管理员密钥仅用于读取脱敏账号明细；不执行刷新、重登、导入或删除。'}</div>
      </>
    )
  }

  const kind = draft.kind as 'new_api' | 'sub2api'
  const configuredMode = existing?.credentialMode ?? (existing ? defaultCredentialMode(existing.kind) : undefined)
  const configuredForCurrentMode = Boolean(existing?.authConfigured && configuredMode === draft.credentialMode)
  const changeCredentialMode = (credentialMode: CredentialMode) => {
    setAttempt(null)
    setShowSecret(false)
    const patch: Partial<TargetDraft> = { credentialMode, browserAuthAttemptId: '' }
    if (credentialMode === 'password') Object.assign(patch, { accessToken: '', refreshToken: '', cookie: '', userId: '' })
    if (credentialMode === 'access_token') Object.assign(patch, { password: '', totpSecret: '', totpCode: '', cookie: '' })
    if (credentialMode === 'browser_session') Object.assign(patch, { username: '', email: '', password: '', totpSecret: '', totpCode: '', accessToken: '', refreshToken: '' })
    if (credentialMode === 'browser_oauth') Object.assign(patch, { email: '', password: '', totpSecret: '', totpCode: '', accessToken: '', refreshToken: '', cookie: '', userId: '' })
    update(patch)
  }

  return (
    <>
      <fieldset className="credential-mode-picker span-2">
        <legend>选择登录方式</legend>
        <div className="credential-mode-grid">
          {credentialModeOptions[kind].map((option) => (
            <label className={draft.credentialMode === option.mode ? 'credential-mode-card selected' : 'credential-mode-card'} key={option.mode}>
              <input type="radio" name="credential-mode" value={option.mode} checked={draft.credentialMode === option.mode} onChange={() => changeCredentialMode(option.mode)} />
              <span className="credential-mode-icon">{credentialModeIcon(option.mode)}</span>
              <span><strong>{option.title}</strong><small>{option.description}</small></span>
            </label>
          ))}
        </div>
      </fieldset>

      {draft.credentialMode === 'browser_session' || draft.credentialMode === 'browser_oauth' ? (
        <BrowserAuthorizationFields draft={draft} update={update} editing={editing} configured={configuredForCurrentMode} attempt={attempt} setAttempt={setAttempt} isCurrentAuthTarget={isCurrentAuthTarget} />
      ) : null}

      {draft.credentialMode === 'access_token' ? (
        <>
          <SecretField label="访问令牌" value={draft.accessToken} show={showSecret} editing={editing} onChange={(accessToken) => update({ accessToken })} onToggle={() => setShowSecret((value) => !value)} />
          {kind === 'new_api' ? <label className="field"><span>用户 ID</span><input value={draft.userId} onChange={(event) => update({ userId: event.target.value })} inputMode="numeric" placeholder={editing ? '留空表示保持不变' : ''} /></label> : null}
          {kind === 'sub2api' ? <SecretField label="刷新令牌" value={draft.refreshToken} show={showSecret} editing={editing} onChange={(refreshToken) => update({ refreshToken })} onToggle={() => setShowSecret((value) => !value)} optional /> : null}
        </>
      ) : null}

      {draft.credentialMode === 'password' ? (
        <>
          <label className="field"><span>{kind === 'sub2api' ? '邮箱' : '账号'}</span><input type={kind === 'sub2api' ? 'email' : 'text'} value={kind === 'sub2api' ? draft.email : draft.username} onChange={(event) => update(kind === 'sub2api' ? { email: event.target.value } : { username: event.target.value })} autoComplete="username" /></label>
          <SecretField label="登录密码" value={draft.password} show={showSecret} editing={editing} onChange={(password) => update({ password })} onToggle={() => setShowSecret((value) => !value)} />
          <SecretField label="二步验证密钥（自动生成验证码）" value={draft.totpSecret} show={showSecret} editing={editing} onChange={(totpSecret) => update({ totpSecret })} onToggle={() => setShowSecret((value) => !value)} optional />
          <label className="field"><span>一次性二步验证码 <em>仅首次连接测试</em></span><input value={draft.totpCode} onChange={(event) => update({ totpCode: event.target.value })} inputMode="numeric" autoComplete="one-time-code" /></label>
        </>
      ) : null}

      {kind === 'sub2api' ? (
        <>
          <SecretField label="Admin API Key（读取号池）" value={draft.adminKey} show={showSecret} editing={editing} onChange={(adminKey) => update({ adminKey })} onToggle={() => setShowSecret((value) => !value)} optional />
          <div className="inline-message tone-info span-2">使用管理员账号登录时可以不填；普通账号无法读取号池，如需查看账号明细请填写管理员 API Key。{editing ? '留空会沿用服务器中已配置的密钥。' : ''}</div>
        </>
      ) : null}

      {configuredForCurrentMode ? <div className="inline-message tone-info span-2">当前渠道已经配置此登录方式。秘密字段留空时沿用服务器中的加密配置；重新填写后才会替换。</div> : null}
    </>
  )
}

function SecretField({ label, value, show, editing, optional, hideToggle, onChange, onToggle }: { label: string; value: string; show: boolean; editing: boolean; optional?: boolean; hideToggle?: boolean; onChange: (value: string) => void; onToggle: () => void }) {
  return (
    <label className="field">
      <span>{label} {optional ? <em>可选</em> : null}</span>
      <span className="input-with-action"><input type={show ? 'text' : 'password'} value={value} onChange={(event) => onChange(event.target.value)} autoComplete="off" placeholder={editing ? '留空表示保持不变' : ''} />{hideToggle ? null : <button className="input-icon-button" type="button" aria-label={show ? '隐藏秘密' : '显示秘密'} onClick={onToggle}>{show ? <EyeOff aria-hidden="true" size={18} /> : <Eye aria-hidden="true" size={18} />}</button>}</span>
    </label>
  )
}

// 凭据测试记录只保留在当前页面内存中，用于阻止修改凭据后沿用旧的成功结果。
function authorizationTestKey(draft: TargetDraft): string {
  return JSON.stringify([draft.kind, draft.baseUrl, draft.credentialMode, draft.cookie, draft.userId, draft.accessToken, draft.browserAuthAttemptId])
}

function WizardForm({ existing, defaultCheckIntervalMinutes, recoverBrowserSession = false }: { existing?: Target; defaultCheckIntervalMinutes: number; recoverBrowserSession?: boolean }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const initialRecovery = recoverBrowserSession && existing?.kind === 'new_api'
  const [recoveringBrowser, setRecoveringBrowser] = useState(Boolean(initialRecovery))
  const verifiedAuthorization = useRef('')
  const [draft, setDraft] = useState<TargetDraft>(() => {
    const initial = existing ? targetToDraft(existing) : makeDraft('new_api', defaultCheckIntervalMinutes)
    return initialRecovery ? { ...initial, credentialMode: 'browser_session' } : initial
  })
  const [step, setStep] = useState(initialRecovery ? 1 : 0)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<TestConnectionResult | null>(null)
  const [detectionMessage, setDetectionMessage] = useState('')
  const [authAttempt, setAuthAttempt] = useState<TargetAuthAttempt | null>(null)
  const authTargetRef = useRef({ kind: draft.kind, baseUrl: draft.baseUrl })
  const editing = Boolean(existing)

  const update = (patch: Partial<TargetDraft>) => setDraft((current) => ({ ...current, ...patch }))
  const isCurrentAuthTarget = (kind: TargetKind, baseUrl: string) => authTargetRef.current.kind === kind && authTargetRef.current.baseUrl === baseUrl
  const changeBaseUrl = (baseUrl: string) => {
    // 地址一旦变化就立即作废旧授权上下文，异步返回的旧任务也不会重新覆盖当前表单。
    authTargetRef.current = { kind: draft.kind, baseUrl }
    setDraft((current) => ({
      ...current,
      ...(targetOriginChanged(current.baseUrl, baseUrl) ? clearedCredentialFields() : {}),
      baseUrl,
      browserAuthAttemptId: ''
    }))
    setAuthAttempt(null)
    setDetectionMessage('')
  }
  const subscriptionMonitoringEnabled = draft.thresholds.some((item) => item.key === 'subscription_balance')
  const setSubscriptionMonitoring = (enabled: boolean) => {
    setDraft((current) => {
      if (!enabled) {
        return { ...current, thresholds: current.thresholds.filter((item) => item.key !== 'subscription_balance') }
      }
      if (current.thresholds.some((item) => item.key === 'subscription_balance')) return current
      return { ...current, thresholds: [...current.thresholds, { ...newAPISubscriptionThreshold }] }
    })
  }
  const changeKind = (kind: TargetKind, preserveDetectionMessage = false) => {
    const candidate = getTopupCandidate(draft.baseUrl, kind)
    authTargetRef.current = { kind, baseUrl: draft.baseUrl }
    setDraft((current) => ({
      ...current,
      ...clearedCredentialFields(),
      kind,
      thresholds: thresholdsByKind[kind].map((item) => ({ ...item })),
      topupUrl: candidate,
      credentialMode: defaultCredentialMode(kind),
      browserAuthAttemptId: '',
      cookie: '',
      authType: kind === 'custom' ? 'none' : current.authType
    }))
    setAuthAttempt(null)
    setTestResult(null)
    if (!preserveDetectionMessage) setDetectionMessage('')
  }
  const detectMutation = useMutation({
    mutationFn: () => api.detectTarget(draft.baseUrl),
    onSuccess: (result) => {
      changeKind(result.kind, true)
      setDetectionMessage(result.message)
    }
  })
  const testMutation = useMutation({
		mutationFn: api.testTarget,
    onMutate: () => {
      verifiedAuthorization.current = ''
      setTestResult(null)
    },
		onSuccess: (result, submittedDraft) => {
      verifiedAuthorization.current = result.ok ? authorizationTestKey(submittedDraft) : ''
			setTestResult(result)
			if (result.metrics?.length) {
				setDraft((current) => ({
					...current,
					thresholds: current.thresholds.map((threshold) => {
						const metric = result.metrics?.find((item) => item.key === threshold.key)
						return metric ? { ...threshold, label: metric.label, unit: metric.unit } : threshold
					})
				}))
			}
		}
	})
  const saveMutation = useMutation({
    mutationFn: () => existing ? api.updateTarget(existing.id, draft) : api.createTarget(draft),
    onSuccess: async (target) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
        queryClient.invalidateQueries({ queryKey: ['targets'] }),
        queryClient.invalidateQueries({ queryKey: ['target', target.id] })
      ])
      navigate(`/targets/${target.id}`)
    }
  })

  const validateStep = (): boolean => {
    setError('')
    if (recoveringBrowser && draft.kind === 'new_api' && step >= 1) {
      const hasBrowserSession = draft.credentialMode === 'browser_session' && (Boolean(draft.browserAuthAttemptId) || Boolean(draft.cookie.trim() && draft.userId.trim()))
      const hasManagementToken = draft.credentialMode === 'access_token' && Boolean(draft.accessToken.trim() && draft.userId.trim())
      if (!hasBrowserSession && !hasManagementToken) return setError('请先完成当前站点的网页授权，或填写管理访问令牌和用户 ID。'), false
      if (step === 3 && verifiedAuthorization.current !== authorizationTestKey(draft)) return setError('请先测试连接成功后再保存；更改登录信息后需要重新测试。'), false
    }
    if (step === 0) {
      if (!draft.name.trim()) return setError('请填写渠道名称。'), false
      if (!validateUrl(draft.baseUrl)) return setError('请输入有效的 HTTP 或 HTTPS 地址。'), false
    }
    if (step === 1 && draft.kind === 'custom' && draft.authType === 'headers' && !(editing && existing?.customHeadersConfigured && !draft.customHeaders.trim())) {
      try {
        const value = JSON.parse(draft.customHeaders)
        if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error()
      } catch {
        return setError('自定义请求头必须是有效的 JSON 对象。'), false
      }
    }
    if (step === 1 && draft.kind === 'cliproxyapi' && !draft.adminKey.trim() && !(editing && existing?.authConfigured)) {
      return setError('请填写 CLIProxyAPI 管理密钥。'), false
    }
    if (step === 2) {
      if (!draft.thresholds.length || draft.thresholds.some((item) => item.alertEnabled && (!item.value || !Number.isFinite(Number(item.value))))) return setError('已开启告警的指标需要填写有效阈值。'), false
      if (draft.kind === 'custom' && !draft.jsonPointer.startsWith('/')) return setError('指标字段必须使用以 / 开头的 JSON Pointer。'), false
    }
    if (step === 3) {
      if (!validateUrl(draft.topupUrl, false)) return setError('充值地址不是有效的 HTTP 或 HTTPS 地址。'), false
      if (draft.requestMethod === 'POST' && !draft.confirmPost) return setError('使用 POST 检测前必须确认该请求不会修改远端数据。'), false
    }
    return true
  }

  const next = () => {
    if (validateStep()) setStep((value) => Math.min(steps.length - 1, value + 1))
  }
  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    if (step < steps.length - 1) next()
    else if (validateStep()) saveMutation.mutate()
  }

  const beginBrowserRecovery = () => {
    // 只切换本地草稿，授权和连接测试成功前不改写服务器保存的渠道。
    setDraft((current) => ({ ...current, ...clearedCredentialFields(), credentialMode: 'browser_session', browserAuthAttemptId: '' }))
    setRecoveringBrowser(true)
    verifiedAuthorization.current = ''
    setAuthAttempt(null)
    setTestResult(null)
    testMutation.reset()
    setError('')
    setStep(1)
  }
  const showBrowserRecovery = draft.kind === 'new_api' && (recoveringBrowser || requiresBrowserAuthorization(draft.kind, existing?.lastError) || requiresBrowserAuthorization(draft.kind, testResult?.message) || requiresBrowserAuthorization(draft.kind, testMutation.error?.message))

  return (
    <form className="wizard" onSubmit={handleSubmit} noValidate>
      {showBrowserRecovery ? <BrowserAuthRecovery baseUrl={draft.baseUrl} onRecover={recoveringBrowser ? undefined : beginBrowserRecovery} /> : null}
      <ol className="wizard-steps" aria-label="配置进度">
        {steps.map((label, index) => <li key={label} className={index === step ? 'active' : index < step ? 'done' : ''} aria-current={index === step ? 'step' : undefined}><span>{index < step ? <Check aria-hidden="true" size={16} /> : index + 1}</span><b>{label}</b></li>)}
      </ol>

      <section className="wizard-panel" aria-labelledby={`step-title-${step}`}>
        {step === 0 ? <>
          <div className="panel-heading"><h2 id="step-title-0">连接到渠道</h2><p>填写名称、类型与站点根地址。</p></div>
          <div className="form-grid">
            <label className="field"><span>渠道名称 <b aria-hidden="true">*</b></span><input autoFocus value={draft.name} onChange={(event) => update({ name: event.target.value })} placeholder="例如：主站额度" /></label>
            <label className="field"><span>渠道类型</span><select value={draft.kind} onChange={(event) => changeKind(event.target.value as TargetKind)}>{Object.entries(targetKindLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <div className="field span-2"><label htmlFor="target-base-url">站点地址 <b aria-hidden="true">*</b></label><div className="url-detect-row"><input id="target-base-url" type="url" value={draft.baseUrl} onChange={(event) => changeBaseUrl(event.target.value)} onBlur={() => { if (!draft.topupUrl) update({ topupUrl: getTopupCandidate(draft.baseUrl, draft.kind) }) }} placeholder="https://api.example.com" inputMode="url" />{!editing ? <button className="button secondary" type="button" disabled={detectMutation.isPending || !draft.baseUrl.trim()} onClick={() => { setError(''); if (!validateUrl(draft.baseUrl)) { setError('请先输入有效的 HTTP 或 HTTPS 地址。'); return } detectMutation.mutate() }}>{detectMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Search aria-hidden="true" size={18} />}{detectMutation.isPending ? '识别中' : '自动识别'}</button> : null}</div><small>只允许 HTTP/HTTPS。服务器默认会阻止回环、内网和云元数据地址。</small></div>
            {detectionMessage ? <div className="span-2"><InlineMessage tone="success">{detectionMessage}</InlineMessage></div> : null}
            {detectMutation.error ? <div className="span-2"><InlineMessage tone="danger">{detectMutation.error.message}</InlineMessage></div> : null}
          </div>
        </> : null}

        {step === 1 ? <>
          <div className="panel-heading"><h2 id="step-title-1">登录与认证</h2><p>秘密只发送到服务器并加密保存，页面不会重新显示。</p></div>
          <div className="form-grid"><AuthenticationFields draft={draft} update={update} existing={recoveringBrowser && existing ? { ...existing, authConfigured: false } : existing} attempt={authAttempt} setAttempt={setAuthAttempt} isCurrentAuthTarget={isCurrentAuthTarget} /></div>
        </> : null}

        {step === 2 ? <>
          <div className="panel-heading"><h2 id="step-title-2">指标与阈值</h2><p>可按指标单独关闭告警；关闭后仍会展示数值，不会按该阈值提醒。</p></div>
          {draft.kind === 'custom' ? (
            <div className="form-grid custom-map-fields">
              <label className="field"><span>请求方法</span><select value={draft.requestMethod} onChange={(event) => update({ requestMethod: event.target.value as 'GET' | 'POST', confirmPost: false })}><option value="GET">GET</option><option value="POST">POST</option></select></label>
              <label className="field"><span>指标字段</span><input value={draft.jsonPointer} onChange={(event) => update({ jsonPointer: event.target.value })} placeholder="/data/balance" /></label>
              <label className="field"><span>状态字段 <em>可选</em></span><input value={draft.statusPointer} onChange={(event) => update({ statusPointer: event.target.value })} placeholder="/data/status" /></label>
              <div className="field"><span>响应字段测试</span><button className="button secondary" type="button" disabled={testMutation.isPending || !validateUrl(draft.baseUrl)} onClick={() => testMutation.mutate(draft)}><FlaskConical aria-hidden="true" size={18} />{testMutation.isPending ? '测试中' : '读取响应'}</button></div>
              {testResult?.sample ? <div className="span-2"><JsonPointerPicker sample={testResult.sample} value={draft.jsonPointer} onChange={(jsonPointer) => update({ jsonPointer })} /></div> : null}
            </div>
          ) : null}
          <div className="threshold-list">
            {draft.kind === 'new_api' ? (
              <label className="toggle-row">
                <input type="checkbox" checked={subscriptionMonitoringEnabled} onChange={(event) => setSubscriptionMonitoring(event.target.checked)} />
                <span>
                  <strong>监控订阅额度</strong>
                  <small>关闭后不读取订阅数据，也不会产生订阅额度告警。阈值设为 0 时，额度等于 0 仍会告警。</small>
                </span>
              </label>
            ) : null}
            {draft.thresholds.map((threshold, index) => (
              <div className="threshold-row" key={`${threshold.key}-${index}`}>
                {draft.kind === 'custom' ? <div className="custom-threshold-settings"><label className="field"><span>指标名称</span><input value={threshold.label} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, label: event.target.value } : item) })} /></label><label className="threshold-label threshold-alert-option custom-threshold-alert-option"><input type="checkbox" checked={threshold.alertEnabled} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, alertEnabled: event.target.checked } : item) })} aria-label={`${threshold.label || metricLabels[threshold.key]}告警`} /><span><strong>额度告警</strong><small>{threshold.alertEnabled ? '已开启告警' : '仅展示，不告警'}</small></span></label></div> : <label className="threshold-label threshold-alert-option"><input type="checkbox" checked={threshold.alertEnabled} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, alertEnabled: event.target.checked } : item) })} aria-label={`${threshold.label || metricLabels[threshold.key]}告警`} /><span><strong>{threshold.label || metricLabels[threshold.key]}</strong><small>{threshold.key} · {threshold.alertEnabled ? '已开启告警' : '仅展示，不告警'}</small></span></label>}
                <label className="field"><span>告警条件</span><select value={threshold.comparison} disabled={!threshold.alertEnabled} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, comparison: event.target.value as ThresholdDraft['comparison'] } : item) })}><option value="lte">小于或等于（≤）</option><option value="gte">大于或等于（≥）</option></select></label>
                <label className="field"><span>告警阈值</span><input type="number" min="0" step="any" value={threshold.value} disabled={!threshold.alertEnabled} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, value: event.target.value } : item) })} inputMode="decimal" /></label>
                <label className="field"><span>单位</span><input value={threshold.unit} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, unit: event.target.value } : item) })} readOnly={draft.kind !== 'custom'} /></label>
              </div>
            ))}
          </div>
        </> : null}

        {step === 3 ? <>
          <div className="panel-heading"><h2 id="step-title-3">检测与保存</h2><p>设置检测频率和充值入口，并在保存前确认连接。</p></div>
          <div className="form-grid">
            <label className="field"><span>检测间隔（分钟）</span><input type="number" min="1" max="1440" value={draft.checkIntervalMinutes} onChange={(event) => update({ checkIntervalMinutes: Number(event.target.value) })} inputMode="numeric" /></label>
            <label className="field span-2"><span>官方充值地址 <em>可选</em></span><input type="url" value={draft.topupUrl} onChange={(event) => update({ topupUrl: event.target.value })} placeholder="https://api.example.com/console/topup" /><small>软件只会在新标签页打开此地址，不代收款、不保存支付信息。</small></label>
            <label className="toggle-row span-2"><input type="checkbox" checked={draft.enabled} onChange={(event) => update({ enabled: event.target.checked })} /><span><strong>启用定时检测</strong><small>保存后由服务器按设定间隔执行。</small></span></label>
            {draft.kind === 'custom' && draft.requestMethod === 'POST' ? <label className="toggle-row span-2 warning-toggle"><input type="checkbox" checked={draft.confirmPost} onChange={(event) => update({ confirmPost: event.target.checked })} /><span><strong>确认 POST 请求不会修改远端数据</strong><small>仅对明确安全的查询接口使用 POST。</small></span></label> : null}
          </div>
          <div className="connection-test-block">
            <button className="button secondary" type="button" disabled={testMutation.isPending} onClick={() => testMutation.mutate(draft)}>{testMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <FlaskConical aria-hidden="true" size={18} />}{testMutation.isPending ? '正在测试' : '测试连接'}</button>
            {testResult ? <InlineMessage tone={testResult.ok ? 'success' : 'danger'}>{testResult.message}</InlineMessage> : <span>建议测试成功后再保存。</span>}
            {testMutation.error ? <InlineMessage tone="danger">{testMutation.error.message}</InlineMessage> : null}
          </div>
        </> : null}

        {error ? <InlineMessage tone="danger">{error}</InlineMessage> : null}
        {saveMutation.error ? <InlineMessage tone="danger">{saveMutation.error.message}</InlineMessage> : null}
      </section>

      <div className="wizard-actions">
        <button className="button secondary" type="button" onClick={() => step === 0 ? navigate(existing ? `/targets/${existing.id}` : '/targets') : setStep((value) => value - 1)}>{step === 0 ? <X aria-hidden="true" size={18} /> : <ArrowLeft aria-hidden="true" size={18} />}{step === 0 ? '取消' : '上一步'}</button>
        <button className="button primary" type="submit" disabled={saveMutation.isPending}>{saveMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : step === steps.length - 1 ? <Check aria-hidden="true" size={18} /> : <ArrowRight aria-hidden="true" size={18} />}{saveMutation.isPending ? '正在保存' : step === steps.length - 1 ? editing ? '保存修改' : '添加渠道' : '下一步'}</button>
      </div>
    </form>
  )
}

export default function TargetWizardPage() {
  const { id } = useParams()
  const [searchParams] = useSearchParams()
  const recoverBrowserSession = searchParams.get('auth') === 'browser'
  const [createSettingsReady, setCreateSettingsReady] = useState(false)
  const targetQuery = useQuery({ queryKey: ['target', id], queryFn: () => api.target(id!), enabled: Boolean(id) })
  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.settings, enabled: !id, refetchOnMount: 'always' })
  useEffect(() => {
    if (!id && settingsQuery.data && !settingsQuery.isFetching) setCreateSettingsReady(true)
  }, [id, settingsQuery.data, settingsQuery.isFetching])
  if (id && targetQuery.isPending) return <LoadingView label="正在读取渠道配置" />
  if (id && targetQuery.isError) return <ErrorView message={targetQuery.error.message} onRetry={() => void targetQuery.refetch()} />
  if (!id && settingsQuery.isError && !settingsQuery.data) return <ErrorView message={settingsQuery.error.message} onRetry={() => void settingsQuery.refetch()} />
  if (!id && !createSettingsReady) return <LoadingView label="正在读取默认检测设置" />

  return (
    <div className="page-stack narrow-page">
      <PageHeader title={id ? '编辑渠道' : '添加渠道'} description={id ? '秘密字段留空时会保留服务器中原有的值。' : '完成四步设置后，服务器会开始定时检测。'} actions={<Link className="button ghost" to={id ? `/targets/${id}` : '/targets'}><ArrowLeft aria-hidden="true" size={18} />返回</Link>} />
      <WizardForm key={`${targetQuery.data?.id ?? 'new'}:${recoverBrowserSession}`} existing={targetQuery.data} recoverBrowserSession={recoverBrowserSession} defaultCheckIntervalMinutes={settingsQuery.data?.defaultCheckIntervalMinutes ?? 5} />
    </div>
  )
}
