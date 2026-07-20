package com.zzlye.poolwatch.network

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SessionCookiePolicyTest {
    @Test
    fun `从混合Cookie中读取号池会话`() {
        assertEquals(
            "session-value",
            SessionCookiePolicy.sessionValue("theme=dark; poolwatch_session=session-value; mode=compact"),
        )
        assertNull(SessionCookiePolicy.sessionValue("theme=dark"))
    }

    @Test
    fun `迟到响应不能覆盖网页的新会话`() {
        assertFalse(
            SessionCookiePolicy.shouldApplyResponseCookie(
                requestSession = "expired-session",
                currentSession = "new-session",
            ),
        )
        assertTrue(
            SessionCookiePolicy.shouldApplyResponseCookie(
                requestSession = "same-session",
                currentSession = "same-session",
            ),
        )
        assertTrue(
            SessionCookiePolicy.shouldApplyResponseCookie(
                requestSession = null,
                currentSession = null,
            ),
        )
    }
}
