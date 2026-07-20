package com.zzlye.poolwatch.monitoring

object MonitoringRecoveryPolicy {
    private const val ANDROID_12_API_LEVEL = 31
    const val HEARTBEAT_INTERVAL_MILLIS = 30_000L
    const val HEARTBEAT_STALE_AFTER_MILLIS = 120_000L

    // 前台服务心跳缺失或过期时，由周期任务尝试恢复实时监听。
    fun shouldRestartRealtimeService(
        monitoringEnabled: Boolean,
        nowMillis: Long,
        lastHeartbeatMillis: Long,
    ): Boolean {
        if (!monitoringEnabled) return false
        if (lastHeartbeatMillis <= 0L) return true
        if (nowMillis <= lastHeartbeatMillis) return false
        return nowMillis - lastHeartbeatMillis >= HEARTBEAT_STALE_AFTER_MILLIS
    }

    // Android 12 起后台任务不能任意新建前台服务，仅在应用可见或已有前台服务时尝试恢复。
    fun canAttemptWorkerServiceRestart(
        platformApiLevel: Int,
        processEligibleForForegroundStart: Boolean,
    ): Boolean = platformApiLevel < ANDROID_12_API_LEVEL || processEligibleForForegroundStart
}
