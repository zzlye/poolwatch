import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { ThemeProvider } from '../hooks/useTheme'
import SettingsPage from '../pages/SettingsPage'
import type { EmailSettings, Settings } from '../types'

const generalSettings: Settings = {
  productName: '号池监控',
  historyRetentionDays: 7,
  defaultCheckIntervalMinutes: 5,
  allowPrivateTargets: false,
  totpEnabled: false
}

const configuredEmail: EmailSettings = {
  enabled: true,
  provider: 'qq',
  host: 'smtp.qq.com',
  port: 465,
  security: 'tls',
  username: 'sender@qq.com',
  fromName: '号池监控',
  fromAddress: 'sender@qq.com',
  recipients: ['owner@example.com'],
  passwordConfigured: true
}

function renderSettingsPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <ThemeProvider><SettingsPage /></ThemeProvider>
    </QueryClientProvider>
  )
}

describe('邮件提醒设置', () => {
  beforeEach(() => {
    vi.spyOn(api, 'settings').mockResolvedValue(generalSettings)
    vi.spyOn(api, 'pushInfo').mockResolvedValue({ supported: false, vapidPublicKey: '', devices: [] })
  })

  afterEach(() => vi.restoreAllMocks())

  it('默认配置返回空收件人时设置页不会白屏', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue({
      ...configuredEmail,
      enabled: false,
      username: '',
      fromAddress: '',
      recipients: null as unknown as string[],
      passwordConfigured: false
    })
    renderSettingsPage()

    expect(await screen.findByRole('heading', { name: '邮件提醒' })).toBeInTheDocument()
    expect(await screen.findByLabelText(/收件邮箱/)).toHaveValue('')
  })

  it('提供常用邮箱预设并允许切换到自定义服务器', async () => {
    const readEmail = vi.spyOn(api, 'emailSettings').mockResolvedValue(configuredEmail)
    renderSettingsPage()

    expect(await screen.findByRole('heading', { name: '邮件提醒' })).toBeInTheDocument()
    expect(screen.getByText('无需额外付费接口')).toBeInTheDocument()
    await waitFor(() => expect(readEmail).toHaveBeenCalledOnce())

    fireEvent.change(await screen.findByLabelText(/邮箱服务商/), { target: { value: 'outlook' } })
    expect(screen.getByLabelText('SMTP 服务器')).toHaveValue('smtp.office365.com')
    expect(screen.getByLabelText('SMTP 端口')).toHaveValue(587)
    expect(screen.getByLabelText('连接安全')).toHaveValue('starttls')
    expect(screen.getByLabelText('连接安全')).toBeDisabled()

    fireEvent.change(screen.getByLabelText(/邮箱服务商/), { target: { value: 'custom' } })
    expect(screen.getByLabelText('SMTP 服务器')).not.toHaveAttribute('readonly')
    expect(screen.getByLabelText('连接安全')).toBeEnabled()
  })

  it('认证身份未变时把收件地址转为数组并沿用空白授权码', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue(configuredEmail)
    const update = vi.spyOn(api, 'updateEmailSettings').mockResolvedValue({
      ...configuredEmail,
      recipients: ['first@example.com', 'second@example.com']
    })
    renderSettingsPage()

    await screen.findByLabelText(/邮箱服务商/)
    fireEvent.change(screen.getByLabelText(/收件邮箱/), { target: { value: 'first@example.com\nsecond@example.com, first@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '保存邮件设置' }))

    await waitFor(() => expect(update).toHaveBeenCalledOnce())
    expect(update.mock.calls[0][0]).toEqual(expect.objectContaining({
      provider: 'qq',
      host: 'smtp.qq.com',
      port: 465,
      security: 'tls',
      recipients: ['first@example.com', 'second@example.com']
    }))
    expect(update.mock.calls[0][0]).not.toHaveProperty('password')
    expect(await screen.findByText('邮件设置已保存。')).toBeInTheDocument()
  })

  it('更换服务商或账号后提示重新填写授权码', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue(configuredEmail)
    const update = vi.spyOn(api, 'updateEmailSettings').mockResolvedValue({
      ...configuredEmail,
      provider: 'outlook',
      host: 'smtp.office365.com',
      port: 587,
      security: 'starttls',
      passwordConfigured: true
    })
    renderSettingsPage()

    fireEvent.change(await screen.findByLabelText(/邮箱服务商/), { target: { value: 'outlook' } })
    expect(screen.getByText('认证身份已变更，请重新填写')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存邮件设置' }))

    expect(await screen.findByText('发件服务商、服务器或账号已变更，请重新填写 SMTP 授权码或应用密码。')).toBeInTheDocument()
    expect(update).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText(/SMTP 授权码或应用密码/), { target: { value: 'new-outlook-password' } })
    fireEvent.click(screen.getByRole('button', { name: '保存邮件设置' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'outlook',
      username: 'sender@qq.com',
      password: 'new-outlook-password'
    })))
  })

  it('确认后清除邮件设置和凭据并恢复 QQ 默认状态', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue(configuredEmail)
    const cleared: EmailSettings = {
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
    const remove = vi.spyOn(api, 'deleteEmailSettings').mockResolvedValue(cleared)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderSettingsPage()

    fireEvent.click(await screen.findByRole('button', { name: '清除邮件设置' }))

    await waitFor(() => expect(remove).toHaveBeenCalledOnce())
    expect(screen.getByLabelText(/邮箱服务商/)).toHaveValue('qq')
    expect(screen.getByLabelText('SMTP 服务器')).toHaveValue('smtp.qq.com')
    expect(screen.getByLabelText('发件邮箱账号')).toHaveValue('')
    expect(screen.getByText('首次配置必填')).toBeInTheDocument()
    expect(screen.getByText('邮件设置和已保存的授权码已清除。')).toBeInTheDocument()
  })

  it('发送测试邮件时直接提交当前草稿而不要求先保存', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue({
      ...configuredEmail,
      enabled: false,
      username: '',
      fromAddress: '',
      recipients: [],
      passwordConfigured: false
    })
    const testEmail = vi.spyOn(api, 'testEmailSettings').mockResolvedValue()
    const update = vi.spyOn(api, 'updateEmailSettings')
    renderSettingsPage()

    await screen.findByRole('heading', { name: '邮件提醒' })
    fireEvent.change(await screen.findByLabelText(/邮箱服务商/), { target: { value: '163' } })
    fireEvent.change(screen.getByLabelText('发件邮箱账号'), { target: { value: 'sender@163.com' } })
    fireEvent.change(screen.getByLabelText(/SMTP 授权码或应用密码/), { target: { value: 'mail-app-password' } })
    fireEvent.change(screen.getByLabelText(/收件邮箱/), { target: { value: 'mobile@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送测试邮件' }))

    await waitFor(() => expect(testEmail).toHaveBeenCalledWith({
      enabled: false,
      provider: '163',
      host: 'smtp.163.com',
      port: 465,
      security: 'tls',
      username: 'sender@163.com',
      password: 'mail-app-password',
      fromName: '号池监控',
      fromAddress: 'sender@163.com',
      recipients: ['mobile@example.com']
    }))
    expect(update).not.toHaveBeenCalled()
    expect(await screen.findByText(/测试邮件已发送/)).toBeInTheDocument()
  })

  it('首次配置缺少授权码时在页面提示并阻止测试请求', async () => {
    vi.spyOn(api, 'emailSettings').mockResolvedValue({
      ...configuredEmail,
      passwordConfigured: false
    })
    const testEmail = vi.spyOn(api, 'testEmailSettings').mockResolvedValue()
    renderSettingsPage()

    await screen.findByLabelText(/邮箱服务商/)
    fireEvent.click(screen.getByRole('button', { name: '发送测试邮件' }))

    expect(await screen.findByText('请填写 SMTP 授权码或应用密码。')).toBeInTheDocument()
    expect(testEmail).not.toHaveBeenCalled()
  })
})
