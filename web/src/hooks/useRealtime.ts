import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'

export function useRealtime(enabled: boolean): void {
  const queryClient = useQueryClient()

  useEffect(() => {
    if (!enabled || import.meta.env.VITE_USE_MOCKS === 'true') return undefined
    const source = new EventSource('/api/events', { withCredentials: true })

    // 事件只负责让对应缓存失效，具体数据仍通过普通接口读取并校验。
    const refreshTargets = () => {
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      void queryClient.invalidateQueries({ queryKey: ['targets'] })
      void queryClient.invalidateQueries({ queryKey: ['target'] })
      void queryClient.invalidateQueries({ queryKey: ['history'] })
    }
    const refreshAlerts = () => {
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      void queryClient.invalidateQueries({ queryKey: ['alerts'] })
    }
    const refreshTargetConfiguration = () => {
      refreshTargets()
      // 渠道配置变化可能关闭某个告警指标，告警页也要同步刷新。
      void queryClient.invalidateQueries({ queryKey: ['alerts'] })
    }
    source.addEventListener('snapshot', refreshTargets)
    source.addEventListener('target.updated', refreshTargetConfiguration)
    source.addEventListener('alert', refreshAlerts)
    source.addEventListener('settings.updated', () => void queryClient.invalidateQueries({ queryKey: ['settings'] }))

    return () => source.close()
  }, [enabled, queryClient])
}
