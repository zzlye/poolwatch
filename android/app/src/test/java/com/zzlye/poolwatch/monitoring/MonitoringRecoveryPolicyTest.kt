package com.zzlye.poolwatch.monitoring

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MonitoringRecoveryPolicyTest {
    @Test
    fun `未开启监控时不恢复服务`() {
        assertFalse(
            MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = false,
                nowMillis = 300_000L,
                lastHeartbeatMillis = 0L,
            ),
        )
    }

    @Test
    fun `心跳缺失或过期时恢复服务`() {
        assertTrue(
            MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = true,
                nowMillis = 300_000L,
                lastHeartbeatMillis = 0L,
            ),
        )
        assertTrue(
            MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = true,
                nowMillis = 300_000L,
                lastHeartbeatMillis = 180_000L,
            ),
        )
    }

    @Test
    fun `心跳仍新鲜或系统时间回拨时不重复启动`() {
        assertFalse(
            MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = true,
                nowMillis = 300_000L,
                lastHeartbeatMillis = 240_001L,
            ),
        )
        assertFalse(
            MonitoringRecoveryPolicy.shouldRestartRealtimeService(
                monitoringEnabled = true,
                nowMillis = 300_000L,
                lastHeartbeatMillis = 310_000L,
            ),
        )
    }

    @Test
    fun `安卓十二后台任务只保留定时补查`() {
        assertFalse(
            MonitoringRecoveryPolicy.canAttemptWorkerServiceRestart(
                platformApiLevel = 31,
                processEligibleForForegroundStart = false,
            ),
        )
        assertTrue(
            MonitoringRecoveryPolicy.canAttemptWorkerServiceRestart(
                platformApiLevel = 31,
                processEligibleForForegroundStart = true,
            ),
        )
        assertTrue(
            MonitoringRecoveryPolicy.canAttemptWorkerServiceRestart(
                platformApiLevel = 30,
                processEligibleForForegroundStart = false,
            ),
        )
    }
}
