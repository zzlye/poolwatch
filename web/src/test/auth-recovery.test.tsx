import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { api } from '../api/client'
import TargetWizardPage from '../pages/TargetWizardPage'
import TargetDetailPage from '../pages/TargetDetailPage'
import type { Target } from '../types'

const target: Target = {
  id: 'target_turnstile', name: '验证站点', kind: 'new_api', baseUrl: 'https://channel.example.com',
  topupUrl: 'https://channel.example.com/console/topup', enabled: true, checkIntervalMinutes: 12,
  status: 'error', statusText: '检测失败', authConfigured: true, credentialMode: 'password',
  lastError: '站点启用了浏览器验证，请改用网页登录或访问令牌', metrics: []
}

function mount(entry: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[entry]}><Routes>
    <Route path="/targets/:id" element={<TargetDetailPage />} />
    <Route path="/targets/:id/edit" element={<TargetWizardPage />} />
  </Routes></MemoryRouter></QueryClientProvider>)
}

afterEach(() => vi.restoreAllMocks())

describe('浏览器验证恢复登录', () => {
  it('详情页提供恢复入口并直接进入当前渠道的网页授权步骤', async () => {
    vi.spyOn(api, 'target').mockResolvedValue(target)
    vi.spyOn(api, 'multiplierState').mockResolvedValue({ targetId: target.id, groups: [] } as never)
    const update = vi.spyOn(api, 'updateTarget')
    mount('/targets/target_turnstile')
    const link = await screen.findByRole('link', { name: '网页登录恢复检测' })
    expect(link).toHaveAttribute('href', '/targets/target_turnstile/edit?auth=browser')
    fireEvent.click(link)
    expect(await screen.findByRole('heading', { name: '登录与认证' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /网页授权/ })).toBeChecked()
    expect(screen.getByRole('link', { name: '打开渠道登录页' })).toHaveAttribute('href', 'https://channel.example.com/login')
    expect(update).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请先完成当前站点的网页授权')
  })

  it('普通编辑保持原登录方式，点击恢复按钮后才切换', async () => {
    vi.spyOn(api, 'target').mockResolvedValue(target)
    mount('/targets/target_turnstile/edit')
    expect(await screen.findByLabelText(/渠道名称/)).toHaveValue('验证站点')
    fireEvent.click(screen.getByRole('button', { name: '网页登录恢复检测' }))
    expect(screen.getByRole('radio', { name: /网页授权/ })).toBeChecked()
    expect(screen.getByRole('heading', { name: '登录与认证' })).toBeInTheDocument()
  })

  it('更改已通过测试的凭据后必须重新验证', async () => {
    vi.spyOn(api, 'target').mockResolvedValue(target)
    vi.spyOn(api, 'testTarget').mockResolvedValue({ ok: true, message: '连接成功' })
    const update = vi.spyOn(api, 'updateTarget')
    mount('/targets/target_turnstile/edit?auth=browser')
    await screen.findByRole('heading', { name: '登录与认证' })
    fireEvent.click(screen.getByText('手工填写 Cookie 和用户 ID'))
    fireEvent.change(screen.getByLabelText(/登录 Cookie/), { target: { value: 'session=verified' } })
    fireEvent.change(screen.getByLabelText('用户 ID'), { target: { value: '42' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '测试连接' }))
    await screen.findByText('连接成功')
    fireEvent.click(screen.getByRole('button', { name: '上一步' }))
    fireEvent.click(screen.getByRole('button', { name: '上一步' }))
    fireEvent.click(screen.getByText('手工填写 Cookie 和用户 ID'))
    fireEvent.change(screen.getByLabelText(/登录 Cookie/), { target: { value: 'session=changed' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请先测试连接成功后再保存')
    expect(update).not.toHaveBeenCalled()
  })

  it('非 New API 渠道不会因恢复参数改变原配置', async () => {
    vi.spyOn(api, 'target').mockResolvedValue({ ...target, kind: 'sub2api' })
    mount('/targets/target_turnstile/edit?auth=browser')
    expect(await screen.findByLabelText(/渠道名称/)).toHaveValue('验证站点')
    expect(screen.queryByRole('button', { name: '网页登录恢复检测' })).not.toBeInTheDocument()
  })

  it('导入验证成功的会话后才保存并保留原渠道配置', async () => {
    vi.spyOn(api, 'target').mockResolvedValue(target)
    vi.spyOn(api, 'testTarget').mockResolvedValue({ ok: true, message: '连接成功' })
    const update = vi.spyOn(api, 'updateTarget').mockResolvedValue({ ...target, credentialMode: 'browser_session', lastError: undefined, status: 'healthy' })
    mount('/targets/target_turnstile/edit?auth=browser')
    await screen.findByRole('heading', { name: '登录与认证' })
    fireEvent.click(screen.getByText('手工填写 Cookie 和用户 ID'))
    fireEvent.change(screen.getByLabelText(/登录 Cookie/), { target: { value: 'session=test-authorized-session' } })
    fireEvent.change(screen.getByLabelText('用户 ID'), { target: { value: '42' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请先测试连接成功后再保存')
    expect(update).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '测试连接' }))
    await screen.findByText('连接成功')
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(target.id, expect.objectContaining({
      name: target.name, baseUrl: target.baseUrl, topupUrl: target.topupUrl, checkIntervalMinutes: 12,
      credentialMode: 'browser_session', cookie: 'session=test-authorized-session', userId: '42', password: ''
    })))
  })
})
