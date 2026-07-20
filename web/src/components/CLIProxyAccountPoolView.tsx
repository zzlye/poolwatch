import { ChevronLeft, ChevronRight, Search } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { formatDateTime } from '../lib/format'
import type { SanitizedAccount, TargetStatus } from '../types'
import { EmptyState } from './Common'
import { StatusPill } from './StatusPill'

type AccountStatusFilter = 'all' | TargetStatus

const statusOptions: Array<{ value: AccountStatusFilter; label: string }> = [
  { value: 'all', label: '全部状态' },
  { value: 'healthy', label: '正常' },
  { value: 'warning', label: '限流' },
  { value: 'error', label: '异常' },
  { value: 'disabled', label: '禁用' },
  { value: 'unknown', label: '待检测' }
]

const statusLabels: Record<TargetStatus, string> = {
  healthy: '正常',
  warning: '限流',
  error: '异常',
  disabled: '禁用',
  unknown: '待检测'
}

const pageSizeOptions = [10, 20, 50, 100]

function normalizeFilterValue(value?: string): string {
  return value?.trim().toLocaleLowerCase() ?? ''
}

function accountTitle(account: SanitizedAccount): string {
  const named = account.displayName?.trim() || account.email?.trim()
  if (named) return named
  // 缺少可读名称时只展示内部哈希的短前缀，避免把完整标识直接放到页面。
  const shortHash = account.id.trim().slice(0, 8)
  return shortHash ? `账号 ${shortHash}${account.id.trim().length > 8 ? '…' : ''}` : '未命名账号'
}

function buildFilterOptions(accounts: SanitizedAccount[], field: 'provider' | 'type') {
  const values = new Map<string, string>()
  accounts.forEach((account) => {
    const raw = account[field]?.trim()
    const normalized = normalizeFilterValue(raw)
    if (raw && normalized && !values.has(normalized)) values.set(normalized, raw)
  })
  return [...values.entries()].sort((left, right) => left[1].localeCompare(right[1], 'zh-CN', { sensitivity: 'base' }))
}

function buildPageItems(currentPage: number, totalPages: number): Array<number | string> {
  const candidates = new Set([1, totalPages, currentPage - 1, currentPage, currentPage + 1])
  const pages = [...candidates].filter((page) => page >= 1 && page <= totalPages).sort((left, right) => left - right)
  const result: Array<number | string> = []
  pages.forEach((page, index) => {
    const previous = pages[index - 1]
    if (previous && page - previous > 1) result.push(`ellipsis-${previous}`)
    result.push(page)
  })
  return result
}

function formatCounter(value?: number): string {
  return value === undefined ? '—' : value.toLocaleString('zh-CN')
}

export function CLIProxyAccountPoolView({ accounts }: { accounts: SanitizedAccount[] }) {
  const [search, setSearch] = useState('')
  const [provider, setProvider] = useState('all')
  const [type, setType] = useState('all')
  const [status, setStatus] = useState<AccountStatusFilter>('all')
  const [pageSize, setPageSize] = useState(10)
  const [requestedPage, setRequestedPage] = useState(1)

  const providerOptions = useMemo(() => buildFilterOptions(accounts, 'provider'), [accounts])
  const typeOptions = useMemo(() => buildFilterOptions(accounts, 'type'), [accounts])
  const filteredAccounts = useMemo(() => {
    const keyword = search.trim().toLocaleLowerCase()
    return accounts.filter((account) => {
      const searchable = [account.displayName, account.email, account.provider, account.type, account.id]
        .filter(Boolean)
        .join(' ')
        .toLocaleLowerCase()
      return (!keyword || searchable.includes(keyword))
        && (provider === 'all' || normalizeFilterValue(account.provider) === provider)
        && (type === 'all' || normalizeFilterValue(account.type) === type)
        && (status === 'all' || account.status === status)
    })
  }, [accounts, provider, search, status, type])

  const totalPages = Math.max(1, Math.ceil(filteredAccounts.length / pageSize))
  const currentPage = Math.min(requestedPage, totalPages)
  const startIndex = (currentPage - 1) * pageSize
  const pagedAccounts = filteredAccounts.slice(startIndex, startIndex + pageSize)
  const firstVisible = filteredAccounts.length ? startIndex + 1 : 0
  const lastVisible = filteredAccounts.length ? Math.min(startIndex + pageSize, filteredAccounts.length) : 0
  const hasFilters = Boolean(search.trim()) || provider !== 'all' || type !== 'all' || status !== 'all'

  useEffect(() => {
    // 账号刷新后若当前页已经不存在，则回到仍然存在的最后一页。
    if (requestedPage !== currentPage) setRequestedPage(currentPage)
  }, [currentPage, requestedPage])

  const resetPage = () => setRequestedPage(1)
  const resetFilters = () => {
    setSearch('')
    setProvider('all')
    setType('all')
    setStatus('all')
    resetPage()
  }

  return (
    <>
      <div className="account-filter-bar cliproxy-filter-bar" aria-label="CLIProxyAPI 账号筛选">
        <label className="search-field">
          <span className="sr-only">搜索 CLIProxyAPI 账号</span>
          <Search aria-hidden="true" size={18} />
          <input type="search" value={search} onChange={(event) => { setSearch(event.target.value); resetPage() }} placeholder="搜索账号、邮箱或提供商" />
        </label>
        <label className="compact-field account-filter-field"><span>提供商</span><select value={provider} onChange={(event) => { setProvider(event.target.value); resetPage() }}><option value="all">全部提供商</option>{providerOptions.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label className="compact-field account-filter-field"><span>账号类型</span><select value={type} onChange={(event) => { setType(event.target.value); resetPage() }}><option value="all">全部类型</option>{typeOptions.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label className="compact-field account-filter-field"><span>账号状态</span><select value={status} onChange={(event) => { setStatus(event.target.value as AccountStatusFilter); resetPage() }}>{statusOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
      </div>

      {pagedAccounts.length ? (
        <div className="table-wrap cliproxy-account-table-wrap">
          <table className="cliproxy-account-table">
            <thead><tr><th scope="col">账号</th><th scope="col">提供商</th><th scope="col">类型</th><th scope="col">状态</th><th scope="col">成功</th><th scope="col">失败</th><th scope="col">恢复时间</th></tr></thead>
            <tbody>{pagedAccounts.map((account) => (
              <tr key={account.id}>
                <td data-label="账号"><strong>{accountTitle(account)}</strong>{account.displayName && account.email ? <small>{account.email}</small> : null}</td>
                <td data-label="提供商">{account.provider || '—'}</td>
                <td data-label="类型">{account.type || '—'}</td>
                <td data-label="状态"><StatusPill status={account.status} label={account.statusText || statusLabels[account.status]} /></td>
                <td data-label="成功">{formatCounter(account.success)}</td>
                <td data-label="失败">{formatCounter(account.fail)}</td>
                <td data-label="恢复时间">{account.recoveryAt ? formatDateTime(account.recoveryAt) : '—'}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      ) : (
        <EmptyState title="没有符合条件的账号" description="请调整账号、提供商、类型或状态筛选。" action={<button className="button secondary" type="button" onClick={resetFilters}>清除筛选</button>} />
      )}

      <nav className="account-pagination" aria-label="CLIProxyAPI 账号分页">
        <p className="account-page-summary" aria-live="polite">显示第 {firstVisible}–{lastVisible} 条，共 {filteredAccounts.length} 条{hasFilters ? <span>（账号总数 {accounts.length} 条）</span> : null}</p>
        <label className="compact-field account-page-size"><span>每页数量</span><select value={pageSize} onChange={(event) => { setPageSize(Number(event.target.value)); resetPage() }}>{pageSizeOptions.map((size) => <option key={size} value={size}>{size} 条/页</option>)}</select></label>
        <div className="account-page-buttons">
          <button className="icon-button" type="button" aria-label="上一页" disabled={currentPage <= 1} onClick={() => setRequestedPage((page) => Math.max(1, page - 1))}><ChevronLeft aria-hidden="true" size={18} /></button>
          {buildPageItems(currentPage, totalPages).map((item) => typeof item === 'number' ? <button key={item} className={item === currentPage ? 'account-page-button current' : 'account-page-button'} type="button" aria-label={`第 ${item} 页`} aria-current={item === currentPage ? 'page' : undefined} onClick={() => setRequestedPage(item)}>{item}</button> : <span key={item} className="account-page-ellipsis" aria-hidden="true">…</span>)}
          <button className="icon-button" type="button" aria-label="下一页" disabled={currentPage >= totalPages} onClick={() => setRequestedPage((page) => Math.min(totalPages, page + 1))}><ChevronRight aria-hidden="true" size={18} /></button>
        </div>
      </nav>
    </>
  )
}
