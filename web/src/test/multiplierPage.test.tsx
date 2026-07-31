import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { AppShell } from '../components/AppShell'
import AlertsPage from '../pages/AlertsPage'
import MultiplierPage from '../pages/MultiplierPage'
import type { Alert, Target, TargetMultiplierState } from '../types'

const targets: Target[] = [
  {
    id: 'new-1', name: 'New 主站', kind: 'new_api', baseUrl: 'https://new.example.com',
    status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5,
    authConfigured: true, metrics: []
  },
  {
    id: 'sub-1', name: 'Sub 备用站', kind: 'sub2api', baseUrl: 'https://sub.example.com',
    status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5,
    authConfigured: true, metrics: []
  },
  {
    id: 'chat-1', name: 'Chat 号池', kind: 'chatgpt2api', baseUrl: 'https://chat.example.com',
    status: 'healthy', statusText: '运行正常', enabled: true, checkIntervalMinutes: 5,
    authConfigured: true, metrics: []
  }
]

function multiplierState(targetId: 'new-1' | 'sub-1', detected = false): TargetMultiplierState {
  const target = targets.find((item) => item.id === targetId)!
  const base = {
    targetId,
    targetName: target.name,
    targetKind: target.kind as 'new_api' | 'sub2api',
    enabled: true
  }
  if (targetId === 'sub-1') {
    return {
      ...base,
      groups: [{ key: '10', name: '基础组', multiplier: '0.8', monitored: true, status: 'stable' }]
    }
  }
  return {
    ...base,
    groups: detected ? [
      { key: 'default', name: '默认分组', multiplier: '1', monitored: false, status: 'unknown' },
      { key: 'vip', name: '会员分组', multiplier: '0.333333', monitored: false, status: 'unknown' }
    ] : []
  }
}

function renderMultiplierPage(path = '/multipliers?target=new-1') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes><Route path="/multipliers" element={<MultiplierPage />} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

afterEach(() => vi.restoreAllMocks())

describe('分组倍率页面', () => {
  it('只列出支持渠道，并可自动检测后选择保存精确倍率', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockResolvedValue(multiplierState('new-1'))
    vi.spyOn(api, 'detectMultiplierGroups').mockResolvedValue(multiplierState('new-1', true))
    const save = vi.spyOn(api, 'saveMultiplierGroups').mockImplementation(async (id, keys) => ({
      ...multiplierState('new-1', true),
      targetId: id,
      groups: multiplierState('new-1', true).groups.map((group) => ({ ...group, monitored: keys.includes(group.key), status: keys.includes(group.key) ? 'stable' : 'unknown' }))
    }))

    renderMultiplierPage()

    const targetSelect = await screen.findByLabelText('已经添加的渠道')
    expect(targetSelect).toHaveTextContent('New 主站')
    expect(targetSelect).toHaveTextContent('Sub 备用站')
    expect(targetSelect).not.toHaveTextContent('Chat 号池')

    fireEvent.click(screen.getByRole('button', { name: '自动检测分组' }))
    expect(await screen.findByText('0.333333×')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('checkbox', { name: '监控 会员分组' }))
    fireEvent.click(screen.getByRole('button', { name: '保存监控' }))

    await waitFor(() => expect(save).toHaveBeenCalledWith('new-1', ['vip']))
    expect(await screen.findByText('已监控 1 个分组，首次倍率已作为基准。')).toBeInTheDocument()
  })

  it('切换渠道后只刷新当前选中的渠道', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockImplementation(async (id) => multiplierState(id as 'new-1' | 'sub-1', id === 'new-1'))
    const check = vi.spyOn(api, 'checkMultiplierGroups').mockImplementation(async (id) => multiplierState(id as 'new-1' | 'sub-1', true))

    renderMultiplierPage()
    fireEvent.change(await screen.findByLabelText('已经添加的渠道'), { target: { value: 'sub-1' } })
    expect(await screen.findByText('基础组')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '刷新当前渠道' }))

    await waitFor(() => expect(check).toHaveBeenCalledTimes(1))
    expect(check.mock.calls[0][0]).toBe('sub-1')
  })

  it('未保存选择时禁止刷新，恢复原选择后解除限制', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockResolvedValue(multiplierState('sub-1'))

    renderMultiplierPage('/multipliers?target=sub-1')
    const monitoredCheckbox = await screen.findByRole('checkbox', { name: '监控 基础组' })
    const detectButton = screen.getByRole('button', { name: '自动检测分组' })
    const refreshButton = screen.getByRole('button', { name: '刷新当前渠道' })

    fireEvent.click(monitoredCheckbox)
    expect(await screen.findByText('分组选择尚未保存，请先保存或恢复原来的选择再检测倍率。')).toBeInTheDocument()
    expect(detectButton).toBeDisabled()
    expect(refreshButton).toBeDisabled()

    fireEvent.click(monitoredCheckbox)
    expect(screen.queryByText('分组选择尚未保存，请先保存或恢复原来的选择再检测倍率。')).not.toBeInTheDocument()
    expect(detectButton).toBeEnabled()
    expect(refreshButton).toBeEnabled()
    expect(screen.getByRole('button', { name: '保存监控' })).toBeDisabled()
  })

  it('检测请求进行中禁止切换渠道，避免旧结果覆盖新表单', async () => {
    let resolveDetection: ((value: TargetMultiplierState) => void) | undefined
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    vi.spyOn(api, 'multiplierState').mockResolvedValue(multiplierState('new-1'))
    vi.spyOn(api, 'detectMultiplierGroups').mockImplementation(() => new Promise((resolve) => { resolveDetection = resolve }))

    renderMultiplierPage()
    const targetSelect = await screen.findByLabelText('已经添加的渠道')
    fireEvent.click(screen.getByRole('button', { name: '自动检测分组' }))
    await waitFor(() => expect(targetSelect).toBeDisabled())
    await waitFor(() => expect(screen.getByRole('button', { name: '自动检测分组' })).toBeDisabled())

    resolveDetection?.(multiplierState('new-1', true))
    expect(await screen.findByText('0.333333×')).toBeInTheDocument()
    expect(targetSelect).toBeEnabled()
  })

  it('地址栏渠道不存在时自动回到第一个可用渠道且不请求失效标识', async () => {
    vi.spyOn(api, 'targets').mockResolvedValue(targets)
    const readState = vi.spyOn(api, 'multiplierState').mockResolvedValue(multiplierState('new-1'))

    renderMultiplierPage('/multipliers?target=deleted-target')

    expect(await screen.findByLabelText('已经添加的渠道')).toHaveValue('new-1')
    await waitFor(() => expect(readState).toHaveBeenCalled())
    expect(readState.mock.calls.some(([id]) => id === 'deleted-target')).toBe(false)
    expect(readState.mock.calls[0][0]).toBe('new-1')
  })

  it('桌面侧栏和手机底部导航都提供倍率入口', () => {
    render(
      <MemoryRouter>
        <AppShell bootstrap={{ initialized: true, authenticated: true, productName: '号池监控', totpEnabled: false }} onLogout={vi.fn()} />
      </MemoryRouter>
    )
    expect(screen.getAllByRole('link', { name: '倍率' })).toHaveLength(2)
  })
})

describe('倍率变化告警', () => {
  it('显示为已记录，并可返回对应渠道倍率页', async () => {
    const alert: Alert = {
      id: 'alert-rate', targetId: 'new-1', targetName: 'New 主站', type: 'multiplier_changed',
      title: '分组倍率已变更', message: '会员分组：0.5× → 0.333333×。', severity: 'warning',
      status: 'resolved', createdAt: '2026-07-31T10:00:00Z'
    }
    vi.spyOn(api, 'alerts').mockResolvedValue([alert])
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter><AlertsPage /></MemoryRouter>
      </QueryClientProvider>
    )

    expect((await screen.findAllByText('倍率变更')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('已记录').length).toBeGreaterThan(0)
    expect(screen.getByRole('link', { name: 'New 主站' })).toHaveAttribute('href', '/multipliers?target=new-1')
  })
})
