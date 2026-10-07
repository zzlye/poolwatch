import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
function renderPage(path = '/prices?target=price-1') { const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><ModelPricesPage /></MemoryRouter></QueryClientProvider>); return client }
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
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
    fireEvent.click(await screen.findByRole('button', { name: '自动检测分组' }))
    const checkbox = await screen.findByRole('checkbox', { name: '监控 model-a' })
    expect(screen.getByRole('checkbox', { name: '监控 dynamic' })).toBeDisabled()
    const card = checkbox.closest('tr')!
    expect(card).toHaveTextContent('输入'); expect(card).toHaveTextContent('输出')
    expect(screen.getAllByText('USD/百万令牌')).toHaveLength(2)
    fireEvent.click(checkbox)
    expect(screen.getByRole('button', { name: '返回监控列表' })).toBeDisabled()
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
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
    fireEvent.click(await screen.findByRole('button', { name: '取消当前分组全部选择' }))
    fireEvent.click(screen.getByRole('button', { name: '保存价格监控' }))
    await waitFor(() => expect(save).toHaveBeenCalledWith('price-1', 'default', []))
    expect(multiplier).not.toHaveBeenCalled()
  })
  it('读取失败仍展示最近有效价格且可重试', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([{ ...saved, lastError: '读取失败，保留旧价格' }])
    vi.spyOn(api, 'modelPriceCatalog').mockRejectedValue(new Error('上游暂不可用'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
    expect(await screen.findByText('上游暂不可用')).toBeInTheDocument()
    expect(screen.getByText('1.25')).toBeInTheDocument()
    expect(screen.getAllByText('检测异常').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })
  it('模型支持搜索分页，切换筛选保留跨页选择', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    vi.spyOn(api, 'modelPriceCatalog').mockResolvedValue({ ...catalog, models: Array.from({ length: 25 }, (_, i) => ({ name: `model-${i}`, prices: catalog.models[0].prices })) })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
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


describe('渠道折叠价格总览', () => {
  it('按价格状态筛选渠道，并随缓存更新同步筛选结果', async () => {
    const names = ['稳定站', '变化站', '异常站', '待检测站', '未配置站', '读取失败站']
    vi.spyOn(api, 'targets').mockResolvedValue(names.map((name, i) => ({ ...target, id: `status-${i}`, name, kind: i % 2 ? 'sub2api' : 'new_api' })))
    vi.spyOn(api, 'modelPrices').mockImplementation(async (id) => {
      if (id === 'status-5') throw new Error('服务暂不可用')
      if (id === 'status-4') return []
      return [{ ...saved, targetId: id, changedAt: id === 'status-1' ? saved.lastCheckedAt : '', lastError: id === 'status-2' ? '上游读取失败' : '', prices: id === 'status-3' ? [] : saved.prices }]
    })
    const client = renderPage('/prices')
    const filter = await screen.findByRole('combobox', { name: '筛选价格状态' })
    await screen.findByText('读取失败')
    for (const [status, expected] of [['stable', ['稳定站']], ['changed', ['变化站']], ['error', ['异常站', '读取失败站']], ['loading', ['待检测站']], ['unconfigured', ['未配置站']]] as const) {
      fireEvent.change(filter, { target: { value: status } })
      for (const name of names) {
        const card = screen.getByText(name).closest('details')!
        if ((expected as readonly string[]).includes(name)) expect(card).not.toHaveAttribute('hidden')
        else expect(card).toHaveAttribute('hidden')
      }
    }
    fireEvent.change(filter, { target: { value: 'changed' } })
    // SSE 和手动刷新均更新同一个缓存，状态筛选应立即跟随变化。
    act(() => client.setQueryData(['model-prices', 'status-1'], [{ ...saved, targetId: 'status-1' }]))
    expect(await screen.findByText('没有匹配状态的渠道')).toBeInTheDocument()
    fireEvent.change(filter, { target: { value: 'all' } })
    expect(screen.queryByText('没有匹配状态的渠道')).not.toBeInTheDocument()
    expect(api.modelPriceCatalog).not.toHaveBeenCalled()
    expect(api.discoverPriceGroups).not.toHaveBeenCalled()
  })
  it('状态筛选隐藏渠道后恢复时保留展开状态和未保存选择', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    const save = vi.spyOn(api, 'saveModelPrices')
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: '监控 model-a' }))
    const filter = screen.getByRole('combobox', { name: '筛选价格状态' })
    fireEvent.change(filter, { target: { value: 'error' } })
    expect(screen.queryByRole('checkbox', { name: '监控 model-a' })).not.toBeInTheDocument()
    fireEvent.change(filter, { target: { value: 'all' } })
    expect(screen.getByRole('checkbox', { name: '监控 model-a' })).not.toBeChecked()
    expect(document.getElementById('price-channel-price-1')).toHaveAttribute('open')
    expect(save).not.toHaveBeenCalled()
  })
  it('默认并列显示全部渠道摘要且不读取上游价格目录', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue([target, { ...target, id: 'sub-2', name: '第二站', kind: 'sub2api' }])
    vi.spyOn(api, 'modelPrices').mockImplementation(async (id) => id === target.id ? [saved] : [])
    renderPage('/prices')
    expect(await screen.findByText('价格站')).toBeInTheDocument()
    expect(await screen.findByText('第二站')).toBeInTheDocument()
    await screen.findByText('1 组 · 1 模型')
    expect(document.querySelectorAll('details')).toHaveLength(2)
    expect(document.querySelectorAll('details[open]')).toHaveLength(0)
    expect(screen.queryByRole('combobox', { name: '选择已有渠道' })).not.toBeInTheDocument()
    expect(api.modelPriceCatalog).not.toHaveBeenCalled()
    expect(api.discoverPriceGroups).not.toHaveBeenCalled()
  })
  it('深链接展开指定渠道，默认仅显示已监控模型的紧凑行', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    renderPage()
    expect(await screen.findByRole('table', { name: '价格站模型价格列表' })).toBeInTheDocument()
    expect(screen.getByText('model-a').closest('tr')).toHaveTextContent('输入1.25USD/百万令牌')
    expect(screen.getByText('model-a').closest('tr')).toHaveTextContent('输出3USD/百万令牌')
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    expect(screen.queryByText('dynamic')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '保存价格监控' })).not.toBeInTheDocument()
    expect(api.modelPriceCatalog).not.toHaveBeenCalled()
  })
  it('折叠再展开保留草稿而不提交，另一渠道仍可展开', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue([target, { ...target, id: 'sub-2', name: '第二站', kind: 'sub2api' }])
    vi.spyOn(api, 'modelPrices').mockImplementation(async (id) => id === target.id ? [saved] : [])
    const save = vi.spyOn(api, 'saveModelPrices')
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '管理模型' }))
    const checkbox = await screen.findByRole('checkbox', { name: '监控 model-a' })
    fireEvent.click(checkbox)
    const details = document.getElementById('price-channel-price-1') as HTMLDetailsElement
    fireEvent.click(details.querySelector('summary')!)
    await waitFor(() => expect(details.open).toBe(false))
    expect(screen.queryByRole('checkbox', { name: '监控 model-a' })).not.toBeInTheDocument()
    const second = document.getElementById('price-channel-sub-2') as HTMLDetailsElement
    fireEvent.click(second.querySelector('summary')!)
    await waitFor(() => expect(second.open).toBe(true))
    fireEvent.click(details.querySelector('summary')!)
    expect(await screen.findByRole('checkbox', { name: '监控 model-a' })).not.toBeChecked()
    expect(save).not.toHaveBeenCalled()
  })
  it('分组使用按钮切换且更改选择时禁止切换，保存后返回已监控列表', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved, { ...saved, groupKey: 'vip', groupName: '会员组', modelName: 'model-b' }])
    vi.spyOn(api, 'saveModelPrices').mockResolvedValue([{ ...saved, groupKey: 'vip', groupName: '会员组', modelName: 'model-b' }])
    renderPage()
    expect(await screen.findByRole('button', { name: '默认 1 个模型', pressed: true })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '会员组 1 个模型' }))
    expect(await screen.findByText('model-b')).toBeInTheDocument()
    expect(screen.queryByText('model-a')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '默认 1 个模型' }))
    fireEvent.click(screen.getByRole('button', { name: '管理模型' }))
    fireEvent.click(await screen.findByRole('checkbox', { name: '监控 model-a' }))
    expect(screen.getByRole('button', { name: '会员组 1 个模型' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: '保存价格监控' }))
    await waitFor(() => expect(screen.queryByRole('checkbox')).not.toBeInTheDocument())
    expect(await screen.findByText('model-b')).toBeInTheDocument()
  })
  it('刷新仅当前渠道且摘要同步显示变化', async () => {
    vi.spyOn(api, 'modelPrices').mockResolvedValue([saved])
    const refresh = vi.spyOn(api, 'checkModelPrices').mockResolvedValue([{ ...saved, previous: saved.prices, prices: [{ ...saved.prices[0], value: '2.5' }], changedAt: saved.lastCheckedAt }])
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: '刷新价格' }))
    expect(await screen.findByText('有变化')).toBeInTheDocument()
    expect(screen.getByText(/↑ 上涨/)).toBeInTheDocument()
    expect(refresh).toHaveBeenCalledWith('price-1')
    expect(api.modelPriceCatalog).not.toHaveBeenCalled()
  })
})
