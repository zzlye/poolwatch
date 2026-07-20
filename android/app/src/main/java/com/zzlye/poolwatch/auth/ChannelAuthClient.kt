package com.zzlye.poolwatch.auth

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import org.json.JSONObject
import java.util.concurrent.TimeUnit

data class ChannelAuthConfig(
    val id: String,
    val kind: String,
    val baseUrl: String,
    val loginUrl: String,
    val captureToken: String,
)

class ChannelAuthClient {
    private val client = OkHttpClient.Builder()
        // 授权票据接口不接受重定向，避免把上传令牌带往其他主机。
        .followRedirects(false)
        .followSslRedirects(false)
        .callTimeout(20, TimeUnit.SECONDS)
        .build()

    suspend fun loadConfig(serverUrl: String, attemptId: String): ChannelAuthConfig = withContext(Dispatchers.IO) {
        require(ChannelAuthSecurity.isValidAttemptId(attemptId)) { "授权票据格式无效" }
        val endpoint = serverUrl.toHttpUrlOrNull()
            ?.newBuilder()
            ?.addPathSegments("api/target-auth/native")
            ?.addPathSegment(attemptId)
            ?.build()
            ?: error("服务器地址无效")
        val response = client.newCall(Request.Builder().url(endpoint).get().build()).execute()
        response.use {
            val body = it.body?.string().orEmpty()
            if (!it.isSuccessful) throw IllegalStateException(apiMessage(body, "读取网页登录任务失败"))
            val json = JSONObject(body)
            val config = ChannelAuthConfig(
                id = json.getString("id"),
                kind = json.getString("kind"),
                baseUrl = json.getString("baseUrl"),
                loginUrl = json.getString("loginUrl"),
                captureToken = json.getString("captureToken"),
            )
            require(config.id == attemptId) { "网页登录任务不匹配" }
            require(config.kind == "new_api" || config.kind == "sub2api") { "渠道类型不受支持" }
            require(ChannelAuthSecurity.isAllowedHttpsUrl(config.baseUrl)) { "渠道必须使用 HTTPS" }
            require(ChannelAuthSecurity.isAllowedHttpsUrl(config.loginUrl)) { "登录地址必须使用 HTTPS" }
            require(ChannelAuthSecurity.sameOrigin(config.baseUrl, config.loginUrl)) { "登录地址与渠道来源不一致" }
            require(config.captureToken.length in 32..256) { "网页登录上传令牌无效" }
            config
        }
    }

    suspend fun captureNewAPI(
        serverUrl: String,
        config: ChannelAuthConfig,
        cookie: String,
        userId: String,
    ) = capture(serverUrl, config, JSONObject().put("cookie", cookie).put("userId", userId))

    suspend fun captureSub2API(
        serverUrl: String,
        config: ChannelAuthConfig,
        tokens: Sub2OAuthTokens,
    ) = capture(
        serverUrl,
        config,
        JSONObject().put("accessToken", tokens.accessToken).put("refreshToken", tokens.refreshToken),
    )

    private suspend fun capture(serverUrl: String, config: ChannelAuthConfig, payload: JSONObject) =
        withContext(Dispatchers.IO) {
            val endpoint = serverUrl.toHttpUrlOrNull()
                ?.newBuilder()
                ?.addPathSegments("api/target-auth/native")
                ?.addPathSegment(config.id)
                ?.addPathSegment("capture")
                ?.build()
                ?: error("服务器地址无效")
            val request = Request.Builder()
                .url(endpoint)
                .header("X-Target-Auth-Token", config.captureToken)
                .post(payload.toString().toRequestBody(JSON_MEDIA_TYPE))
                .build()
            val response = client.newCall(request).execute()
            response.use {
                val body = it.body?.string().orEmpty()
                if (!it.isSuccessful) throw IllegalStateException(apiMessage(body, "验证网页登录状态失败"))
            }
        }

    private fun apiMessage(body: String, fallback: String): String = runCatching {
        JSONObject(body).optString("message").takeIf(String::isNotBlank)
    }.getOrNull() ?: fallback

    private companion object {
        val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
    }
}
