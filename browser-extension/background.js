const DEFAULT_TRUSTED_ORIGINS = [
  'https://jiance.zzlye.xyz',
  'http://127.0.0.1',
  'http://127.0.0.1:8080',
  'http://localhost',
  'http://localhost:8080'
]
const BRIDGE_SCRIPT_ID = 'poolwatch-trusted-page-bridge'
let bridgeSyncQueue = Promise.resolve()

chrome.runtime.onInstalled.addListener(async () => {
  const stored = await chrome.storage.local.get('trustedOrigins')
  if (!Array.isArray(stored.trustedOrigins) || stored.trustedOrigins.length === 0) {
    await chrome.storage.local.set({ trustedOrigins: DEFAULT_TRUSTED_ORIGINS })
  }
  await scheduleBridgeContentScriptSync()
})

chrome.runtime.onStartup.addListener(() => {
  void scheduleBridgeContentScriptSync()
})

chrome.storage.onChanged.addListener((changes, areaName) => {
  if (areaName === 'local' && changes.trustedOrigins) void scheduleBridgeContentScriptSync()
})

chrome.action.onClicked.addListener(async () => {
  const trustedOrigins = await loadTrustedOrigins()
  for (const origin of trustedOrigins) {
    const tabs = await chrome.tabs.query({ url: `${origin}/*` })
    const existing = tabs.find((tab) => normalizeTrustedOrigin(tab.url) === origin && typeof tab.id === 'number')
    if (existing?.id) {
      await chrome.tabs.update(existing.id, { active: true })
      if (typeof existing.windowId === 'number') await chrome.windows.update(existing.windowId, { focused: true })
      return
    }
  }
  await chrome.tabs.create({ url: `${trustedOrigins[0] || DEFAULT_TRUSTED_ORIGINS[0]}/` })
})

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message?.type !== 'POOLWATCH_IMPORT_NEW_API') return false
  importNewAPISession(message, sender)
    .then(sendResponse)
    .catch((error) => sendResponse({ ok: false, message: safeErrorMessage(error) }))
  return true
})

async function importNewAPISession(message, sender) {
  // 只接受可信号池监控页面发起的任务，渠道地址必须以服务器保存的任务内容为准。
  const serverOrigin = normalizeTrustedOrigin(message.serverOrigin)
  const senderOrigin = normalizeTrustedOrigin(sender.tab?.url)
  const trustedOrigins = await loadTrustedOrigins()
  if (!serverOrigin || senderOrigin !== serverOrigin || !trustedOrigins.includes(serverOrigin)) {
    throw new Error('当前号池监控地址尚未加入浏览器助手。')
  }
  const attemptId = String(message.attemptId || '').trim()
  if (!/^[A-Za-z0-9_-]{12,200}$/.test(attemptId)) throw new Error('网页登录任务格式无效。')

  const taskURL = `${serverOrigin}/api/target-auth/native/${encodeURIComponent(attemptId)}`
  const taskResponse = await fetch(taskURL, { cache: 'no-store' })
  const taskPayload = await readJSON(taskResponse)
  if (!taskResponse.ok) throw new Error(apiMessage(taskPayload, '读取网页登录任务失败。'))
  if (taskPayload?.kind !== 'new_api' || !taskPayload?.captureToken) throw new Error('网页登录任务与 New API 不匹配。')

  const baseURL = normalizeHTTPURL(taskPayload.baseUrl)
  if (!baseURL) throw new Error('渠道地址格式无效。')
  const targetOrigin = new URL(baseURL).origin
  // 直接打开任务中的单个地址并复用当前浏览器会话，不枚举其他标签页或渠道。
  const targetTab = await chrome.tabs.create({ url: baseURL, active: false })
  if (!targetTab?.id) throw new Error('打开渠道站点失败。')
  let leaveTargetOpen = false
  try {
    await waitForTabComplete(targetTab.id)
    const loadedTab = await chrome.tabs.get(targetTab.id)
    if (normalizeOrigin(loadedTab.url) !== targetOrigin) {
      leaveTargetOpen = true
      await focusTab(loadedTab)
      return { ok: false, code: 'login_required', message: '请在打开的页面完成登录，返回渠道站点后再点击一键读取。' }
    }

    const selfResult = await readNewAPIUser(targetTab.id, baseURL)
    if (!selfResult.ok || !selfResult.userId) {
      leaveTargetOpen = true
      await focusTab(targetTab)
      return { ok: false, code: 'login_required', message: '渠道站点已经打开，请完成登录后回到号池监控再次点击一键读取。' }
    }

    const selfURL = new URL('/api/user/self', baseURL).toString()
    const cookies = await chrome.cookies.getAll({ url: selfURL })
    const cookieHeader = cookies
      .filter((cookie) => cookie.name && cookie.value !== undefined)
      .sort((left, right) => (right.path?.length || 0) - (left.path?.length || 0) || left.name.localeCompare(right.name))
      .map((cookie) => `${cookie.name}=${cookie.value}`)
      .join('; ')
    if (!cookieHeader) {
      leaveTargetOpen = true
      await focusTab(targetTab)
      return { ok: false, code: 'login_required', message: '当前站点尚未读取到登录会话，请登录后再次点击一键读取。' }
    }

    const captureURL = `${serverOrigin}/api/target-auth/native/${encodeURIComponent(attemptId)}/capture`
    const captureResponse = await fetch(captureURL, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Target-Auth-Token': String(taskPayload.captureToken)
      },
      body: JSON.stringify({ cookie: cookieHeader, userId: selfResult.userId })
    })
    const capturePayload = await readJSON(captureResponse)
    if (!captureResponse.ok) throw new Error(apiMessage(capturePayload, '导入登录会话失败。'))
    if (sender.tab?.id) await focusTab(sender.tab)
    return { ok: true, attemptId, message: '已读取登录会话，号池监控正在完成校验。' }
  } finally {
    if (!leaveTargetOpen) await closeTabQuietly(targetTab.id)
  }
}

async function readNewAPIUser(tabId, baseURL) {
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: 'MAIN',
    args: [baseURL],
    func: async (targetBaseURL) => {
      try {
        // New API 的不同版本可能把用户编号放在不同的本地存储键中，只读取固定白名单。
        const readStoredUserId = () => {
          const objectKeys = ['user', 'new-api-user', 'user_info', 'userInfo']
          for (const key of objectKeys) {
            const raw = localStorage.getItem(key)
            if (!raw) continue
            try {
              const value = JSON.parse(raw)
              const candidate = value?.id ?? value?.user_id ?? value?.userId ?? value?.data?.id ?? value?.data?.user_id
              const parsed = candidate === undefined || candidate === null ? '' : String(candidate).trim()
              if (/^\d+$/.test(parsed) && parsed !== '0') return parsed
            } catch {
              // 单个键不是 JSON 时继续检查其他白名单键。
            }
          }
          for (const key of ['user_id', 'userId']) {
            const parsed = String(localStorage.getItem(key) || '').trim()
            if (/^\d+$/.test(parsed) && parsed !== '0') return parsed
          }
          return ''
        }

        const endpoint = new URL('/api/user/self', targetBaseURL).toString()
        const storedUserId = readStoredUserId()
        const headers = /** @type {Record<string, string>} */ ({ Accept: 'application/json' })
        if (storedUserId) headers['New-Api-User'] = storedUserId
        const response = await fetch(endpoint, {
          cache: 'no-store',
          credentials: 'include',
          headers
        })
        const text = await response.text()
        let payload = null
        try {
          payload = JSON.parse(text)
        } catch {
          return { ok: false, status: response.status }
        }
        const data = payload && typeof payload === 'object' && payload.data && typeof payload.data === 'object'
          ? payload.data
          : payload
        const rawID = data?.id ?? data?.user_id
        const responseUserId = rawID === undefined || rawID === null ? '' : String(rawID).trim()
        const userId = /^\d+$/.test(responseUserId) && responseUserId !== '0' ? responseUserId : storedUserId
        return { ok: response.ok && /^\d+$/.test(userId) && userId !== '0', status: response.status, userId }
      } catch {
        return { ok: false, status: 0 }
      }
    }
  })
  return results[0]?.result || { ok: false, status: 0 }
}

async function waitForTabComplete(tabId) {
  await new Promise((resolve, reject) => {
    let settled = false
    const cleanup = () => {
      clearTimeout(timeout)
      chrome.tabs.onUpdated.removeListener(updatedListener)
      chrome.tabs.onRemoved.removeListener(removedListener)
    }
    const finish = () => {
      if (settled) return
      settled = true
      cleanup()
      resolve()
    }
    const fail = (error) => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }
    const timeout = setTimeout(() => {
      fail(new Error('渠道站点加载超时。'))
    }, 20000)
    const updatedListener = (updatedTabId, changeInfo) => {
      if (updatedTabId !== tabId || changeInfo.status !== 'complete') return
      finish()
    }
    const removedListener = (removedTabId) => {
      if (removedTabId === tabId) fail(new Error('渠道站点页面已经关闭。'))
    }
    // 先挂载监听再复查状态，避免页面恰好在两步之间完成加载而漏掉事件。
    chrome.tabs.onUpdated.addListener(updatedListener)
    chrome.tabs.onRemoved.addListener(removedListener)
    chrome.tabs.get(tabId).then((tab) => {
      if (tab.status === 'complete') finish()
    }).catch(() => fail(new Error('读取渠道站点页面失败。')))
  })
}

async function focusTab(tab) {
  if (typeof tab.id === 'number') await chrome.tabs.update(tab.id, { active: true })
  if (typeof tab.windowId === 'number') await chrome.windows.update(tab.windowId, { focused: true })
}

async function closeTabQuietly(tabId) {
  try {
    await chrome.tabs.remove(tabId)
  } catch {
    // 用户可能已经手工关闭临时标签页，此时无需再次处理。
  }
}

async function loadTrustedOrigins() {
  const stored = await chrome.storage.local.get('trustedOrigins')
  const values = Array.isArray(stored.trustedOrigins) && stored.trustedOrigins.length > 0
    ? stored.trustedOrigins
    : DEFAULT_TRUSTED_ORIGINS
  const normalized = [...new Set(values.map(normalizeTrustedOrigin).filter(Boolean))]
  return normalized.length > 0 ? normalized : [...DEFAULT_TRUSTED_ORIGINS]
}

function scheduleBridgeContentScriptSync() {
  bridgeSyncQueue = bridgeSyncQueue.then(syncBridgeContentScript, syncBridgeContentScript)
  return bridgeSyncQueue
}

async function syncBridgeContentScript() {
  const matches = (await loadTrustedOrigins()).map((origin) => `${origin}/*`)
  const registered = await chrome.scripting.getRegisteredContentScripts({ ids: [BRIDGE_SCRIPT_ID] })
  const definition = {
    id: BRIDGE_SCRIPT_ID,
    matches,
    js: ['content.js'],
    runAt: 'document_start',
    persistAcrossSessions: true
  }
  // 动态脚本只注入可信的号池监控页面，不会在普通浏览页面运行。
  if (registered.length > 0) {
    await chrome.scripting.updateContentScripts([definition])
    return
  }
  await chrome.scripting.registerContentScripts([definition])
}

function normalizeOrigin(rawURL) {
  try {
    const parsed = new URL(String(rawURL || ''))
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return ''
    return parsed.origin
  } catch {
    return ''
  }
}

function normalizeTrustedOrigin(rawURL) {
  const origin = normalizeOrigin(rawURL)
  if (!origin) return ''
  const parsed = new URL(origin)
  if (parsed.protocol === 'https:' || isLoopbackHost(parsed.hostname)) return origin
  return ''
}

function isLoopbackHost(hostname) {
  const normalized = String(hostname || '').toLowerCase().replace(/^\[|\]$/g, '')
  return normalized === 'localhost' || normalized === '127.0.0.1' || normalized === '::1'
}

function normalizeHTTPURL(rawURL) {
  try {
    const parsed = new URL(String(rawURL || ''))
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return ''
    parsed.username = ''
    parsed.password = ''
    parsed.hash = ''
    parsed.search = ''
    return parsed.toString()
  } catch {
    return ''
  }
}

async function readJSON(response) {
  const text = await response.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

function apiMessage(payload, fallback) {
  return typeof payload?.message === 'string' && payload.message.trim() ? payload.message.trim() : fallback
}

function safeErrorMessage(error) {
  const message = error instanceof Error ? error.message : String(error || '')
  return message && message.length <= 200 ? message : '浏览器助手执行失败，请刷新页面后重试。'
}
