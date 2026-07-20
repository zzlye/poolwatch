package com.zzlye.poolwatch.monitoring

enum class WebSessionChange {
    NONE,
    LOGGED_IN,
    LOGGED_OUT,
}

object AuthenticationPolicy {
    // 网页会话变化需要与原生监听同步，普通的会话续期不触发重复连接。
    fun detectWebSessionChange(
        previousSession: String?,
        currentSession: String?,
        authenticationInvalidated: Boolean,
    ): WebSessionChange = when {
        previousSession != null && currentSession == null -> WebSessionChange.LOGGED_OUT
        authenticationInvalidated && currentSession != null && currentSession != previousSession ->
            WebSessionChange.LOGGED_IN
        else -> WebSessionChange.NONE
    }

    // 持久化标记保证 Worker 无法直接唤起服务时，仍可由服务心跳主动断开旧连接。
    fun shouldDisconnectRealtime(
        authenticationInvalidated: Boolean,
        invalidationHandled: Boolean,
    ): Boolean = authenticationInvalidated && !invalidationHandled

    // 请求只可修改自己发出时所属的认证代际，避免迟到响应覆盖新登录状态。
    fun isResponseCurrent(
        requestAuthenticationGeneration: Long,
        currentAuthenticationGeneration: Long,
    ): Boolean = requestAuthenticationGeneration == currentAuthenticationGeneration
}
