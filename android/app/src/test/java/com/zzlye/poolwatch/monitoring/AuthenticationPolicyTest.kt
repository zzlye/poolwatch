package com.zzlye.poolwatch.monitoring

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AuthenticationPolicyTest {
    @Test
    fun `网页退出后通知原生监听断开连接`() {
        assertEquals(
            WebSessionChange.LOGGED_OUT,
            AuthenticationPolicy.detectWebSessionChange(
                previousSession = "old-session",
                currentSession = null,
                authenticationInvalidated = false,
            ),
        )
    }

    @Test
    fun `重新登录后通知原生监听重新连接`() {
        assertEquals(
            WebSessionChange.LOGGED_IN,
            AuthenticationPolicy.detectWebSessionChange(
                previousSession = null,
                currentSession = "new-session",
                authenticationInvalidated = true,
            ),
        )
    }

    @Test
    fun `连接正常时的会话续期不重复重连`() {
        assertEquals(
            WebSessionChange.NONE,
            AuthenticationPolicy.detectWebSessionChange(
                previousSession = "old-session",
                currentSession = "renewed-session",
                authenticationInvalidated = false,
            ),
        )
    }

    @Test
    fun `失效标记只触发一次主动断开`() {
        assertTrue(
            AuthenticationPolicy.shouldDisconnectRealtime(
                authenticationInvalidated = true,
                invalidationHandled = false,
            ),
        )
        assertFalse(
            AuthenticationPolicy.shouldDisconnectRealtime(
                authenticationInvalidated = true,
                invalidationHandled = true,
            ),
        )
        assertFalse(
            AuthenticationPolicy.shouldDisconnectRealtime(
                authenticationInvalidated = false,
                invalidationHandled = false,
            ),
        )
    }

    @Test
    fun `临时连接状态不会吞掉重新登录事件`() {
        assertEquals(
            WebSessionChange.LOGGED_IN,
            AuthenticationPolicy.detectWebSessionChange(
                previousSession = "expired-session",
                currentSession = "new-session",
                authenticationInvalidated = true,
            ),
        )
    }

    @Test
    fun `旧请求不能覆盖较新的认证结果`() {
        assertTrue(AuthenticationPolicy.isResponseCurrent(3L, 3L))
        assertFalse(AuthenticationPolicy.isResponseCurrent(3L, 4L))
    }
}
