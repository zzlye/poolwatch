import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, Search } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { TargetCards, TargetTable } from '../components/TargetViews'
import { targetKindLabels, type TargetKind } from '../types'

export default function TargetsPage() {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState<'all' | TargetKind>('all')
  const query = useQuery({ queryKey: ['targets'], queryFn: api.targets })
  const refreshMutation = useMutation({
    mutationFn: api.checkAll,
    onSuccess: async () => {
      // 全部刷新请求完成后，同时更新渠道列表与总览中的状态数据。
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ])
    }
  })
  const filtered = useMemo(() => (query.data ?? []).filter((target) => {
    const matchesSearch = `${target.name} ${target.baseUrl}`.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())
    return matchesSearch && (kind === 'all' || target.kind === kind)
  }), [kind, query.data, search])

  if (query.isPending) return <LoadingView label="正在读取渠道" />
  if (query.isError) return <ErrorView message={query.error.message} onRetry={() => void query.refetch()} />

  return (
    <div className="page-stack">
      <PageHeader title="渠道管理" description="管理检测地址、登录方式、指标阈值和充值入口。" actions={<>
        <button className="button secondary" type="button" disabled={refreshMutation.isPending} onClick={() => refreshMutation.mutate()}><RefreshCw className={refreshMutation.isPending ? 'spin' : ''} aria-hidden="true" size={18} />{refreshMutation.isPending ? '正在刷新' : '全部刷新'}</button>
        <Link className="button primary" to="/targets/new"><Plus aria-hidden="true" size={18} />添加渠道</Link>
      </>} />
      {refreshMutation.isSuccess ? <InlineMessage tone="success">刷新任务已提交，渠道状态会自动更新。</InlineMessage> : null}
      {refreshMutation.isError ? <InlineMessage tone="danger">刷新失败：{refreshMutation.error.message}</InlineMessage> : null}
      <div className="filter-bar">
        <label className="search-field"><span className="sr-only">搜索渠道</span><Search aria-hidden="true" size={18} /><input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索名称或地址" /></label>
        <label className="compact-field"><span>类型</span><select value={kind} onChange={(event) => setKind(event.target.value as 'all' | TargetKind)}><option value="all">全部类型</option>{Object.entries(targetKindLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      </div>
      <section className="content-section" aria-label="渠道列表">
        {filtered.length ? <><TargetTable targets={filtered} /><TargetCards targets={filtered} /></> : <EmptyState title={query.data.length ? '没有匹配的渠道' : '还没有渠道'} description={query.data.length ? '请调整搜索词或类型筛选。' : '添加渠道后即可定时检测余额与账号状态。'} action={!query.data.length ? <Link className="button primary" to="/targets/new"><Plus aria-hidden="true" size={18} />添加渠道</Link> : undefined} />}
      </section>
    </div>
  )
}
