import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AccountPoolView } from '../components/AccountPoolView'
import { CLIProxyAccountPoolView, QuotaAccountPoolView } from '../components/CLIProxyAccountPoolView'
import type { SanitizedAccount, TargetStatus } from '../types'

const statusSequence: TargetStatus[] = ['healthy', 'warning', 'error', 'disabled']

function makeAccounts(total: number): SanitizedAccount[] {
  return Array.from({ length: total }, (_, index) => ({
    id: `account-${index + 1}`,
    email: `user${String(index + 1).padStart(2, '0')}***@example.com`,
    type: index % 3 === 0 ? 'free' : index % 3 === 1 ? 'plus' : 'team',
    status: statusSequence[index % statusSequence.length],
    imageQuota: String(index + 1)
  }))
}

describe('号池账号筛选与分页', () => {
  it('账号类型与账号状态独立筛选，并使用账号语义显示状态', () => {
    const accounts = makeAccounts(12)
    accounts[4].type = 'PLUS'
    render(<AccountPoolView accounts={accounts} />)

    const typeSelect = screen.getByRole('combobox', { name: '账号类型' })
    const statusSelect = screen.getByRole('combobox', { name: '账号状态' })
    expect(typeSelect).toContainHTML('<option value="free">free</option>')
    expect(typeSelect).toContainHTML('<option value="plus">plus</option>')
    expect(typeSelect).toContainHTML('<option value="team">team</option>')
    expect(screen.getAllByRole('option', { name: 'plus' })).toHaveLength(1)
    expect(statusSelect).toContainHTML('<option value="warning">限流</option>')
    expect(statusSelect).toContainHTML('<option value="disabled">禁用</option>')

    fireEvent.change(typeSelect, { target: { value: 'plus' } })
    fireEvent.change(statusSelect, { target: { value: 'warning' } })
    expect(screen.getByText(/共 1 条/)).toBeInTheDocument()
    expect(screen.getByText('user02***@example.com')).toBeInTheDocument()
    expect(document.querySelector('.status-pill')).toHaveTextContent('限流')
    expect(screen.queryByText('user06***@example.com')).not.toBeInTheDocument()
  })

  it('邮箱搜索应用到桌面表格和手机列表的同一分页结果', () => {
    render(<AccountPoolView accounts={makeAccounts(12)} />)

    fireEvent.change(screen.getByRole('searchbox', { name: '搜索账号邮箱' }), { target: { value: 'user12' } })
    expect(screen.getByText(/共 1 条/)).toBeInTheDocument()
    expect(screen.getByText('user12***@example.com')).toBeInTheDocument()
    expect(screen.queryByText('user01***@example.com')).not.toBeInTheDocument()
  })

  it('默认每页十条并支持页码、前后翻页和切换每页数量', () => {
    render(<AccountPoolView accounts={makeAccounts(12)} />)

    expect(screen.getByText('显示第 1–10 条，共 12 条')).toBeInTheDocument()
    expect(screen.queryByText('user11***@example.com')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    expect(screen.getByText('显示第 11–12 条，共 12 条')).toBeInTheDocument()
    expect(screen.getByText('user11***@example.com')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: '上一页' }))
    expect(screen.getByText('显示第 1–10 条，共 12 条')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('combobox', { name: '每页数量' }), { target: { value: '20' } })
    expect(screen.getByText('显示第 1–12 条，共 12 条')).toBeInTheDocument()
    expect(screen.getByText('user12***@example.com')).toBeInTheDocument()
  })
})

describe('CLIProxyAPI 账号筛选与分页', () => {
  const accounts: SanitizedAccount[] = Array.from({ length: 12 }, (_, index) => ({
    id: `cli-${index + 1}`,
    displayName: `代理账号 ${index + 1}`,
    email: `proxy${String(index + 1).padStart(2, '0')}@example.com`,
    provider: index % 2 === 0 ? 'OpenAI' : 'Anthropic',
    type: index % 3 === 0 ? 'OAuth' : 'API Key',
    status: statusSequence[index % statusSequence.length],
    statusText: statusSequence[index % statusSequence.length] === 'warning' ? '正在限流' : undefined,
    success: 100 + index,
    fail: index
  }))

  it('提供商、账号类型和状态互相独立，并显示调用统计而不显示图片额度', () => {
    render(<CLIProxyAccountPoolView accounts={accounts} />)

    fireEvent.change(screen.getByRole('combobox', { name: '提供商' }), { target: { value: 'openai' } })
    fireEvent.change(screen.getByRole('combobox', { name: '账号类型' }), { target: { value: 'api key' } })
    fireEvent.change(screen.getByRole('combobox', { name: '账号状态' }), { target: { value: 'error' } })

    expect(screen.getByText(/共 2 条/)).toBeInTheDocument()
    expect(screen.getByText('代理账号 3')).toBeInTheDocument()
    expect(screen.getByText('代理账号 11')).toBeInTheDocument()
    expect(screen.queryByText('代理账号 1')).not.toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '成功' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '失败' })).toBeInTheDocument()
    expect(screen.queryByText('图片额度')).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '账号状态' })).toContainHTML('<option value="warning">警告</option>')
  })

  it('支持账号搜索和十条一页的分页', () => {
    render(<CLIProxyAccountPoolView accounts={accounts} />)

    expect(screen.getByText('显示第 1–10 条，共 12 条')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    expect(screen.getByText('代理账号 12')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索 CLIProxyAPI 账号' }), { target: { value: 'proxy04' } })
    expect(screen.getByText('显示第 1–1 条，共 1 条')).toBeInTheDocument()
    expect(screen.getByText('代理账号 4')).toBeInTheDocument()
  })

  it('缺少名称和邮箱时只展示账号标识的短哈希', () => {
    const fullID = 'abcdef0123456789abcdef0123456789'
    render(<CLIProxyAccountPoolView accounts={[{ id: fullID, provider: 'OpenAI', type: 'OAuth', status: 'healthy', success: 1, fail: 0 }]} />)

    expect(screen.getByText('账号 abcdef01…')).toBeInTheDocument()
    expect(screen.queryByText(fullID)).not.toBeInTheDocument()
  })

  it('进入页面自动刷新默认第一页，按钮也只提交当前十个账号', async () => {
    const onRefreshQuota = vi.fn(async (accountIds: string[]) => ({
      accounts: accounts.filter((account) => accountIds.includes(account.id)),
      refreshedCount: accountIds.length,
      unavailableCount: 0,
      unsupportedCount: 0
    }))
    render(<CLIProxyAccountPoolView accounts={accounts} onRefreshQuota={onRefreshQuota} />)

    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(1))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(accounts.slice(0, 10).map((account) => account.id))
    await screen.findByText('本页额度已刷新：更新 10 个，暂未获取 0 个。')

    fireEvent.click(screen.getByRole('button', { name: '刷新本页额度' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(2))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(accounts.slice(0, 10).map((account) => account.id))
  })

  it('翻页自动刷新新页面，筛选变化等待用户手动刷新', async () => {
    const onRefreshQuota = vi.fn(async (accountIds: string[]) => ({
      accounts: accounts.filter((account) => accountIds.includes(account.id)),
      refreshedCount: accountIds.length,
      unavailableCount: 0,
      unsupportedCount: 0
    }))
    render(<CLIProxyAccountPoolView accounts={accounts} onRefreshQuota={onRefreshQuota} />)
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(1))

    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(2))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(['cli-11', 'cli-12'])

    fireEvent.change(screen.getByRole('combobox', { name: '提供商' }), { target: { value: 'openai' } })
    fireEvent.change(screen.getByRole('combobox', { name: '账号状态' }), { target: { value: 'error' } })

    await new Promise((resolve) => window.setTimeout(resolve, 20))
    expect(onRefreshQuota).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('button', { name: '刷新本页额度' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(3))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(['cli-3', 'cli-7', 'cli-11'])
  })

  it('相同页码切换到另一组筛选结果后仍刷新新页面账号', async () => {
    const filteredAccounts: SanitizedAccount[] = Array.from({ length: 40 }, (_, index) => ({
      id: `filtered-${index + 1}`,
      displayName: `筛选账号 ${index + 1}`,
      provider: index < 20 ? 'OpenAI' : 'Anthropic',
      type: 'OAuth',
      status: 'healthy'
    }))
    const onRefreshQuota = vi.fn(async (accountIds: string[]) => ({
      accounts: filteredAccounts.filter((account) => accountIds.includes(account.id)),
      refreshedCount: accountIds.length,
      unavailableCount: 0,
      unsupportedCount: 0
    }))
    render(<CLIProxyAccountPoolView accounts={filteredAccounts} onRefreshQuota={onRefreshQuota} />)
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(1))

    fireEvent.change(screen.getByRole('combobox', { name: '提供商' }), { target: { value: 'openai' } })
    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(2))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(filteredAccounts.slice(10, 20).map((account) => account.id))

    fireEvent.change(screen.getByRole('combobox', { name: '提供商' }), { target: { value: 'anthropic' } })
    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledTimes(3))
    expect(onRefreshQuota).toHaveBeenLastCalledWith(filteredAccounts.slice(30, 40).map((account) => account.id))
  })
})

describe('Sub2API 账号筛选与额度', () => {
  const accounts: SanitizedAccount[] = Array.from({ length: 12 }, (_, index) => ({
    id: `sub2-${index + 1}`,
    displayName: `订阅账号 ${index + 1}`,
    email: `sub${String(index + 1).padStart(2, '0')}@example.com`,
    provider: index % 2 === 0 ? 'OpenAI' : 'Anthropic',
    type: index % 3 === 0 ? '共享' : '独享',
    status: statusSequence[index % statusSequence.length],
    success: 100 + index,
    fail: index
  }))

  it('使用平台和类型独立筛选、分页并隐藏调用成功失败列', () => {
    render(<QuotaAccountPoolView kind="sub2api" accounts={accounts} />)

    expect(screen.getByRole('navigation', { name: 'Sub2API 账号分页' })).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: '搜索 Sub2API 账号' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '平台' })).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: '提供商' })).not.toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: '成功' })).not.toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: '失败' })).not.toBeInTheDocument()
    expect(screen.getByText('显示第 1–10 条，共 12 条')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    expect(screen.getByText('订阅账号 12')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('combobox', { name: '平台' }), { target: { value: 'anthropic' } })
    fireEvent.change(screen.getByRole('combobox', { name: '账号类型' }), { target: { value: '独享' } })
    expect(screen.getByText(/共 4 条/)).toBeInTheDocument()
    expect(screen.getByText('订阅账号 2')).toBeInTheDocument()
    expect(screen.queryByText('订阅账号 1')).not.toBeInTheDocument()
  })

  it('实时数据移除已选平台后自动恢复全部筛选', async () => {
    const { rerender } = render(<QuotaAccountPoolView kind="sub2api" accounts={accounts} />)
    fireEvent.change(screen.getByRole('combobox', { name: '平台' }), { target: { value: 'anthropic' } })
    expect(screen.getByRole('combobox', { name: '平台' })).toHaveValue('anthropic')

    const openAIAccounts = accounts.filter((account) => account.provider === 'OpenAI')
    rerender(<QuotaAccountPoolView kind="sub2api" accounts={openAIAccounts} />)

    await waitFor(() => expect(screen.getByRole('combobox', { name: '平台' })).toHaveValue('all'))
    expect(screen.getByText('订阅账号 1')).toBeInTheDocument()
  })

  it('自动与手动刷新都只提交当前页，并原样展示绝对额度和百分比', async () => {
    const quotaAccounts = accounts.map((account, index) => index === 0 ? {
      ...account,
      quotaState: 'available' as const,
      quotaWindows: [{
        key: 'balance',
        label: '账号余额',
        remainingValue: '0001.2300',
        limitValue: '010.0000',
        unit: 'USD',
        remainingPercent: '12.3',
        resetAt: '2026-08-01T08:00:00Z'
      }]
    } : account)
    const onRefreshQuota = vi.fn(async (accountIds: string[]) => ({
      accounts: quotaAccounts.filter((account) => accountIds.includes(account.id)),
      refreshedCount: accountIds.length,
      unavailableCount: 0,
      unsupportedCount: 0
    }))

    render(<QuotaAccountPoolView kind="sub2api" accounts={quotaAccounts} onRefreshQuota={onRefreshQuota} />)

    expect(screen.getByText('0001.2300 / 010.0000 USD')).toBeInTheDocument()
    expect(screen.getByText('剩余 12.3%')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: '账号余额剩余 12.3%' })).toHaveValue(12.3)
    await waitFor(() => expect(onRefreshQuota).toHaveBeenCalledWith(accounts.slice(0, 10).map((account) => account.id)))
    fireEvent.click(screen.getByRole('button', { name: '第 2 页' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenLastCalledWith(['sub2-11', 'sub2-12']))
    fireEvent.click(screen.getByRole('button', { name: '刷新本页额度' }))
    await waitFor(() => expect(onRefreshQuota).toHaveBeenLastCalledWith(['sub2-11', 'sub2-12']))
  })
})
