import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AuthPage from '../pages/AuthPage'
import SettingsPage from '../pages/SettingsPage'
import TargetWizardPage, { parseSub2APIOAuthCallback, targetToDraft } from '../pages/TargetWizardPage'
import TargetDetailPage from '../pages/TargetDetailPage'
import { AppShell } from '../components/AppShell'
import { LineChart } from '../components/LineChart'
import { StatusPill } from '../components/StatusPill'
import { ThemeProvider, useTheme } from '../hooks/useTheme'
import { useRealtime } from '../hooks/useRealtime'
import { api } from '../api/client'
import { enablePush } from '../lib/push'
import type { Settings, Snapshot, Target } from '../types'

function createQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
}

function renderWithClient(node: ReactNode) {
  const client = createQueryClient()
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

describe('首次设置', () => {
  it('缺少必填项时给出就地错误且不发送请求', () => {
    renderWithClient(<AuthPage initialized={false} productName="号池监控" onAuthenticated={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: '完成设置' }))
    expect(screen.getByRole('alert')).toHaveTextContent('请填写管理员账号和密码')
  })
})

describe('跨端导航', () => {
  it('桌面侧栏和手机顶栏都提供退出登录入口', () => {
    const logout = vi.fn()
    render(
      <MemoryRouter>
        <AppShell bootstrap={{ initialized: true, authenticated: true, productName: '号池监控', totpEnabled: false }} onLogout={logout} />
      </MemoryRouter>
    )
    const buttons = screen.getAllByRole('button', { name: '退出登录' })
    expect(buttons).toHaveLength(2)
    fireEvent.click(buttons[1])
    expect(logout).toHaveBeenCalledOnce()
  })
})

describe('渠道向导', () => {
  const defaultSettings: Settings = {
    productName: '号池监控',
    historyRetentionDays: 7,
    defaultCheckIntervalMinutes: 5,
    allowPrivateTargets: false,
    totpEnabled: false
  }

  beforeEach(() => {
    vi.spyOn(api, 'settings').mockResolvedValue(defaultSettings)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('完成基本信息后进入登录步骤', async () => {
    renderWithClient(
      <MemoryRouter initialEntries={['/targets/new']}>
        <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
      </MemoryRouter>
    )
    fireEvent.change(await screen.findByLabelText(/渠道名称/), { target: { value: '测试渠道' } })
    fireEvent.change(screen.getByLabelText(/站点地址/), { target: { value: 'https://api.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    expect(screen.getByRole('heading', { name: '登录与认证' })).toBeInTheDocument()
    expect(screen.getByText('选择登录方式')).toBeInTheDocument()
    expect(screen.getByText('支持 Linux.do、GitHub 等站点网页登录。')).toBeInTheDocument()
  })

  it('网页登录先准备任务再由真实点击打开渠道页面', async () => {
    vi.spyOn(api, 'createTargetAuthAttempt').mockResolvedValue({
      id: 'auth_0123456789abcdef0123456789abcdef',
      status: 'waiting',
      loginUrl: 'https://api.example.com/login',
      expiresAt: new Date(Date.now() + 600_000).toISOString()
    })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderWithClient(
      <MemoryRouter initialEntries={['/targets/new']}>
        <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
      </MemoryRouter>
    )
    fireEvent.change(await screen.findByLabelText(/渠道名称/), { target: { value: '授权渠道' } })
    fireEvent.change(screen.getByLabelText(/站点地址/), { target: { value: 'https://api.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '准备网页登录' }))
    const launch = await screen.findByRole('button', { name: '打开授权窗口' })
    expect(open).not.toHaveBeenCalled()
    fireEvent.click(launch)
    expect(open).toHaveBeenCalledWith('https://api.example.com/login', '_blank', 'noopener,noreferrer')
  })

  it('Sub2API OAuth 回调只解析同源 fragment 且不保留完整地址', () => {
    expect(parseSub2APIOAuthCallback(
      'https://sub.example.com/oauth/callback#access_token=access%2Evalue&refresh_token=refresh%2Bvalue',
      'https://sub.example.com'
    )).toEqual({ accessToken: 'access.value', refreshToken: 'refresh+value' })
    expect(() => parseSub2APIOAuthCallback(
      'https://other.example.com/oauth/callback#access_token=secret',
      'https://sub.example.com'
    )).toThrow('与当前渠道不是同一来源')
  })

  it('自动识别成功后切换渠道类型并显示结果', async () => {
    const detect = vi.spyOn(api, 'detectTarget').mockResolvedValue({ kind: 'sub2api', message: '已识别为 Sub2API' })
    renderWithClient(
      <MemoryRouter initialEntries={['/targets/new']}>
        <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
      </MemoryRouter>
    )
    fireEvent.change(await screen.findByLabelText(/站点地址/), { target: { value: 'https://sub.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '自动识别' }))
    await waitFor(() => expect(screen.getByText('已识别为 Sub2API')).toBeInTheDocument())
    expect(screen.getByLabelText('渠道类型')).toHaveValue('sub2api')
    expect(detect).toHaveBeenCalledWith('https://sub.example.com')
    detect.mockRestore()
  })

  it('CLIProxyAPI 仅填写管理密钥并使用三项默认告警方向', async () => {
    const create = vi.spyOn(api, 'createTarget').mockResolvedValue({
      id: 'cli-proxy-1',
      name: '代理号池',
      kind: 'cliproxyapi',
      baseUrl: 'https://proxy.example.com',
      status: 'unknown',
      statusText: '等待检测',
      enabled: true,
      checkIntervalMinutes: 5,
      authConfigured: true,
      metrics: []
    })
    renderWithClient(
      <MemoryRouter initialEntries={['/targets/new']}>
        <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
      </MemoryRouter>
    )

    fireEvent.change(await screen.findByLabelText(/渠道名称/), { target: { value: '代理号池' } })
    fireEvent.change(screen.getByLabelText('渠道类型'), { target: { value: 'cliproxyapi' } })
    fireEvent.change(screen.getByLabelText(/站点地址/), { target: { value: 'https://proxy.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))

    expect(screen.getByLabelText('管理密钥')).toBeInTheDocument()
    expect(screen.getByText('管理密钥仅用于只读查询账号状态与统计，不会启停、重置或删除账号。')).toBeInTheDocument()
    expect(screen.queryByText('选择登录方式')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('管理密钥'), { target: { value: 'management-secret' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))

    expect(screen.getAllByLabelText('告警条件').map((element) => (element as HTMLSelectElement).value)).toEqual(['lte', 'gte', 'gte'])
    expect(screen.getAllByLabelText('告警阈值').map((element) => (element as HTMLInputElement).value)).toEqual(['0', '1', '1'])
    expect(screen.getByText('healthy_accounts')).toBeInTheDocument()
    expect(screen.getByText('limited_accounts')).toBeInTheDocument()
    expect(screen.getByText('error_accounts')).toBeInTheDocument()
    expect(screen.queryByText('disabled_accounts')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '添加渠道' }))
    await waitFor(() => expect(create).toHaveBeenCalledOnce())
    expect(create.mock.calls[0][0].thresholds).toEqual([
      expect.objectContaining({ key: 'healthy_accounts', comparison: 'lte' }),
      expect.objectContaining({ key: 'limited_accounts', comparison: 'gte' }),
      expect.objectContaining({ key: 'error_accounts', comparison: 'gte' })
    ])
    create.mockRestore()
  })

  it('编辑自定义渠道时只回填非秘密配置', () => {
    const target: Target = {
      id: 'custom-1',
      name: '自定义余额站',
      kind: 'custom',
      baseUrl: 'https://custom.example.com/status',
      status: 'healthy',
      statusText: '运行正常',
      enabled: true,
      checkIntervalMinutes: 8,
      authConfigured: true,
      authType: 'headers',
      requestMethod: 'POST',
      confirmPost: true,
      jsonPointer: '/result/credit',
      statusPointer: '/result/state',
      customHeadersConfigured: true,
      metrics: [{ key: 'wallet_balance', label: '剩余额度', value: '88', unit: '点', threshold: '15', status: 'healthy' }]
    }

    const draft = targetToDraft(target)
    expect(draft.authType).toBe('headers')
    expect(draft.requestMethod).toBe('POST')
    expect(draft.confirmPost).toBe(true)
    expect(draft.jsonPointer).toBe('/result/credit')
    expect(draft.statusPointer).toBe('/result/state')
    expect(draft.customHeaders).toBe('')
    expect(draft.password).toBe('')
    expect(draft.accessToken).toBe('')
    expect(draft.totpSecret).toBe('')
    expect(draft.thresholds[0].comparison).toBe('lte')
  })

  it('可以关闭 New API 订阅监控且保留零阈值语义', async () => {
    const create = vi.spyOn(api, 'createTarget').mockResolvedValue({
      id: 'new-api-1',
      name: '只监控钱包',
      kind: 'new_api',
      baseUrl: 'https://api.example.com',
      status: 'unknown',
      statusText: '等待检测',
      enabled: true,
      checkIntervalMinutes: 5,
      authConfigured: false,
      metrics: [{ key: 'wallet_balance', label: '钱包余额', value: '0', unit: '站点单位', threshold: '0', status: 'unknown' }]
    })
    renderWithClient(
      <MemoryRouter initialEntries={['/targets/new']}>
        <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
      </MemoryRouter>
    )

    fireEvent.change(await screen.findByLabelText(/渠道名称/), { target: { value: '只监控钱包' } })
    fireEvent.change(screen.getByLabelText(/站点地址/), { target: { value: 'https://api.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    const subscriptionToggle = screen.getByRole('checkbox', { name: /监控订阅额度/ })
    expect(subscriptionToggle).not.toBeChecked()
    fireEvent.click(subscriptionToggle)
    expect(screen.getByText('subscription_balance')).toBeInTheDocument()
    fireEvent.click(subscriptionToggle)
    expect(screen.queryByText('subscription_balance')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('告警阈值'), { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '添加渠道' }))

    await waitFor(() => expect(create).toHaveBeenCalledOnce())
    expect(create.mock.calls[0][0].thresholds).toEqual([
      expect.objectContaining({ key: 'wallet_balance', value: '0' })
    ])
    expect(create.mock.calls[0][0].thresholds.some((item) => item.key === 'subscription_balance')).toBe(false)
    create.mockRestore()
  })

  it('即使已有旧缓存也等待刷新后使用保存的默认检测间隔', async () => {
    let resolveSettings: ((value: Settings) => void) | undefined
    vi.mocked(api.settings).mockImplementation(() => new Promise((resolve) => { resolveSettings = resolve }))
    const client = createQueryClient()
    client.setQueryData(['settings'], defaultSettings)
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/targets/new']}>
          <Routes><Route path="/targets/new" element={<TargetWizardPage />} /></Routes>
        </MemoryRouter>
      </QueryClientProvider>
    )

    expect(screen.getByText('正在读取默认检测设置')).toBeInTheDocument()
    await act(async () => {
      resolveSettings?.({ ...defaultSettings, defaultCheckIntervalMinutes: 10 })
    })
    fireEvent.change(await screen.findByLabelText(/渠道名称/), { target: { value: '十分钟检测' } })
    fireEvent.change(screen.getByLabelText(/站点地址/), { target: { value: 'https://api.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))
    fireEvent.click(screen.getByRole('button', { name: '下一步' }))

    expect(screen.getByLabelText('检测间隔（分钟）')).toHaveValue(10)
  })
})

describe('状态和趋势', () => {
  it('状态同时提供图标语义与文字', () => {
    render(<StatusPill status="warning" />)
    expect(screen.getByText('需关注')).toBeInTheDocument()
  })

  it('趋势图包含可访问标题和阈值说明', () => {
    const snapshots: Snapshot[] = [
      { id: '1', targetId: 't1', metricKey: 'wallet_balance', value: '35', unit: '元', measuredAt: '2026-07-18T10:00:00Z' },
      { id: '2', targetId: 't1', metricKey: 'wallet_balance', value: '28', unit: '元', measuredAt: '2026-07-18T11:00:00Z' }
    ]
    render(<LineChart snapshots={snapshots} threshold="20" label="钱包余额" unit="元" />)
    expect(screen.getByRole('img', { name: '钱包余额趋势，共 2 个数据点' })).toBeInTheDocument()
    expect(screen.getByText((_, element) => element?.classList.contains('chart-legend') ?? false)).toHaveTextContent('告警阈值')
  })

  it('大于等于阈值在指标卡、趋势图和历史表格中使用同一方向', async () => {
    const cliTarget: Target = {
      id: 'cli-target',
      name: '代理号池',
      kind: 'cliproxyapi',
      baseUrl: 'https://proxy.example.com',
      status: 'warning',
      statusText: '存在限流账号',
      enabled: true,
      checkIntervalMinutes: 5,
      authConfigured: true,
      metrics: [{ key: 'limited_accounts', label: '限流账号', value: '2', unit: '个', threshold: '1', comparison: 'gte', status: 'warning' }]
    }
    const snapshots: Snapshot[] = [
      { id: '1', targetId: 'cli-target', metricKey: 'limited_accounts', value: '0', unit: '个', measuredAt: '2026-07-18T10:00:00Z' },
      { id: '2', targetId: 'cli-target', metricKey: 'limited_accounts', value: '1', unit: '个', measuredAt: '2026-07-18T11:00:00Z' }
    ]
    const target = vi.spyOn(api, 'target').mockResolvedValue(cliTarget)
    const history = vi.spyOn(api, 'history').mockResolvedValue({ target: cliTarget, snapshots })

    renderWithClient(<MemoryRouter initialEntries={['/targets/cli-target']}><Routes><Route path="/targets/:id" element={<TargetDetailPage />} /></Routes></MemoryRouter>)

    expect(await screen.findByText('告警条件 ≥ 1 个')).toBeInTheDocument()
    await screen.findByRole('img', { name: '限流账号趋势，共 2 个数据点' })
    expect(screen.getByText((_, element) => element?.classList.contains('chart-legend') ?? false)).toHaveTextContent('告警阈值（≥）')
    fireEvent.click(screen.getByText('查看同数据表格'))
    expect(screen.getByText('低于阈值')).toBeInTheDocument()
    expect(screen.getByText('达到告警条件')).toBeInTheDocument()
    target.mockRestore()
    history.mockRestore()
  })
})

describe('充值与推送', () => {
	it('充值入口只在新标签页打开配置地址', async () => {
		const target = vi.spyOn(api, 'target').mockResolvedValue({
			id: 'target-1', name: '主站', kind: 'new_api', baseUrl: 'https://api.example.com',
			topupUrl: 'https://api.example.com/console/topup', status: 'healthy', statusText: '运行正常',
			enabled: true, checkIntervalMinutes: 5, authConfigured: true,
			metrics: [{ key: 'wallet_balance', label: '钱包余额', value: '20', unit: 'USD', threshold: '10', status: 'healthy' }]
		})
		const history = vi.spyOn(api, 'history').mockResolvedValue({ target: await api.target('target-1'), snapshots: [] })
		renderWithClient(<MemoryRouter initialEntries={['/targets/target-1']}><Routes><Route path="/targets/:id" element={<TargetDetailPage />} /></Routes></MemoryRouter>)
		const link = await screen.findByRole('link', { name: '充值' })
		expect(link).toHaveAttribute('href', 'https://api.example.com/console/topup')
		expect(link).toHaveAttribute('target', '_blank')
		expect(link).toHaveAttribute('rel', 'noopener noreferrer')
		target.mockRestore()
		history.mockRestore()
	})

	it('浏览器授权后把标准订阅发送给服务器', async () => {
		const subscribePush = vi.spyOn(api, 'subscribePush').mockResolvedValue()
		const subscribe = vi.fn().mockResolvedValue({
			toJSON: () => ({ endpoint: 'https://push.example/sub', expirationTime: null, keys: { p256dh: 'public', auth: 'auth' } })
		})
		Object.defineProperty(navigator, 'serviceWorker', {
			configurable: true,
			value: { ready: Promise.resolve({ pushManager: { getSubscription: vi.fn().mockResolvedValue(null), subscribe } }) }
		})
		vi.stubGlobal('PushManager', class {})
		vi.stubGlobal('Notification', { requestPermission: vi.fn().mockResolvedValue('granted') })
		await enablePush('AQID', '安卓手机')
		expect(subscribe).toHaveBeenCalledWith(expect.objectContaining({ userVisibleOnly: true }))
		expect(subscribePush).toHaveBeenCalledWith(expect.objectContaining({ endpoint: 'https://push.example/sub', name: '安卓手机' }))
		subscribePush.mockRestore()
		vi.unstubAllGlobals()
	})
})

describe('主题和实时事件', () => {
  beforeEach(() => window.localStorage.clear())
  afterEach(() => vi.unstubAllGlobals())

  it('可以在当前设备切换深色主题', () => {
    function Probe() {
      const { setPreference } = useTheme()
      return <button type="button" onClick={() => setPreference('dark')}>切换深色</button>
    }
    render(<ThemeProvider><Probe /></ThemeProvider>)
    fireEvent.click(screen.getByRole('button', { name: '切换深色' }))
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(window.localStorage.getItem('pool-monitor-theme')).toBe('dark')
  })

  it('收到快照或渠道更新事件后刷新对应缓存并在卸载时关闭连接', () => {
    const listeners = new Map<string, EventListener>()
    const close = vi.fn()
    class FakeEventSource {
      constructor(_url: string, _options?: EventSourceInit) {}
      addEventListener(name: string, listener: EventListener) { listeners.set(name, listener) }
      close() { close() }
    }
    vi.stubGlobal('EventSource', FakeEventSource)
    const client = createQueryClient()
    const invalidate = vi.spyOn(client, 'invalidateQueries').mockResolvedValue()
    function Probe() { useRealtime(true); return null }
    const view = render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>)
    act(() => listeners.get('snapshot')?.(new Event('snapshot')))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['history'] })
    act(() => listeners.get('target.updated')?.(new Event('target.updated')))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['alerts'] })
    view.unmount()
    expect(close).toHaveBeenCalledOnce()
  })
})

describe('系统设置', () => {
  it('读取设置失败时显示错误而非持续加载', async () => {
    const settings = vi.spyOn(api, 'settings').mockRejectedValue(new Error('读取设置失败'))
    const pushInfo = vi.spyOn(api, 'pushInfo').mockResolvedValue({ supported: false, vapidPublicKey: '', devices: [] })

    renderWithClient(<ThemeProvider><SettingsPage /></ThemeProvider>)

    expect(await screen.findByText('读取设置失败')).toBeInTheDocument()
    settings.mockRestore()
    pushInfo.mockRestore()
  })
})
