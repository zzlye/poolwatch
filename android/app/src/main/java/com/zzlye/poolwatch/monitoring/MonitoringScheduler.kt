package com.zzlye.poolwatch.monitoring

import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.config.MonitorStatus
import java.util.concurrent.TimeUnit

object MonitoringScheduler {
    private const val PERIODIC_WORK_NAME = "poolwatch_periodic_alert_sync"
    private const val IMMEDIATE_WORK_NAME = "poolwatch_immediate_alert_sync"

    fun enable(context: Context) {
        AppSettings(context).monitoringEnabled = true
        schedulePeriodic(context)
        startRealtimeService(context)
    }

    fun disable(context: Context) {
        AppSettings(context).apply {
            monitoringEnabled = false
            monitorStatus = MonitorStatus.STOPPED.value
            authenticationWarningShown = false
        }
        WorkManager.getInstance(context).cancelUniqueWork(PERIODIC_WORK_NAME)
        WorkManager.getInstance(context).cancelUniqueWork(IMMEDIATE_WORK_NAME)
        NotificationHelper.clearAuthenticationRequired(context)
        context.stopService(Intent(context, RealtimeMonitorService::class.java))
    }

    fun schedulePeriodic(context: Context) {
        if (!AppSettings(context).monitoringEnabled) return
        val request = PeriodicWorkRequestBuilder<AlertSyncWorker>(15, TimeUnit.MINUTES)
            .setConstraints(networkConstraints())
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.SECONDS)
            .addTag(RealtimeMonitorService.WORK_TAG)
            .build()
        WorkManager.getInstance(context).enqueueUniquePeriodicWork(
            PERIODIC_WORK_NAME,
            ExistingPeriodicWorkPolicy.UPDATE,
            request,
        )
    }

    fun enqueueImmediate(context: Context) {
        if (!AppSettings(context).monitoringEnabled) return
        val request = OneTimeWorkRequestBuilder<AlertSyncWorker>()
            .setConstraints(networkConstraints())
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.SECONDS)
            .addTag(RealtimeMonitorService.WORK_TAG)
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(
            IMMEDIATE_WORK_NAME,
            ExistingWorkPolicy.REPLACE,
            request,
        )
    }

    fun startRealtimeService(context: Context) {
        if (!AppSettings(context).monitoringEnabled) return
        val intent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = RealtimeMonitorService.ACTION_START
        }
        runCatching { ContextCompat.startForegroundService(context, intent) }
            .onFailure { enqueueImmediate(context) }
    }

    fun reconnect(context: Context) {
        if (!AppSettings(context).monitoringEnabled) return
        val intent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = RealtimeMonitorService.ACTION_RECONNECT
        }
        runCatching { ContextCompat.startForegroundService(context, intent) }
            .onFailure { enqueueImmediate(context) }
    }

    fun reportAuthenticationInvalid(context: Context) {
        val intent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = RealtimeMonitorService.ACTION_AUTHENTICATION_INVALID
        }
        // 服务已经在前台运行时立即断开 SSE；未运行或受后台限制时由周期任务状态兜底。
        runCatching { context.startService(intent) }
    }

    private fun networkConstraints(): Constraints = Constraints.Builder()
        .setRequiredNetworkType(NetworkType.CONNECTED)
        .build()
}
