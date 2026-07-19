import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute } from 'workbox-precaching'
import { NavigationRoute, registerRoute } from 'workbox-routing'

declare let self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: Array<{ url: string; revision?: string }>
}

cleanupOutdatedCaches()
precacheAndRoute(self.__WB_MANIFEST)

// 只为页面导航提供离线应用壳，所有 API 与事件流都保持网络直连且绝不缓存。
registerRoute(new NavigationRoute(createHandlerBoundToURL('/index.html'), {
  denylist: [/^\/api(?:\/|$)/]
}))

interface PushPayload {
  title?: string
  body?: string
  icon?: string
  badge?: string
  tag?: string
  url?: string
  alertId?: string
}

self.addEventListener('push', (event: PushEvent) => {
  let payload: PushPayload = {}
  try {
    payload = event.data?.json() as PushPayload ?? {}
  } catch {
    payload = { body: event.data?.text() }
  }
  const requestedUrl = payload.url || (payload.alertId ? `/alerts?focus=${encodeURIComponent(payload.alertId)}` : '/alerts')
  let safeUrl = '/alerts'
  try {
    const parsed = new URL(requestedUrl, self.location.origin)
    if (parsed.origin === self.location.origin) safeUrl = `${parsed.pathname}${parsed.search}${parsed.hash}`
  } catch {
    safeUrl = '/alerts'
  }

  event.waitUntil(self.registration.showNotification(payload.title || '号池监控告警', {
    body: payload.body || '有新的渠道状态需要处理。',
    icon: payload.icon || '/icon-192.png',
    badge: payload.badge || '/icon-192.png',
    tag: payload.tag || payload.alertId || 'pool-monitor-alert',
    data: { url: safeUrl }
  }))
})

self.addEventListener('notificationclick', (event: NotificationEvent) => {
  event.notification.close()
  const url = new URL(event.notification.data?.url || '/alerts', self.location.origin).href
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const client of windows) {
      if ('navigate' in client) await client.navigate(url)
      return client.focus()
    }
    return self.clients.openWindow(url)
  })())
})
