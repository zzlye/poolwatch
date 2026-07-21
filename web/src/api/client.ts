import { mockRequest } from './mock'
import type {
  AccountQuotaRefreshResult,
  Alert,
  BootstrapState,
  DashboardData,
  DetectTargetResult,
  HistoryResult,
  PushInfo,
  Settings,
  Target,
  TargetAuthAttempt,
  TargetDraft,
  TestConnectionResult,
  TotpSetup
} from '../types'

const useMocks = import.meta.env.VITE_USE_MOCKS === 'true'

export class ApiError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

function normalizePayload<T>(payload: unknown): T {
  if (payload && typeof payload === 'object' && 'data' in payload) {
    return (payload as { data: T }).data
  }
  return payload as T
}

export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  if (useMocks) return mockRequest<T>(path, init)

  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin'
  })

  if (response.status === 204) return undefined as T
  const contentType = response.headers.get('content-type') ?? ''
  const payload = contentType.includes('application/json') ? await response.json() : await response.text()

  if (!response.ok) {
    const message = typeof payload === 'object' && payload && 'message' in payload
      ? String((payload as { message: unknown }).message)
      : typeof payload === 'string' && payload
        ? payload
        : `请求失败（${response.status}）`
    throw new ApiError(message, response.status)
  }
  return normalizePayload<T>(payload)
}

const jsonBody = (value: unknown) => JSON.stringify(value)

export const api = {
  bootstrap: () => apiRequest<BootstrapState>('/api/bootstrap'),
  setup: (payload: { initializationToken: string; username: string; password: string }) =>
    apiRequest<{ ok: boolean }>('/api/setup', { method: 'POST', body: jsonBody(payload) }),
  login: (payload: { username: string; password: string; secondFactor?: string }) =>
    apiRequest<{ ok: boolean }>('/api/session', { method: 'POST', body: jsonBody(payload) }),
  logout: () => apiRequest<void>('/api/session', { method: 'DELETE' }),
  dashboard: () => apiRequest<DashboardData>('/api/dashboard'),
  targets: () => apiRequest<Target[]>('/api/targets'),
  detectTarget: (baseUrl: string) => apiRequest<DetectTargetResult>('/api/targets/detect', { method: 'POST', body: jsonBody({ baseUrl }) }),
  createTargetAuthAttempt: (payload: { kind: TargetDraft['kind']; baseUrl: string }) =>
    apiRequest<TargetAuthAttempt>('/api/target-auth/attempts', { method: 'POST', body: jsonBody(payload) }),
  targetAuthAttempt: (id: string) => apiRequest<TargetAuthAttempt>(`/api/target-auth/attempts/${encodeURIComponent(id)}`),
  cancelTargetAuthAttempt: (id: string) => apiRequest<void>(`/api/target-auth/attempts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  target: (id: string) => apiRequest<Target>(`/api/targets/${encodeURIComponent(id)}`),
  createTarget: (payload: TargetDraft) => apiRequest<Target>('/api/targets', { method: 'POST', body: jsonBody(payload) }),
  updateTarget: (id: string, payload: TargetDraft) => apiRequest<Target>(`/api/targets/${encodeURIComponent(id)}`, { method: 'PUT', body: jsonBody(payload) }),
  deleteTarget: (id: string) => apiRequest<void>(`/api/targets/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testTarget: (payload: TargetDraft) => apiRequest<TestConnectionResult>('/api/targets/test', { method: 'POST', body: jsonBody(payload) }),
  checkTarget: (id: string) => apiRequest<void>(`/api/targets/${encodeURIComponent(id)}/check`, { method: 'POST' }),
  refreshTargetAccountQuotas: (id: string, accountIds: string[]) => apiRequest<AccountQuotaRefreshResult>(`/api/targets/${encodeURIComponent(id)}/accounts/quota/refresh`, {
    method: 'POST',
    body: jsonBody({ accountIds })
  }),
  checkAll: () => apiRequest<void>('/api/checks', { method: 'POST' }),
  history: (id: string, metric?: string) => apiRequest<HistoryResult>(`/api/targets/${encodeURIComponent(id)}/history${metric ? `?metric=${encodeURIComponent(metric)}` : ''}`),
  alerts: (status = 'all') => apiRequest<Alert[]>(`/api/alerts?status=${encodeURIComponent(status)}`),
  acknowledgeAlert: (id: string) => apiRequest<Alert>(`/api/alerts/${encodeURIComponent(id)}`, { method: 'PATCH', body: jsonBody({ status: 'acknowledged' }) }),
  settings: () => apiRequest<Settings>('/api/settings'),
  updateSettings: (payload: Partial<Settings>) => apiRequest<Settings>('/api/settings', { method: 'PUT', body: jsonBody(payload) }),
  pushInfo: () => apiRequest<PushInfo>('/api/push'),
  subscribePush: (payload: PushSubscriptionJSON & { name: string }) => apiRequest<void>('/api/push/subscriptions', { method: 'POST', body: jsonBody(payload) }),
  removePushDevice: (id: string) => apiRequest<void>(`/api/push/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testPush: () => apiRequest<void>('/api/push/test', { method: 'POST' }),
  startTotp: () => apiRequest<TotpSetup>('/api/security/totp/start', { method: 'POST' }),
  confirmTotp: (code: string) => apiRequest<{ recoveryCodes: string[] }>('/api/security/totp/confirm', { method: 'POST', body: jsonBody({ code }) }),
  disableTotp: (code: string) => apiRequest<void>('/api/security/totp', { method: 'DELETE', body: jsonBody({ code }) })
}
