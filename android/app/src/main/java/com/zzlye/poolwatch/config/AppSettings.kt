package com.zzlye.poolwatch.config

import android.content.Context
import com.zzlye.poolwatch.BuildConfig

class AppSettings(context: Context) {
    private val preferences = context.applicationContext.getSharedPreferences(
        PREFERENCES_NAME,
        Context.MODE_PRIVATE,
    )

    var serverUrl: String
        get() = preferences.getString(KEY_SERVER_URL, BuildConfig.DEFAULT_SERVER_URL)
            ?: BuildConfig.DEFAULT_SERVER_URL
        set(value) {
            preferences.edit().putString(KEY_SERVER_URL, value).apply()
        }

    var monitoringEnabled: Boolean
        get() = preferences.getBoolean(KEY_MONITORING_ENABLED, false)
        set(value) {
            preferences.edit().putBoolean(KEY_MONITORING_ENABLED, value).apply()
        }

    var alertBaselineReady: Boolean
        get() = preferences.getBoolean(KEY_ALERT_BASELINE_READY, false)
        set(value) {
            preferences.edit().putBoolean(KEY_ALERT_BASELINE_READY, value).apply()
        }

    var authenticationWarningShown: Boolean
        get() = preferences.getBoolean(KEY_AUTH_WARNING_SHOWN, false)
        set(value) {
            preferences.edit().putBoolean(KEY_AUTH_WARNING_SHOWN, value).apply()
        }

    var authenticationInvalidated: Boolean
        get() = preferences.getBoolean(KEY_AUTH_INVALIDATED, false)
        private set(value) {
            preferences.edit().putBoolean(KEY_AUTH_INVALIDATED, value).apply()
        }

    val authenticationGeneration: Long
        get() = preferences.getLong(KEY_AUTH_GENERATION, 0L)

    var monitorStatus: String
        get() = preferences.getString(KEY_MONITOR_STATUS, MonitorStatus.STOPPED.value)
            ?: MonitorStatus.STOPPED.value
        set(value) {
            preferences.edit().putString(KEY_MONITOR_STATUS, value).apply()
        }

    var serviceHeartbeatAt: Long
        get() = preferences.getLong(KEY_SERVICE_HEARTBEAT_AT, 0L)
        set(value) {
            preferences.edit().putLong(KEY_SERVICE_HEARTBEAT_AT, value).apply()
        }

    fun markAuthenticationInvalidated(): Long = advanceAuthenticationGeneration(invalidated = true)

    fun markAuthenticationInvalidatedIfCurrent(expectedGeneration: Long): Long? =
        synchronized(preferences) {
            if (authenticationGeneration != expectedGeneration) return@synchronized null
            advanceAuthenticationGenerationLocked(invalidated = true)
        }

    fun markAuthenticationRefreshed(): Long = advanceAuthenticationGeneration(invalidated = false)

    fun clearAuthenticationInvalidatedIfCurrent(expectedGeneration: Long): Boolean =
        synchronized(preferences) {
            if (authenticationGeneration != expectedGeneration) return@synchronized false
            authenticationInvalidated = false
            true
        }

    fun resetForServerChange() {
        alertBaselineReady = false
        authenticationWarningShown = false
        markAuthenticationRefreshed()
        monitorStatus = MonitorStatus.STOPPED.value
        serviceHeartbeatAt = 0L
    }

    private fun advanceAuthenticationGeneration(invalidated: Boolean): Long = synchronized(preferences) {
        advanceAuthenticationGenerationLocked(invalidated)
    }

    private fun advanceAuthenticationGenerationLocked(invalidated: Boolean): Long {
        val current = authenticationGeneration
        val next = if (current == Long.MAX_VALUE) 1L else current + 1L
        val editor = preferences.edit()
            .putLong(KEY_AUTH_GENERATION, next)
            .putBoolean(KEY_AUTH_INVALIDATED, invalidated)
        if (invalidated) editor.putString(KEY_MONITOR_STATUS, MonitorStatus.LOGIN_REQUIRED.value)
        editor.apply()
        return next
    }

    companion object {
        private const val PREFERENCES_NAME = "poolwatch_settings"
        private const val KEY_SERVER_URL = "server_url"
        private const val KEY_MONITORING_ENABLED = "monitoring_enabled"
        private const val KEY_ALERT_BASELINE_READY = "alert_baseline_ready"
        private const val KEY_AUTH_WARNING_SHOWN = "auth_warning_shown"
        private const val KEY_AUTH_INVALIDATED = "auth_invalidated"
        private const val KEY_AUTH_GENERATION = "auth_generation"
        private const val KEY_MONITOR_STATUS = "monitor_status"
        private const val KEY_SERVICE_HEARTBEAT_AT = "service_heartbeat_at"
    }
}

enum class MonitorStatus(val value: String, val label: String) {
    STOPPED("stopped", "未开启"),
    CONNECTING("connecting", "正在连接"),
    CONNECTED("connected", "实时监听中"),
    RETRYING("retrying", "网络重连中"),
    LOGIN_REQUIRED("login_required", "需要重新登录"),
    ERROR("error", "监控异常"),
    ;

    companion object {
        fun fromValue(value: String): MonitorStatus = entries.firstOrNull { it.value == value } ?: STOPPED
    }
}
