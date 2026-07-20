package com.zzlye.poolwatch.config

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class BackgroundRunSettingsPolicyTest {
    @Test
    fun `新版荣耀优先使用荣耀系统管家并保留华为兼容入口`() {
        val candidates = BackgroundRunSettingsPolicy.componentCandidates("HONOR")

        assertEquals("com.hihonor.systemmanager", candidates.first().packageName)
        assertTrue(candidates.any { it.packageName == "com.huawei.systemmanager" })
    }

    @Test
    fun `一加OPPO和Realme覆盖新旧系统管家入口`() {
        val onePlus = BackgroundRunSettingsPolicy.componentCandidates("OnePlus")
        val oppo = BackgroundRunSettingsPolicy.componentCandidates("OPPO")
        val realme = BackgroundRunSettingsPolicy.componentCandidates("realme")

        assertTrue(onePlus.any { it.packageName == "com.oplus.safecenter" })
        assertTrue(onePlus.any { it.packageName == "com.oneplus.security" })
        assertTrue(oppo.any { it.packageName == "com.coloros.safecenter" })
        assertTrue(oppo.any { it.packageName == "com.oppo.safe" })
        assertTrue(realme.any { it.packageName == "com.oplus.safecenter" })
        assertTrue(realme.any { it.packageName == "com.coloros.safecenter" })
    }

    @Test
    fun `显式入口启动失败后继续尝试下一个候选`() {
        val attempted = mutableListOf<String>()

        val opened = BackgroundRunSettingsPolicy.tryCandidates(
            candidates = listOf("第一个", "第二个", "第三个"),
        ) { candidate ->
            attempted += candidate
            if (candidate == "第一个") error("模拟页面不存在")
        }

        assertTrue(opened)
        assertEquals(listOf("第一个", "第二个"), attempted)
    }

    @Test
    fun `所有显式入口均失败时交由调用方打开系统详情页`() {
        val opened = BackgroundRunSettingsPolicy.tryCandidates(
            candidates = listOf("第一个", "第二个"),
        ) {
            error("模拟页面不存在")
        }

        assertFalse(opened)
    }
}
