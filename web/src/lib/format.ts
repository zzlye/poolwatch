export function formatDateTime(value?: string): string {
  if (!value) return '尚未检测'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间未知'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  }).format(date)
}

export function formatRelativeTime(value?: string): string {
  if (!value) return '尚未检测'
  const time = new Date(value).getTime()
  if (Number.isNaN(time)) return '时间未知'
  const minutes = Math.round((time - Date.now()) / 60_000)
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })
  if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute')
  const hours = Math.round(minutes / 60)
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour')
  return formatter.format(Math.round(hours / 24), 'day')
}

export function formatMetric(value: string, unit: string): string {
  const number = Number(value)
  if (!Number.isFinite(number)) return `${value} ${unit}`
  const fractionDigits = Math.abs(number) >= 1000 ? 0 : 2
  return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: fractionDigits }).format(number)} ${unit}`
}

export function redactHost(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}
