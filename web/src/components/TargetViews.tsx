import { ChevronRight, ExternalLink } from 'lucide-react'
import { Link } from 'react-router-dom'
import { formatMetric, formatRelativeTime, redactHost } from '../lib/format'
import type { Target } from '../types'
import { targetKindLabels } from '../types'
import { StatusPill } from './StatusPill'

function primaryMetric(target: Target) {
  return target.metrics.find((metric) => ['wallet_balance', 'subscription_balance', 'image_quota'].includes(metric.key)) ?? target.metrics[0]
}

function thresholdLabel(comparison: 'lte' | 'gte' | undefined, value: string, unit: string): string {
  return `告警 ${comparison === 'gte' ? '≥' : '≤'} ${value} ${unit}`
}

export function TargetTable({ targets }: { targets: Target[] }) {
  return (
    <div className="table-wrap desktop-table">
      <table>
        <thead>
          <tr>
            <th scope="col">渠道</th>
            <th scope="col">状态</th>
            <th scope="col">主要指标</th>
            <th scope="col">最近检测</th>
            <th scope="col"><span className="sr-only">操作</span></th>
          </tr>
        </thead>
        <tbody>
          {targets.map((target) => {
            const metric = primaryMetric(target)
            return (
              <tr key={target.id}>
                <td>
                  <Link className="target-name-link" to={`/targets/${target.id}`}><strong>{target.name}</strong><span>{targetKindLabels[target.kind]} · {redactHost(target.baseUrl)}</span></Link>
                </td>
                <td><StatusPill status={target.enabled ? target.status : 'disabled'} label={target.enabled ? target.statusText : '已停用'} /></td>
                <td>{metric ? <span className="metric-cell"><strong>{formatMetric(metric.value, metric.unit)}</strong>{metric.threshold !== undefined ? <small>{thresholdLabel(metric.comparison, metric.threshold, metric.unit)}</small> : null}</span> : '暂无指标'}</td>
                <td><span title={target.lastCheckedAt}>{formatRelativeTime(target.lastCheckedAt)}</span></td>
                <td className="row-action"><Link className="icon-button" to={`/targets/${target.id}`} aria-label={`查看 ${target.name}`}><ChevronRight aria-hidden="true" size={20} /></Link></td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

export function TargetCards({ targets }: { targets: Target[] }) {
  return (
    <div className="target-card-list mobile-list">
      {targets.map((target) => {
        const metric = primaryMetric(target)
        return (
          <article className="target-card" key={target.id}>
            <Link to={`/targets/${target.id}`} aria-label={`查看 ${target.name}`} className="card-link-overlay" />
            <div className="target-card-top"><div><strong>{target.name}</strong><span>{targetKindLabels[target.kind]}</span></div><StatusPill status={target.enabled ? target.status : 'disabled'} /></div>
            <div className="target-card-metric">{metric ? <><strong>{formatMetric(metric.value, metric.unit)}</strong><span>{metric.label}</span></> : <span>暂无指标</span>}</div>
            <div className="target-card-foot"><span>{formatRelativeTime(target.lastCheckedAt)}</span>{target.topupUrl ? <ExternalLink aria-hidden="true" size={17} /> : <ChevronRight aria-hidden="true" size={18} />}</div>
          </article>
        )
      })}
    </div>
  )
}
