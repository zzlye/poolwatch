import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AccountPoolView } from '../components/AccountPoolView'
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
