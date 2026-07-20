package com.zzlye.poolwatch.ui

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DrawerGesturePolicyTest {
    @Test
    fun `侧栏关闭时禁用滑动手势`() {
        assertFalse(drawerGesturesEnabled(isDrawerOpen = false))
    }

    @Test
    fun `侧栏打开后保留滑动关闭手势`() {
        assertTrue(drawerGesturesEnabled(isDrawerOpen = true))
    }
}
