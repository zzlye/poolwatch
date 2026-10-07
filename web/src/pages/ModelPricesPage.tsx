import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, BellRing, CheckCircle2, ChevronDown, CircleOff, Clock3, RefreshCw, Save, Search, Settings2 } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { targetKindLabels, type ModelPriceMonitor, type PricePoint, type Target } from '../types'

// 只对相同币种及计价单位比较涨跌，使用整数运算避免小数精度损失。
export function priceDirection(before: PricePoint | undefined, after: PricePoint): string {
  if (!before) return '新增'
  if (before.unit !== after.unit) return '单位变更'
  const parts = [before.value, after.value].map((v) => v.split('.'))
  const scale = Math.max(...parts.map((v) => (v[1] ?? '').length))
  const [oldValue, newValue] = parts.map(([whole, fraction = '']) => BigInt(whole + fraction.padEnd(scale, '0')))
  return newValue > oldValue ? '↑ 上涨' : newValue < oldValue ? '↓ 下调' : ''
}

// 表格内按计费档位归并价格，输入、输出和缓存并排显示，不再为单项价格铺满大卡片。
export function PriceValues({ prices, previous = [] }: { prices: PricePoint[]; previous?: PricePoint[] }) {
  const ranges = new Map<string, PricePoint[]>()
  for (const point of prices) ranges.set(point.range, [...(ranges.get(point.range) ?? []), point])
  if (!prices.length) return <span className="price-muted">未公开固定价格</span>
  return <div className="price-inline-ranges">{[...ranges].map(([range, items]) => <div className="price-inline-range" key={range}>
    {range !== '通用价格' ? <small className="price-range-label">{range}</small> : null}
    <div className="price-inline-items">{items.map((point) => {
      const before = previous.find((p) => p.key === point.key)
      const direction = previous.length ? priceDirection(before, point) : ''
      return <span className="price-inline-item" key={point.key}><span>{point.label}</span><strong>{point.value}</strong><small>{point.unit}</small>{direction ? <small className="price-difference">{direction}{before ? ` · 原 ${before.value} ${before.unit}` : ''}</small> : null}</span>
    })}</div>
  </div>)}{previous.filter((p) => !prices.some((v) => v.key === p.key)).map((p) => <small key={p.key}>已移除：{p.range} {p.label} · 原 {p.value} {p.unit}</small>)}</div>
}

function modelState(item?: ModelPriceMonitor) {
  if (!item) return { label: '未监控', className: 'is-unconfigured', icon: CircleOff }
  if (item.lastError || item.missing) return { label: '检测异常', className: 'is-error', icon: AlertCircle }
  if (!item.prices.length) return { label: '待检测', className: 'is-loading', icon: Clock3 }
  if (item.changedAt && item.changedAt === item.lastCheckedAt) return { label: '价格已变化', className: 'is-changed', icon: BellRing }
  return { label: '监控中', className: 'is-stable', icon: CheckCircle2 }
}

function PriceChannel({ target, expanded, onToggle }: { target: Target; expanded: boolean; onToggle: (open: boolean) => void }) {
  const client = useQueryClient()
  // 折叠摘要只读本地保存的状态；浏览及展开不会自动向上游请求模型价格。
  const state = useQuery({ queryKey: ['model-prices', target.id], queryFn: () => api.modelPrices(target.id) })
  const [editing, setEditing] = useState(false)
  const [discovered, setDiscovered] = useState<{ key: string; name: string }[]>([])
  const [requestedGroup, setRequestedGroup] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dirty, setDirty] = useState(false)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [message, setMessage] = useState('')
  const storedGroups = useMemo(() => {
    const map = new Map<string, { key: string; name: string }>()
    for (const item of state.data ?? []) map.set(item.groupKey, { key: item.groupKey, name: item.groupName || item.groupKey })
    return [...map.values()]
  }, [state.data])
  const groups = useMemo(() => {
    if (!editing) return storedGroups
    const map = new Map(discovered.map((g) => [g.key, g]))
    for (const group of storedGroups) if (!map.has(group.key)) map.set(group.key, group)
    return [...map.values()]
  }, [discovered, storedGroups, editing])
  const groupKey = groups.some((g) => g.key === requestedGroup) ? requestedGroup : groups[0]?.key ?? ''
  const catalog = useQuery({
    queryKey: ['model-price-catalog', target.id, groupKey],
    queryFn: () => api.modelPriceCatalog(target.id, groupKey),
    enabled: expanded && editing && Boolean(groupKey) && !dirty && target.authConfigured,
    staleTime: 60000
  })
  const monitored = (state.data ?? []).filter((item) => item.groupKey === groupKey)

  useEffect(() => {
    // 折叠或后台更新不覆盖尚未保存的选择，重新展开后继续编辑。
    if (!dirty) setSelected(new Set((state.data ?? []).filter((item) => item.groupKey === groupKey).map((item) => item.modelName)))
  }, [state.data, groupKey, dirty])
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  const updateState = (items: ModelPriceMonitor[]) => {
    client.setQueryData(['model-prices', target.id], items)
    void client.invalidateQueries({ queryKey: ['alerts'] })
    void client.invalidateQueries({ queryKey: ['model-price-catalog', target.id] })
  }
  const detect = useMutation({ mutationFn: () => api.discoverPriceGroups(target.id), onSuccess: (items) => { setDiscovered(items); setMessage(items.length ? `已检测到 ${items.length} 个分组。` : '当前账号没有可读取的分组。') } })
  const save = useMutation({ mutationFn: () => api.saveModelPrices(target.id, groupKey, [...selected]), onSuccess: (items) => { updateState(items); setDirty(false); setEditing(false); setSearch(''); setPage(1); setMessage(selected.size ? '模型价格监控已保存。' : '已取消当前分组的模型价格监控。') } })
  const check = useMutation({ mutationFn: () => api.checkModelPrices(target.id), onSuccess: (items) => { updateState(items); setMessage('当前渠道的已监控价格已刷新。') }, onError: () => { void client.invalidateQueries({ queryKey: ['model-prices', target.id] }) } })
  const busy = detect.isPending || save.isPending || check.isPending
  const error = detect.error ?? save.error ?? check.error
  const changeSelection = (values: Set<string>) => { setSelected(values); setDirty(true); setMessage(''); save.reset() }
  const switchEditing = (value: boolean) => { setEditing(value); setSearch(''); setPage(1); setMessage(''); detect.reset(); save.reset(); check.reset() }
  const candidates = editing ? [...(catalog.data?.models ?? [])] : []
  for (const item of monitored) if (!candidates.some((v) => v.name === item.modelName)) candidates.push({ name: item.modelName, prices: item.prices })
  const filtered = candidates.filter((v) => v.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize))
  const currentPage = Math.min(page, pages)
  const visible = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)
  const records = state.data ?? []
  const lastCheckedAt = records.reduce((latest, item) => item.lastCheckedAt > latest ? item.lastCheckedAt : latest, '')
  const summaryState = state.isPending ? { label: '正在读取', className: 'is-loading', icon: Clock3 }
    : state.isError ? { label: '读取失败', className: 'is-error', icon: AlertCircle }
      : records.some((r) => r.lastError || r.missing) ? { label: '检测异常', className: 'is-error', icon: AlertCircle }
        : records.some((r) => r.changedAt && r.changedAt === r.lastCheckedAt) ? { label: '有变化', className: 'is-changed', icon: BellRing }
          : records.some((r) => !r.prices.length) ? { label: '待检测', className: 'is-loading', icon: Clock3 }
            : records.length ? { label: '稳定', className: 'is-stable', icon: CheckCircle2 } : { label: '未配置', className: 'is-unconfigured', icon: CircleOff }
  const SummaryIcon = summaryState.icon

  return <details className="multiplier-channel-details price-channel-details" id={`price-channel-${encodeURIComponent(target.id)}`} open={expanded} onToggle={(event) => onToggle(event.currentTarget.open)}>
    <summary aria-label={`${target.name}模型价格`}>
      <span className="multiplier-channel-identity"><strong>{target.name}</strong><small>{targetKindLabels[target.kind]} · {target.enabled ? '定时检测中' : '已暂停'}{dirty ? ' · 选择未保存' : ''}</small></span>
      <span className={`multiplier-channel-stat multiplier-channel-health ${summaryState.className}`}><small>价格状态</small><b><SummaryIcon size={16} aria-hidden="true" />{summaryState.label}</b></span>
      <span className="multiplier-channel-stat"><small>监控范围</small><b>{storedGroups.length} 组 · {records.length} 模型</b></span>
      <span className="multiplier-channel-stat multiplier-channel-checked"><small>最近检测</small><b title={lastCheckedAt ? formatDateTime(lastCheckedAt) : undefined}>{lastCheckedAt ? formatRelativeTime(lastCheckedAt) : '暂无记录'}</b></span>
      <ChevronDown className="multiplier-disclosure-icon" size={20} aria-hidden="true" />
    </summary>
    <div className="multiplier-channel-content price-channel-content" hidden={!expanded}>
      {state.isPending ? <LoadingView /> : null}
      {state.isError ? <ErrorView message={state.error.message} onRetry={() => void state.refetch()} /> : null}
      {state.data && (expanded || editing) ? <>
        <div className="multiplier-channel-toolbar">
          <span>{editing ? '选择分组后勾选模型，保存后生效。' : '仅展示已监控模型，价格包含所选分组倍率。'}</span>
          <div className="compact-actions">{editing ? <>
            <button type="button" className="button secondary compact" disabled={busy || dirty || !target.authConfigured} onClick={() => { detect.reset(); setMessage(''); detect.mutate() }}><Search size={17} aria-hidden="true" />自动检测分组</button>
            <button type="button" className="button ghost compact" disabled={busy || dirty} onClick={() => switchEditing(false)}>返回监控列表</button>
          </> : <>
            <button type="button" className="button ghost compact" disabled={busy} onClick={() => switchEditing(true)}><Settings2 size={17} aria-hidden="true" />管理模型</button>
            <button type="button" className="button secondary compact" disabled={busy || !records.length || !target.authConfigured} onClick={() => { check.reset(); setMessage(''); check.mutate() }}><RefreshCw size={17} aria-hidden="true" className={check.isPending ? 'spin' : ''} />刷新价格</button>
          </>}</div>
        </div>
        {!target.enabled ? <InlineMessage tone="warning">渠道已暂停，后台价格检测也已暂停，仍可手动刷新。</InlineMessage> : null}
        {!target.authConfigured ? <InlineMessage tone="warning">请先<Link to={`/targets/${target.id}/edit`}>配置渠道登录信息</Link>。</InlineMessage> : null}
        {error ? <InlineMessage tone="danger">{error.message}</InlineMessage> : null}
        {message ? <InlineMessage tone="success">{message}</InlineMessage> : null}
        {dirty ? <InlineMessage tone="warning">选择尚未保存，收起渠道会保留本次编辑。<button type="button" className="button ghost compact" disabled={busy} onClick={() => setDirty(false)}>放弃本次选择</button></InlineMessage> : null}
        {groups.length ? <>
          <div className="price-group-tabs" role="group" aria-label={`${target.name}价格分组`}>{groups.map((g) => {
            const count = records.filter((r) => r.groupKey === g.key).length
            return <button type="button" className={`price-group-tab${g.key === groupKey ? ' selected' : ''}`} aria-label={`${g.name} ${count} 个模型`} aria-pressed={g.key === groupKey} disabled={busy || dirty} key={g.key} onClick={() => { setRequestedGroup(g.key); setSearch(''); setPage(1); setMessage(''); save.reset() }}><strong>{g.name}</strong><small>{count} 个模型</small></button>
          })}</div>
          <div className="price-list-toolbar">
            <label className="search-field"><Search size={17} aria-hidden="true" /><input type="search" aria-label="搜索价格模型" placeholder={editing ? '搜索可选模型' : '搜索已监控模型'} value={search} onChange={(e) => { setSearch(e.target.value); setPage(1) }} /></label>
            <span className="price-muted">{editing ? `已选 ${selected.size} 个 · ` : ''}共 {filtered.length} 个模型</span>
            {editing ? <button type="button" className="button ghost compact" disabled={busy || dirty || catalog.isFetching || !target.authConfigured} onClick={() => void catalog.refetch()}>重新读取模型</button> : null}
          </div>
          {editing && catalog.data?.notice ? <InlineMessage>{catalog.data.notice}</InlineMessage> : null}
          {editing && catalog.isError ? <ErrorView message={catalog.error.message} onRetry={() => void catalog.refetch()} /> : null}
          {editing && catalog.isPending && catalog.isFetching ? <LoadingView label="正在读取模型价格" /> : null}
          {editing ? <div className="price-selection-toolbar"><div className="compact-actions">
            <button type="button" className="button ghost compact" disabled={busy || catalog.isError || catalog.isPending} onClick={() => changeSelection(new Set([...selected, ...filtered.filter((v) => v.prices.length).map((v) => v.name)]))}>选择筛选结果</button>
            <button type="button" className="button ghost compact" disabled={busy} onClick={() => changeSelection(new Set())}>取消当前分组全部选择</button>
          </div><button type="button" className="button primary compact" disabled={busy || !dirty} onClick={() => save.mutate()}><Save size={17} aria-hidden="true" />保存价格监控</button></div> : null}
          {visible.length ? <div className="price-compact-table-wrap"><table className={`price-compact-table${editing ? ' is-editing' : ''}`} aria-label={`${target.name}模型价格列表`}>
            <thead><tr><th scope="col">{editing ? '选择 / 模型' : '模型'}</th><th scope="col">价格明细</th><th scope="col">状态 / 检测时间</th></tr></thead>
            <tbody>{visible.map((model) => {
              const saved = monitored.find((v) => v.modelName === model.name)
              const status = modelState(saved)
              const Icon = status.icon
              return <tr key={model.name}>
                <th scope="row">{editing ? <label className="price-model-checkbox"><input type="checkbox" aria-label={`监控 ${model.name}`} checked={selected.has(model.name)} disabled={busy || (!saved && (!model.prices.length || catalog.isError))} onChange={() => { const next = new Set(selected); if (next.has(model.name)) next.delete(model.name); else next.add(model.name); changeSelection(next) }} /><strong>{model.name}</strong></label> : <strong>{model.name}</strong>}</th>
                <td data-label="价格明细"><PriceValues prices={saved ? saved.prices : model.prices} previous={saved?.previous} /></td>
                <td data-label="状态"><span className={`price-model-state ${status.className}`}><Icon size={15} aria-hidden="true" />{status.label}</span>{saved?.lastError ? <small className="price-row-error">{saved.lastError}</small> : null}{saved?.lastCheckedAt ? <time dateTime={saved.lastCheckedAt} title={formatDateTime(saved.lastCheckedAt)}>{formatRelativeTime(saved.lastCheckedAt)}</time> : null}{saved?.changedAt ? <small>变化于 {formatDateTime(saved.changedAt)}</small> : null}</td>
              </tr>
            })}</tbody>
          </table></div> : !(editing && (catalog.isPending || catalog.isError)) ? <EmptyState title="没有匹配的模型" description="调整搜索词或切换分组。" /> : null}
          {filtered.length ? <nav className="price-pagination" aria-label={`${target.name}模型价格分页`}><label className="compact-field"><span>每页</span><select aria-label={`${target.name}每页模型`} value={pageSize} onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1) }}><option value={20}>20 个</option><option value={50}>50 个</option><option value={100}>100 个</option></select></label><span>第 {currentPage} / {pages} 页</span><div className="compact-actions"><button type="button" className="button ghost compact" disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>上一页</button><button type="button" className="button ghost compact" disabled={currentPage === pages} onClick={() => setPage(currentPage + 1)}>下一页</button></div></nav> : null}
        </> : <EmptyState title={editing ? '选择需要监控的分组' : '尚未配置模型价格监控'} description={editing ? '点击“自动检测分组”，读取当前渠道的分组和模型。' : '点击“管理模型”，添加需要关注的分组与模型。'} />}
      </> : null}
    </div>
  </details>
}

export default function ModelPricesPage() {
  const [params] = useSearchParams()
  const requestedTargetId = params.get('target') ?? ''
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(requestedTargetId ? [requestedTargetId] : []))
  const targets = useQuery({ queryKey: ['targets'], queryFn: api.targets })
  const supported = useMemo(() => (targets.data ?? []).filter((v) => v.kind === 'new_api' || v.kind === 'sub2api'), [targets.data])
  useEffect(() => {
    if (!requestedTargetId || !supported.some((v) => v.id === requestedTargetId)) return
    setExpanded((current) => current.has(requestedTargetId) ? current : new Set([...current, requestedTargetId]))
    const frame = window.requestAnimationFrame(() => document.getElementById(`price-channel-${encodeURIComponent(requestedTargetId)}`)?.scrollIntoView?.({ block: 'nearest' }))
    return () => window.cancelAnimationFrame(frame)
  }, [requestedTargetId, supported])
  return <div className="page-stack multiplier-page model-prices-page"><PageHeader title="模型价格" description="按渠道查看已监控分组与模型价格，价格变化时提醒。" />
    {targets.isPending ? <LoadingView /> : targets.isError ? <ErrorView message={targets.error.message} onRetry={() => void targets.refetch()} /> : supported.length ? <section className="multiplier-channel-list" aria-label="渠道模型价格列表">
      {supported.map((target) => <PriceChannel key={target.id} target={target} expanded={expanded.has(target.id)} onToggle={(open) => setExpanded((current) => {
        if (current.has(target.id) === open) return current
        const next = new Set(current)
        if (open) next.add(target.id)
        else next.delete(target.id)
        return next
      })} />)}
    </section> : <EmptyState title="还没有支持的渠道" description="先添加 New API 或 Sub2API 渠道，即可配置独立模型价格监控。" action={<Link className="button primary" to="/targets/new">添加渠道</Link>} />}
  </div>
}
