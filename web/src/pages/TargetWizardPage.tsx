import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Check, ExternalLink, Eye, EyeOff, FlaskConical, Globe, KeyRound, LoaderCircle, Lock, Search, X } from 'lucide-react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api/client'
import { ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import type { CredentialMode, Target, TargetAuthAttempt, TargetDraft, TargetKind, TestConnectionResult, ThresholdDraft } from '../types'
import { metricLabels, targetKindLabels } from '../types'

const steps = ['基本信息', '登录方式', '指标阈值', '检测与保存']

const newAPISubscriptionThreshold: ThresholdDraft = {
  key: 'subscription_balance',
  label: '订阅余额',
  value: '20',
  unit: '站点单位',
  comparison: 'lte'
}

const thresholdsByKind: Record<TargetKind, ThresholdDraft[]> = {
  new_api: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: '站点单位', comparison: 'lte' }],
  sub2api: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: 'USD', comparison: 'lte' }],
  chatgpt2api: [{ key: 'image_quota', label: '图片额度总和', value: '80', unit: '次', comparison: 'lte' }],
  cliproxyapi: [
    { key: 'healthy_accounts', label: '可用账号', value: '0', unit: '个', comparison: 'lte' },
    { key: 'limited_accounts', label: '限流账号', value: '1', unit: '个', comparison: 'gte' },
    { key: 'error_accounts', label: '异常账号', value: '1', unit: '个', comparison: 'gte' }
  ],
  custom: [{ key: 'wallet_balance', label: '自定义指标', value: '10', unit: '个', comparison: 'lte' }]
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

export function targetToDraft(target: Target): TargetDraft {
  const draft = makeDraft(target.kind)
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
    thresholds: target.metrics.filter((item) => item.threshold !== undefined).map((item) => ({
      key: item.key,
      label: item.label,
      value: item.threshold ?? '',
      unit: item.unit,
      // 旧版本数据没有比较方向，沿用原有的“小于等于”语义。
      comparison: item.comparison ?? 'lte'
    }))
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
  if (!accessToken) throw new Error('回调地址的 fragment 中没有 access_token。')
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
  setAttempt
}: {
  draft: TargetDraft
  update: (patch: Partial<TargetDraft>) => void
  editing: boolean
  configured: boolean
  attempt: TargetAuthAttempt | null
  setAttempt: (attempt: TargetAuthAttempt | null) => void
}) {
  const isAndroidApp = typeof navigator !== 'undefined' && navigator.userAgent.includes('PoolWatchAndroid/')
  const [pollError, setPollError] = useState('')
  const [callbackUrl, setCallbackUrl] = useState('')
  const [callbackError, setCallbackError] = useState('')
  const [callbackImported, setCallbackImported] = useState(false)

  const applyAttempt = (next: TargetAuthAttempt) => {
    setAttempt(next)
    if (next.status === 'ready') {
      update({ browserAuthAttemptId: next.id, userId: next.userId || draft.userId })
      setPollError('')
    } else if (next.status === 'expired' || next.status === 'cancelled') {
      update({ browserAuthAttemptId: '' })
    }
  }

  const createMutation = useMutation({
    mutationFn: () => api.createTargetAuthAttempt({ kind: draft.kind, baseUrl: draft.baseUrl }),
    onSuccess: applyAttempt
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
      <div className="browser-auth-actions">
        <button className="button secondary" type="button" disabled={createMutation.isPending || attempt?.status === 'waiting'} onClick={prepareLogin}>
          {createMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Globe aria-hidden="true" size={18} />}
          {createMutation.isPending ? '正在准备' : attempt ? '重新准备网页登录' : '准备网页登录'}
        </button>
        {attempt?.status === 'waiting' ? <button className="button primary" type="button" onClick={openLoginWindow}><ExternalLink aria-hidden="true" size={18} />打开授权窗口</button> : null}
        {attempt?.status === 'waiting' ? <button className="button ghost" type="button" disabled={cancelMutation.isPending} onClick={() => cancelMutation.mutate()}><X aria-hidden="true" size={18} />取消</button> : null}
      </div>
      {statusMessage ? <InlineMessage tone={attempt?.status === 'ready' ? 'success' : attempt?.status === 'expired' || attempt?.status === 'cancelled' ? 'danger' : 'info'}>{statusMessage}</InlineMessage> : null}
      {createMutation.error ? <InlineMessage tone="danger">{createMutation.error.message}</InlineMessage> : null}
      {cancelMutation.error ? <InlineMessage tone="danger">{cancelMutation.error.message}</InlineMessage> : null}
      {pollError ? <InlineMessage tone="danger">网页登录状态更新失败：{pollError}</InlineMessage> : null}

      {!isAndroidApp && draft.kind === 'new_api' ? (
        <div className="manual-auth-import">
          <div><strong>桌面浏览器手工导入</strong><p>在渠道页面完成 Linux.do、GitHub 等登录后，填写该站点的 Cookie 和用户 ID。</p></div>
          <div className="form-grid">
            <SecretField label="登录 Cookie" value={draft.cookie} show={false} editing={editing} onChange={(cookie) => update({ cookie, browserAuthAttemptId: '' })} onToggle={() => undefined} hideToggle />
            <label className="field"><span>用户 ID</span><input value={draft.userId} onChange={(event) => update({ userId: event.target.value, browserAuthAttemptId: '' })} inputMode="numeric" placeholder={editing ? '留空表示保持不变' : ''} /></label>
          </div>
          {draft.cookie && draft.userId ? <InlineMessage tone="success">Cookie 和用户 ID 已填写，保存前可在最后一步测试连接。</InlineMessage> : null}
        </div>
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
  setAttempt
}: {
  draft: TargetDraft
  update: (patch: Partial<TargetDraft>) => void
  existing?: Target
  attempt: TargetAuthAttempt | null
  setAttempt: (attempt: TargetAuthAttempt | null) => void
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
        <BrowserAuthorizationFields draft={draft} update={update} editing={editing} configured={configuredForCurrentMode} attempt={attempt} setAttempt={setAttempt} />
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

function WizardForm({ existing, defaultCheckIntervalMinutes }: { existing?: Target; defaultCheckIntervalMinutes: number }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<TargetDraft>(() => existing ? targetToDraft(existing) : makeDraft('new_api', defaultCheckIntervalMinutes))
  const [step, setStep] = useState(0)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<TestConnectionResult | null>(null)
  const [detectionMessage, setDetectionMessage] = useState('')
  const [authAttempt, setAuthAttempt] = useState<TargetAuthAttempt | null>(null)
  const editing = Boolean(existing)

  const update = (patch: Partial<TargetDraft>) => setDraft((current) => ({ ...current, ...patch }))
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
    setDraft((current) => ({
      ...current,
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
		onSuccess: (result) => {
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
      if (!draft.thresholds.length || draft.thresholds.some((item) => !item.value || !Number.isFinite(Number(item.value)))) return setError('每个指标都需要填写有效阈值。'), false
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

  return (
    <form className="wizard" onSubmit={handleSubmit} noValidate>
      <ol className="wizard-steps" aria-label="配置进度">
        {steps.map((label, index) => <li key={label} className={index === step ? 'active' : index < step ? 'done' : ''} aria-current={index === step ? 'step' : undefined}><span>{index < step ? <Check aria-hidden="true" size={16} /> : index + 1}</span><b>{label}</b></li>)}
      </ol>

      <section className="wizard-panel" aria-labelledby={`step-title-${step}`}>
        {step === 0 ? <>
          <div className="panel-heading"><h2 id="step-title-0">连接到渠道</h2><p>填写名称、类型与站点根地址。</p></div>
          <div className="form-grid">
            <label className="field"><span>渠道名称 <b aria-hidden="true">*</b></span><input autoFocus value={draft.name} onChange={(event) => update({ name: event.target.value })} placeholder="例如：主站额度" /></label>
            <label className="field"><span>渠道类型</span><select value={draft.kind} onChange={(event) => changeKind(event.target.value as TargetKind)}>{Object.entries(targetKindLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <div className="field span-2"><label htmlFor="target-base-url">站点地址 <b aria-hidden="true">*</b></label><div className="url-detect-row"><input id="target-base-url" type="url" value={draft.baseUrl} onChange={(event) => { update({ baseUrl: event.target.value }); setDetectionMessage('') }} onBlur={() => { if (!draft.topupUrl) update({ topupUrl: getTopupCandidate(draft.baseUrl, draft.kind) }) }} placeholder="https://api.example.com" inputMode="url" />{!editing ? <button className="button secondary" type="button" disabled={detectMutation.isPending || !draft.baseUrl.trim()} onClick={() => { setError(''); if (!validateUrl(draft.baseUrl)) { setError('请先输入有效的 HTTP 或 HTTPS 地址。'); return } detectMutation.mutate() }}>{detectMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Search aria-hidden="true" size={18} />}{detectMutation.isPending ? '识别中' : '自动识别'}</button> : null}</div><small>只允许 HTTP/HTTPS。服务器默认会阻止回环、内网和云元数据地址。</small></div>
            {detectionMessage ? <div className="span-2"><InlineMessage tone="success">{detectionMessage}</InlineMessage></div> : null}
            {detectMutation.error ? <div className="span-2"><InlineMessage tone="danger">{detectMutation.error.message}</InlineMessage></div> : null}
          </div>
        </> : null}

        {step === 1 ? <>
          <div className="panel-heading"><h2 id="step-title-1">登录与认证</h2><p>秘密只发送到服务器并加密保存，页面不会重新显示。</p></div>
          <div className="form-grid"><AuthenticationFields draft={draft} update={update} existing={existing} attempt={authAttempt} setAttempt={setAuthAttempt} /></div>
        </> : null}

        {step === 2 ? <>
          <div className="panel-heading"><h2 id="step-title-2">指标与阈值</h2><p>每个指标按自己的比较方向独立判断，不同单位不会相加。</p></div>
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
                {draft.kind === 'custom' ? <label className="field"><span>指标名称</span><input value={threshold.label} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, label: event.target.value } : item) })} /></label> : <div className="threshold-label"><span>{threshold.label || metricLabels[threshold.key]}</span><small>{threshold.key}</small></div>}
                <label className="field"><span>告警条件</span><select value={threshold.comparison} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, comparison: event.target.value as ThresholdDraft['comparison'] } : item) })}><option value="lte">小于或等于（≤）</option><option value="gte">大于或等于（≥）</option></select></label>
                <label className="field"><span>告警阈值</span><input type="number" min="0" step="any" value={threshold.value} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, value: event.target.value } : item) })} inputMode="decimal" /></label>
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
      <WizardForm key={targetQuery.data?.id ?? 'new'} existing={targetQuery.data} defaultCheckIntervalMinutes={settingsQuery.data?.defaultCheckIntervalMinutes ?? 5} />
    </div>
  )
}
