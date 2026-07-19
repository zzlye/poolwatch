import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, BellRing, Check, CheckCircle2, KeyRound, RefreshCw, ServerCrash } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import type { Alert, AlertType } from '../types'

const typeLabels: Record<AlertType, string> = { threshold: '额度不足', credential: '凭据失效', unreachable: '连接失败', recovered: '状态恢复' }
const typeIcons = { threshold: AlertTriangle, credential: KeyRound, unreachable: ServerCrash, recovered: CheckCircle2 }

export default function AlertsPage() {
  const queryClient = useQueryClient()
  const [params] = useSearchParams()
  const [status, setStatus] = useState<'all' | Alert['status']>('all')
  const [type, setType] = useState<'all' | AlertType>('all')
  const query = useQuery({ queryKey: ['alerts', status], queryFn: () => api.alerts(status) })
  const acknowledgeMutation = useMutation({
    mutationFn: api.acknowledgeAlert,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['alerts'] })
      await queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    }
  })
  const filtered = useMemo(() => (query.data ?? []).filter((alert) => type === 'all' || alert.type === type), [query.data, type])

  useEffect(() => {
    const focusedId = params.get('focus')
    if (focusedId && query.data) document.getElementById(`alert-${focusedId}`)?.scrollIntoView({ block: 'center' })
  }, [params, query.data])

  if (query.isPending) return <LoadingView label="正在读取告警" />
  if (query.isError) return <ErrorView message={query.error.message} onRetry={() => void query.refetch()} />

  return (
    <div className="page-stack">
      <PageHeader title="告警中心" description="额度、凭据、连接错误与恢复事件统一记录。" actions={<button className="button secondary" type="button" onClick={() => void query.refetch()}><RefreshCw aria-hidden="true" size={18} />刷新</button>} />
      {acknowledgeMutation.isError ? <InlineMessage tone="danger">{acknowledgeMutation.error.message}</InlineMessage> : null}
      <div className="filter-bar">
        <label className="compact-field"><span>处理状态</span><select value={status} onChange={(event) => setStatus(event.target.value as typeof status)}><option value="all">全部</option><option value="open">未处理</option><option value="acknowledged">已知晓</option><option value="resolved">已恢复</option></select></label>
        <label className="compact-field"><span>事件类型</span><select value={type} onChange={(event) => setType(event.target.value as typeof type)}><option value="all">全部类型</option>{Object.entries(typeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      </div>
      <section className="content-section" aria-label="告警列表">
        {filtered.length ? <div className="alert-list">{filtered.map((alert) => {
          const Icon = typeIcons[alert.type]
          const focused = params.get('focus') === alert.id
          return <article id={`alert-${alert.id}`} className={`alert-item severity-${alert.severity}${focused ? ' focused' : ''}`} key={alert.id}>
            <span className="alert-icon"><Icon aria-hidden="true" /></span>
            <div className="alert-main"><div className="alert-title-line"><span><strong>{alert.title}</strong><small>{typeLabels[alert.type]}</small></span><time dateTime={alert.createdAt} title={formatDateTime(alert.createdAt)}>{formatRelativeTime(alert.createdAt)}</time></div><p>{alert.message}</p><div className="alert-meta"><Link to={`/targets/${alert.targetId}`}>{alert.targetName}</Link><span>{alert.status === 'open' ? '未处理' : alert.status === 'acknowledged' ? '已知晓' : '已恢复'}</span>{alert.resolvedAt ? <span>恢复于 {formatDateTime(alert.resolvedAt)}</span> : null}</div></div>
            {alert.status === 'open' ? <button className="button secondary compact" type="button" disabled={acknowledgeMutation.isPending} onClick={() => acknowledgeMutation.mutate(alert.id)}><Check aria-hidden="true" size={17} />标记已知晓</button> : <span className="alert-state"><CheckCircle2 aria-hidden="true" size={17} />已记录</span>}
          </article>
        })}</div> : <EmptyState title="没有匹配的告警" description="当前筛选条件下没有事件。" />}
      </section>
      <div className="alert-rule-note"><BellRing aria-hidden="true" size={18} /><p><strong>通知规则</strong>额度首次达到阈值立即通知；凭据失效立即通知；连接连续失败三次才通知；恢复后发送一次恢复消息。</p></div>
    </div>
  )
}
