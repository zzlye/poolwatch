package com.zzlye.poolwatch.network

import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Test

class PoolWatchApiClientTest {
    @Test
    fun `实时连接保留总时长并检测静默断线`() {
        val client = PoolWatchApi.buildEventClient(OkHttpClient.Builder())

        assertEquals(0, client.callTimeoutMillis)
        assertEquals(
            PoolWatchApi.EVENT_READ_TIMEOUT_SECONDS * 1_000L,
            client.readTimeoutMillis.toLong(),
        )
    }
}
