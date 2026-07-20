package com.zzlye.poolwatch

import com.zzlye.poolwatch.network.AlertRecord
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AlertRecordTest {
    @Test
    fun `同时支持接口字段和实时事件字段`() {
        val apiAlert = AlertRecord.fromJson(
            JSONObject(
                """{"id":"alert_1","targetId":"target_1","title":"余额不足","message":"余额为 1","severity":"warning"}""",
            ),
        )
        val realtimeAlert = AlertRecord.fromJson(
            JSONObject(
                """{"alertId":"alert_2","targetId":"target_2","title":"渠道异常","message":"检测失败","severity":"critical"}""",
            ),
        )

        assertEquals("alert_1", apiAlert?.id)
        assertEquals("alert_2", realtimeAlert?.id)
    }

    @Test
    fun `忽略不含完整标题的精简事件`() {
        assertNull(AlertRecord.fromJson(JSONObject("""{"alertId":"alert_3"}""")))
    }
}
