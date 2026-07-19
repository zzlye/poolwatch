import { AlertTriangle, Ban, CheckCircle2, CircleHelp, XCircle } from 'lucide-react'
import type { TargetStatus } from '../types'

const statusMap: Record<TargetStatus, { label: string; icon: typeof CheckCircle2 }> = {
  healthy: { label: '正常', icon: CheckCircle2 },
  warning: { label: '需关注', icon: AlertTriangle },
  error: { label: '异常', icon: XCircle },
  disabled: { label: '已停用', icon: Ban },
  unknown: { label: '待检测', icon: CircleHelp }
}

export function StatusPill({ status, label }: { status: TargetStatus; label?: string }) {
  const definition = statusMap[status]
  const Icon = definition.icon
  return (
    <span className={`status-pill status-${status}`}>
      <Icon aria-hidden="true" size={14} />
      {label ?? definition.label}
    </span>
  )
}
