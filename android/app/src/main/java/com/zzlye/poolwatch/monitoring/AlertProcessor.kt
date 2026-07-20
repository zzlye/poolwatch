package com.zzlye.poolwatch.monitoring

import android.content.Context
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.network.AlertRecord

object AlertProcessor {
    /**
     * 第一次启用时只建立现有告警基线，之后仅提示未处理过的新告警。
     */
    @Synchronized
    fun process(context: Context, alerts: List<AlertRecord>): Int {
        val settings = AppSettings(context)
        val seen = SeenAlertStore(context)
        if (!settings.alertBaselineReady) {
            seen.markAll(alerts.map(AlertRecord::id))
            settings.alertBaselineReady = true
            return 0
        }

        var notified = 0
        alerts.sortedBy(AlertRecord::createdAt).forEach { alert ->
            if (!seen.contains(alert.id) && NotificationHelper.showAlert(context, alert)) {
                seen.mark(alert.id)
                notified++
            }
        }
        return notified
    }

    @Synchronized
    fun processRealtime(context: Context, alert: AlertRecord) {
        val settings = AppSettings(context)
        val seen = SeenAlertStore(context)
        if (!settings.alertBaselineReady) return
        if (!seen.contains(alert.id) && NotificationHelper.showAlert(context, alert)) {
            seen.mark(alert.id)
        }
    }
}
