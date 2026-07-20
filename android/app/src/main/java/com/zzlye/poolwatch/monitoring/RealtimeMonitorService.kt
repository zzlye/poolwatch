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

    override fun onCreate() {
        super.onCreate()
        settings = AppSettings(this)
        api = PoolWatchApi(settings)
        running = true
        startInForeground(MonitorStatus.CONNECTING)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!settings.monitoringEnabled && intent?.action != ACTION_STOP) {
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
                androidx.work.WorkManager.getInstance(this).cancelAllWorkByTag(WORK_TAG)
                NotificationHelper.clearAuthenticationRequired(this)
                broadcastStatus(MonitorStatus.STOPPED, false)
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }

            ACTION_RECONNECT -> {
                reconnectTask?.cancel(false)
                reconnectTask = null
                cancelConnection()
                executor.execute(::synchronizeAndConnect)
            }

            ACTION_AUTHENTICATION_INVALID -> {
                reconnectTask?.cancel(false)
                reconnectTask = null
                cancelConnection()
                handleAuthenticationRequired()
                scheduleReconnect(LOGIN_RETRY_SECONDS)
            }

            else -> executor.execute(::synchronizeAndConnect)
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        running = false
        reconnectTask?.cancel(false)
        cancelConnection()
        executor.shutdownNow()
        if (!settings.monitoringEnabled) {
            settings.monitorStatus = MonitorStatus.STOPPED.value
        }
        super.onDestroy()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        if (settings.monitoringEnabled) MonitoringScheduler.enqueueImmediate(this)
        super.onTaskRemoved(rootIntent)
    }

    private fun synchronizeAndConnect() {
        if (!running || !settings.monitoringEnabled || !synchronizing.compareAndSet(false, true)) return
        updateStatus(MonitorStatus.CONNECTING)
        try {
            val alerts = api.fetchAlerts()
            AlertProcessor.process(this, alerts)
            settings.authenticationWarningShown = false
            NotificationHelper.clearAuthenticationRequired(this)
            openEventStream()
        } catch (error: ApiException) {
            if (error.statusCode == 401) {
                handleAuthenticationRequired()
                scheduleReconnect(LOGIN_RETRY_SECONDS)
            } else {
                scheduleNetworkRetry()
            }
        } catch (_: Exception) {
            scheduleNetworkRetry()
        } finally {
            synchronizing.set(false)
        }
    }

    private fun openEventStream() {
        if (!running || !settings.monitoringEnabled) return
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
                    if (!isCurrent(generation)) return
                    updateStatus(MonitorStatus.CONNECTED)
                    // 建连后再补拉一次，覆盖首次查询与 SSE 订阅之间的短暂事件窗口。
                    executor.execute(::refreshAlertsOnly)
                    // 连接稳定一段时间后才重置退避，避免代理立即断流时每秒重连。
                    executor.schedule(
                        { if (isCurrent(generation)) retryAttempt = 0 },
                        STABLE_CONNECTION_SECONDS,
                        TimeUnit.SECONDS,
                    )
                }

                override fun onEvent(eventSource: EventSource, id: String?, type: String?, data: String) {
                    if (!isCurrent(generation) || type != "alert") return
                    val alert = runCatching { AlertRecord.fromJson(JSONObject(data)) }.getOrNull()
                    if (alert != null) {
                        AlertProcessor.processRealtime(this@RealtimeMonitorService, alert)
                    } else {
                        // 确认告警等精简事件不含完整内容，重新读取列表以防漏掉同时发生的新告警。
                        executor.execute(::refreshAlertsOnly)
                    }
                }

                override fun onClosed(eventSource: EventSource) {
                    if (isCurrent(generation)) scheduleNetworkRetry()
                }

                override fun onFailure(eventSource: EventSource, throwable: Throwable?, response: Response?) {
                    if (!isCurrent(generation)) return
                    if (response?.code == 401) {
                        handleAuthenticationRequired()
                        scheduleReconnect(LOGIN_RETRY_SECONDS)
                    } else {
                        scheduleNetworkRetry()
                    }
                }
            },
        )
    }

    private fun refreshAlertsOnly() {
        if (!running || !settings.monitoringEnabled) return
        try {
            AlertProcessor.process(this, api.fetchAlerts())
        } catch (error: ApiException) {
            if (error.statusCode == 401) handleAuthenticationRequired()
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

    private fun isCurrent(generation: Long): Boolean =
        running && settings.monitoringEnabled && connectionGeneration.get() == generation

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

    companion object {
        const val ACTION_START = "com.zzlye.poolwatch.action.START_MONITORING"
        const val ACTION_STOP = "com.zzlye.poolwatch.action.STOP_MONITORING"
        const val ACTION_RECONNECT = "com.zzlye.poolwatch.action.RECONNECT_MONITORING"
        const val ACTION_AUTHENTICATION_INVALID = "com.zzlye.poolwatch.action.AUTHENTICATION_INVALID"
        const val ACTION_STATUS_CHANGED = "com.zzlye.poolwatch.action.MONITOR_STATUS_CHANGED"
        const val EXTRA_STATUS = "monitor_status"
        const val EXTRA_ENABLED = "monitor_enabled"
        const val WORK_TAG = "poolwatch_monitoring"
        private const val LOGIN_RETRY_SECONDS = 60L
        private const val MAX_RETRY_SECONDS = 60L
        private const val STABLE_CONNECTION_SECONDS = 30L
    }
}
