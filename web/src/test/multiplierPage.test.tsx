import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { GroupMultiplierEditor } from '../components/GroupMultiplierEditor'
import MultiplierPage from '../pages/MultiplierPage'
import TargetDetailPage from '../pages/TargetDetailPage'
import type { GroupPriceResult, Target, TargetMultiplierState } from '../types'

const targets: Target[] = [
  { id: 'new-1', name: 'New 主站', kind: 'new_api', baseUrl: 'https://new.example.com', status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5, authConfigured: true, metrics: [] },
  { id: 'sub-1', name: 'Sub 备用站', kind: 'sub2api', baseUrl: 'https://sub.example.com', status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5, authConfigured: true, metrics: [] },
  { id: 'chat-1', name: 'Chat 号池', kind: 'chatgpt2api', baseUrl: 'https://chat.example.com', status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5, authConfigured: true, metrics: [] }
]

function state(targetId: string, detected = false): TargetMultiplierState {
  const target = targets.find((item) => item.id === targetId)!
  return {
    targetId,
    targetName: target.name,
    targetKind: target.kind as 'new_api' | 'sub2api',
    enabled: target.enabled,
    lastCheckedAt: '2026-07-31T10:00:00Z',
    groups: targetId === 'sub-1' ? [] : [
      { key: 'default', name: '默认分组', multiplier: '1', monitored: true, status: 'stable' },
      ...(detected ? [{ key: 'vip', name: '会员分组', multiplier: '0.333333', monitored: false, status: 'unknown' as const }] : []),
      { key: 'hidden', name: '未选分组', multiplier: '2', monitored: false, status: 'unknown' }
    ]
  }
}

function prices(groupKey = 'default'): GroupPriceResult {
  return {
    targetId: 'new-1', groupKey, groupName: groupKey === 'default' ? '默认分组' : '会员分组', multiplier: groupKey === 'default' ? '1' : '0.333333',
    models: [
      { name: 'gpt-4.1', billingMode: 'token', prices: [{ key: 'input', label: '输入', value: '2', unit: '元/百万 Token' }] },
      { name: 'gpt-4.1-long', billingMode: 'tiered', prices: [], intervals: [{ label: '长上下文', minTokens: '200001', prices: [{ key: 'output', label: '输出', value: '15', unit: '元/百万 Token' }] }] },
      { name: 'image-tier', billingMode: 'image', prices: [], intervals: [{ label: '2K 高清', prices: [{ key: 'per_request', label: '按图片', value: '0.08', unit: 'USD/张' }] }] },
      { name: 'gpt-dynamic', billingMode: 'tiered', prices: [], intervals: [{ label: '高输入档', condition: '输入 Token > 10000', prices: [{ key: 'input', label: '输入', value: '3', unit: 'USD/百万令牌' }] }] }
    ]
  }
}

function renderPage(path = '/multipliers?target=new-1') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes><Route path="/multipliers" element={<MultiplierPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  return { ...view, client }
}

afterEach(() => vi.restoreAllMocks())

describe('倍率折叠总览', () => {
  it('按查询参数展开渠道，只显示已选分组并懒加载全部价格区间', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => state(id))
    const readPrices = vi.spyOn(api, 'groupPrices').mockImplementation(async (_id, groupKey) => prices(groupKey))

    renderPage()

    const details = (await screen.findByText('New 主站')).closest('details')
    expect(details).toHaveAttribute('open')
    expect(await screen.findByRole('button', { name: /默认分组/, pressed: true })).toBeInTheDocument()
    expect(screen.queryByText('未选分组')).not.toBeInTheDocument()
    await waitFor(() => expect(readPrices).toHaveBeenCalledWith('new-1', 'default'))
    expect(await screen.findByText('gpt-4.1-long')).toBeInTheDocument()
    expect(screen.getAllByText('阶梯计费')).toHaveLength(2)
    expect(screen.getByText('长上下文（≥ 200001 tokens）')).toBeInTheDocument()
    expect(screen.getByText('2K 高清')).toBeInTheDocument()
    expect(screen.queryByText(/2K 高清（.*tokens/)).not.toBeInTheDocument()
    expect(screen.getByText('高输入档（输入 Token > 10000）')).toBeInTheDocument()
    expect(screen.getByText('15')).toBeInTheDocument()
  })

  it('价格模型很多时先搜索再分页，手机不会一次渲染全部模型', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => state(id))
    vi.spyOn(api, 'groupPrices').mockResolvedValue({
      ...prices(),
      models: Array.from({ length: 25 }, (_, index) => ({
        name: `model-${index + 1}`,
        billingMode: 'token',
        prices: [{ key: 'input', label: '输入', value: String(index + 1), unit: 'USD/百万令牌' }]
      }))
    })

    renderPage()

    expect(await screen.findByText('model-20')).toBeInTheDocument()
    expect(screen.queryByText('model-21')).not.toBeInTheDocument()
    expect(screen.getByText('显示第 1–20 个模型，共 25 个')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '下一页模型' }))
    expect(await screen.findByText('model-21')).toBeInTheDocument()
    expect(screen.queryByText('model-1')).not.toBeInTheDocument()

    fireEvent.change(screen.getByRole('searchbox', { name: '搜索模型或计费方式' }), { target: { value: 'model-24' } })
    expect(await screen.findByText('显示第 1–1 个模型，共 1 个')).toBeInTheDocument()
    expect(screen.getByText('model-24')).toBeInTheDocument()
  })

  it('渠道汇总区分待检测和检测失败，错误分组使用错误状态样式', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => {
      if (id === 'new-1') return { ...state(id), groups: [{ key: 'default', name: '默认分组', multiplier: '', monitored: true, status: 'unknown' }] }
      if (id === 'sub-1') return { ...state(id), groups: [{ key: 'vip', name: '故障分组', multiplier: '1', monitored: true, status: 'unknown', lastError: '读取失败' }] }
      return state(id)
    })
    vi.spyOn(api, 'groupPrices').mockResolvedValue(prices())

    renderPage()

    const newDetails = (await screen.findByText('New 主站')).closest('details')!
    const subDetails = screen.getByText('Sub 备用站').closest('details')!
    await waitFor(() => expect(newDetails.querySelector('.multiplier-channel-health')).toHaveTextContent('待检测'))
    expect(newDetails.querySelector('.multiplier-channel-health')).toHaveClass('is-loading')
    await waitFor(() => expect(subDetails.querySelector('.multiplier-channel-health')).toHaveTextContent('检测失败'))
    expect(subDetails.querySelector('.multiplier-channel-health')).toHaveClass('is-error')
    expect(subDetails.querySelector('.multiplier-group-option-value')).toHaveClass('state-error')
  })

  it('零配置渠道显示详情入口，单个渠道读取失败不影响其他渠道', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => {
      if (id === 'sub-1') throw new Error('Sub 倍率读取失败')
      return state(id)
    })
    vi.spyOn(api, 'groupPrices').mockResolvedValue(prices())

    renderPage('/multipliers?target=sub-1')

    expect(await screen.findByText('Sub 倍率读取失败')).toBeInTheDocument()
    expect(screen.getByText('New 主站')).toBeInTheDocument()
  })

  it('没有已选分组时只提供渠道详情配置入口', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => state(id))
    vi.spyOn(api, 'groupPrices').mockResolvedValue(prices())

    renderPage('/multipliers?target=sub-1')

    expect(await screen.findByText('尚未配置倍率监控')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '去配置分组' })).toHaveAttribute('href', '/targets/sub-1#multiplier-settings')
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })

  it('刷新动作只检查当前展开渠道并让该渠道价格缓存失效', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => state(id))
    vi.spyOn(api, 'groupPrices').mockResolvedValue(prices())
    const check = vi.spyOn(api, 'checkMultiplierGroups').mockResolvedValue(state('new-1'))
    const { client } = renderPage()
    const invalidate = vi.spyOn(client, 'invalidateQueries')

    fireEvent.click(await screen.findByRole('button', { name: '刷新倍率' }))

    await waitFor(() => expect(check).toHaveBeenCalledWith('new-1'))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['group-prices', 'new-1'] })
  })
})

describe('渠道内倍率配置', () => {
  it('检测候选留在编辑器本地，规范缓存只保留已监控分组', async () => {
    vi.spyOn(api, 'multiplierState').mockResolvedValue(state('new-1'))
    vi.spyOn(api, 'detectMultiplierGroups').mockResolvedValue(state('new-1', true))
    const save = vi.spyOn(api, 'saveMultiplierGroups').mockImplementation(async (_id, keys) => ({
      ...state('new-1', true),
      groups: state('new-1', true).groups.map((group) => ({ ...group, monitored: keys.includes(group.key), status: keys.includes(group.key) ? 'stable' as const : 'unknown' as const }))
    }))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    render(<QueryClientProvider client={client}><MemoryRouter><GroupMultiplierEditor target={targets[0]} /></MemoryRouter></QueryClientProvider>)

    fireEvent.click(await screen.findByRole('button', { name: '自动检测分组' }))
    expect(await screen.findByText('0.333333×')).toBeInTheDocument()
    await waitFor(() => {
      const cached = client.getQueryData<TargetMultiplierState>(['group-multipliers', 'new-1'])
      expect(cached?.groups.map((group) => group.key)).toEqual(['default'])
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['group-prices', 'new-1'] })
    fireEvent.click(screen.getByRole('checkbox', { name: '监控 会员分组' }))
    fireEvent.click(screen.getByRole('button', { name: '保存监控' }))
    await waitFor(() => expect(save).toHaveBeenCalledWith('new-1', ['default', 'vip']))
  })

  it('切换渠道标识时会卸载旧编辑器并清除候选分组', async () => {
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => state(id))
    vi.spyOn(api, 'detectMultiplierGroups').mockResolvedValue(state('new-1', true))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    const tree = (target: Target) => <QueryClientProvider client={client}><MemoryRouter><GroupMultiplierEditor key={target.id} target={target} /></MemoryRouter></QueryClientProvider>
    const view = render(tree(targets[0]))

    fireEvent.click(await screen.findByRole('button', { name: '自动检测分组' }))
    expect(await screen.findByText('会员分组')).toBeInTheDocument()
    view.rerender(tree(targets[1]))

    expect(await screen.findByText('尚未检测分组')).toBeInTheDocument()
    expect(screen.queryByText('会员分组')).not.toBeInTheDocument()
  })

  it('渠道详情异步出现倍率区块后会重新执行锚点滚动', async () => {
    const scrollIntoView = vi.fn()
    Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView })
    vi.spyOn(api, 'target').mockResolvedValue(targets[0])
    vi.spyOn(api, 'multiplierState').mockResolvedValue(state('new-1'))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })

    render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/targets/new-1#multiplier-settings']}><Routes><Route path="/targets/:id" element={<TargetDetailPage />} /></Routes></MemoryRouter></QueryClientProvider>)

    expect(await screen.findByRole('heading', { name: '分组倍率监控' })).toBeInTheDocument()
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' }))
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
  })

  it('渠道普通检测完成后同时刷新倍率状态和模型价格', async () => {
    vi.spyOn(api, 'target').mockResolvedValue(targets[0])
    vi.spyOn(api, 'multiplierState').mockResolvedValue(state('new-1'))
    vi.spyOn(api, 'checkTarget').mockResolvedValue()
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/targets/new-1']}><Routes><Route path="/targets/:id" element={<TargetDetailPage />} /></Routes></MemoryRouter></QueryClientProvider>)

    fireEvent.click(await screen.findByRole('button', { name: '立即检测' }))

    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ['group-multipliers', 'new-1'] }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['group-prices', 'new-1'] })
  })
})
