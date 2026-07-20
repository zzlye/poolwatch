package com.zzlye.poolwatch.monitoring

import android.app.ActivityManager
import android.content.Context
import android.content.Intent
import android.os.Build
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
        AppSettings(context).apply {
            monitoringEnabled = true
            serviceHeartbeatAt = 0L
        }
        schedulePeriodic(context)
        startRealtimeService(context)
    }

    fun disable(context: Context) {
        AppSettings(context).apply {
            monitoringEnabled = false
            monitorStatus = MonitorStatus.STOPPED.value
            authenticationWarningShown = false
            markAuthenticationRefreshed()
            serviceHeartbeatAt = 0L
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
        if (!requestRealtimeService(context, RealtimeMonitorService.ACTION_START)) {
            enqueueImmediate(context)
        }
    }

    fun reconnect(context: Context) {
        if (!AppSettings(context).monitoringEnabled) return
        if (!requestRealtimeService(context, RealtimeMonitorService.ACTION_RECONNECT)) {
            enqueueImmediate(context)
        }
    }

    fun recoverRealtimeService(context: Context, nowMillis: Long = System.currentTimeMillis()): Boolean {
        val settings = AppSettings(context)
        if (!MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = settings.monitoringEnabled,
                nowMillis = nowMillis,
                lastHeartbeatMillis = settings.serviceHeartbeatAt,
            )
        ) {
            return false
        }
        settings.monitorStatus = MonitorStatus.RETRYING.value
        if (!canRestartForegroundServiceFromWorker()) return false
        // 已经位于 WorkManager 兜底任务中，失败时等待下一次周期任务，避免循环创建工作。
        return requestRealtimeService(context, RealtimeMonitorService.ACTION_START)
    }

    fun reportAuthenticationInvalid(context: Context, expectedGeneration: Long? = null): Long? {
        val settings = AppSettings(context)
        val invalidationGeneration = if (expectedGeneration == null) {
            settings.markAuthenticationInvalidated()
        } else {
            settings.markAuthenticationInvalidatedIfCurrent(expectedGeneration) ?: return null
        }
        val intent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = RealtimeMonitorService.ACTION_AUTHENTICATION_INVALID
            putExtra(
                RealtimeMonitorService.EXTRA_AUTHENTICATION_GENERATION,
                invalidationGeneration,
            )
        }
        // 服务已经在前台运行时立即断开 SSE；受后台限制时由持久化标记和服务心跳兜底。
        runCatching { context.startService(intent) }
        return invalidationGeneration
    }

    private fun networkConstraints(): Constraints = Constraints.Builder()
        .setRequiredNetworkType(NetworkType.CONNECTED)
        .build()

    private fun requestRealtimeService(context: Context, actionValue: String): Boolean {
        val intent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = actionValue
        }
        return runCatching { ContextCompat.startForegroundService(context, intent) }.isSuccess
    }

    private fun canRestartForegroundServiceFromWorker(): Boolean {
        val processState = ActivityManager.RunningAppProcessInfo()
        ActivityManager.getMyMemoryState(processState)
        return MonitoringRecoveryPolicy.canAttemptWorkerServiceRestart(
            platformApiLevel = Build.VERSION.SDK_INT,
            processEligibleForForegroundStart =
                processState.importance <= ActivityManager.RunningAppProcessInfo.IMPORTANCE_VISIBLE,
        )
    }
}
