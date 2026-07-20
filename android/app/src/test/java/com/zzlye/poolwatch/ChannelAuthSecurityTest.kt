package com.zzlye.poolwatch

import com.zzlye.poolwatch.auth.ChannelAuthSecurity
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ChannelAuthSecurityTest {
    @Test
    fun `只接受固定格式的授权启动地址`() {
        val id = "auth_0123456789abcdef0123456789abcdef"
        assertEquals(id, ChannelAuthSecurity.attemptIdFromLaunchUrl("poolwatch-auth://start/$id"))
        assertNull(ChannelAuthSecurity.attemptIdFromLaunchUrl("https://example.com/$id"))
        assertNull(ChannelAuthSecurity.attemptIdFromLaunchUrl("poolwatch-auth://start/../$id"))
    }

    @Test
    fun `同源比较包含协议主机和有效端口`() {
        assertTrue(ChannelAuthSecurity.sameOrigin("https://api.example.com/login", "https://api.example.com:443"))
        assertFalse(ChannelAuthSecurity.sameOrigin("https://api.example.com", "http://api.example.com"))
        assertFalse(ChannelAuthSecurity.sameOrigin("https://api.example.com", "https://other.example.com"))
    }

    @Test
    fun `解析Sub2API回调片段中的令牌`() {
        val tokens = ChannelAuthSecurity.parseSub2Tokens(
            "https://api.example.com/oauth/callback#access_token=access%2Evalue&refresh_token=refresh%2Bvalue",
        )
        assertEquals("access.value", tokens?.accessToken)
        assertEquals("refresh+value", tokens?.refreshToken)
        assertNull(ChannelAuthSecurity.parseSub2Tokens("https://api.example.com/oauth/callback#state=ok"))
    }

    @Test
    fun `Cookie和用户ID会先经过格式清洗`() {
        assertEquals("session=abc; theme=dark", ChannelAuthSecurity.sanitizeCookie(" session=abc; theme=dark "))
        assertNull(ChannelAuthSecurity.sanitizeCookie("session=abc\r\nInjected: yes"))
        assertEquals("42", ChannelAuthSecurity.parseEvaluatedUserId("\"42\""))
        assertEquals("", ChannelAuthSecurity.parseEvaluatedUserId("\"not-a-number\""))
    }
}
