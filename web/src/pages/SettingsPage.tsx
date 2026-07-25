import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Check, Copy, KeyRound, Laptop, LoaderCircle, Mail, Moon, Save, Send, ShieldCheck, Smartphone, Sun, Trash2 } from 'lucide-react'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { useTheme } from '../hooks/useTheme'
import { canUsePush, enablePush } from '../lib/push'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import type { EmailProvider, EmailSecurity, EmailSettings, EmailSettingsInput, Settings, ThemePreference, TotpSetup } from '../types'

const themeOptions: { value: ThemePreference; label: string; icon: typeof Sun }[] = [
  { value: 'system', label: '跟随系统', icon: Laptop },
  { value: 'light', label: '浅色', icon: Sun },
  { value: 'dark', label: '深色', icon: Moon }
]

const emailProviderOptions: Array<{ value: EmailProvider; label: string; description: string }> = [
  { value: 'qq', label: 'QQ 邮箱', description: '使用 QQ 邮箱设置中的 SMTP 授权码。' },
  { value: '163', label: '163 邮箱', description: '使用 163 邮箱设置中的客户端授权码。' },
  { value: 'gmail', label: 'Gmail', description: '开启二步验证后使用应用专用密码。' },
  { value: 'outlook', label: 'Outlook', description: '使用支持 SMTP 登录的 Outlook 邮箱。' },
  { value: 'custom', label: '自定义 SMTP', description: '手工填写发件服务器及连接安全方式。' }
]

const emailProviderPresets: Record<Exclude<EmailProvider, 'custom'>, { host: string; port: number; security: EmailSecurity }> = {
  qq: { host: 'smtp.qq.com', port: 465, security: 'tls' },
  '163': { host: 'smtp.163.com', port: 465, security: 'tls' },
  gmail: { host: 'smtp.gmail.com', port: 465, security: 'tls' },
  outlook: { host: 'smtp.office365.com', port: 587, security: 'starttls' }
}

interface EmailFormState extends Omit<EmailSettings, 'recipients'> {
  password: string
  recipientsText: string
}

function toEmailForm(settings: EmailSettings): EmailFormState {
  const { recipients, ...rest } = settings
  // 同时兼容旧版本接口和浏览器缓存中的空值，避免整个设置页因格式异常而中断渲染。
  const safeRecipients = Array.isArray(recipients) ? recipients : []
  return { ...rest, password: '', recipientsText: safeRecipients.join('\n') }
}

function parseEmailRecipients(value: string): string[] {
  return [...new Set(value.split(/[\n,;]+/).map((item) => item.trim()).filter(Boolean))]
}

function toEmailInput(form: EmailFormState): EmailSettingsInput {
  return {
    enabled: form.enabled,
    provider: form.provider,
    host: form.host.trim(),
    port: form.port,
    security: form.security,
    username: form.username.trim(),
    ...(form.password ? { password: form.password } : {}),
    fromName: form.fromName.trim(),
    fromAddress: form.fromAddress.trim(),
    recipients: parseEmailRecipients(form.recipientsText)
  }
}

// 已保存授权码只绑定到保存时的服务商、服务器和发件账号。
function canReuseEmailPassword(form: EmailFormState, saved?: EmailSettings): boolean {
  if (!saved?.passwordConfigured) return false
  return form.provider === saved.provider && form.host.trim().toLowerCase().replace(/\.$/, '') === saved.host.trim().toLowerCase().replace(/\.$/, '') &&
    form.port === saved.port && form.security === saved.security && form.username.trim() === saved.username.trim()
}

function validateEmailForm(form: EmailFormState, requireComplete: boolean, passwordReusable: boolean): string {
  if (!requireComplete) {
    return form.passwordConfigured && !passwordReusable && !form.password
      ? '发件服务商、服务器或账号已变更，请重新填写 SMTP 授权码或应用密码。'
      : ''
  }
  if (!form.host.trim()) return '请填写 SMTP 服务器地址。'
  if (!Number.isInteger(form.port) || form.port < 1 || form.port > 65535) return 'SMTP 端口需要在 1 至 65535 之间。'
  if (!form.username.trim()) return '请填写发件邮箱账号。'
  if (!passwordReusable && !form.password) {
    return form.passwordConfigured
      ? '发件服务商、服务器或账号已变更，请重新填写 SMTP 授权码或应用密码。'
      : '请填写 SMTP 授权码或应用密码。'
  }
  if (!/^\S+@\S+\.\S+$/.test(form.fromAddress.trim())) return '请填写有效的发件邮箱地址。'
  const recipients = parseEmailRecipients(form.recipientsText)
  if (!recipients.length) return '请至少填写一个收件邮箱。'
  if (recipients.some((address) => !/^\S+@\S+\.\S+$/.test(address))) return '收件邮箱格式有误，请每行填写一个邮箱。'
  return ''
}

function EmailSettingsSection() {
  const queryClient = useQueryClient()
  const emailQuery = useQuery({ queryKey: ['email'], queryFn: api.emailSettings })
  const [form, setForm] = useState<EmailFormState | null>(null)
  const [validationError, setValidationError] = useState('')

  useEffect(() => {
    if (emailQuery.data) setForm(toEmailForm(emailQuery.data))
  }, [emailQuery.data])

  const saveMutation = useMutation({
    mutationFn: (payload: EmailSettingsInput) => api.updateEmailSettings(payload),
    onSuccess: (settings) => {
      queryClient.setQueryData(['email'], settings)
      setForm(toEmailForm(settings))
    }
  })
  const testMutation = useMutation({ mutationFn: (payload: EmailSettingsInput) => api.testEmailSettings(payload) })
  const clearMutation = useMutation({
    mutationFn: api.deleteEmailSettings,
    onSuccess: (settings) => {
      queryClient.setQueryData(['email'], settings)
      setForm(toEmailForm(settings))
      saveMutation.reset()
      testMutation.reset()
    }
  })

  const passwordReusable = form ? canReuseEmailPassword(form, emailQuery.data) : false

  const changeProvider = (provider: EmailProvider) => {
    setValidationError('')
    setForm((current) => {
      if (!current) return current
      if (provider === 'custom') return { ...current, provider, password: '' }
      return { ...current, provider, ...emailProviderPresets[provider], password: '' }
    })
  }

  const changeUsername = (username: string) => {
    setValidationError('')
    setForm((current) => {
      if (!current) return current
      const syncFromAddress = !current.fromAddress || current.fromAddress === current.username
      const currentRecipients = current.recipientsText.trim()
      const syncRecipients = !currentRecipients || currentRecipients === current.username
      return {
        ...current,
        username,
        password: username === current.username ? current.password : '',
        fromAddress: syncFromAddress ? username : current.fromAddress,
        recipientsText: syncRecipients ? username : current.recipientsText
      }
    })
  }

  const submitEmailSettings = (event: FormEvent) => {
    event.preventDefault()
    if (!form) return
    const message = validateEmailForm(form, form.enabled, passwordReusable)
    setValidationError(message)
    if (!message) saveMutation.mutate(toEmailInput(form))
  }

  const sendTestEmail = () => {
    if (!form) return
    const message = validateEmailForm(form, true, passwordReusable)
    setValidationError(message)
    if (!message) testMutation.mutate(toEmailInput(form))
  }

  const clearEmailSettings = () => {
    if (!window.confirm('确定清除邮件设置和已保存的授权码吗？清除后邮件提醒会停止。')) return
    setValidationError('')
    clearMutation.mutate()
  }

  const operationPending = saveMutation.isPending || testMutation.isPending || clearMutation.isPending

  return (
    <section className="settings-section" aria-labelledby="email-title">
      <div className="settings-heading"><span className="settings-icon"><Mail aria-hidden="true" /></span><div><h2 id="email-title">邮件提醒</h2><p>告警和恢复事件可同时发送到一个或多个邮箱。</p></div></div>
      <div className="email-free-note"><strong>无需额外付费接口</strong><span>可直接使用 QQ、163、Gmail 或 Outlook 邮箱自带的 SMTP 发信功能。</span></div>

      {emailQuery.isPending ? <div className="email-settings-state"><LoaderCircle className="spin" aria-hidden="true" size={20} /><span>正在读取邮件设置</span></div> : null}
      {emailQuery.isError ? <div className="email-settings-state error"><span>{emailQuery.error.message}</span><button className="button secondary" type="button" onClick={() => void emailQuery.refetch()}>重新读取</button></div> : null}

      {form ? (
        <form className="settings-form email-settings-form" onSubmit={submitEmailSettings} noValidate>
          <label className="toggle-row span-2"><input type="checkbox" checked={form.enabled} onChange={(event) => { setValidationError(''); setForm({ ...form, enabled: event.target.checked }) }} aria-label="启用邮件提醒" /><span><strong>启用邮件提醒</strong><small>开启后，新告警和恢复通知会由服务器发送邮件；保存前可先测试当前填写的设置。</small></span></label>

          <label className="field"><span>邮箱服务商</span><select value={form.provider} onChange={(event) => changeProvider(event.target.value as EmailProvider)}>{emailProviderOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select><small>{emailProviderOptions.find((option) => option.value === form.provider)?.description}</small></label>
          <label className="field"><span>发件邮箱账号</span><input type="email" inputMode="email" autoComplete="email" value={form.username} onChange={(event) => changeUsername(event.target.value)} placeholder="name@example.com" /></label>

          <label className="field"><span>SMTP 服务器</span><input value={form.host} readOnly={form.provider !== 'custom'} onChange={(event) => { setValidationError(''); setForm({ ...form, host: event.target.value, password: event.target.value === form.host ? form.password : '' }) }} placeholder="smtp.example.com" /></label>
          <label className="field"><span>SMTP 端口</span><input type="number" min="1" max="65535" inputMode="numeric" value={form.port} readOnly={form.provider !== 'custom'} onChange={(event) => { const port = Number(event.target.value); setValidationError(''); setForm({ ...form, port, password: port === form.port ? form.password : '' }) }} /></label>
          <label className="field"><span>连接安全</span><select value={form.security} disabled={form.provider !== 'custom'} onChange={(event) => { const security = event.target.value as EmailSecurity; setValidationError(''); setForm({ ...form, security, password: security === form.security ? form.password : '' }) }}><option value="tls">SSL/TLS</option><option value="starttls">STARTTLS</option></select></label>
          <label className="field"><span>SMTP 授权码或应用密码 <em>{passwordReusable ? '已配置，认证身份未变时可留空沿用' : form.passwordConfigured ? '认证身份已变更，请重新填写' : '首次配置必填'}</em></span><input type="password" autoComplete="new-password" value={form.password} onChange={(event) => { setValidationError(''); setForm({ ...form, password: event.target.value }) }} placeholder={passwordReusable ? '留空表示保持现有授权码' : '请输入新的授权码或应用密码'} /><small>更换服务商、服务器、端口、连接安全或发件账号后，需要重新填写。</small></label>

          <label className="field"><span>发件人名称 <em>可选</em></span><input value={form.fromName} onChange={(event) => { setValidationError(''); setForm({ ...form, fromName: event.target.value }) }} placeholder="号池监控" /></label>
          <label className="field"><span>发件邮箱地址</span><input type="email" inputMode="email" autoComplete="email" value={form.fromAddress} onChange={(event) => { setValidationError(''); setForm({ ...form, fromAddress: event.target.value }) }} placeholder="name@example.com" /></label>
          <label className="field span-2"><span>收件邮箱</span><textarea rows={3} value={form.recipientsText} onChange={(event) => { setValidationError(''); setForm({ ...form, recipientsText: event.target.value }) }} inputMode="email" autoComplete="email" placeholder={'owner@example.com\nbackup@example.com'} /><small>每行填写一个邮箱，也可以使用逗号或分号分隔。</small></label>

          {validationError ? <div className="span-2"><InlineMessage tone="danger">{validationError}</InlineMessage></div> : null}
          {saveMutation.error ? <div className="span-2"><InlineMessage tone="danger">保存邮件设置失败：{saveMutation.error.message}</InlineMessage></div> : null}
          {saveMutation.isSuccess ? <div className="span-2"><InlineMessage tone="success">邮件设置已保存。</InlineMessage></div> : null}
          {testMutation.error ? <div className="span-2"><InlineMessage tone="danger">测试邮件发送失败：{testMutation.error.message}</InlineMessage></div> : null}
          {testMutation.isSuccess ? <div className="span-2"><InlineMessage tone="success">测试邮件已发送，请检查收件箱和垃圾邮件目录。</InlineMessage></div> : null}
          {clearMutation.error ? <div className="span-2"><InlineMessage tone="danger">清除邮件设置失败：{clearMutation.error.message}</InlineMessage></div> : null}
          {clearMutation.isSuccess ? <div className="span-2"><InlineMessage tone="success">邮件设置和已保存的授权码已清除。</InlineMessage></div> : null}

          <div className="email-settings-actions span-2">
            <button className="button primary" type="submit" disabled={operationPending}>{saveMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Save aria-hidden="true" size={18} />}{saveMutation.isPending ? '正在保存' : '保存邮件设置'}</button>
            <button className="button secondary" type="button" disabled={operationPending} onClick={sendTestEmail}>{testMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Send aria-hidden="true" size={18} />}{testMutation.isPending ? '正在发送' : '发送测试邮件'}</button>
            <button className="button danger" type="button" disabled={operationPending} onClick={clearEmailSettings}>{clearMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Trash2 aria-hidden="true" size={18} />}{clearMutation.isPending ? '正在清除' : '清除邮件设置'}</button>
          </div>
        </form>
      ) : null}
    </section>
  )
}

export default function SettingsPage() {
  const queryClient = useQueryClient()
  const { preference, setPreference } = useTheme()
  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const pushQuery = useQuery({ queryKey: ['push'], queryFn: api.pushInfo })
  const [form, setForm] = useState<Settings | null>(null)
  const [deviceName, setDeviceName] = useState('这台设备')
  const [totpSetup, setTotpSetup] = useState<TotpSetup | null>(null)
  const [totpCode, setTotpCode] = useState('')
	const [disableTotpCode, setDisableTotpCode] = useState('')
  const [copied, setCopied] = useState(false)

  useEffect(() => { if (settingsQuery.data) setForm(settingsQuery.data) }, [settingsQuery.data])

  const saveMutation = useMutation({
    mutationFn: (value: Settings) => api.updateSettings(value),
    onSuccess: (value) => {
      setForm(value)
      // 保存接口返回服务器最终设置，立即写入共享缓存，避免新增渠道读到旧默认值。
      queryClient.setQueryData(['settings'], value)
    }
  })
  const pushMutation = useMutation({
    mutationFn: () => enablePush(pushQuery.data?.vapidPublicKey ?? '', deviceName),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['push'] })
  })
  const pushTestMutation = useMutation({ mutationFn: api.testPush })
  const removeDeviceMutation = useMutation({ mutationFn: api.removePushDevice, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['push'] }) })
  const startTotpMutation = useMutation({ mutationFn: api.startTotp, onSuccess: setTotpSetup })
  const confirmTotpMutation = useMutation({ mutationFn: () => api.confirmTotp(totpCode), onSuccess: () => { setTotpSetup(null); setTotpCode(''); void queryClient.invalidateQueries({ queryKey: ['settings'] }) } })
	const disableTotpMutation = useMutation({
		mutationFn: () => api.disableTotp(disableTotpCode),
		onSuccess: () => {
			setDisableTotpCode('')
			setForm((current) => current ? { ...current, totpEnabled: false } : current)
			void queryClient.invalidateQueries({ queryKey: ['settings'] })
			void queryClient.invalidateQueries({ queryKey: ['bootstrap'] })
		}
	})

  const submitSettings = (event: FormEvent) => {
    event.preventDefault()
    if (form) saveMutation.mutate(form)
  }

  if (settingsQuery.isPending || pushQuery.isPending) return <LoadingView label="正在读取系统设置" />
  if (settingsQuery.isError) return <ErrorView message={settingsQuery.error.message} onRetry={() => void settingsQuery.refetch()} />
  if (pushQuery.isError) return <ErrorView message={pushQuery.error.message} onRetry={() => void pushQuery.refetch()} />
  if (!form) return <LoadingView label="正在读取系统设置" />

  return (
    <div className="page-stack settings-page">
      <PageHeader title="系统与安全" description="调整检测保留策略、界面主题、通知方式和管理员保护。" />

      <section className="settings-section" aria-labelledby="general-title">
        <div className="settings-heading"><span className="settings-icon"><Save aria-hidden="true" /></span><div><h2 id="general-title">常规设置</h2><p>这些设置由服务器统一应用到所有前端。</p></div></div>
        <form className="settings-form" onSubmit={submitSettings}>
          <label className="field"><span>产品名称</span><input value={form.productName} onChange={(event) => setForm({ ...form, productName: event.target.value })} /></label>
          <label className="field"><span>历史保留天数</span><input type="number" min="1" max="365" value={form.historyRetentionDays} onChange={(event) => setForm({ ...form, historyRetentionDays: Number(event.target.value) })} inputMode="numeric" /><small>允许 1 至 365 天，默认 7 天。</small></label>
          <label className="field"><span>默认检测间隔（分钟）</span><input type="number" min="1" max="1440" value={form.defaultCheckIntervalMinutes} onChange={(event) => setForm({ ...form, defaultCheckIntervalMinutes: Number(event.target.value) })} inputMode="numeric" /></label>
          <label className="toggle-row span-2"><input type="checkbox" checked={form.allowPrivateTargets} disabled /><span><strong>允许访问自有内网地址</strong><small>此项由服务器部署配置控制；链路本地和云元数据地址始终保持阻止。</small></span></label>
          {saveMutation.error ? <InlineMessage tone="danger">{saveMutation.error.message}</InlineMessage> : null}
          {saveMutation.isSuccess ? <InlineMessage tone="success">设置已保存。</InlineMessage> : null}
          <div className="form-actions span-2"><button className="button primary" type="submit" disabled={saveMutation.isPending}>{saveMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Save aria-hidden="true" size={18} />}{saveMutation.isPending ? '正在保存' : '保存设置'}</button></div>
        </form>
      </section>

      <section className="settings-section" aria-labelledby="appearance-title">
        <div className="settings-heading"><span className="settings-icon"><Sun aria-hidden="true" /></span><div><h2 id="appearance-title">界面主题</h2><p>主题选择保存在当前设备。</p></div></div>
        <div className="segmented-control" role="radiogroup" aria-label="界面主题">{themeOptions.map(({ value, label, icon: Icon }) => <button key={value} type="button" role="radio" aria-checked={preference === value} className={preference === value ? 'selected' : ''} onClick={() => setPreference(value)}><Icon aria-hidden="true" size={18} />{label}</button>)}</div>
      </section>

      <section className="settings-section" aria-labelledby="push-title">
        <div className="settings-heading"><span className="settings-icon"><Bell aria-hidden="true" /></span><div><h2 id="push-title">推送设备</h2><p>额度或账号异常时，服务器会向已订阅设备发送系统通知。</p></div></div>
        {!canUsePush() ? <InlineMessage tone="warning">当前浏览器不支持 Web Push，请使用新版 Chrome 或 Edge 并通过 HTTPS 访问。</InlineMessage> : null}
        <div className="push-actions"><label className="field"><span>设备名称</span><input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} /></label><button className="button primary" type="button" disabled={!canUsePush() || pushMutation.isPending} onClick={() => pushMutation.mutate()}>{pushMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Smartphone aria-hidden="true" size={18} />}{pushMutation.isPending ? '正在启用' : '在此设备启用'}</button><button className="button secondary" type="button" disabled={pushTestMutation.isPending} onClick={() => pushTestMutation.mutate()}><Send aria-hidden="true" size={18} />发送测试通知</button></div>
        {pushMutation.error ? <InlineMessage tone="danger">{pushMutation.error.message}</InlineMessage> : null}
        {pushMutation.isSuccess ? <InlineMessage tone="success">这台设备已订阅系统通知。</InlineMessage> : null}
        {pushTestMutation.isSuccess ? <InlineMessage tone="success">测试通知已发送，请检查系统通知中心。</InlineMessage> : null}
        {pushTestMutation.error ? <InlineMessage tone="danger">{pushTestMutation.error.message}</InlineMessage> : null}
        {pushQuery.data.devices.length ? <div className="device-list">{pushQuery.data.devices.map((device) => <article key={device.id}><span className="device-icon">{device.userAgent.toLowerCase().includes('android') ? <Smartphone aria-hidden="true" /> : <Laptop aria-hidden="true" />}</span><div><strong>{device.name}{device.current ? <small>当前</small> : null}</strong><span>{device.userAgent}</span><span>最近使用 {formatRelativeTime(device.lastSeenAt ?? device.createdAt)}</span></div><button className="icon-button danger-icon" type="button" aria-label={`移除 ${device.name}`} disabled={removeDeviceMutation.isPending} onClick={() => removeDeviceMutation.mutate(device.id)}><Trash2 aria-hidden="true" size={18} /></button></article>)}</div> : <EmptyState title="还没有推送设备" description="在常用电脑和安卓手机上分别打开本页并启用。" />}
      </section>

      <EmailSettingsSection />

      <section className="settings-section" aria-labelledby="security-title">
        <div className="settings-heading"><span className="settings-icon"><ShieldCheck aria-hidden="true" /></span><div><h2 id="security-title">管理员二步验证</h2><p>登录时可使用认证器验证码，恢复码用于设备遗失时登录。</p></div></div>
        <div className="security-status"><span className={form.totpEnabled ? 'security-badge enabled' : 'security-badge'}>{form.totpEnabled ? <Check aria-hidden="true" size={17} /> : <KeyRound aria-hidden="true" size={17} />}{form.totpEnabled ? '已启用' : '未启用'}</span>{!form.totpEnabled ? <button className="button secondary" type="button" disabled={startTotpMutation.isPending} onClick={() => startTotpMutation.mutate()}><KeyRound aria-hidden="true" size={18} />开始配置</button> : null}</div>
        {startTotpMutation.error ? <InlineMessage tone="danger">{startTotpMutation.error.message}</InlineMessage> : null}
        {totpSetup ? <div className="totp-setup"><h3>在认证器中添加密钥</h3><p>将以下密钥添加到认证器，然后输入 6 位验证码确认。</p><div className="secret-copy"><code>{totpSetup.secret}</code><button className="icon-button" type="button" aria-label="复制密钥" onClick={async () => { await navigator.clipboard.writeText(totpSetup.secret); setCopied(true) }}><Copy aria-hidden="true" size={18} /></button></div>{copied ? <InlineMessage tone="success">密钥已复制。</InlineMessage> : null}<label className="field"><span>认证器验证码</span><input value={totpCode} onChange={(event) => setTotpCode(event.target.value)} inputMode="numeric" autoComplete="one-time-code" maxLength={8} /></label><div className="recovery-codes"><strong>恢复码（请立即离线保存）</strong><div>{totpSetup.recoveryCodes.map((code) => <code key={code}>{code}</code>)}</div></div>{confirmTotpMutation.error ? <InlineMessage tone="danger">{confirmTotpMutation.error.message}</InlineMessage> : null}<button className="button primary" type="button" disabled={totpCode.length < 6 || confirmTotpMutation.isPending} onClick={() => confirmTotpMutation.mutate()}>{confirmTotpMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Check aria-hidden="true" size={18} />}确认并启用</button></div> : null}
		{form.totpEnabled ? <div className="totp-disable"><label className="field"><span>关闭二步验证</span><input value={disableTotpCode} onChange={(event) => setDisableTotpCode(event.target.value)} inputMode="text" autoComplete="one-time-code" autoCapitalize="characters" placeholder="动态验证码或恢复码" /></label><button className="button danger" type="button" disabled={disableTotpCode.trim().length < 6 || disableTotpMutation.isPending} onClick={() => disableTotpMutation.mutate()}>{disableTotpMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Trash2 aria-hidden="true" size={18} />}验证并关闭</button>{disableTotpMutation.error ? <InlineMessage tone="danger">{disableTotpMutation.error.message}</InlineMessage> : null}{disableTotpMutation.isSuccess ? <InlineMessage tone="success">管理员二步验证已关闭。</InlineMessage> : null}</div> : null}
      </section>

      <footer className="settings-footer">系统时间：{formatDateTime(new Date().toISOString())}</footer>
    </div>
  )
}
