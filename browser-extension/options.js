const DEFAULT_TRUSTED_ORIGINS = [
  'https://jiance.zzlye.xyz',
  'http://127.0.0.1',
  'http://127.0.0.1:8080',
  'http://localhost',
  'http://localhost:8080'
]

const originsField = document.querySelector('#origins')
const statusField = document.querySelector('#status')

loadOptions()
document.querySelector('#save').addEventListener('click', saveOptions)

async function loadOptions() {
  const stored = await chrome.storage.local.get('trustedOrigins')
  const values = Array.isArray(stored.trustedOrigins) && stored.trustedOrigins.length
    ? stored.trustedOrigins
    : DEFAULT_TRUSTED_ORIGINS
  originsField.value = values.join('\n')
}

async function saveOptions() {
  // 仅保存规范化后的来源，路径、查询参数和片段不会进入可信列表。
  const lines = originsField.value
    .split(/\r?\n/)
    .map((value) => value.trim())
    .filter(Boolean)
  const values = lines.map(normalizeTrustedOrigin)
  if (values.some((value) => !value)) {
    statusField.textContent = '公网地址必须使用 HTTPS；HTTP 只支持本机地址。'
    return
  }
  const unique = [...new Set(values)]
  if (unique.length === 0) {
    statusField.textContent = '请至少填写一个有效的 HTTP 或 HTTPS 地址。'
    return
  }
  await chrome.storage.local.set({ trustedOrigins: unique })
  originsField.value = unique.join('\n')
  statusField.textContent = '设置已保存，请刷新号池监控页面。第一行地址会作为助手图标的默认打开页面。'
}

function normalizeTrustedOrigin(rawURL) {
  try {
    const parsed = new URL(rawURL.trim())
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return ''
    const hostname = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, '')
    if (parsed.protocol !== 'https:' && hostname !== 'localhost' && hostname !== '127.0.0.1' && hostname !== '::1') return ''
    return parsed.origin
  } catch {
    return ''
  }
}
