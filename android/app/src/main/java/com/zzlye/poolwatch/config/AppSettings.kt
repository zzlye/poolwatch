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

    var monitorStatus: String
        get() = preferences.getString(KEY_MONITOR_STATUS, MonitorStatus.STOPPED.value)
            ?: MonitorStatus.STOPPED.value
        set(value) {
            preferences.edit().putString(KEY_MONITOR_STATUS, value).apply()
        }

    fun resetForServerChange() {
        alertBaselineReady = false
        authenticationWarningShown = false
        monitorStatus = MonitorStatus.STOPPED.value
    }

    companion object {
        private const val PREFERENCES_NAME = "poolwatch_settings"
        private const val KEY_SERVER_URL = "server_url"
        private const val KEY_MONITORING_ENABLED = "monitoring_enabled"
        private const val KEY_ALERT_BASELINE_READY = "alert_baseline_ready"
        private const val KEY_AUTH_WARNING_SHOWN = "auth_warning_shown"
        private const val KEY_MONITOR_STATUS = "monitor_status"
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
