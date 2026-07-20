package com.zzlye.poolwatch.monitoring

import android.content.Context
import androidx.work.Worker
import androidx.work.WorkerParameters
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.network.ApiException
import com.zzlye.poolwatch.network.PoolWatchApi

class AlertSyncWorker(
    appContext: Context,
    workerParameters: WorkerParameters,
) : Worker(appContext, workerParameters) {
    override fun doWork(): Result {
        val settings = AppSettings(applicationContext)
        if (!settings.monitoringEnabled) return Result.success()
        val authenticationGeneration = settings.authenticationGeneration
        return try {
            val alerts = PoolWatchApi(settings).fetchAlerts()
            if (!settings.clearAuthenticationInvalidatedIfCurrent(authenticationGeneration)) return Result.success()
            AlertProcessor.process(applicationContext, alerts)
            settings.authenticationWarningShown = false
            NotificationHelper.clearAuthenticationRequired(applicationContext)
            MonitoringScheduler.recoverRealtimeService(applicationContext)
            Result.success()
        } catch (error: ApiException) {
            if (error.statusCode == 401) {
                val invalidationGeneration = MonitoringScheduler.reportAuthenticationInvalid(
                        context = applicationContext,
                        expectedGeneration = authenticationGeneration,
                    )
                    ?: return Result.success()
                if (settings.authenticationGeneration == invalidationGeneration &&
                    settings.authenticationInvalidated &&
                    !settings.authenticationWarningShown &&
                    NotificationHelper.showAuthenticationRequired(applicationContext)
                ) {
                    if (settings.authenticationGeneration == invalidationGeneration &&
                        settings.authenticationInvalidated
                    ) {
                        settings.authenticationWarningShown = true
                    } else {
                        NotificationHelper.clearAuthenticationRequired(applicationContext)
                    }
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
