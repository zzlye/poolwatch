package com.zzlye.poolwatch.network

import com.zzlye.poolwatch.config.AppSettings
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONArray
import java.io.IOException
import java.util.concurrent.TimeUnit

class PoolWatchApi(
    private val settings: AppSettings,
) {
    private val cookieInterceptor = WebViewCookieInterceptor(settings)

    val httpClient: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(20, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .writeTimeout(20, TimeUnit.SECONDS)
        // 管理接口不接受重定向，避免手工附加的同源 Cookie 被带到其他主机。
        .followRedirects(false)
        .followSslRedirects(false)
        .addInterceptor(cookieInterceptor)
        .build()

    val eventClient: OkHttpClient = buildEventClient(httpClient.newBuilder())

    fun endpoint(path: String): HttpUrl {
        val base = settings.serverUrl.trimEnd('/').toHttpUrl()
        return base.resolve(path) ?: error("接口地址无效")
    }

    @Throws(IOException::class, ApiException::class)
    fun fetchAlerts(): List<AlertRecord> {
        val request = Request.Builder()
            .url(endpoint("/api/alerts?status=all"))
            .header("Accept", "application/json")
            .header("User-Agent", "PoolWatchAndroid/${com.zzlye.poolwatch.BuildConfig.VERSION_NAME}")
            .build()
        httpClient.newCall(request).execute().use { response ->
            if (response.code == 401) throw ApiException(401, "登录状态已经失效")
            if (!response.isSuccessful) throw ApiException(response.code, "读取告警失败")
            val body = response.body?.string() ?: throw IOException("服务器响应为空")
            val array = JSONArray(body)
            return buildList {
                for (index in 0 until array.length()) {
                    AlertRecord.fromJson(array.optJSONObject(index) ?: continue)?.let(::add)
                }
            }
        }
    }

    companion object {
        const val EVENT_READ_TIMEOUT_SECONDS = 75L

        internal fun buildEventClient(builder: OkHttpClient.Builder): OkHttpClient = builder
            // 服务端每二十五秒发送心跳，连续三次没有任何数据时重建静默连接。
            .readTimeout(EVENT_READ_TIMEOUT_SECONDS, TimeUnit.SECONDS)
            .callTimeout(0, TimeUnit.MILLISECONDS)
            .build()
    }
}

class ApiException(
    val statusCode: Int,
    message: String,
) : IOException(message)
