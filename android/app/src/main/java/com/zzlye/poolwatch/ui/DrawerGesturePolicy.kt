package com.zzlye.poolwatch.ui

/**
 * 侧栏关闭时禁用滑动打开，避免页面纵向滚动被误判；侧栏打开后保留滑动关闭。
 */
internal fun drawerGesturesEnabled(isDrawerOpen: Boolean): Boolean = isDrawerOpen
