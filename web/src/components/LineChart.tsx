import { useId } from 'react'
import { formatDateTime, formatMetric } from '../lib/format'
import type { Snapshot } from '../types'

interface LineChartProps {
  snapshots: Snapshot[]
  threshold?: string
  label: string
  unit: string
}

export function LineChart({ snapshots, threshold, label, unit }: LineChartProps) {
  const titleId = useId()
  if (snapshots.length === 0) return <p className="chart-empty">还没有历史数据，完成首次检测后会显示趋势。</p>

  const width = 720
  const height = 240
  const padding = 28
  const values = snapshots.map((item) => Number(item.value)).filter(Number.isFinite)
  if (threshold && Number.isFinite(Number(threshold))) values.push(Number(threshold))
  const min = Math.min(...values)
  const max = Math.max(...values)
  const spread = Math.max(max - min, Math.abs(max) * 0.08, 1)
  const lower = Math.max(0, min - spread * 0.16)
  const upper = max + spread * 0.16

  const x = (index: number) => padding + (index / Math.max(1, snapshots.length - 1)) * (width - padding * 2)
  const y = (value: number) => height - padding - ((value - lower) / (upper - lower)) * (height - padding * 2)
  const points = snapshots.map((item, index) => `${x(index)},${y(Number(item.value))}`).join(' ')
  const thresholdY = threshold ? y(Number(threshold)) : undefined

  return (
    <div className="chart-wrap">
      <svg className="line-chart" viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={titleId}>
        <title id={titleId}>{label}趋势，共 {snapshots.length} 个数据点</title>
        {[0, 1, 2, 3].map((index) => {
          const gridY = padding + (index / 3) * (height - padding * 2)
          return <line key={index} x1={padding} x2={width - padding} y1={gridY} y2={gridY} className="chart-grid" />
        })}
        {thresholdY !== undefined ? (
          <g>
            <line x1={padding} x2={width - padding} y1={thresholdY} y2={thresholdY} className="chart-threshold" />
            <text x={width - padding} y={Math.max(14, thresholdY - 7)} textAnchor="end" className="chart-threshold-label">阈值 {threshold}</text>
          </g>
        ) : null}
        <polyline points={points} className="chart-line" />
        {snapshots.map((item, index) => (
          <circle key={item.id} cx={x(index)} cy={y(Number(item.value))} r="4" className="chart-point">
            <title>{formatDateTime(item.measuredAt)}：{formatMetric(item.value, unit)}</title>
          </circle>
        ))}
      </svg>
      <div className="chart-legend"><span className="legend-line" />{label}<span className="legend-threshold" />告警阈值</div>
    </div>
  )
}
