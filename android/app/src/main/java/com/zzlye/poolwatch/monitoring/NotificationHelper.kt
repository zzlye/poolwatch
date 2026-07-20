package com.zzlye.poolwatch.monitoring

import android.Manifest
import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.zzlye.poolwatch.MainActivity
import com.zzlye.poolwatch.R
import com.zzlye.poolwatch.config.MonitorStatus
import com.zzlye.poolwatch.network.AlertRecord

object NotificationHelper {
    const val MONITORING_NOTIFICATION_ID = 10_001
    private const val AUTHENTICATION_NOTIFICATION_ID = 10_002
    private const val TEST_NOTIFICATION_ID = 10_003
    private const val CHANNEL_MONITORING = "poolwatch_monitoring"
    private const val CHANNEL_ALERTS = "poolwatch_alerts"
    private const val ALERT_GROUP = "poolwatch_alert_group"

    fun createChannels(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java)
        val monitoring = NotificationChannel(
            CHANNEL_MONITORING,
            context.getString(R.string.monitoring_channel_name),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = context.getString(R.string.monitoring_channel_description)
            setShowBadge(false)
        }
        val alerts = NotificationChannel(
            CHANNEL_ALERTS,
            context.getString(R.string.alert_channel_name),
            NotificationManager.IMPORTANCE_HIGH,
        ).apply {
            description = context.getString(R.string.alert_channel_description)
            enableVibration(true)
        }
        manager.createNotificationChannels(listOf(monitoring, alerts))
    }

    fun monitoringNotification(context: Context, status: MonitorStatus): Notification {
        val stopIntent = Intent(context, RealtimeMonitorService::class.java).apply {
            action = RealtimeMonitorService.ACTION_STOP
        }
        val stopAction = PendingIntent.getService(
            context,
            1,
            stopIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(context, CHANNEL_MONITORING)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("号池监控正在运行")
            .setContentText(status.label)
            .setContentIntent(openAppIntent(context, null, 0))
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .addAction(0, "停止监控", stopAction)
            .build()
    }

    @SuppressLint("MissingPermission")
    fun showAlert(context: Context, alert: AlertRecord): Boolean {
        if (!canPostAlerts(context)) return false
        val priority = if (alert.severity == "critical") {
            NotificationCompat.PRIORITY_MAX
        } else {
            NotificationCompat.PRIORITY_HIGH
        }
        val color = when (alert.severity) {
            "critical" -> Color.rgb(190, 45, 45)
            "info" -> Color.rgb(23, 122, 74)
            else -> Color.rgb(190, 125, 20)
        }
        val notification = NotificationCompat.Builder(context, CHANNEL_ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(alert.title)
            .setContentText(alert.message)
            .setStyle(NotificationCompat.BigTextStyle().bigText(alert.message))
            .setContentIntent(openAppIntent(context, alert.id, alert.id.hashCode()))
            .setAutoCancel(true)
            .setColor(color)
            .setCategory(NotificationCompat.CATEGORY_ALARM)
            .setPriority(priority)
            .setGroup(ALERT_GROUP)
            .build()
        return runCatching {
            NotificationManagerCompat.from(context).notify(alert.id.hashCode() and Int.MAX_VALUE, notification)
            true
        }.getOrDefault(false)
    }

    @SuppressLint("MissingPermission")
    fun showAuthenticationRequired(context: Context): Boolean {
        if (!canPostAlerts(context)) return false
        val notification = NotificationCompat.Builder(context, CHANNEL_ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("号池监控需要重新登录")
            .setContentText("登录状态已经失效，打开应用登录后会自动恢复监控。")
            .setStyle(NotificationCompat.BigTextStyle().bigText("登录状态已经失效，打开应用登录后会自动恢复监控。"))
            .setContentIntent(openAppIntent(context, null, AUTHENTICATION_NOTIFICATION_ID))
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_ERROR)
            .build()
        return runCatching {
            NotificationManagerCompat.from(context).notify(AUTHENTICATION_NOTIFICATION_ID, notification)
            true
        }.getOrDefault(false)
    }

    fun clearAuthenticationRequired(context: Context) {
        NotificationManagerCompat.from(context).cancel(AUTHENTICATION_NOTIFICATION_ID)
    }

    @SuppressLint("MissingPermission")
    fun showTest(context: Context): Boolean {
        if (!canPostAlerts(context)) return false
        val notification = NotificationCompat.Builder(context, CHANNEL_ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("号池监控测试通知")
            .setContentText("安卓原生通知已经可以正常显示。")
            .setContentIntent(openAppIntent(context, null, TEST_NOTIFICATION_ID))
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .build()
        return runCatching {
            NotificationManagerCompat.from(context).notify(TEST_NOTIFICATION_ID, notification)
            true
        }.getOrDefault(false)
    }

    fun canPostNotifications(context: Context): Boolean {
        val permissionGranted = Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        return permissionGranted && NotificationManagerCompat.from(context).areNotificationsEnabled()
    }

    fun canPostAlerts(context: Context): Boolean {
        if (!canPostNotifications(context)) return false
        val channel = context.getSystemService(NotificationManager::class.java)
            .getNotificationChannel(CHANNEL_ALERTS)
        return channel != null && channel.importance != NotificationManager.IMPORTANCE_NONE
    }

    private fun openAppIntent(context: Context, alertId: String?, requestCode: Int): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP
            alertId?.let { putExtra(MainActivity.EXTRA_ALERT_ID, it) }
        }
        return PendingIntent.getActivity(
            context,
            requestCode,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}
