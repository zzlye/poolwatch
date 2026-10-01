import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { api } from '../api/client'
import TargetDetailPage from '../pages/TargetDetailPage'
import { TargetCards, TargetTable } from '../components/TargetViews'
import type { Target } from '../types'

// 兼容旧接口的零占位值，同时确认真正检测到的零额度仍能正常展示。
const failed: Target = {
  id: 'chat-large', name: '测试号池', kind: 'chatgpt2api', baseUrl: 'https://pool.example',
  enabled: true, status: 'error', statusText: '检测失败', authConfigured: true,
  checkIntervalMinutes: 10, lastError: '渠道响应超过 1 MB 限制',
  metrics: [{ key: 'image_quota', label: '图片额度', value: '0', unit: '次', status: 'unknown' }]
}

function mountDetail(target: Target) {
  vi.spyOn(api, 'target').mockResolvedValue(target)
  vi.spyOn(api, 'history').mockResolvedValue({ target, snapshots: [] })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/targets/chat-large']}><Routes>
    <Route path="/targets/:id" element={<TargetDetailPage />} />
  </Routes></MemoryRouter></QueryClientProvider>)
}

afterEach(() => vi.restoreAllMocks())

describe('指标缺失与明细读取提示', () => {
  it('检测失败的详情页不把未知额度显示成零', async () => {
    mountDetail(failed)
    await screen.findByRole('heading', { name: '测试号池' })
    expect(screen.queryByText('0 次')).not.toBeInTheDocument()
    expect(screen.getAllByText('读取失败').length).toBeGreaterThan(0)
  })

  it('桌面和手机渠道列表都隐藏未知的零占位值', () => {
    render(<MemoryRouter><TargetTable targets={[failed]} /><TargetCards targets={[failed]} /></MemoryRouter>)
    expect(screen.queryByText('0 次')).not.toBeInTheDocument()
    expect(screen.getAllByText('读取失败')).toHaveLength(2)
  })

  it('实际检测到的零额度仍然显示零', async () => {
    mountDetail({ ...failed, status: 'healthy', lastError: '', metrics: [{ ...failed.metrics[0], status: 'healthy' }] })
    expect(await screen.findByText('0 次')).toBeInTheDocument()
  })

  it('显示明细读取警告但保留有效图片额度', async () => {
    mountDetail({ ...failed, status: 'warning', lastError: '', accountsWarning: '账号明细读取失败，汇总额度已更新。',
      metrics: [{ ...failed.metrics[0], status: 'healthy', value: '123' }] } as Target)
    expect(await screen.findByText('123 次')).toBeInTheDocument()
    expect(screen.getByText('账号明细读取失败，汇总额度已更新。')).toBeInTheDocument()
  })
})
