import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Check, Eye, EyeOff, FlaskConical, LoaderCircle, Search, X } from 'lucide-react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api/client'
import { ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import type { Target, TargetDraft, TargetKind, TestConnectionResult, ThresholdDraft } from '../types'
import { metricLabels, targetKindLabels } from '../types'

const steps = ['基本信息', '登录方式', '指标阈值', '检测与保存']

const thresholdsByKind: Record<TargetKind, ThresholdDraft[]> = {
  new_api: [
    { key: 'wallet_balance', label: '钱包余额', value: '20', unit: '站点单位' },
    { key: 'subscription_balance', label: '订阅余额', value: '20', unit: '站点单位' }
  ],
  sub2api: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: 'USD' }],
  chatgpt2api: [{ key: 'image_quota', label: '图片额度总和', value: '80', unit: '次' }],
  custom: [{ key: 'wallet_balance', label: '自定义指标', value: '10', unit: '个' }]
}

function makeDraft(kind: TargetKind = 'new_api'): TargetDraft {
  return {
    name: '',
    kind,
    baseUrl: '',
    topupUrl: '',
    enabled: true,
    checkIntervalMinutes: 5,
    username: '',
    email: '',
    password: '',
    totpSecret: '',
    totpCode: '',
    accessToken: '',
    refreshToken: '',
    adminKey: '',
    userId: '',
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
    authType: target.authType ?? draft.authType,
    requestMethod: target.requestMethod ?? draft.requestMethod,
    confirmPost: target.confirmPost ?? draft.confirmPost,
    jsonPointer: target.jsonPointer ?? draft.jsonPointer,
    statusPointer: target.statusPointer ?? draft.statusPointer,
    // 自定义请求头可能包含秘密，接口只返回是否已配置；编辑时留空表示沿用原值。
    customHeaders: target.customHeadersConfigured ? '' : draft.customHeaders,
    thresholds: target.metrics.filter((item) => item.threshold !== undefined).map((item) => ({ key: item.key, label: item.label, value: item.threshold ?? '', unit: item.unit }))
  }
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

function AuthenticationFields({ draft, update, editing }: { draft: TargetDraft; update: (patch: Partial<TargetDraft>) => void; editing: boolean }) {
  const [showSecret, setShowSecret] = useState(false)
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

  const isSub = draft.kind === 'sub2api'
  const isChat = draft.kind === 'chatgpt2api'
  return (
    <>
      {!isChat ? (
        <label className="field"><span>{isSub ? '邮箱' : '账号'}</span><input type={isSub ? 'email' : 'text'} value={isSub ? draft.email : draft.username} onChange={(event) => update(isSub ? { email: event.target.value } : { username: event.target.value })} autoComplete="username" /></label>
      ) : null}
      {!isChat ? <SecretField label="登录密码" value={draft.password} show={showSecret} editing={editing} onChange={(value) => update({ password: value })} onToggle={() => setShowSecret((value) => !value)} /> : null}
      <SecretField label={isChat ? '管理员密钥' : '访问令牌'} value={isChat ? draft.adminKey : draft.accessToken} show={showSecret} editing={editing} onChange={(value) => update(isChat ? { adminKey: value } : { accessToken: value })} onToggle={() => setShowSecret((value) => !value)} optional />
      {isSub ? <SecretField label="刷新令牌" value={draft.refreshToken} show={showSecret} editing={editing} onChange={(value) => update({ refreshToken: value })} onToggle={() => setShowSecret((value) => !value)} optional /> : null}
      {draft.kind === 'new_api' ? <label className="field"><span>用户 ID <em>令牌登录时填写</em></span><input value={draft.userId} onChange={(event) => update({ userId: event.target.value })} inputMode="numeric" /></label> : null}
      {!isChat ? <SecretField label="二步验证密钥（自动生成验证码）" value={draft.totpSecret} show={showSecret} editing={editing} onChange={(value) => update({ totpSecret: value })} onToggle={() => setShowSecret((value) => !value)} optional /> : null}
      {!isChat ? <label className="field"><span>一次性二步验证码 <em>仅首次连接测试</em></span><input value={draft.totpCode} onChange={(event) => update({ totpCode: event.target.value })} inputMode="numeric" autoComplete="one-time-code" /></label> : null}
      <div className="inline-message tone-info span-2">{isChat ? '管理员密钥仅用于读取脱敏账号明细；不执行刷新、重登、导入或删除。' : '自动检测优先使用访问令牌或加密保存的二步验证密钥；一次性验证码只用于首次连接测试。若站点要求浏览器挑战，请直接粘贴访问令牌。'}</div>
    </>
  )
}

function SecretField({ label, value, show, editing, optional, onChange, onToggle }: { label: string; value: string; show: boolean; editing: boolean; optional?: boolean; onChange: (value: string) => void; onToggle: () => void }) {
  return (
    <label className="field">
      <span>{label} {optional ? <em>可选</em> : null}</span>
      <span className="input-with-action"><input type={show ? 'text' : 'password'} value={value} onChange={(event) => onChange(event.target.value)} autoComplete="off" placeholder={editing ? '留空表示保持不变' : ''} /><button className="input-icon-button" type="button" aria-label={show ? '隐藏秘密' : '显示秘密'} onClick={onToggle}>{show ? <EyeOff aria-hidden="true" size={18} /> : <Eye aria-hidden="true" size={18} />}</button></span>
    </label>
  )
}

function WizardForm({ existing }: { existing?: Target }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<TargetDraft>(() => existing ? targetToDraft(existing) : makeDraft())
  const [step, setStep] = useState(0)
  const [error, setError] = useState('')
  const [testResult, setTestResult] = useState<TestConnectionResult | null>(null)
  const [detectionMessage, setDetectionMessage] = useState('')
  const editing = Boolean(existing)

  const update = (patch: Partial<TargetDraft>) => setDraft((current) => ({ ...current, ...patch }))
  const changeKind = (kind: TargetKind, preserveDetectionMessage = false) => {
    const candidate = getTopupCandidate(draft.baseUrl, kind)
    setDraft((current) => ({ ...current, kind, thresholds: thresholdsByKind[kind].map((item) => ({ ...item })), topupUrl: candidate, authType: kind === 'custom' ? 'none' : current.authType }))
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
          <div className="form-grid"><AuthenticationFields draft={draft} update={update} editing={editing} /></div>
        </> : null}

        {step === 2 ? <>
          <div className="panel-heading"><h2 id="step-title-2">指标与阈值</h2><p>不同单位分别判断，钱包、订阅额度和图片次数不会相加。</p></div>
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
            {draft.thresholds.map((threshold, index) => (
              <div className="threshold-row" key={`${threshold.key}-${index}`}>
                {draft.kind === 'custom' ? <label className="field"><span>指标名称</span><input value={threshold.label} onChange={(event) => update({ thresholds: draft.thresholds.map((item, itemIndex) => itemIndex === index ? { ...item, label: event.target.value } : item) })} /></label> : <div className="threshold-label"><span>{metricLabels[threshold.key]}</span><small>{threshold.key}</small></div>}
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
  const targetQuery = useQuery({ queryKey: ['target', id], queryFn: () => api.target(id!), enabled: Boolean(id) })
  if (id && targetQuery.isPending) return <LoadingView label="正在读取渠道配置" />
  if (id && targetQuery.isError) return <ErrorView message={targetQuery.error.message} onRetry={() => void targetQuery.refetch()} />

  return (
    <div className="page-stack narrow-page">
      <PageHeader title={id ? '编辑渠道' : '添加渠道'} description={id ? '秘密字段留空时会保留服务器中原有的值。' : '完成四步设置后，服务器会开始定时检测。'} actions={<Link className="button ghost" to={id ? `/targets/${id}` : '/targets'}><ArrowLeft aria-hidden="true" size={18} />返回</Link>} />
      <WizardForm key={targetQuery.data?.id ?? 'new'} existing={targetQuery.data} />
    </div>
  )
}
