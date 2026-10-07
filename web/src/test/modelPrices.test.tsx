import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import ModelPricesPage, { priceDirection } from '../pages/ModelPricesPage'
import { AppShell } from '../components/AppShell'
import { useRealtime } from '../hooks/useRealtime'
import type { ModelPriceCatalog, ModelPriceMonitor, Target } from '../types'

const target: Target = { id: 'price-1', name: '价格站', kind: 'new_api', baseUrl: 'https://example.com', enabled: true, checkIntervalMinutes: 5, authConfigured: true, metrics: [], status: 'healthy', statusText: '正常' }
const catalog: ModelPriceCatalog = { groupKey: 'default', groupName: '默认', multiplier: '1', models: [{ name: 'model-a', prices: [{ key: 'input', label: '输入', range: '通用价格', value: '1.25', unit: 'USD/百万令牌' }, { key: 'output', label: '输出', range: '通用价格', value: '3', unit: 'USD/百万令牌' }] }, { name: 'dynamic', prices: [] }] }
const saved: ModelPriceMonitor = { targetId: target.id, groupKey: 'default', groupName: '默认', modelName: 'model-a', prices: catalog.models[0].prices, previous: [], missing: false, lastError: '', lastCheckedAt: '2026-10-07T01:00:00Z', changedAt: '' }
function renderPage() { const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); render(<QueryClientProvider client={client}><MemoryRouter><ModelPricesPage /></MemoryRouter></QueryClientProvider>); return client }
beforeEach(() => {
  vi.spyOn(api, 'targets').mockResolvedValue([target])
  vi.spyOn(api, 'modelPrices').mockResolvedValue([])
  vi.spyOn(api, 'discoverPriceGroups').mockResolvedValue([{ key: 'default', name: '默认' }])
  vi.spyOn(api, 'modelPriceCatalog').mockResolvedValue(catalog)
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('独立模型价格监控', () => {
  it('无需倍率配置发现分组并选择模型，保存不调用倍率接口', async () => {
    const multiplier = vi.spyOn(api, 'saveMultiplierGroups')
    const save = vi.spyOn(api, 'saveModelPrices').mockResolvedValue([saved])
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '自动检测分组' }))
    const checkbox = await screen.findByRole('checkbox', { name: '监控 model-a' })
    expect(screen.getByRole('checkbox', { name: '监控 dynamic' })).toBeDisabled()
    const card = checkbox.closest('article')!
    expect(card).toHaveTextContent('输入'); expect(card).toHaveTextContent('输出')
    expect(screen.getAllByText('USD/百万令牌')).toHaveLength(2)
    fireEvent.click(checkbox)
    expect(screen.getByRole('combobox', { name: '选择已有渠道' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: '保存价格监控' }))
    await waitFor(() => expect(save).toHaveBeenCalledWith('price-1', 'default', ['model-a']))
    expect(await screen.findByText('监控中')).toBeInTheDocument()
    expect(multiplier).not.toHaveBeenCalled()
  })
  it('取消独立价格配置不修改倍率选择', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    const save = vi.spyOn(api, 'saveModelPrices').mockResolvedValue([])
    const multiplier = vi.spyOn(api, 'saveMultiplierGroups')
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '取消当前分组全部选择' }))
    fireEvent.click(screen.getByRole('button', { name: '保存价格监控' }))
    await waitFor(() => expect(save).toHaveBeenCalledWith('price-1', 'default', []))
    expect(multiplier).not.toHaveBeenCalled()
  })
  it('读取失败仍展示最近有效价格且可重试', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([{ ...saved, lastError: '读取失败，保留旧价格' }])
    vi.spyOn(api, 'modelPriceCatalog').mockRejectedValue(new Error('上游暂不可用'))
    renderPage()
    expect(await screen.findByText('上游暂不可用')).toBeInTheDocument()
    expect(screen.getByText('1.25')).toBeInTheDocument()
    expect(screen.getByText('检测异常')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })
  it('模型支持搜索分页，切换筛选保留跨页选择', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    vi.spyOn(api, 'modelPriceCatalog').mockResolvedValue({ ...catalog, models: Array.from({ length: 25 }, (_, i) => ({ name: `model-${i}`, prices: catalog.models[0].prices })) })
    renderPage()
    const first = await screen.findByRole('checkbox', { name: '监控 model-0' })
    fireEvent.click(first)
    fireEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByRole('checkbox', { name: '监控 model-24' })).toBeInTheDocument()
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索价格模型' }), { target: { value: 'model-0' } })
    expect(screen.getByRole('checkbox', { name: '监控 model-0' })).toBeChecked()
  })
  it('价格涨跌不使用浮点且不同币种不误报涨跌', () => {
    const p = catalog.models[0].prices[0]
    expect(priceDirection({ ...p, value: '9007199254740992.0001' }, { ...p, value: '9007199254740992.0002' })).toBe('↑ 上涨')
    expect(priceDirection(p, { ...p, value: '1.2500' })).toBe('')
    expect(priceDirection(p, { ...p, value: '1.00' })).toBe('↓ 下调')
    expect(priceDirection(p, { ...p, unit: '元/次' })).toBe('单位变更')
  })
  it('桌面及手机导航均提供模型价格入口', () => {
    render(<MemoryRouter><AppShell bootstrap={{ initialized: true, authenticated: true, productName: '号池监控', totpEnabled: false }} onLogout={() => {}} /></MemoryRouter>)
    const links = screen.getAllByRole('link', { name: '模型价格' })
    expect(links).toHaveLength(2); expect(links[0]).toHaveAttribute('href', '/prices')
  })
  it('独立价格 SSE 仅刷新价格与告警缓存', () => {
    const listeners = new Map<string, () => void>()
    vi.stubGlobal('EventSource', class { addEventListener(name: string, fn: () => void) { listeners.set(name, fn) } close() {} })
    const client = new QueryClient()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    function Events() { useRealtime(true); return null }
    render(<QueryClientProvider client={client}><Events /></QueryClientProvider>)
    listeners.get('price.updated')?.()
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['model-prices'] })
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ['group-multipliers'] })
  })
})
