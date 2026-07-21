import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, CheckCircle2, Clock3, Edit3, ExternalLink, LoaderCircle, RefreshCw, Trash2, X } from 'lucide-react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api/client'
import { AccountPoolView } from '../components/AccountPoolView'
import { CLIProxyAccountPoolView } from '../components/CLIProxyAccountPoolView'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { LineChart } from '../components/LineChart'
import { StatusPill } from '../components/StatusPill'
import { formatDateTime, formatMetric, formatRelativeTime } from '../lib/format'
import { metricLabels, targetKindLabels, type MetricKey, type MetricValue, type Target, type ThresholdComparison } from '../types'

function thresholdComparison(metric?: MetricValue): ThresholdComparison {
  // 历史渠道没有保存比较方向时，保持旧版“小于等于”判断。
  return metric?.comparison ?? 'lte'
}

function thresholdSymbol(comparison: ThresholdComparison): string {
  return comparison === 'gte' ? '≥' : '≤'
}

function historyThresholdText(value: string, threshold: string, comparison: ThresholdComparison): string {
  const reached = comparison === 'gte' ? Number(value) >= Number(threshold) : Number(value) <= Number(threshold)
  if (reached) return '达到告警条件'
  return comparison === 'gte' ? '低于阈值' : '高于阈值'
}

export default function TargetDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [selectedMetric, setSelectedMetric] = useState<MetricKey | ''>('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const targetQuery = useQuery({ queryKey: ['target', id], queryFn: () => api.target(id) })

  useEffect(() => {
    if (!selectedMetric && targetQuery.data?.metrics[0]) setSelectedMetric(targetQuery.data.metrics[0].key)
  }, [selectedMetric, targetQuery.data?.metrics])

  const historyQuery = useQuery({
    queryKey: ['history', id, selectedMetric],
    queryFn: () => api.history(id, selectedMetric),
    enabled: Boolean(selectedMetric)
  })
  const checkMutation = useMutation({
    mutationFn: () => api.checkTarget(id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['target', id] }),
        queryClient.invalidateQueries({ queryKey: ['history', id] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ])
    }
  })
  const quotaRefreshMutation = useMutation({
    mutationFn: (accountIds: string[]) => api.refreshTargetAccountQuotas(id, accountIds),
    onSuccess: (result) => {
      const refreshedAccounts = new Map(result.accounts.map((account) => [account.id, account]))
      queryClient.setQueryData<Target>(['target', id], (current) => {
        if (!current?.accounts) return current
        return {
          ...current,
          accounts: current.accounts.map((account) => {
            const refreshed = refreshedAccounts.get(account.id)
            if (!refreshed) return account
            // 额度刷新只合并额度相关结果，健康状态仍以常规渠道检测为准。
            return {
              ...account,
              ...refreshed,
              status: account.status,
              statusText: account.statusText,
              recoveryAt: account.recoveryAt,
              success: account.success,
              fail: account.fail
            }
          })
        }
      })
    }
  })
  const deleteMutation = useMutation({
    mutationFn: () => api.deleteTarget(id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ])
      navigate('/targets')
    }
  })

  const selectedDefinition = useMemo(() => targetQuery.data?.metrics.find((item) => item.key === selectedMetric), [selectedMetric, targetQuery.data?.metrics])
  if (targetQuery.isPending) return <LoadingView label="正在读取渠道详情" />
  if (targetQuery.isError) return <ErrorView message={targetQuery.error.message} onRetry={() => void targetQuery.refetch()} />
  const target = targetQuery.data

  return (
    <div className="page-stack">
      <PageHeader
        title={target.name}
        description={`${targetKindLabels[target.kind]} · ${target.baseUrl}`}
        actions={<>
          <button className="button secondary" type="button" disabled={checkMutation.isPending} onClick={() => checkMutation.mutate()}>{checkMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <RefreshCw aria-hidden="true" size={18} />}{checkMutation.isPending ? '检测中' : '立即检测'}</button>
          {target.topupUrl ? <a className="button secondary" href={target.topupUrl} target="_blank" rel="noopener noreferrer"><ExternalLink aria-hidden="true" size={18} />充值</a> : null}
          <Link className="button primary" to={`/targets/${id}/edit`}><Edit3 aria-hidden="true" size={18} />编辑</Link>
        </>}
      />
      <Link className="back-link" to="/targets"><ArrowLeft aria-hidden="true" size={17} />返回渠道列表</Link>
      {checkMutation.isSuccess ? <InlineMessage tone="success">检测已完成，最新结果已经更新。</InlineMessage> : null}
      {checkMutation.isError ? <InlineMessage tone="danger">{checkMutation.error.message}</InlineMessage> : null}

      <section className="target-hero" aria-label="渠道当前状态">
        <div><span>当前状态</span><StatusPill status={target.enabled ? target.status : 'disabled'} label={target.enabled ? target.statusText : '已停用'} /></div>
        <div><span>最近检测</span><strong>{formatRelativeTime(target.lastCheckedAt)}</strong><small>{formatDateTime(target.lastCheckedAt)}</small></div>
        <div><span>下次检测</span><strong>{formatRelativeTime(target.nextCheckAt)}</strong><small>每 {target.checkIntervalMinutes} 分钟</small></div>
        <div><span>认证信息</span><strong>{target.authConfigured ? '已配置' : '未配置'}</strong><small>秘密不会回传页面</small></div>
      </section>
      {target.lastError ? <InlineMessage tone="danger">最近错误：{target.lastError}</InlineMessage> : null}

      <section className="content-section" aria-labelledby="metric-title">
        <div className="section-heading"><div><h2 id="metric-title">当前指标</h2><p>每个指标按自己的单位与阈值判断。</p></div></div>
        <div className="metric-grid">
          {target.metrics.map((metric) => (
            <button key={metric.key} type="button" className={selectedMetric === metric.key ? 'metric-card selected' : 'metric-card'} onClick={() => setSelectedMetric(metric.key)} aria-pressed={selectedMetric === metric.key}>
              <span><strong>{metric.label}</strong><StatusPill status={metric.status} /></span>
              <b>{formatMetric(metric.value, metric.unit)}</b>
              <small>{metric.threshold !== undefined ? `告警条件 ${thresholdSymbol(thresholdComparison(metric))} ${metric.threshold} ${metric.unit}` : '仅记录状态，不设置额度告警'}</small>
            </button>
          ))}
        </div>
      </section>

      <section className="content-section" aria-labelledby="history-title">
        <div className="section-heading"><div><h2 id="history-title">历史趋势</h2><p>{selectedDefinition ? `${selectedDefinition.label} · ${selectedDefinition.unit}` : '选择一个指标查看趋势'}</p></div>{target.metrics.length > 1 ? <label className="compact-field"><span>指标</span><select value={selectedMetric} onChange={(event) => setSelectedMetric(event.target.value as MetricKey)}>{target.metrics.map((metric) => <option key={metric.key} value={metric.key}>{metric.label}</option>)}</select></label> : null}</div>
        {historyQuery.isPending ? <LoadingView label="正在读取历史" /> : historyQuery.isError ? <ErrorView message={historyQuery.error.message} onRetry={() => void historyQuery.refetch()} /> : historyQuery.data ? (
          <>
            <LineChart snapshots={historyQuery.data.snapshots} threshold={selectedDefinition?.threshold} comparison={thresholdComparison(selectedDefinition)} label={selectedDefinition?.label ?? metricLabels[selectedMetric as MetricKey]} unit={selectedDefinition?.unit ?? ''} />
            <details className="history-details"><summary>查看同数据表格</summary><div className="table-wrap"><table><thead><tr><th scope="col">检测时间</th><th scope="col">数值</th><th scope="col">与阈值关系</th></tr></thead><tbody>{historyQuery.data.snapshots.slice().reverse().map((snapshot) => <tr key={snapshot.id}><td>{formatDateTime(snapshot.measuredAt)}</td><td>{formatMetric(snapshot.value, snapshot.unit)}</td><td>{selectedDefinition?.threshold !== undefined ? historyThresholdText(snapshot.value, selectedDefinition.threshold, thresholdComparison(selectedDefinition)) : '未设阈值'}</td></tr>)}</tbody></table></div></details>
          </>
        ) : null}
      </section>

      {target.kind === 'chatgpt2api' ? (
        <section className="content-section" aria-labelledby="account-title">
          <div className="section-heading"><div><h2 id="account-title">号池账号状态</h2><p>仅显示脱敏邮箱、类型、状态、额度与恢复时间。</p></div></div>
          {target.accounts?.length ? <AccountPoolView accounts={target.accounts} /> : <EmptyState title="暂无账号明细" description="配置管理员密钥后才能读取只读脱敏明细。" />}
        </section>
      ) : null}

      {target.kind === 'cliproxyapi' ? (
        <section className="content-section" aria-labelledby="cliproxy-account-title">
          <div className="section-heading"><div><h2 id="cliproxy-account-title">CLIProxyAPI 账号状态</h2><p>只读显示账号、提供商、类型、状态、真实额度、调用统计与恢复时间。</p></div></div>
          {target.accounts?.length ? <CLIProxyAccountPoolView key={id} accounts={target.accounts} onRefreshQuota={quotaRefreshMutation.mutateAsync} /> : <EmptyState title="暂无账号明细" description="请确认管理密钥有效并完成一次检测。" />}
        </section>
      ) : null}

      <section className="danger-zone" aria-labelledby="danger-title"><div><h2 id="danger-title">删除渠道</h2><p>历史快照与关联告警也会停止更新。</p></div><button className="button danger" type="button" onClick={() => setConfirmDelete(true)}><Trash2 aria-hidden="true" size={18} />删除</button></section>

      {confirmDelete ? <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) setConfirmDelete(false) }}><div className="modal" role="dialog" aria-modal="true" aria-labelledby="delete-title"><button className="icon-button modal-close" type="button" aria-label="关闭" onClick={() => setConfirmDelete(false)}><X aria-hidden="true" /></button><div className="modal-icon danger"><Trash2 aria-hidden="true" /></div><h2 id="delete-title">确认删除“{target.name}”？</h2><p>此操作无法撤销，服务器会停止检测该渠道。</p>{deleteMutation.error ? <InlineMessage tone="danger">{deleteMutation.error.message}</InlineMessage> : null}<div className="modal-actions"><button className="button secondary" type="button" onClick={() => setConfirmDelete(false)}>取消</button><button className="button danger" type="button" disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate()}>{deleteMutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <Trash2 aria-hidden="true" size={18} />}{deleteMutation.isPending ? '正在删除' : '确认删除'}</button></div></div></div> : null}
    </div>
  )
}
