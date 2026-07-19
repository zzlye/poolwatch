import { useState } from 'react'
import { RefreshCw, X } from 'lucide-react'
import { registerSW } from 'virtual:pwa-register'

export function UpdatePrompt() {
  const [needRefresh, setNeedRefresh] = useState(false)
  const [offlineReady, setOfflineReady] = useState(false)
  const [updateServiceWorker] = useState(() => registerSW({
    immediate: true,
    onNeedRefresh: () => setNeedRefresh(true),
    onOfflineReady: () => setOfflineReady(true)
  }))

  if (!needRefresh && !offlineReady) return null
  return (
    <div className="update-toast" role="status" aria-live="polite">
      <span>{needRefresh ? '新版本已准备好' : '应用已可离线打开'}</span>
      {needRefresh ? (
        <button className="button compact" type="button" onClick={() => void updateServiceWorker(true)}>
          <RefreshCw aria-hidden="true" size={16} />更新
        </button>
      ) : null}
      <button className="icon-button compact-icon" type="button" aria-label="关闭提示" onClick={() => { setNeedRefresh(false); setOfflineReady(false) }}>
        <X aria-hidden="true" size={18} />
      </button>
    </div>
  )
}
