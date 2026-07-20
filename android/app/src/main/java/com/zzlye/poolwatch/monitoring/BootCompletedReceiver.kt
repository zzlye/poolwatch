package com.zzlye.poolwatch.monitoring

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.zzlye.poolwatch.config.AppSettings

class BootCompletedReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) {
            return
        }
        if (!AppSettings(context).monitoringEnabled) return

        // 启动广播中只恢复调度，实时服务启动受限时仍由十五分钟检查兜底。
        MonitoringScheduler.schedulePeriodic(context)
        MonitoringScheduler.enqueueImmediate(context)
        MonitoringScheduler.startRealtimeService(context)
    }
}
