import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, BellRing, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, CircleOff, Clock3, RefreshCw, Search } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { targetKindLabels, type GroupMultiplier, type GroupPriceModel, type MultiplierStatus, type Target, type TargetMultiplierState } from '../types'

const statusLabels: Record<MultiplierStatus, string> = {
  stable: '稳定',
  changed: '已变化',
  missing: '本次未找到',
  unknown: '待检测'
}

const statusIcons = {
  stable: CheckCircle2,
  changed: BellRing,
  missing: CircleOff,
  unknown: Clock3
}

function multiplierText(value: string): string {
  // 服务端返回的十进制字符串需要原样展示，避免浏览器浮点转换损失精度。
  return value ? `${value}×` : '等待建立基准'
}

function monitoredState(state: TargetMultiplierState): TargetMultiplierState {
  return { ...state, groups: state.groups.filter((group) => group.monitored) }
}

function groupStatusLabel(group: GroupMultiplier): string {
  if (group.lastError) return '检测失败'
  return statusLabels[group.status]
}

interface PriceRow {
  id: string
  modelName: string
  billingMode: string
  range: string
  priceLabel: string
  value: string
  unit: string
  note?: string
}

function billingModeLabel(value: string): string {
  switch (value.trim().toLocaleLowerCase()) {
    case 'token':
    case 'per_token':
    case 'tokens':
      return '按 Token'
    case 'request':
    case 'per_request':
      return '按次'
    case 'image':
    case 'per_image':
      return '按图片'
    case 'tiered':
    case 'interval':
      return '阶梯计费'
    case 'dynamic':
      return '动态计费'
    default:
      return '其他计费'
  }
}

function intervalText(label: string, minTokens?: string, maxTokens?: string, condition?: string): string {
  // 优先展示服务端安全解析后的条件；图片档位没有 Token 边界时只显示渠道给出的名称。
  if (condition) return `${label}（${condition}）`
  if (!minTokens && !maxTokens) return label
  if (!minTokens) return `${label}（≤ ${maxTokens} tokens）`
  return maxTokens ? `${label}（${minTokens}–${maxTokens} tokens）` : `${label}（≥ ${minTokens} tokens）`
}

function priceRows(models: GroupPriceModel[]): PriceRow[] {
  return models.flatMap((model, modelIndex) => {
    const baseRows = model.prices.map((price, priceIndex) => ({
      id: `${modelIndex}-base-${price.key}-${priceIndex}`,
      modelName: model.name,
      billingMode: billingModeLabel(model.billingMode),
      range: '通用价格',
      priceLabel: price.label,
      value: price.value,
      unit: price.unit,
      note: model.note
    }))
    const intervalRows = (model.intervals ?? []).flatMap((interval, intervalIndex) => interval.prices.map((price, priceIndex) => ({
      id: `${modelIndex}-${intervalIndex}-${price.key}-${priceIndex}`,
      modelName: model.name,
      billingMode: billingModeLabel(model.billingMode),
      range: intervalText(interval.label, interval.minTokens, interval.maxTokens, interval.condition),
      priceLabel: price.label,
      value: price.value,
      unit: price.unit,
      note: model.note
    })))
    const rows = [...baseRows, ...intervalRows]
    if (rows.length) return rows
    // 动态计费模型可能只有渠道说明，没有可安全解析的固定价格，仍需保留模型信息。
    return [{
      id: `${modelIndex}-note`, modelName: model.name, billingMode: billingModeLabel(model.billingMode),
      range: '动态或未公开', priceLabel: '渠道说明', value: '—', unit: '', note: model.note
    }]
  })
}

function GroupPricePanel({ targetId, group, expanded }: { targetId: string; group: GroupMultiplier; expanded: boolean }) {
  const [search, setSearch] = useState('')
  const [pageSize, setPageSize] = useState(20)
  const [requestedPage, setRequestedPage] = useState(1)
  const headingId = `group-price-title-${encodeURIComponent(targetId)}-${encodeURIComponent(group.key)}`
  const query = useQuery({
    queryKey: ['group-prices', targetId, group.key],
    queryFn: () => api.groupPrices(targetId, group.key),
    enabled: expanded
  })

  useEffect(() => {
    setSearch('')
    setRequestedPage(1)
  }, [group.key])

  const filteredModels = useMemo(() => {
    const keyword = search.trim().toLocaleLowerCase()
    if (!keyword) return query.data?.models ?? []
    return (query.data?.models ?? []).filter((model) => {
      const searchable = [model.name, billingModeLabel(model.billingMode), model.note, ...model.prices.map((price) => `${price.label} ${price.unit}`), ...(model.intervals ?? []).map((interval) => interval.label)].filter(Boolean).join(' ').toLocaleLowerCase()
      return searchable.includes(keyword)
    })
  }, [query.data?.models, search])
  const totalPages = Math.max(1, Math.ceil(filteredModels.length / pageSize))
  const currentPage = Math.min(requestedPage, totalPages)
  const startIndex = (currentPage - 1) * pageSize
  const pagedModels = filteredModels.slice(startIndex, startIndex + pageSize)
  const rows = useMemo(() => priceRows(pagedModels), [pagedModels])
  const lastVisible = filteredModels.length ? Math.min(startIndex + pageSize, filteredModels.length) : 0

  if (query.isPending) return <LoadingView label={`正在读取 ${group.name} 的模型价格`} />
  if (query.isError) return <ErrorView message={query.error.message} onRetry={() => void query.refetch()} />
  if (!query.data) return null

  return (
    <section className="group-price-panel" aria-labelledby={headingId}>
      <div className="group-price-heading">
        <div><h3 id={headingId}>{query.data.groupName}模型价格</h3><p>分组倍率 {multiplierText(query.data.multiplier)} · 价格仅展示，不参与告警。</p></div>
        <label className="search-field group-price-search"><span className="sr-only">搜索模型或计费方式</span><Search aria-hidden="true" size={18} /><input type="search" value={search} onChange={(event) => { setSearch(event.target.value); setRequestedPage(1) }} placeholder="搜索模型或计费方式" /></label>
      </div>
      {query.data.notice ? <InlineMessage>{query.data.notice}</InlineMessage> : null}
      {filteredModels.length === 0 ? <EmptyState title="没有匹配的模型" description={query.data.models.length ? '请调整搜索词。' : '该分组当前没有可展示的模型价格。'} /> : rows.length === 0 ? <EmptyState title="暂无价格项目" description="渠道返回了模型信息，但暂时没有具体价格项目。" /> : (
        <div className="table-wrap group-price-table-wrap">
          <table className="group-price-table">
            <thead><tr><th scope="col">模型</th><th scope="col">计费方式</th><th scope="col">价格区间</th><th scope="col">价格项目</th><th scope="col">价格</th></tr></thead>
            <tbody>{rows.map((row) => <tr key={row.id}>
              <td data-label="模型"><strong>{row.modelName}</strong>{row.note ? <small>{row.note}</small> : null}</td>
              <td data-label="计费方式">{row.billingMode}</td>
              <td data-label="价格区间">{row.range}</td>
              <td data-label="价格项目">{row.priceLabel}</td>
              <td data-label="价格"><strong>{row.value}</strong> {row.unit}</td>
            </tr>)}</tbody>
          </table>
        </div>
      )}
      {filteredModels.length > 0 ? <nav className="account-pagination group-price-pagination" aria-label={`${group.name}模型价格分页`}>
        <p className="account-page-summary" aria-live="polite"><span>显示第 {startIndex + 1}–{lastVisible} 个模型，共 {filteredModels.length} 个</span></p>
        <label className="compact-field account-page-size"><span>每页模型</span><select value={pageSize} onChange={(event) => { setPageSize(Number(event.target.value)); setRequestedPage(1) }}><option value={20}>20 个/页</option><option value={50}>50 个/页</option><option value={100}>100 个/页</option></select></label>
        <div className="account-page-buttons">
          <button className="icon-button" type="button" aria-label="上一页模型" disabled={currentPage <= 1} onClick={() => setRequestedPage((page) => Math.max(1, page - 1))}><ChevronLeft aria-hidden="true" size={18} /></button>
          <span>第 {currentPage} / {totalPages} 页</span>
          <button className="icon-button" type="button" aria-label="下一页模型" disabled={currentPage >= totalPages} onClick={() => setRequestedPage((page) => Math.min(totalPages, page + 1))}><ChevronRight aria-hidden="true" size={18} /></button>
        </div>
      </nav> : null}
    </section>
  )
}

function MultiplierChannelDetails({ target, expanded, onToggle }: { target: Target; expanded: boolean; onToggle: (open: boolean) => void }) {
  const queryClient = useQueryClient()
  const [selectedGroupKey, setSelectedGroupKey] = useState('')
  const [successMessage, setSuccessMessage] = useState('')
  const query = useQuery({
    queryKey: ['group-multipliers', target.id],
    queryFn: () => api.multiplierState(target.id)
  })
  const groups = useMemo(() => (query.data?.groups ?? []).filter((group) => group.monitored), [query.data?.groups])

  useEffect(() => {
    if (groups.some((group) => group.key === selectedGroupKey)) return
    setSelectedGroupKey(groups[0]?.key ?? '')
  }, [groups, selectedGroupKey])

  const refreshMutation = useMutation({
    mutationFn: () => api.checkMultiplierGroups(target.id),
    onSuccess: (result) => {
      queryClient.setQueryData(['group-multipliers', target.id], monitoredState(result))
      void queryClient.invalidateQueries({ queryKey: ['group-prices', target.id] })
      void queryClient.invalidateQueries({ queryKey: ['alerts'] })
      setSuccessMessage('当前渠道倍率已经刷新。')
    }
  })
  const selectedGroup = groups.find((group) => group.key === selectedGroupKey)
  const summaryStatus = query.isPending
    ? { label: '正在读取', className: 'is-loading', icon: Clock3 }
    : query.isError
      ? { label: '读取失败', className: 'is-error', icon: AlertCircle }
      : groups.length === 0
        ? { label: '未配置', className: 'is-unconfigured', icon: CircleOff }
        : query.data?.lastError || groups.some((group) => group.lastError || group.status === 'missing')
          ? { label: '检测失败', className: 'is-error', icon: AlertCircle }
          : groups.some((group) => group.status === 'changed')
            ? { label: '有变化', className: 'is-changed', icon: BellRing }
            : groups.some((group) => group.status === 'unknown')
              ? { label: '待检测', className: 'is-loading', icon: Clock3 }
              : { label: '稳定', className: 'is-stable', icon: CheckCircle2 }
  const SummaryStatusIcon = summaryStatus.icon

  return (
    <details className="multiplier-channel-details" id={`multiplier-channel-${encodeURIComponent(target.id)}`} open={expanded} onToggle={(event) => onToggle(event.currentTarget.open)}>
      <summary>
        <span className="multiplier-channel-identity"><strong>{target.name}</strong><small>{targetKindLabels[target.kind]} · {target.enabled ? '定时检测中' : '已暂停'}</small></span>
        <span className={`multiplier-channel-stat multiplier-channel-health ${summaryStatus.className}`}><small>倍率状态</small><b><SummaryStatusIcon aria-hidden="true" size={16} />{summaryStatus.label}</b></span>
        <span className="multiplier-channel-stat"><small>已选分组</small><b>{groups.length} 个</b></span>
        <span className="multiplier-channel-stat multiplier-channel-checked"><small>最近检测</small><b>{query.data?.lastCheckedAt ? formatRelativeTime(query.data.lastCheckedAt) : '暂无记录'}</b></span>
        <ChevronDown className="multiplier-disclosure-icon" aria-hidden="true" size={20} />
      </summary>
      <div className="multiplier-channel-content">
        {query.isPending ? <LoadingView label="正在读取倍率监控" /> : null}
        {query.isError && !query.data ? <ErrorView message={query.error.message} onRetry={() => void query.refetch()} /> : null}
        {query.isError && query.data ? <InlineMessage tone="warning">倍率状态重新读取失败，当前显示的是上一次结果。<button className="button ghost compact" type="button" onClick={() => void query.refetch()}>重新读取</button></InlineMessage> : null}
        {query.data?.lastError ? <InlineMessage tone="warning"><AlertCircle aria-hidden="true" size={17} />最近一次倍率检测失败：{query.data.lastError}</InlineMessage> : null}
        {refreshMutation.isError ? <InlineMessage tone="danger">刷新失败：{refreshMutation.error.message}</InlineMessage> : null}
        {successMessage ? <InlineMessage tone="success">{successMessage}</InlineMessage> : null}

        {query.data ? <>
          <div className="multiplier-channel-toolbar">
            <span>{query.data.lastCheckedAt ? <>最近检测：<time dateTime={query.data.lastCheckedAt} title={formatDateTime(query.data.lastCheckedAt)}>{formatRelativeTime(query.data.lastCheckedAt)}</time></> : '尚未完成倍率检测'}</span>
            <div className="compact-actions"><Link className="button ghost compact" to={`/targets/${target.id}#multiplier-settings`}>配置分组</Link><button className="button secondary compact" type="button" disabled={!target.authConfigured || groups.length === 0 || refreshMutation.isPending} onClick={() => { setSuccessMessage(''); refreshMutation.mutate() }}><RefreshCw className={refreshMutation.isPending ? 'spin' : ''} aria-hidden="true" size={17} />{refreshMutation.isPending ? '刷新中' : '刷新倍率'}</button></div>
          </div>
          {groups.length === 0 ? <EmptyState title="尚未配置倍率监控" description="进入渠道详情自动检测分组，并选择需要监控的分组。" action={<Link className="button primary" to={`/targets/${target.id}#multiplier-settings`}>去配置分组</Link>} /> : <>
            <div className="multiplier-group-picker" role="group" aria-label={`${target.name}已监控分组`}>
              {groups.map((group) => {
                const displayStatus = group.lastError ? 'error' : group.status
                const StatusIcon = group.lastError ? AlertCircle : statusIcons[group.status]
                const selected = group.key === selectedGroupKey
                return <button key={group.key} className={selected ? 'multiplier-group-option selected' : 'multiplier-group-option'} type="button" aria-pressed={selected} onClick={() => setSelectedGroupKey(group.key)}>
                  <span><strong>{group.name}</strong><small>{group.description || `标识：${group.key}`}</small></span><span className={`multiplier-group-option-value state-${displayStatus}`}><b>{multiplierText(group.multiplier)}</b><small><StatusIcon aria-hidden="true" size={15} />{groupStatusLabel(group)}</small></span>
                </button>
              })}
            </div>
            {selectedGroup ? <GroupPricePanel targetId={target.id} group={selectedGroup} expanded={expanded} /> : null}
          </>}
        </> : null}
      </div>
    </details>
  )
}

export default function MultiplierPage() {
  const [params] = useSearchParams()
  const requestedTargetId = params.get('target') ?? ''
  const [expandedTargets, setExpandedTargets] = useState<Set<string>>(() => new Set(requestedTargetId ? [requestedTargetId] : []))
  const targetsQuery = useQuery({ queryKey: ['targets'], queryFn: api.targets })
  const eligibleTargets = useMemo(() => (targetsQuery.data ?? []).filter((target) => target.kind === 'new_api' || target.kind === 'sub2api'), [targetsQuery.data])

  useEffect(() => {
    if (!requestedTargetId || !eligibleTargets.some((target) => target.id === requestedTargetId)) return
    setExpandedTargets((current) => current.has(requestedTargetId) ? current : new Set([...current, requestedTargetId]))
    const frame = window.requestAnimationFrame(() => document.getElementById(`multiplier-channel-${encodeURIComponent(requestedTargetId)}`)?.scrollIntoView?.({ block: 'nearest' }))
    return () => window.cancelAnimationFrame(frame)
  }, [eligibleTargets, requestedTargetId])

  if (targetsQuery.isPending) return <LoadingView label="正在读取可监控渠道" />
  if (targetsQuery.isError) return <ErrorView message={targetsQuery.error.message} onRetry={() => void targetsQuery.refetch()} />

  return (
    <div className="page-stack multiplier-page">
      <PageHeader title="分组倍率" description="按渠道查看已经选择监控的分组、倍率状态和模型价格；价格仅供查看，不参与告警。" />
      {eligibleTargets.length === 0 ? <EmptyState title="还没有可监控的渠道" description="先添加 New API 或 Sub2API 渠道并配置登录信息。" action={<Link className="button primary" to="/targets/new">添加渠道</Link>} /> : (
        <section className="multiplier-channel-list" aria-label="渠道倍率列表">
          {eligibleTargets.map((target) => <MultiplierChannelDetails key={target.id} target={target} expanded={expandedTargets.has(target.id)} onToggle={(open) => setExpandedTargets((current) => {
            if (current.has(target.id) === open) return current
            const next = new Set(current)
            if (open) next.add(target.id)
            else next.delete(target.id)
            return next
          })} />)}
        </section>
      )}
    </div>
  )
}
