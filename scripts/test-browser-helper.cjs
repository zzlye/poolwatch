// 测试使用合成站点和浏览器接口，不读取真实浏览器或任何账号凭据。
const { test } = require('node:test')
const assert = require('node:assert/strict')
const vm = require('node:vm')
const fs = require('node:fs')
const path = require('node:path')
const source = fs.readFileSync(path.join(__dirname, '../browser-extension/background.js'), 'utf8')

function harness(options = {}) {
  const actions = { created: [], closed: [], focused: [], queries: [], fetches: [], cookies: [], captures: [] }
  const tab = { id: 7, windowId: 1, status: 'complete', active: true, url: 'https://channel.example/console', ...options.tab }
  const listener = { addListener() {}, removeListener() {} }
  const response = (status, body) => ({ ok: status >= 200 && status < 300, status, text: async () => typeof body === 'string' ? body : JSON.stringify(body) })
  const page = {
    window: { location: { origin: options.origin || 'https://channel.example' } },
    localStorage: { getItem: key => (options.storage || { user: '{"id":42}' })[key] || null },
    sessionStorage: { getItem: () => null }, document: { cookie: options.modern ? 'new_api_has_session=1' : '' },
    URL, AbortController, setTimeout, clearTimeout,
    fetch: async (url, init) => {
      actions.fetches.push({ url, init })
      return response(options.status || 200, options.body ?? { success: true, data: { id: 42, quota: 500 } })
    }
  }
  page.window.localStorage = page.localStorage
  page.window.sessionStorage = page.sessionStorage
  const chrome = {
    runtime: { onInstalled: listener, onStartup: listener, onMessage: listener },
    storage: { onChanged: listener, local: { get: async () => ({ trustedOrigins: ['https://monitor.example'] }) } },
    action: { onClicked: listener }, windows: { update: async () => {} },
    tabs: {
      query: async query => { actions.queries.push(query); return options.existing === false ? [] : [tab] },
      create: async input => { actions.created.push(input); return { ...tab, id: 8, url: input.url } },
      get: async id => ({ ...tab, id }), update: async id => { actions.focused.push(id) },
      remove: async id => { actions.closed.push(id) }, onUpdated: listener, onRemoved: listener
    },
    cookies: {
      getAllCookieStores: async () => [{ id: 'test-store', tabIds: [7, 8] }],
      getAll: async query => { actions.cookies.push(query); return options.noCookies ? [] : [{ name: 'session', value: 'synthetic', path: '/' }] }
    },
    scripting: { executeScript: async input => [{ result: await vm.runInNewContext('(' + input.func.toString() + ')(...args)', { ...page, args: input.args }) }] }
  }
  const context = vm.createContext({
    chrome, URL, setTimeout, clearTimeout, console,
    fetch: async (url, init) => {
      if (url.endsWith('/capture')) { actions.captures.push(JSON.parse(init.body)); return response(200, { status: 'ready' }) }
      return response(200, { kind: 'new_api', captureToken: 'synthetic-capture', baseUrl: 'https://channel.example' })
    }
  })
  vm.runInContext(source, context)
  return { context, actions, read: () => context.readNewAPIUser(7, 'https://channel.example'),
    import: () => context.importTargetSession({ serverOrigin: 'https://monitor.example', attemptId: 'auth_synthetic_attempt_123' }, { tab: { id: 1, url: 'https://monitor.example/', windowId: 1 } }, 'new_api') }
}

test('新版短期会话应明确标记，不反复要求登录', async () => {
  const h = harness({ modern: true, storage: {}, status: 401, body: { success: false, message: 'PRIVATE_ERROR' } })
  assert.equal((await h.read()).code, 'modern_auth_required')
  await assert.rejects(h.import(), error => error.code === 'modern_auth_required' && !error.message.includes('PRIVATE_ERROR'))
  assert.equal(h.actions.created.length, 0)
  assert.equal(h.actions.focused.length, 0)
  assert.equal(h.actions.closed.length, 0)
  assert.equal(h.actions.captures.length, 0)
})

test('已打开控制台但旧式接口返回 401 时不再跳转登录页', async () => {
  const h = harness({ storage: {}, status: 401, body: { success: false } })
  await assert.rejects(h.import(), error => error.code === 'login_required' && error.message.includes('管理访问令牌'))
  assert.equal(h.actions.created.length, 0)
  assert.equal(h.actions.focused.length, 0)
})

test('复用当前站点已登录标签页，不新建或关闭用户页面', async () => {
  const h = harness()
  const result = await h.import()
  assert.equal(result.ok, true)
  assert.equal(h.actions.created.length, 0)
  assert.equal(h.actions.closed.length, 0)
  assert.equal(h.actions.queries[0].url, 'https://channel.example/*')
  assert.equal(h.actions.cookies[0].storeId, 'test-store')
  assert.equal(h.actions.captures[0].userId, '42')
})

test('接口五百错误不伪装成未登录或导致跳转', async () => {
  const h = harness({ status: 502, body: '<html>PRIVATE_ERROR</html>' })
  assert.equal((await h.read()).code, 'upstream_error')
  await assert.rejects(h.import(), error => error.code === 'upstream_error')
  assert.equal(h.actions.focused.length, 0)
})

test('跨来源导航后禁止读取存储及发送用户请求', async () => {
  const h = harness({ origin: 'https://other.example' })
  const result = await h.read()
  assert.equal(result.code, 'origin_changed')
  assert.equal(h.actions.fetches.length, 0)
})

test('业务失败响应不能仅凭本地用户编号判为成功', async () => {
  const h = harness({ body: { success: false, message: 'PRIVATE_ERROR' } })
  assert.equal((await h.read()).ok, false)
})

test('没有已有标签页时只关闭自己创建的临时标签页', async () => {
  const h = harness({ existing: false })
  assert.equal((await h.import()).ok, true)
  assert.equal(h.actions.created.length, 1)
  assert.deepEqual(h.actions.closed, [8])
})

test('真正未登录时保留站点页供用户完成登录', async () => {
  const h = harness({ existing: false, storage: {}, status: 401, body: { success: false } })
  const result = await h.import()
  assert.equal(result.code, 'login_required')
  assert.deepEqual(h.actions.closed, [])
  assert.deepEqual(h.actions.focused, [8])
})

test('只读取固定用户字段并且禁止跨主机重定向', async () => {
  const h = harness()
  await h.read()
  assert.equal(h.actions.fetches[0].init.redirect, 'error')
  assert.equal(h.actions.fetches[0].init.headers['New-Api-User'], '42')
})
