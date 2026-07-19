import type { ReactNode } from 'react'
import { AlertCircle, Inbox, LoaderCircle, RefreshCw } from 'lucide-react'

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div>
        <h1 tabIndex={-1}>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </header>
  )
}

export function LoadingView({ label = '正在读取数据' }: { label?: string }) {
  return (
    <div className="state-view" role="status">
      <LoaderCircle className="spin" aria-hidden="true" />
      <p>{label}</p>
    </div>
  )
}

export function ErrorView({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="state-view state-error" role="alert">
      <AlertCircle aria-hidden="true" />
      <strong>暂时无法加载</strong>
      <p>{message}</p>
      {onRetry ? (
        <button className="button secondary" type="button" onClick={onRetry}>
          <RefreshCw aria-hidden="true" size={18} />重试
        </button>
      ) : null}
    </div>
  )
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return (
    <div className="state-view">
      <Inbox aria-hidden="true" />
      <strong>{title}</strong>
      <p>{description}</p>
      {action}
    </div>
  )
}

export function InlineMessage({ tone = 'info', children }: { tone?: 'info' | 'success' | 'warning' | 'danger'; children: ReactNode }) {
  return <div className={`inline-message tone-${tone}`} role={tone === 'danger' ? 'alert' : 'status'}>{children}</div>
}
