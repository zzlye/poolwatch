const DEFAULT_TRUSTED_ORIGINS = [
  'https://jiance.zzlye.xyz',
  'http://127.0.0.1',
  'http://127.0.0.1:8080',
  'http://localhost',
  'http://localhost:8080'
]

initializePoolWatchBridge()

async function initializePoolWatchBridge() {
  // 内容脚本只在可信的号池监控页面建立桥接，不响应普通网页的消息。
  const stored = await chrome.storage.local.get('trustedOrigins')
  const storedOrigins = Array.isArray(stored.trustedOrigins) && stored.trustedOrigins.length
    ? stored.trustedOrigins
    : DEFAULT_TRUSTED_ORIGINS
  const normalizedOrigins = [...new Set(storedOrigins.map(normalizeTrustedOrigin).filter(Boolean))]
  const trustedOrigins = normalizedOrigins.length > 0 ? normalizedOrigins : DEFAULT_TRUSTED_ORIGINS
  const currentOrigin = window.location.origin
  if (!trustedOrigins.includes(currentOrigin)) return

  window.addEventListener('message', async (event) => {
    if (event.source !== window || event.origin !== currentOrigin) return
    const message = event.data
    if (!message || message.source !== 'poolwatch-page') return
    if (message.type === 'POOLWATCH_BROWSER_HELPER_PING') {
      announceReady(currentOrigin)
      return
    }
    if (message.type !== 'POOLWATCH_IMPORT_NEW_API') return
    try {
      const result = await chrome.runtime.sendMessage({
        type: 'POOLWATCH_IMPORT_NEW_API',
        attemptId: message.attemptId,
        serverOrigin: currentOrigin
      })
      window.postMessage({
        source: 'poolwatch-extension',
        type: 'POOLWATCH_IMPORT_RESULT',
        requestId: message.requestId,
        ...result
      }, currentOrigin)
    } catch {
      window.postMessage({
        source: 'poolwatch-extension',
        type: 'POOLWATCH_IMPORT_RESULT',
        requestId: message.requestId,
        ok: false,
        message: '浏览器助手连接中断，请刷新页面后重试。'
      }, currentOrigin)
    }
  })

  announceReady(currentOrigin)
}

function announceReady(origin) {
  window.postMessage({ source: 'poolwatch-extension', type: 'POOLWATCH_BROWSER_HELPER_READY' }, origin)
}

function normalizeOrigin(rawURL) {
  try {
    const parsed = new URL(String(rawURL || ''))
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.origin : ''
  } catch {
    return ''
  }
}

function normalizeTrustedOrigin(rawURL) {
  const origin = normalizeOrigin(rawURL)
  if (!origin) return ''
  const parsed = new URL(origin)
  const hostname = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, '')
  return parsed.protocol === 'https:' || hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1'
    ? origin
    : ''
}
