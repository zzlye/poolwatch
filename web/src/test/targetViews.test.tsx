import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { TargetCards, TargetTable } from '../components/TargetViews'
import type { Target } from '../types'

const cliProxyTarget: Target = {
  id: 'cli-primary-metric',
  name: '代理号池',
  kind: 'cliproxyapi',
  baseUrl: 'https://proxy.example.com',
  status: 'healthy',
  statusText: '运行正常',
  enabled: true,
  checkIntervalMinutes: 5,
  authConfigured: true,
  metrics: [
    { key: 'account_total', label: '账号总数', value: '9', unit: '个', status: 'healthy' },
    { key: 'healthy_accounts', label: '可用账号', value: '6', unit: '个', threshold: '0', comparison: 'lte', status: 'healthy' }
  ]
}

describe('CLIProxyAPI 渠道主要指标', () => {
  it('历史快照仍以账号总数开头时优先显示可用账号', () => {
    render(
      <MemoryRouter>
        <TargetTable targets={[cliProxyTarget]} />
        <TargetCards targets={[cliProxyTarget]} />
      </MemoryRouter>
    )

    expect(screen.getAllByText('6 个')).toHaveLength(2)
    expect(screen.getByText('可用账号')).toBeInTheDocument()
    expect(screen.queryByText('9 个')).not.toBeInTheDocument()
  })
})
