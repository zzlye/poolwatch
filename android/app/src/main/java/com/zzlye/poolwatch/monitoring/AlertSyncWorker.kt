package com.zzlye.poolwatch.monitoring

import android.content.Context
import androidx.work.Worker
import androidx.work.WorkerParameters
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.config.MonitorStatus
import com.zzlye.poolwatch.network.ApiException
import com.zzlye.poolwatch.network.PoolWatchApi

class AlertSyncWorker(
    appContext: Context,
    workerParameters: WorkerParameters,
) : Worker(appContext, workerParameters) {
    override fun doWork(): Result {
        val settings = AppSettings(applicationContext)
        if (!settings.monitoringEnabled) return Result.success()
        return try {
            val alerts = PoolWatchApi(settings).fetchAlerts()
            AlertProcessor.process(applicationContext, alerts)
            settings.authenticationWarningShown = false
            NotificationHelper.clearAuthenticationRequired(applicationContext)
            Result.success()
        } catch (error: ApiException) {
            if (error.statusCode == 401) {
                settings.monitorStatus = MonitorStatus.LOGIN_REQUIRED.value
                MonitoringScheduler.reportAuthenticationInvalid(applicationContext)
                if (!settings.authenticationWarningShown &&
                    NotificationHelper.showAuthenticationRequired(applicationContext)
                ) {
                    settings.authenticationWarningShown = true
                }
                Result.success()
            } else {
                Result.retry()
            }
        } catch (_: Exception) {
            Result.retry()
        }
    }
}
