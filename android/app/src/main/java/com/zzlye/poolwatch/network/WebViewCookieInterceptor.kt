package com.zzlye.poolwatch.network

import android.webkit.CookieManager
import com.zzlye.poolwatch.config.AppSettings
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Interceptor
import okhttp3.Response

class WebViewCookieInterceptor(
    private val settings: AppSettings,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val configured = settings.serverUrl.toHttpUrlOrNull()
        val requestUrl = request.url
        val sameOrigin = configured != null &&
            configured.scheme == requestUrl.scheme &&
            configured.host == requestUrl.host &&
            configured.port == requestUrl.port
        if (!sameOrigin) return chain.proceed(request)

        // CookieManager 返回可直接用于 Cookie 请求头的同源 Cookie，原始值不写入日志或本地配置。
        val cookieHeader = CookieManager.getInstance().getCookie(requestUrl.toString()).orEmpty()
        val requestSession = SessionCookiePolicy.sessionValue(cookieHeader)
        val authenticatedRequest = if (cookieHeader.isBlank()) {
            request
        } else {
            request.newBuilder().header("Cookie", cookieHeader).build()
        }
        val response = chain.proceed(authenticatedRequest)
        val currentSession = SessionCookiePolicy.sessionValue(
            CookieManager.getInstance().getCookie(requestUrl.toString()),
        )
        var cookieUpdated = false
        response.headers("Set-Cookie")
            .asSequence()
            .filter { it.trimStart().startsWith("poolwatch_session=", ignoreCase = true) }
            .filter {
                SessionCookiePolicy.shouldApplyResponseCookie(
                    requestSession = requestSession,
                    currentSession = currentSession,
                )
            }
            .forEach { setCookie ->
                // 迟到的旧请求不能覆盖网页刚写入的新会话。
                CookieManager.getInstance().setCookie(requestUrl.toString(), setCookie)
                cookieUpdated = true
            }
        if (cookieUpdated) CookieManager.getInstance().flush()
        return response
    }
}

object SessionCookiePolicy {
    fun sessionValue(cookieHeader: String?): String? = cookieHeader
        .orEmpty()
        .split(';')
        .asSequence()
        .map(String::trim)
        .firstOrNull { it.startsWith("poolwatch_session=", ignoreCase = true) }
        ?.substringAfter('=')
        ?.takeIf(String::isNotBlank)

    fun shouldApplyResponseCookie(requestSession: String?, currentSession: String?): Boolean =
        requestSession == currentSession
}
