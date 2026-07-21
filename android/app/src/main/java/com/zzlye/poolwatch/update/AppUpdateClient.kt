package com.zzlye.poolwatch.update

import com.zzlye.poolwatch.BuildConfig
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * 只访问官方更新地址，不携带网页会话或渠道凭据。
 */
class AppUpdateClient(
    private val client: OkHttpClient = defaultClient(),
) {
    suspend fun fetchLatest(serverUrl: String): Result<AppUpdateInfo?> = withContext(Dispatchers.IO) {
        // 更新发布、撤回和安装包下载均以当前设置的服务器为唯一可信入口。
        runCatching { fetchSingle(metadataUrl(serverUrl)) }
    }

    private fun fetchSingle(url: String): AppUpdateInfo? {
        val request = Request.Builder()
            .url(url)
            .header("Accept", "application/json")
            .header("User-Agent", "PoolWatchAndroid/${BuildConfig.VERSION_NAME}")
            .get()
            .build()
        client.newCall(request).execute().use { response ->
            if (response.code == 404 || response.code == 204) return null
            if (!response.isSuccessful) throw IOException("更新服务暂时不可用（${response.code}）")
            val body = response.body ?: throw IOException("更新服务返回了空内容")
            if (body.contentLength() > MAX_METADATA_BYTES) throw IOException("更新信息过大")
            val bytes = body.source().readByteArray(MAX_METADATA_BYTES + 1L)
            if (bytes.size > MAX_METADATA_BYTES) throw IOException("更新信息过大")
            return AppUpdateMetadataParser.parse(bytes.toString(Charsets.UTF_8), url).getOrThrow()
        }
    }

    private fun metadataUrl(serverUrl: String): String {
        val normalized = serverUrl.trimEnd('/')
        return "$normalized/api/app/update/android"
    }

    companion object {
        private const val MAX_METADATA_BYTES = 256 * 1024

        private fun defaultClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(15, TimeUnit.SECONDS)
            .callTimeout(20, TimeUnit.SECONDS)
            .followRedirects(true)
            .followSslRedirects(true)
            .build()
    }
}
