package com.zzlye.poolwatch

import com.zzlye.poolwatch.config.ServerUrlValidator
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ServerUrlValidatorTest {
    @Test
    fun `接受标准 HTTPS 根地址并移除末尾斜杠`() {
        val result = ServerUrlValidator.normalize("  https://jiance.zzlye.xyz/  ")

        assertEquals("https://jiance.zzlye.xyz", result.getOrThrow())
    }

    @Test
    fun `保留显式端口`() {
        val result = ServerUrlValidator.normalize("https://example.com:8443")

        assertEquals("https://example.com:8443", result.getOrThrow())
    }

    @Test
    fun `拒绝明文地址`() {
        assertTrue(ServerUrlValidator.normalize("http://example.com").isFailure)
    }

    @Test
    fun `拒绝路径参数和账号信息`() {
        assertTrue(ServerUrlValidator.normalize("https://example.com/admin").isFailure)
        assertTrue(ServerUrlValidator.normalize("https://example.com?token=1").isFailure)
        assertTrue(ServerUrlValidator.normalize("https://user:pass@example.com").isFailure)
    }
}
