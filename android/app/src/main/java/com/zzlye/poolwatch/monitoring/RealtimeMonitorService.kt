package com.zzlye.poolwatch.monitoring

import android.app.Service
import android.annotation.SuppressLint
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.config.MonitorStatus
import com.zzlye.poolwatch.network.AlertRecord
import com.zzlye.poolwatch.network.ApiException
import com.zzlye.poolwatch.network.PoolWatchApi
import okhttp3.Request
import okhttp3.Response
import okhttp3.sse.EventSource
import okhttp3.sse.EventSourceListener
import okhttp3.sse.EventSources
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import kotlin.math.min

class RealtimeMonitorService : Service() {
    private lateinit var settings: AppSettings
    private lateinit var api: PoolWatchApi
    private val executor = Executors.newSingleThreadScheduledExecutor()
    private val synchronizing = AtomicBoolean(false)
    private val connectionGeneration = AtomicLong(0)

    @Volatile
    private var running = false
    private var retryAttempt = 0
    private var eventSource: EventSource? = null
    private var reconnectTask: ScheduledFuture<*>? = null
    private var heartbeatTask: ScheduledFuture<*>? = null
    private var authenticationInvalidationHandled = false

    override fun onCreate() {
        super.onCreate()
        settings = AppSettings(this)
        api = PoolWatchApi(settings)
        running = true
        startInForeground(MonitorStatus.CONNECTING)
        recordHeartbeat()
        heartbeatTask = executor.scheduleWithFixedDelay(
            ::recordHeartbeatAndObserveAuthentication,
            MonitoringRecoveryPolicy.HEARTBEAT_INTERVAL_MILLIS,
            MonitoringRecoveryPolicy.HEARTBEAT_INTERVAL_MILLIS,
            TimeUnit.MILLISECONDS,
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        recordHeartbeat()
        if (!settings.monitoringEnabled && intent?.action != ACTION_STOP) {
            settings.serviceHeartbeatAt = 0L
            settings.monitorStatus = MonitorStatus.STOPPED.value
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        when (intent?.action) {
            ACTION_STOP -> {
                settings.monitoringEnabled = false
                settings.monitorStatus = MonitorStatus.STOPPED.value
                settings.authenticationWarningShown = false
                settings.markAuthenticationRefreshed()
                settings.serviceHeartbeatAt = 0L
                androidx.work.WorkManager.getInstance(this).cancelAllWorkByTag(WORK_TAG)
                NotificationHelper.clearAuthenticationRequired(this)
                broadcastStatus(MonitorStatus.STOPPED, false)
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }

            ACTION_RECONNECT -> {
                submitServiceTask {
                    reconnectTask?.cancel(false)
                    reconnectTask = null
                    cancelConnection()
                    synchronizeAndConnect()
                }
            }

            ACTION_AUTHENTICATION_INVALID -> {
                val expectedGeneration = intent.getLongExtra(EXTRA_AUTHENTICATION_GENERATION, -1L)
                submitServiceTask {
                    if (expectedGeneration >= 0L) {
                        disconnectForAuthentication(
                            expectedGeneration = expectedGeneration,
                            markInvalidated = false,
                        )
                    }
                }
            }

            else -> submitServiceTask(::synchronizeAndConnect)
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        running = false
        reconnectTask?.cancel(false)
        heartbeatTask?.cancel(false)
        cancelConnection()
        executor.shutdownNow()
        settings.serviceHeartbeatAt = 0L
        if (!settings.monitoringEnabled) {
            settings.monitorStatus = MonitorStatus.STOPPED.value
        } else {
            settings.monitorStatus = MonitorStatus.RETRYING.value
            MonitoringScheduler.enqueueImmediate(this)
        }
        super.onDestroy()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        if (settings.monitoringEnabled) {
            // stopWithTask=false 时服务通常仍在运行，只安排补查，不把健康服务误判为已停止。
            MonitoringScheduler.enqueueImmediate(this)
        }
        super.onTaskRemoved(rootIntent)
    }

    private fun synchronizeAndConnect() {
        if (!running || !settings.monitoringEnabled || !synchronizing.compareAndSet(false, true)) return
        val authenticationGeneration = settings.authenticationGeneration
        updateStatus(MonitorStatus.CONNECTING)
        try {
            val alerts = api.fetchAlerts()
            if (!settings.clearAuthenticationInvalidatedIfCurrent(authenticationGeneration)) return
            AlertProcessor.process(this, alerts)
            authenticationInvalidationHandled = false
            settings.authenticationWarningShown = false
            NotificationHelper.clearAuthenticationRequired(this)
            openEventStream(authenticationGeneration)
        } catch (error: ApiException) {
            if (!isAuthenticationResponseCurrent(authenticationGeneration)) return
            if (error.statusCode == 401) {
                disconnectForAuthentication(expectedGeneration = authenticationGeneration)
            } else {
                scheduleNetworkRetry()
            }
        } catch (_: Exception) {
            if (isAuthenticationResponseCurrent(authenticationGeneration)) scheduleNetworkRetry()
        } finally {
            synchronizing.set(false)
        }
    }

    private fun openEventStream(authenticationGeneration: Long) {
        if (!running || !settings.monitoringEnabled || !isAuthenticationResponseCurrent(authenticationGeneration)) {
            return
        }
        val generation = connectionGeneration.incrementAndGet()
        eventSource?.cancel()
        val request = Request.Builder()
            .url(api.endpoint("/api/events"))
            .header("Accept", "text/event-stream")
            .header("Cache-Control", "no-cache")
            .build()
        eventSource = EventSources.createFactory(api.eventClient).newEventSource(
            request,
            object : EventSourceListener() {
                override fun onOpen(eventSource: EventSource, response: Response) {
                    submitServiceTask {
                        if (!isCurrent(generation, authenticationGeneration)) {
                            eventSource.cancel()
                            return@submitServiceTask
                        }
                        updateStatus(MonitorStatus.CONNECTED)
                        // 建连后再补拉一次，覆盖首次查询与 SSE 订阅之间的短暂事件窗口。
                        refreshAlertsOnly()
                        // 连接稳定一段时间后才重置退避，避免代理立即断流时每秒重连。
                        executor.schedule(
                            { if (isCurrent(generation, authenticationGeneration)) retryAttempt = 0 },
                            STABLE_CONNECTION_SECONDS,
                            TimeUnit.SECONDS,
                        )
                    }
                }

                override fun onEvent(eventSource: EventSource, id: String?, type: String?, data: String) {
                    submitServiceTask {
                        if (!isCurrent(generation, authenticationGeneration) || type != "alert") {
                            return@submitServiceTask
                        }
                        val alert = runCatching { AlertRecord.fromJson(JSONObject(data)) }.getOrNull()
                        if (alert != null) {
                            AlertProcessor.processRealtime(this@RealtimeMonitorService, alert)
                        } else {
                            // 确认告警等精简事件不含完整内容，重新读取列表以防漏掉同时发生的新告警。
                            refreshAlertsOnly()
                        }
                    }
                }

                override fun onClosed(eventSource: EventSource) {
                    submitServiceTask {
                        if (isCurrent(generation, authenticationGeneration)) scheduleNetworkRetry()
                    }
                }

                override fun onFailure(eventSource: EventSource, throwable: Throwable?, response: Response?) {
                    submitServiceTask {
                        if (!isCurrent(generation, authenticationGeneration)) return@submitServiceTask
                        if (response?.code == 401) {
                            disconnectForAuthentication(expectedGeneration = authenticationGeneration)
                        } else {
                            scheduleNetworkRetry()
                        }
                    }
                }
            },
        )
    }

    private fun refreshAlertsOnly() {
        if (!running || !settings.monitoringEnabled) return
        val authenticationGeneration = settings.authenticationGeneration
        try {
            val alerts = api.fetchAlerts()
            if (!settings.clearAuthenticationInvalidatedIfCurrent(authenticationGeneration)) return
            AlertProcessor.process(this, alerts)
            authenticationInvalidationHandled = false
            settings.authenticationWarningShown = false
            NotificationHelper.clearAuthenticationRequired(this)
        } catch (error: ApiException) {
            if (error.statusCode == 401 && isAuthenticationResponseCurrent(authenticationGeneration)) {
                disconnectForAuthentication(expectedGeneration = authenticationGeneration)
            }
        } catch (_: Exception) {
            // 实时连接仍在运行时，本次补充刷新失败会由下一条事件或周期任务再次补偿。
        }
    }

    private fun handleAuthenticationRequired() {
        updateStatus(MonitorStatus.LOGIN_REQUIRED)
        if (!settings.authenticationWarningShown && NotificationHelper.showAuthenticationRequired(this)) {
            settings.authenticationWarningShown = true
        }
    }

    private fun disconnectForAuthentication(
        expectedGeneration: Long? = null,
        markInvalidated: Boolean = true,
    ) {
        if (!running || !settings.monitoringEnabled) return
        if (markInvalidated) {
            if (expectedGeneration == null) {
                settings.markAuthenticationInvalidated()
            } else if (settings.markAuthenticationInvalidatedIfCurrent(expectedGeneration) == null) {
                return
            }
        } else {
            if (!settings.authenticationInvalidated) return
            if (expectedGeneration != null && !isAuthenticationResponseCurrent(expectedGeneration)) return
        }
        authenticationInvalidationHandled = true
        handleAuthenticationRequired()
        scheduleReconnect(LOGIN_RETRY_SECONDS)
    }

    private fun scheduleNetworkRetry() {
        retryAttempt = min(retryAttempt + 1, 6)
        val delay = min(1L shl (retryAttempt - 1), MAX_RETRY_SECONDS)
        updateStatus(MonitorStatus.RETRYING)
        scheduleReconnect(delay)
    }

    private fun scheduleReconnect(delaySeconds: Long) {
        if (!running || !settings.monitoringEnabled) return
        cancelConnection()
        reconnectTask?.cancel(false)
        reconnectTask = executor.schedule(::synchronizeAndConnect, delaySeconds, TimeUnit.SECONDS)
    }

    private fun cancelConnection() {
        connectionGeneration.incrementAndGet()
        eventSource?.cancel()
        eventSource = null
    }

    private fun isCurrent(generation: Long, authenticationGeneration: Long): Boolean =
        running &&
            settings.monitoringEnabled &&
            connectionGeneration.get() == generation &&
            !settings.authenticationInvalidated &&
            isAuthenticationResponseCurrent(authenticationGeneration)

    private fun isAuthenticationResponseCurrent(authenticationGeneration: Long): Boolean =
        AuthenticationPolicy.isResponseCurrent(
            requestAuthenticationGeneration = authenticationGeneration,
            currentAuthenticationGeneration = settings.authenticationGeneration,
        )

    @SuppressLint("MissingPermission")
    private fun updateStatus(status: MonitorStatus) {
        settings.monitorStatus = status.value
        if (NotificationHelper.canPostNotifications(this)) {
            runCatching {
                NotificationManagerCompat.from(this).notify(
                    NotificationHelper.MONITORING_NOTIFICATION_ID,
                    NotificationHelper.monitoringNotification(this, status),
                )
            }
        }
        broadcastStatus(status, settings.monitoringEnabled)
    }

    private fun broadcastStatus(status: MonitorStatus, enabled: Boolean) {
        sendBroadcast(
            Intent(ACTION_STATUS_CHANGED)
                .setPackage(packageName)
                .putExtra(EXTRA_STATUS, status.value)
                .putExtra(EXTRA_ENABLED, enabled),
        )
    }

    private fun startInForeground(status: MonitorStatus) {
        settings.monitorStatus = status.value
        val foregroundType = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
        } else {
            0
        }
        ServiceCompat.startForeground(
            this,
            NotificationHelper.MONITORING_NOTIFICATION_ID,
            NotificationHelper.monitoringNotification(this, status),
            foregroundType,
        )
    }

    private fun recordHeartbeat() {
        if (running && settings.monitoringEnabled) {
            settings.serviceHeartbeatAt = System.currentTimeMillis()
        }
    }

    private fun recordHeartbeatAndObserveAuthentication() {
        recordHeartbeat()
        if (!settings.authenticationInvalidated) {
            authenticationInvalidationHandled = false
            return
        }
        if (AuthenticationPolicy.shouldDisconnectRealtime(
                authenticationInvalidated = true,
                invalidationHandled = authenticationInvalidationHandled,
            )
        ) {
            disconnectForAuthentication(markInvalidated = false)
        }
    }

    private fun submitServiceTask(task: () -> Unit) {
        if (!running || executor.isShutdown) return
        runCatching {
            executor.execute {
                if (running) task()
            }
        }
    }

    companion object {
        const val ACTION_START = "com.zzlye.poolwatch.action.START_MONITORING"
        const val ACTION_STOP = "com.zzlye.poolwatch.action.STOP_MONITORING"
        const val ACTION_RECONNECT = "com.zzlye.poolwatch.action.RECONNECT_MONITORING"
        const val ACTION_AUTHENTICATION_INVALID = "com.zzlye.poolwatch.action.AUTHENTICATION_INVALID"
        const val ACTION_STATUS_CHANGED = "com.zzlye.poolwatch.action.MONITOR_STATUS_CHANGED"
        const val EXTRA_STATUS = "monitor_status"
        const val EXTRA_ENABLED = "monitor_enabled"
        const val EXTRA_AUTHENTICATION_GENERATION = "authentication_generation"
        const val WORK_TAG = "poolwatch_monitoring"
        private const val LOGIN_RETRY_SECONDS = 60L
        private const val MAX_RETRY_SECONDS = 60L
        private const val STABLE_CONNECTION_SECONDS = 30L
    }
}
