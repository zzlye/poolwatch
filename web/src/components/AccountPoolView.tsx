import { ChevronLeft, ChevronRight, Search } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { formatDateTime } from '../lib/format'
import type { SanitizedAccount, TargetStatus } from '../types'
import { EmptyState } from './Common'
import { StatusPill } from './StatusPill'

type AccountStatusFilter = 'all' | TargetStatus

const accountStatusOptions: Array<{ value: AccountStatusFilter; label: string }> = [
  { value: 'all', label: '全部状态' },
  { value: 'healthy', label: '正常' },
  { value: 'warning', label: '限流' },
  { value: 'error', label: '异常' },
  { value: 'disabled', label: '禁用' },
  { value: 'unknown', label: '待检测' }
]

const accountStatusLabels: Record<TargetStatus, string> = {
  healthy: '正常',
  warning: '限流',
  error: '异常',
  disabled: '禁用',
  unknown: '待检测'
}

const pageSizeOptions = [10, 20, 50, 100]

function normalizeAccountType(value?: string): string {
  return value?.trim().toLocaleLowerCase() ?? ''
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

export function AccountPoolView({ accounts }: { accounts: SanitizedAccount[] }) {
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<AccountStatusFilter>('all')
  const [type, setType] = useState('all')
  const [pageSize, setPageSize] = useState(10)
  const [requestedPage, setRequestedPage] = useState(1)

  const accountTypes = useMemo(() => {
    const values = accounts.map((account) => normalizeAccountType(account.type)).filter(Boolean)
    return [...new Set(values)].sort((left, right) => left.localeCompare(right, 'zh-CN', { sensitivity: 'base' }))
  }, [accounts])

  const filteredAccounts = useMemo(() => {
    const keyword = search.trim().toLocaleLowerCase()
    return accounts.filter((account) => {
      const matchesSearch = !keyword || (account.email ?? account.displayName ?? account.id).toLocaleLowerCase().includes(keyword)
      const matchesStatus = status === 'all' || account.status === status
      const matchesType = type === 'all' || normalizeAccountType(account.type) === type
      return matchesSearch && matchesStatus && matchesType
    })
  }, [accounts, search, status, type])

  const totalPages = Math.max(1, Math.ceil(filteredAccounts.length / pageSize))
  const currentPage = Math.min(requestedPage, totalPages)
  const startIndex = (currentPage - 1) * pageSize
  const pagedAccounts = filteredAccounts.slice(startIndex, startIndex + pageSize)
  const pageItems = buildPageItems(currentPage, totalPages)

  useEffect(() => {
    // 数据刷新导致页数减少时，把内部页码同步回仍然存在的最后一页。
    if (requestedPage !== currentPage) setRequestedPage(currentPage)
  }, [currentPage, requestedPage])

  function resetPage() {
    setRequestedPage(1)
  }

  function resetFilters() {
    setSearch('')
    setStatus('all')
    setType('all')
    resetPage()
  }

  const firstVisible = filteredAccounts.length ? startIndex + 1 : 0
  const lastVisible = filteredAccounts.length ? Math.min(startIndex + pageSize, filteredAccounts.length) : 0
  const hasFilters = Boolean(search.trim()) || status !== 'all' || type !== 'all'

  return (
    <>
      <div className="account-filter-bar" aria-label="账号筛选">
        <label className="search-field">
          <span className="sr-only">搜索账号邮箱</span>
          <Search aria-hidden="true" size={18} />
          <input
            type="search"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value)
              resetPage()
            }}
            placeholder="搜索邮箱"
          />
        </label>
        <label className="compact-field account-filter-field">
          <span>账号类型</span>
          <select
            value={type}
            onChange={(event) => {
              setType(event.target.value)
              resetPage()
            }}
          >
            <option value="all">全部类型</option>
            {accountTypes.map((accountType) => <option key={accountType} value={accountType}>{accountType}</option>)}
          </select>
        </label>
        <label className="compact-field account-filter-field">
          <span>账号状态</span>
          <select
            value={status}
            onChange={(event) => {
              setStatus(event.target.value as AccountStatusFilter)
              resetPage()
            }}
          >
            {accountStatusOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
          </select>
        </label>
      </div>

      {pagedAccounts.length ? (
        <div className="table-wrap account-table-wrap">
            <table className="account-table">
              <thead><tr><th scope="col">账号</th><th scope="col">类型</th><th scope="col">状态</th><th scope="col">图片额度</th><th scope="col">预计恢复</th></tr></thead>
              <tbody>{pagedAccounts.map((account) => <tr key={account.id}><td data-label="账号"><strong>{account.email ?? account.displayName ?? account.id}</strong></td><td data-label="类型">{normalizeAccountType(account.type) || '—'}</td><td data-label="状态"><StatusPill status={account.status} label={account.statusText || accountStatusLabels[account.status]} /></td><td data-label="图片额度">{account.imageQuota ?? '—'}{account.imageQuota !== undefined ? ' 次' : ''}</td><td data-label="预计恢复">{account.recoveryAt ? formatDateTime(account.recoveryAt) : '—'}</td></tr>)}</tbody>
            </table>
        </div>
      ) : (
        <EmptyState
          title="没有符合条件的账号"
          description="请调整邮箱、账号类型或账号状态筛选。"
          action={<button className="button secondary" type="button" onClick={resetFilters}>清除筛选</button>}
        />
      )}

      <nav className="account-pagination" aria-label="账号分页">
        <p className="account-page-summary" aria-live="polite">
          显示第 {firstVisible}–{lastVisible} 条，共 {filteredAccounts.length} 条
          {hasFilters ? <span>（账号总数 {accounts.length} 条）</span> : null}
        </p>
        <label className="compact-field account-page-size">
          <span>每页数量</span>
          <select
            value={pageSize}
            onChange={(event) => {
              setPageSize(Number(event.target.value))
              resetPage()
            }}
          >
            {pageSizeOptions.map((size) => <option key={size} value={size}>{size} 条/页</option>)}
          </select>
        </label>
        <div className="account-page-buttons">
          <button className="icon-button" type="button" aria-label="上一页" disabled={currentPage <= 1} onClick={() => setRequestedPage((page) => Math.max(1, page - 1))}><ChevronLeft aria-hidden="true" size={18} /></button>
          {pageItems.map((item) => typeof item === 'number' ? (
            <button
              key={item}
              className={item === currentPage ? 'account-page-button current' : 'account-page-button'}
              type="button"
              aria-label={`第 ${item} 页`}
              aria-current={item === currentPage ? 'page' : undefined}
              onClick={() => setRequestedPage(item)}
            >{item}</button>
          ) : <span key={item} className="account-page-ellipsis" aria-hidden="true">…</span>)}
          <button className="icon-button" type="button" aria-label="下一页" disabled={currentPage >= totalPages} onClick={() => setRequestedPage((page) => Math.min(totalPages, page + 1))}><ChevronRight aria-hidden="true" size={18} /></button>
        </div>
      </nav>
    </>
  )
}
