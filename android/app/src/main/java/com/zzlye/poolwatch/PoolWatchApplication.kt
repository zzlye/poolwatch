package com.zzlye.poolwatch

import android.app.Application
import android.app.ActivityManager
import android.os.Build
import android.webkit.WebView
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.monitoring.MonitoringScheduler
import com.zzlye.poolwatch.monitoring.NotificationHelper

class PoolWatchApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        if (isChannelAuthProcess()) {
            // 授权页使用独立 WebView 数据目录，清理渠道 Cookie 时不会影响主应用登录状态。
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                WebView.setDataDirectorySuffix("channel_auth")
            }
            return
        }
        NotificationHelper.createChannels(this)
        if (AppSettings(this).monitoringEnabled) {
            MonitoringScheduler.schedulePeriodic(this)
        }
    }

    private fun isChannelAuthProcess(): Boolean {
        val processName = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            getProcessName()
        } else {
            val manager = getSystemService(ActivityManager::class.java)
            manager.runningAppProcesses
                ?.firstOrNull { it.pid == android.os.Process.myPid() }
                ?.processName
                .orEmpty()
        }
        return processName.endsWith(":channel_auth")
    }
}
