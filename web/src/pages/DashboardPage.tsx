import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, BellRing, CheckCircle2, Plus, RefreshCw, Smartphone, RadioTower } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { TargetCards, TargetTable } from '../components/TargetViews'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import type { Target } from '../types'

type SortKey = 'name' | 'status' | 'checked'

export default function DashboardPage() {
  const queryClient = useQueryClient()
  const [sortKey, setSortKey] = useState<SortKey>('status')
  const query = useQuery({ queryKey: ['dashboard'], queryFn: api.dashboard, refetchInterval: 60_000 })
  const checkMutation = useMutation({
    mutationFn: api.checkAll,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      await queryClient.invalidateQueries({ queryKey: ['targets'] })
    }
  })

  const sortedTargets = useMemo(() => {
    const values = query.data?.targets ? [...query.data.targets] : []
    const statusOrder: Record<Target['status'], number> = { error: 0, warning: 1, unknown: 2, healthy: 3, disabled: 4 }
    return values.sort((left, right) => {
      if (sortKey === 'name') return left.name.localeCompare(right.name, 'zh-CN')
      if (sortKey === 'checked') return (right.lastCheckedAt ?? '').localeCompare(left.lastCheckedAt ?? '')
      return statusOrder[left.status] - statusOrder[right.status]
    })
  }, [query.data?.targets, sortKey])

  if (query.isPending) return <LoadingView label="正在汇总渠道状态" />
  if (query.isError) return <ErrorView message={query.error.message} onRetry={() => void query.refetch()} />
  const data = query.data

  return (
    <div className="page-stack">
      <PageHeader
        title="监控总览"
        description={`最近更新 ${formatDateTime(data.lastUpdatedAt)}`}
        actions={<>
          <button className="button secondary" type="button" disabled={checkMutation.isPending} onClick={() => checkMutation.mutate()}><RefreshCw className={checkMutation.isPending ? 'spin' : ''} aria-hidden="true" size={18} />{checkMutation.isPending ? '正在检测' : '全部检测'}</button>
          <Link className="button primary" to="/targets/new"><Plus aria-hidden="true" size={18} />添加渠道</Link>
        </>}
      />
      {checkMutation.isSuccess ? <InlineMessage tone="success">检测任务已提交，状态会自动刷新。</InlineMessage> : null}
      {checkMutation.isError ? <InlineMessage tone="danger">{checkMutation.error.message}</InlineMessage> : null}

      <section className="summary-grid" aria-label="状态摘要">
        <article className="summary-card"><span className="summary-icon neutral"><RadioTower aria-hidden="true" /></span><div><small>监控渠道</small><strong>{data.summary.totalTargets}</strong><span>已配置渠道</span></div></article>
        <article className="summary-card"><span className="summary-icon success"><CheckCircle2 aria-hidden="true" /></span><div><small>运行正常</small><strong>{data.summary.healthyTargets}</strong><span>无需处理</span></div></article>
        <article className="summary-card"><span className="summary-icon warning"><AlertTriangle aria-hidden="true" /></span><div><small>需要关注</small><strong>{data.summary.warningTargets}</strong><span>余额或账号异常</span></div></article>
        <article className="summary-card"><span className="summary-icon danger"><BellRing aria-hidden="true" /></span><div><small>未处理告警</small><strong>{data.summary.openAlerts}</strong><span>{data.summary.pushDevices} 台推送设备</span></div></article>
      </section>

      <section className="content-section" aria-labelledby="target-overview-title">
        <div className="section-heading"><div><h2 id="target-overview-title">渠道状态</h2><p>优先显示异常和接近阈值的渠道。</p></div><label className="compact-field"><span>排序</span><select value={sortKey} onChange={(event) => setSortKey(event.target.value as SortKey)}><option value="status">按状态</option><option value="name">按名称</option><option value="checked">按检测时间</option></select></label></div>
        {sortedTargets.length ? <><TargetTable targets={sortedTargets} /><TargetCards targets={sortedTargets} /></> : <EmptyState title="还没有监控渠道" description="添加第一个渠道后，这里会显示余额和账号状态。" action={<Link className="button primary" to="/targets/new"><Plus aria-hidden="true" size={18} />添加渠道</Link>} />}
      </section>

      <section className="content-section" aria-labelledby="recent-alert-title">
        <div className="section-heading"><div><h2 id="recent-alert-title">最近告警</h2><p>同一问题只通知一次，恢复后会单独记录。</p></div><Link className="text-link" to="/alerts">查看全部</Link></div>
        {data.alerts.length ? (
          <div className="alert-list compact-alert-list">
            {data.alerts.map((alert) => (
              <Link to={`/alerts?focus=${alert.id}`} className={`alert-row severity-${alert.severity}`} key={alert.id}>
                <span className="alert-indicator">{alert.severity === 'critical' ? <BellRing aria-hidden="true" /> : alert.severity === 'warning' ? <AlertTriangle aria-hidden="true" /> : <CheckCircle2 aria-hidden="true" />}</span>
                <span className="alert-copy"><strong>{alert.title}</strong><span>{alert.targetName} · {alert.message}</span></span>
                <time dateTime={alert.createdAt}>{formatRelativeTime(alert.createdAt)}</time>
              </Link>
            ))}
          </div>
        ) : <EmptyState title="暂无告警" description="所有渠道都在设定范围内。" />}
      </section>

      <div className="mobile-device-summary"><Smartphone aria-hidden="true" size={18} />已连接 {data.summary.pushDevices} 台推送设备</div>
    </div>
  )
}
