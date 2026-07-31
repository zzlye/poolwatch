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
    const refreshSettings = () => {
      void queryClient.invalidateQueries({ queryKey: ['settings'] })
      // 邮件配置也通过设置更新事件同步，确保其他设备上的清除和修改及时生效。
      void queryClient.invalidateQueries({ queryKey: ['email'] })
    }
    source.addEventListener('snapshot', refreshTargets)
    source.addEventListener('target.updated', refreshTargetConfiguration)
    source.addEventListener('alert', refreshAlerts)
    source.addEventListener('multiplier.updated', () => {
      void queryClient.invalidateQueries({ queryKey: ['group-multipliers'] })
      // 模型价格按当前分组倍率计算，倍率变化后不能继续展示旧缓存。
      void queryClient.invalidateQueries({ queryKey: ['group-prices'] })
      refreshAlerts()
    })
    source.addEventListener('settings.updated', refreshSettings)

    return () => source.close()
  }, [enabled, queryClient])
}
