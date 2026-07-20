package com.zzlye.poolwatch.config

import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

object ServerUrlValidator {
    /**
     * 将服务器地址整理为不含路径的 HTTPS 根地址，防止 Cookie 被发往意外主机。
     */
    fun normalize(value: String): Result<String> {
        val parsed = value.trim().trimEnd('/').toHttpUrlOrNull()
            ?: return Result.failure(IllegalArgumentException("请输入完整的 HTTPS 地址"))
        if (!parsed.isHttps) {
            return Result.failure(IllegalArgumentException("服务器地址必须使用 HTTPS"))
        }
        if (!parsed.username.isBlank() || !parsed.password.isBlank()) {
            return Result.failure(IllegalArgumentException("服务器地址不能包含账号信息"))
        }
        if (parsed.encodedPath != "/" || parsed.query != null || parsed.fragment != null) {
            return Result.failure(IllegalArgumentException("服务器地址不能包含路径、参数或片段"))
        }
        return Result.success(
            parsed.newBuilder()
                .encodedPath("/")
                .query(null)
                .fragment(null)
                .build()
                .toString()
                .trimEnd('/'),
        )
    }
}
