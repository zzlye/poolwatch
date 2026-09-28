const DEFAULT_TRUSTED_ORIGINS = [
  'https://jiance.zzlye.xyz',
  'http://127.0.0.1',
  'http://127.0.0.1:8080',
  'http://localhost',
  'http://localhost:8080'
]
const BRIDGE_SCRIPT_ID = 'poolwatch-trusted-page-bridge'
const IMPORT_MESSAGE_KINDS = {
  POOLWATCH_IMPORT_NEW_API: 'new_api',
  POOLWATCH_IMPORT_SUB2_API: 'sub2api'
}
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
  const expectedKind = IMPORT_MESSAGE_KINDS[message?.type]
  if (!expectedKind) return false
  importTargetSession(message, sender, expectedKind)
    .then(sendResponse)
    .catch((error) => sendResponse({ ok: false, code: helperErrorCode(error), message: safeErrorMessage(error) }))
  return true
})

async function importTargetSession(message, sender, expectedKind) {
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
  if (taskPayload?.kind !== expectedKind || !taskPayload?.captureToken) throw new Error('网页登录任务与当前渠道类型不匹配。')

  const baseURL = normalizeHTTPURL(taskPayload.baseUrl)
  if (!baseURL) throw new Error('渠道地址格式无效。')
  const targetOrigin = new URL(baseURL).origin
  // 只查询当前填写来源的标签页，优先复用已打开的控制台，保留页面内的登录上下文。
  const matches = (await chrome.tabs.query({ url: targetOrigin + '/*' }))
    .filter((tab) => typeof tab.id === 'number' && normalizeOrigin(tab.url) === targetOrigin)
    .sort((left, right) => Number(right.active) - Number(left.active))
  const createdTab = matches.length === 0
  const targetTab = matches[0] || await chrome.tabs.create({ url: baseURL, active: false })
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

    const credential = expectedKind === 'sub2api'
      ? await readSub2APITokens(targetTab.id, baseURL)
      : await readNewAPICredential(targetTab.id, baseURL, !createdTab)
    if (!credential) {
      leaveTargetOpen = true
      await focusTab(targetTab)
      return { ok: false, code: 'login_required', message: '渠道站点已经打开，请完成登录后回到号池监控再次点击一键读取。' }
    }

    const captureURL = `${serverOrigin}/api/target-auth/native/${encodeURIComponent(attemptId)}/capture`
    const captureResponse = await fetch(captureURL, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Target-Auth-Token': String(taskPayload.captureToken)
      },
      body: JSON.stringify(credential)
    })
    const capturePayload = await readJSON(captureResponse)
    if (!captureResponse.ok) throw new Error(apiMessage(capturePayload, '导入登录状态失败。'))
    if (sender.tab?.id) await focusTab(sender.tab)
    return {
      ok: true,
      attemptId,
      message: expectedKind === 'sub2api'
        ? '已读取登录令牌，号池监控正在完成校验。'
        : '已读取登录会话，号池监控正在完成校验。'
    }
  } finally {
    if (createdTab && !leaveTargetOpen) await closeTabQuietly(targetTab.id)
  }
}

async function readNewAPICredential(tabId, baseURL, existingTab = false) {
  const selfResult = await readNewAPIUser(tabId, baseURL)
  if (!selfResult.ok || !selfResult.userId) {
    if (selfResult.code === 'login_required' && !existingTab) return null
    const descriptions = {
      login_required: '当前站点页面已打开，但旧式会话接口未授权。新版站点请使用管理访问令牌，不必重复登录。',
      modern_auth_required: '检测到新版登录会话：该站点使用短期令牌，旧式 Cookie 读取不适用。请使用站点管理访问令牌，不必重复登录。',
      upstream_error: '站点用户接口暂时不可用，请稍后重试；这不代表尚未登录。',
      access_denied: '站点用户接口拒绝访问，请检查授权或网页验证；这不代表尚未登录。',
      origin_changed: '站点页面已跳转到其他来源，请回到当前配置的渠道地址后重试。',
      invalid_response: '站点用户接口格式已经变化，请使用管理访问令牌或联系维护者。',
      network_error: '读取站点用户接口失败，请检查网络后重试。'
    }
    const code = Object.hasOwn(descriptions, selfResult.code) ? selfResult.code : 'invalid_response'
    throw Object.assign(new Error(descriptions[code]), { code })
  }

  const selfURL = new URL('/api/user/self', baseURL).toString()
  const stores = await chrome.cookies.getAllCookieStores()
  const store = stores.find((entry) => entry.tabIds.includes(tabId))
  const cookies = await chrome.cookies.getAll({ url: selfURL, ...(store ? { storeId: store.id } : {}) })
  const cookieHeader = cookies
    .filter((cookie) => cookie.name && cookie.value !== undefined)
    .sort((left, right) => (right.path?.length || 0) - (left.path?.length || 0) || left.name.localeCompare(right.name))
    .map((cookie) => `${cookie.name}=${cookie.value}`)
    .join('; ')
  if (!cookieHeader) throw Object.assign(new Error('站点已返回用户信息，但没有可导入的会话 Cookie。请使用管理访问令牌。'), { code: 'cookie_unavailable' })
  return { cookie: cookieHeader, userId: selfResult.userId }
}

async function readSub2APITokens(tabId, baseURL) {
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: 'MAIN',
    args: [baseURL],
    func: (targetBaseURL) => {
      try {
        if (window.location.origin !== new URL(targetBaseURL).origin) return null
        // 只读取 Sub2API 官方键及已知兼容键，不遍历站点存储中的其他内容。
        const storages = [window.localStorage, window.sessionStorage]
        const readFirst = (keys) => {
          for (const storage of storages) {
            for (const key of keys) {
              const value = String(storage.getItem(key) || '').trim()
              if (value) return value
            }
          }
          return ''
        }
        const accessToken = readFirst(['auth_token', 'access_token', 'accessToken'])
        const refreshToken = readFirst(['refresh_token', 'refreshToken'])
        return accessToken || refreshToken ? { accessToken, refreshToken } : null
      } catch {
        return null
      }
    }
  })
  const credential = results[0]?.result
  if (!credential || typeof credential !== 'object') return null
  const accessToken = String(credential.accessToken || '').trim()
  const refreshToken = String(credential.refreshToken || '').trim()
  return accessToken || refreshToken ? { accessToken, refreshToken } : null
}

async function readNewAPIUser(tabId, baseURL) {
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: 'MAIN',
    args: [baseURL],
    func: async (targetBaseURL) => {
      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 15000)
      try {
        if (window.location.origin !== new URL(targetBaseURL).origin) return { ok: false, status: 0, code: 'origin_changed' }
        // 新版站点把短期令牌保存在内存，只用公开会话标记识别协议，不提取或轮换它的续期凭据。
        const modernSession = document.cookie.split(';').some((part) => part.trim().startsWith('new_api_has_session='))
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
          redirect: 'error',
          signal: controller.signal,
          headers
        })
        if (response.status >= 500 || response.status === 429) return { ok: false, status: response.status, code: 'upstream_error' }
        if (!response.ok && modernSession) return { ok: false, status: response.status, code: 'modern_auth_required' }
        if (response.status === 401) return { ok: false, status: 401, code: 'login_required' }
        if (response.status === 403) return { ok: false, status: 403, code: 'access_denied' }
        const text = await response.text()
        let payload = null
        try {
          payload = JSON.parse(text)
        } catch {
          return { ok: false, status: response.status, code: 'invalid_response' }
        }
        if (!response.ok || payload?.success === false) return { ok: false, status: response.status, code: modernSession ? 'modern_auth_required' : 'invalid_response' }
        const data = payload && typeof payload === 'object' && payload.data && typeof payload.data === 'object'
          ? payload.data
          : payload
        const rawID = data?.id ?? data?.user_id
        const responseUserId = rawID === undefined || rawID === null ? '' : String(rawID).trim()
        const userId = /^\d+$/.test(responseUserId) && responseUserId !== '0' ? responseUserId : storedUserId
        const ok = /^\d+$/.test(userId) && userId !== '0'
        return { ok, status: response.status, userId: ok ? userId : '', code: ok ? undefined : 'invalid_response' }
      } catch {
        return { ok: false, status: 0, code: 'network_error' }
      } finally {
        clearTimeout(timeout)
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

function helperErrorCode(error) {
  // 只回传固定错误类别，不把站点响应或异常对象当作诊断详情发送。
  const codes = ['modern_auth_required', 'upstream_error', 'access_denied', 'origin_changed', 'invalid_response', 'network_error', 'cookie_unavailable']
  return codes.includes(error?.code) ? error.code : 'helper_error'
}
