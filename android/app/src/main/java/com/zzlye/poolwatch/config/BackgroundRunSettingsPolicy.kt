package com.zzlye.poolwatch.config

import java.util.Locale

/** 厂商后台设置页的组件描述，调用处再转换为安卓的 ComponentName。 */
internal data class BackgroundSettingsComponent(
    val packageName: String,
    val className: String,
)

internal object BackgroundRunSettingsPolicy {
    /**
     * 按厂商返回从新到旧的候选入口。
     *
     * 这里只保存普通字符串，让候选选择逻辑可以在 JVM 单元测试中直接验证。
     */
    fun componentCandidates(manufacturer: String): List<BackgroundSettingsComponent> {
        val normalized = manufacturer.trim().lowercase(Locale.ROOT)
        return when {
            normalized.contains("xiaomi") || normalized.contains("redmi") -> XIAOMI_COMPONENTS
            normalized.contains("honor") -> HONOR_COMPONENTS
            normalized.contains("huawei") -> HUAWEI_COMPONENTS
            normalized.contains("oneplus") -> ONE_PLUS_COMPONENTS
            normalized.contains("oppo") -> OPPO_COMPONENTS
            normalized.contains("realme") -> REALME_COMPONENTS
            normalized.contains("vivo") || normalized.contains("iqoo") -> VIVO_COMPONENTS
            normalized.contains("meizu") -> MEIZU_COMPONENTS
            normalized.contains("samsung") -> SAMSUNG_COMPONENTS
            else -> emptyList()
        }
    }

    /** 候选入口启动失败时继续尝试，成功启动一个入口后立即停止。 */
    fun <T> tryCandidates(candidates: List<T>, launch: (T) -> Unit): Boolean {
        for (candidate in candidates) {
            try {
                launch(candidate)
                return true
            } catch (_: Exception) {
                // 部分系统保留包名但移除了具体页面，需要继续尝试下一个显式入口。
            }
        }
        return false
    }

    private val XIAOMI_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.miui.securitycenter",
            "com.miui.permcenter.autostart.AutoStartManagementActivity",
        ),
    )

    private val HUAWEI_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.huawei.systemmanager",
            "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
        ),
        BackgroundSettingsComponent(
            "com.huawei.systemmanager",
            "com.huawei.systemmanager.optimize.process.ProtectActivity",
        ),
    )

    private val HONOR_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.hihonor.systemmanager",
            "com.hihonor.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
        ),
        BackgroundSettingsComponent(
            "com.hihonor.systemmanager",
            "com.hihonor.systemmanager.optimize.process.ProtectActivity",
        ),
    ) + HUAWEI_COMPONENTS

    private val MODERN_OPLUS_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.oplus.safecenter",
            "com.oplus.safecenter.permission.startup.StartupAppListActivity",
        ),
        BackgroundSettingsComponent(
            "com.oplus.safecenter",
            "com.coloros.safecenter.permission.startup.StartupAppListActivity",
        ),
    )

    private val COLOR_OS_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.coloros.safecenter",
            "com.coloros.safecenter.permission.startup.StartupAppListActivity",
        ),
        BackgroundSettingsComponent(
            "com.coloros.safecenter",
            "com.coloros.safecenter.startupapp.StartupAppListActivity",
        ),
        BackgroundSettingsComponent(
            "com.oppo.safe",
            "com.oppo.safe.permission.startup.StartupAppListActivity",
        ),
    )

    private val OPPO_COMPONENTS = MODERN_OPLUS_COMPONENTS + COLOR_OS_COMPONENTS

    private val REALME_COMPONENTS = MODERN_OPLUS_COMPONENTS + COLOR_OS_COMPONENTS

    private val ONE_PLUS_COMPONENTS = MODERN_OPLUS_COMPONENTS + listOf(
        BackgroundSettingsComponent(
            "com.oneplus.security",
            "com.oneplus.security.chainlaunch.view.ChainLaunchAppListActivity",
        ),
    ) + COLOR_OS_COMPONENTS

    private val VIVO_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.vivo.permissionmanager",
            "com.vivo.permissionmanager.activity.BgStartUpManagerActivity",
        ),
    )

    private val MEIZU_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.meizu.safe",
            "com.meizu.safe.permission.SmartBGActivity",
        ),
    )

    private val SAMSUNG_COMPONENTS = listOf(
        BackgroundSettingsComponent(
            "com.samsung.android.lool",
            "com.samsung.android.sm.battery.ui.BatteryActivity",
        ),
    )
}
