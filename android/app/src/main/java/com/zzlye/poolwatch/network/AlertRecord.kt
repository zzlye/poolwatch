package com.zzlye.poolwatch.network

import org.json.JSONObject

data class AlertRecord(
    val id: String,
    val targetId: String,
    val title: String,
    val message: String,
    val severity: String,
    val status: String,
    val createdAt: String,
) {
    companion object {
        fun fromJson(value: JSONObject): AlertRecord? {
            val id = value.optString("id", value.optString("alertId")).trim()
            val title = value.optString("title").trim()
            if (id.isBlank() || title.isBlank()) return null
            return AlertRecord(
                id = id,
                targetId = value.optString("targetId").trim(),
                title = title.take(120),
                message = value.optString("message").trim().take(500),
                severity = value.optString("severity", "warning").trim(),
                status = value.optString("status", "open").trim(),
                createdAt = value.optString("createdAt").trim(),
            )
        }
    }
}
