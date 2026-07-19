import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Check, Copy, KeyRound, Laptop, LoaderCircle, Moon, Save, Send, ShieldCheck, Smartphone, Sun, Trash2 } from 'lucide-react'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { useTheme } from '../hooks/useTheme'
import { canUsePush, enablePush } from '../lib/push'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import type { Settings, ThemePreference, TotpSetup } from '../types'

const themeOptions: { value: ThemePreference; label: string; icon: typeof Sun }[] = [
  { value: 'system', label: '跟随系统', icon: Laptop },
  { value: 'light', label: '浅色', icon: Sun },
  { value: 'dark', label: '深色', icon: Moon }
]

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
      <PageHeader title="系统与安全" description="调整检测保留策略、界面主题、推送设备和管理员保护。" />

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
