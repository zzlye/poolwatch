import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, BellRing, CheckCircle2, RefreshCw, Save, Search } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { formatDateTime } from '../lib/format'
import type { ModelPriceMonitor, PricePoint, Target } from '../types'

// 只对相同币种及计价单位比较涨跌，使用整数运算避免小数精度损失。
export function priceDirection(before: PricePoint | undefined, after: PricePoint): string {
  if (!before) return '新增'
  if (before.unit !== after.unit) return '单位变更'
  const parts = [before.value, after.value].map((v) => v.split('.'))
  const scale = Math.max(...parts.map((v) => (v[1] ?? '').length))
  const [oldValue, newValue] = parts.map(([whole, fraction = '']) => BigInt(whole + fraction.padEnd(scale, '0')))
  return newValue > oldValue ? '↑ 上涨' : newValue < oldValue ? '↓ 下调' : ''
}

export function PriceValues({ prices, previous = [] }: { prices: PricePoint[]; previous?: PricePoint[] }) {
  const ranges = new Map<string, PricePoint[]>()
  for (const point of prices) ranges.set(point.range, [...(ranges.get(point.range) ?? []), point])
  if (!prices.length) return <span>未公开固定价格</span>
  return <div className="price-ranges">{[...ranges].map(([range, items]) => <div className="price-range" key={range}>
    <small>{range}</small><div className="group-price-items">{items.map((point) => {
      const before = previous.find((p) => p.key === point.key)
      const direction = previous.length ? priceDirection(before, point) : ''
      return <div className="group-price-item" key={point.key}><span>{point.label}</span><strong>{point.value}</strong><small>{point.unit}</small>{direction ? <small>{direction}{before ? ` · 原 ${before.value} ${before.unit}` : ''}</small> : null}</div>
    })}</div>
  </div>)}{previous.filter((p) => !prices.some((v) => v.key === p.key)).map((p) => <small key={p.key}>已移除：{p.range} {p.label} · 原 {p.value} {p.unit}</small>)}</div>
}

function PriceChannel({ target, onDirty }: { target: Target; onDirty: (value: boolean) => void }) {
  const client = useQueryClient()
  const state = useQuery({ queryKey: ['model-prices', target.id], queryFn: () => api.modelPrices(target.id) })
  const [discovered, setDiscovered] = useState<{ key: string; name: string }[]>([])
  const [requestedGroup, setRequestedGroup] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dirty, setDirty] = useState(false)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [message, setMessage] = useState('')
  const groups = useMemo(() => {
    const map = new Map(discovered.map((g) => [g.key, g]))
    for (const item of state.data ?? []) if (!map.has(item.groupKey)) map.set(item.groupKey, { key: item.groupKey, name: item.groupName })
    return [...map.values()]
  }, [discovered, state.data])
  const groupKey = requestedGroup || groups[0]?.key || ''
  const catalog = useQuery({ queryKey: ['model-price-catalog', target.id, groupKey], queryFn: () => api.modelPriceCatalog(target.id, groupKey), enabled: Boolean(groupKey) && !dirty, staleTime: 60000 })
  const monitored = (state.data ?? []).filter((item) => item.groupKey === groupKey)

  useEffect(() => { onDirty(dirty) }, [dirty, onDirty])
  useEffect(() => {
    // 后台事件不会覆盖尚未保存的勾选；已保存配置始终以服务端为准。
    if (!dirty) setSelected(new Set((state.data ?? []).filter((item) => item.groupKey === groupKey).map((item) => item.modelName)))
  }, [state.data, groupKey, dirty])

  const updateState = (items: ModelPriceMonitor[]) => {
    client.setQueryData(['model-prices', target.id], items)
    void client.invalidateQueries({ queryKey: ['alerts'] })
    void client.invalidateQueries({ queryKey: ['model-price-catalog', target.id] })
  }
  const detect = useMutation({ mutationFn: () => api.discoverPriceGroups(target.id), onSuccess: (items) => { setDiscovered(items); setMessage(items.length ? `已检测到 ${items.length} 个分组。` : '当前账号没有可读取的分组。') } })
  const save = useMutation({ mutationFn: () => api.saveModelPrices(target.id, groupKey, [...selected]), onSuccess: (items) => { updateState(items); setDirty(false); setMessage(selected.size ? '模型价格监控已保存，新选模型已建立价格基准。' : '已取消当前分组的模型价格监控，倍率监控不受影响。') } })
  const check = useMutation({ mutationFn: () => api.checkModelPrices(target.id), onSuccess: (items) => { updateState(items); setMessage('当前渠道的已选模型价格已刷新。') }, onError: () => { void client.invalidateQueries({ queryKey: ['model-prices', target.id] }) } })
  const busy = detect.isPending || save.isPending || check.isPending
  const error = detect.error ?? save.error ?? check.error
  const changeSelection = (values: Set<string>) => { setSelected(values); setDirty(true); setMessage(''); save.reset() }
  const candidates = [...(catalog.data?.models ?? [])]
  for (const item of monitored) if (!candidates.some((v) => v.name === item.modelName)) candidates.push({ name: item.modelName, prices: item.prices })
  const filtered = candidates.filter((v) => v.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize))
  const currentPage = Math.min(page, pages)
  const visible = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)

  if (state.isPending) return <LoadingView />
  if (state.isError) return <ErrorView message={state.error.message} onRetry={() => void state.refetch()} />
  return <section className="content-section price-channel">
    <div className="section-heading-row"><div><h2>{target.name}</h2><p>已监控 {state.data.length} 个模型 · 按渠道检测周期更新</p></div><div className="compact-actions">
      <button type="button" className="button secondary" disabled={busy || dirty || !target.authConfigured} onClick={() => { detect.reset(); setMessage(''); detect.mutate() }}><Search size={17} aria-hidden="true" />自动检测分组</button>
      <button type="button" className="button secondary" disabled={busy || dirty || !state.data.length} onClick={() => { check.reset(); setMessage(''); check.mutate() }}><RefreshCw size={17} aria-hidden="true" />刷新已监控价格</button>
    </div></div>
    {!target.enabled ? <InlineMessage tone="warning">渠道已暂停，后台价格检测也已暂停，仍可手动刷新。</InlineMessage> : null}
    {!target.authConfigured ? <InlineMessage tone="warning">请先<Link to={`/targets/${target.id}/edit`}>配置渠道登录信息</Link>。</InlineMessage> : null}
    {error ? <InlineMessage tone="danger">{error.message}</InlineMessage> : null}
    {message ? <InlineMessage tone="success">{message}</InlineMessage> : null}
    {dirty ? <InlineMessage tone="warning">模型选择尚未保存。<button type="button" className="button ghost compact" disabled={busy} onClick={() => setDirty(false)}>放弃本次选择</button></InlineMessage> : null}
    {groups.length ? <>
      <div className="price-controls"><label className="compact-field"><span>价格分组</span><select aria-label="价格分组" value={groupKey} disabled={busy || dirty} onChange={(e) => { setRequestedGroup(e.target.value); setPage(1); setSearch(''); setMessage(''); save.reset() }}>{groups.map((g) => <option key={g.key} value={g.key}>{g.name}</option>)}</select></label>
        <label className="search-field"><Search size={17} aria-hidden="true" /><input type="search" aria-label="搜索价格模型" placeholder="搜索模型名称" value={search} onChange={(e) => { setSearch(e.target.value); setPage(1) }} /></label>
        <button type="button" className="button ghost" disabled={busy || dirty || catalog.isFetching} onClick={() => void catalog.refetch()}>重新读取模型</button>
      </div>
      <p>展示所选分组的实际价格（包含倍率），输入、输出与缓存合并展示；首次保存不通知，后续价格或计价单位变化通知。</p>
      {catalog.data?.notice ? <InlineMessage>{catalog.data.notice}</InlineMessage> : null}
      {catalog.isError ? <ErrorView message={catalog.error.message} onRetry={() => void catalog.refetch()} /> : null}
      {catalog.isPending ? <LoadingView label="正在读取模型价格" /> : null}
      <div className="section-heading-row"><p>当前分组已选 {selected.size} 个，共 {filtered.length} 个匹配模型</p><div className="compact-actions"><button type="button" className="button ghost compact" disabled={busy || catalog.isError || catalog.isPending} onClick={() => changeSelection(new Set([...selected, ...filtered.filter((v) => v.prices.length).map((v) => v.name)]))}>选择筛选结果</button><button type="button" className="button ghost compact" disabled={busy} onClick={() => changeSelection(new Set())}>取消当前分组全部选择</button></div></div>
      <div className="price-model-list">{visible.map((model) => {
        const saved = monitored.find((v) => v.modelName === model.name)
        const warning = saved?.lastError
        const changed = saved?.changedAt && saved.changedAt === saved.lastCheckedAt
        const Icon = warning ? AlertCircle : changed ? BellRing : CheckCircle2
        return <article className="price-model-row" key={model.name}>
          <div className="price-model-heading"><label><input type="checkbox" aria-label={`监控 ${model.name}`} checked={selected.has(model.name)} disabled={busy || (!saved && (!model.prices.length || catalog.isError))} onChange={() => { const next = new Set(selected); if (next.has(model.name)) next.delete(model.name); else next.add(model.name); changeSelection(next) }} /><strong>{model.name}</strong></label><span className={warning ? 'price-state-warning' : ''}><Icon size={16} aria-hidden="true" />{warning ? '检测异常' : saved ? changed ? '价格已变化' : saved.prices.length ? '监控中' : '待建立基准' : '未监控'}</span></div>
          <PriceValues prices={saved ? saved.prices : model.prices} previous={saved?.previous} />
          {warning ? <InlineMessage tone="warning">{warning}</InlineMessage> : null}
          {saved?.lastCheckedAt ? <small>最近检测：{formatDateTime(saved.lastCheckedAt)}{saved.changedAt ? ` · 上次变化：${formatDateTime(saved.changedAt)}` : ''}</small> : null}
        </article>
      })}</div>
      {!visible.length && !catalog.isPending && !catalog.isError ? <EmptyState title="没有匹配的模型" description="调整搜索词或更换分组后重试。" /> : null}
      {filtered.length ? <nav className="account-pagination" aria-label="模型价格分页"><label className="compact-field"><span>每页模型</span><select value={pageSize} onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1) }}><option value={20}>20 个</option><option value={50}>50 个</option><option value={100}>100 个</option></select></label><div className="compact-actions"><button type="button" className="button ghost" disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>上一页</button><span>第 {currentPage} / {pages} 页</span><button type="button" className="button ghost" disabled={currentPage === pages} onClick={() => setPage(currentPage + 1)}>下一页</button></div></nav> : null}
      <div className="price-save"><button type="button" className="button primary" disabled={busy || !dirty} onClick={() => save.mutate()}><Save size={17} aria-hidden="true" />保存价格监控</button></div>
    </> : <EmptyState title="添加模型价格监控" description="点击“自动检测分组”，选择分组后勾选需要监控的模型。无需先开启倍率监控。" />}
  </section>
}

export default function ModelPricesPage() {
  const [params, setParams] = useSearchParams()
  const [dirty, setDirty] = useState(false)
  const targets = useQuery({ queryKey: ['targets'], queryFn: api.targets })
  const supported = targets.data?.filter((v) => v.kind === 'new_api' || v.kind === 'sub2api') ?? []
  const target = supported.find((v) => v.id === params.get('target')) ?? supported[0]
  return <div className="page-stack"><PageHeader title="模型价格" description="独立监控模型价格变化，与分组倍率监控互不影响。" />
    {targets.isPending ? <LoadingView /> : targets.isError ? <ErrorView message={targets.error.message} onRetry={() => void targets.refetch()} /> : target ? <>
      <label className="content-section compact-field price-target"><span>选择已有渠道</span><select aria-label="选择已有渠道" value={target.id} disabled={dirty} onChange={(e) => setParams({ target: e.target.value })}>{supported.map((v) => <option key={v.id} value={v.id}>{v.name}{v.enabled ? '' : '（已暂停）'}</option>)}</select></label>
      <PriceChannel key={target.id} target={target} onDirty={setDirty} />
    </> : <EmptyState title="还没有支持的渠道" description="先添加 New API 或 Sub2API 渠道，即可配置独立模型价格监控。" action={<Link className="button primary" to="/targets/new">添加渠道</Link>} />}
  </div>
}
