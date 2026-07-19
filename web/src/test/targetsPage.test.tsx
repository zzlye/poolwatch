import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import TargetsPage from '../pages/TargetsPage'
import type { Target } from '../types'

const target: Target = {
  id: 'target-1',
  name: '测试渠道',
  kind: 'new_api',
  baseUrl: 'https://api.example.com',
  status: 'healthy',
  statusText: '运行正常',
  enabled: true,
  checkIntervalMinutes: 10,
  authConfigured: true,
  metrics: [{ key: 'wallet_balance', label: '钱包余额', value: '30', unit: '元', threshold: '20', status: 'healthy' }]
}

function renderTargetsPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const view = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter><TargetsPage /></MemoryRouter>
    </QueryClientProvider>
  )
  return { ...view, queryClient }
}

describe('渠道全部刷新', () => {
  afterEach(() => vi.restoreAllMocks())

  it('按钮位于添加渠道左侧，并在提交期间显示加载状态', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue([target])
    let finishRefresh!: () => void
    const checkAll = vi.spyOn(api, 'checkAll').mockReturnValue(new Promise<void>((resolve) => {
      finishRefresh = resolve
    }))

    renderTargetsPage()

    const refreshButton = await screen.findByRole('button', { name: '全部刷新' })
    const addLink = screen.getByRole('link', { name: '添加渠道' })
    expect(refreshButton.parentElement?.firstElementChild).toBe(refreshButton)
    expect(refreshButton.parentElement?.lastElementChild).toBe(addLink)

    fireEvent.click(refreshButton)

    await waitFor(() => expect(checkAll).toHaveBeenCalledOnce())
    expect(screen.getByRole('button', { name: '正在刷新' })).toBeDisabled()

    await act(async () => finishRefresh())
  })

  it('刷新成功后给出反馈，并刷新渠道列表和仪表盘查询', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue([target])
    vi.spyOn(api, 'checkAll').mockResolvedValue()
    const { queryClient } = renderTargetsPage()
    const invalidateQueries = vi.spyOn(queryClient, 'invalidateQueries').mockResolvedValue()

    fireEvent.click(await screen.findByRole('button', { name: '全部刷新' }))

    expect(await screen.findByRole('status')).toHaveTextContent('刷新任务已提交，渠道状态会自动更新。')
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['targets'] })
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
  })

  it('刷新失败后显示错误并允许再次操作', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue([target])
    vi.spyOn(api, 'checkAll').mockRejectedValue(new Error('检测服务暂时繁忙'))

    renderTargetsPage()
    fireEvent.click(await screen.findByRole('button', { name: '全部刷新' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('刷新失败：检测服务暂时繁忙')
    await waitFor(() => expect(screen.getByRole('button', { name: '全部刷新' })).toBeEnabled())
  })
})
