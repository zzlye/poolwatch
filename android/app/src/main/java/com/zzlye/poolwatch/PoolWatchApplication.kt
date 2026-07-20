package com.zzlye.poolwatch

import android.app.Application
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.monitoring.MonitoringScheduler
import com.zzlye.poolwatch.monitoring.NotificationHelper

class PoolWatchApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        NotificationHelper.createChannels(this)
        if (AppSettings(this).monitoringEnabled) {
            MonitoringScheduler.schedulePeriodic(this)
        }
    }
}
