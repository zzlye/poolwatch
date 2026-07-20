package com.zzlye.poolwatch.auth

import java.net.URI
import java.net.URLDecoder
import java.nio.charset.StandardCharsets

data class Sub2OAuthTokens(
    val accessToken: String,
    val refreshToken: String,
)

object ChannelAuthSecurity {
    private val attemptPattern = Regex("^auth_[a-f0-9]{32}$")

    fun isValidAttemptId(value: String): Boolean = attemptPattern.matches(value)

    fun attemptIdFromLaunchUrl(rawUrl: String): String? {
        val uri = runCatching { URI(rawUrl) }.getOrNull() ?: return null
        if (!uri.scheme.equals("poolwatch-auth", true) || !uri.host.equals("start", true)) return null
        val attemptId = uri.path.orEmpty().trim('/')
        return attemptId.takeIf(::isValidAttemptId)
    }

    fun sameOrigin(first: String, second: String): Boolean {
        val left = runCatching { URI(first) }.getOrNull() ?: return false
        val right = runCatching { URI(second) }.getOrNull() ?: return false
        if (left.host.isNullOrBlank() || right.host.isNullOrBlank()) return false
        return left.scheme.equals(right.scheme, true) &&
            left.host.equals(right.host, true) &&
            effectivePort(left) == effectivePort(right)
    }

    fun isAllowedHttpsUrl(rawUrl: String): Boolean {
        val uri = runCatching { URI(rawUrl) }.getOrNull() ?: return false
        return uri.scheme.equals("https", true) && !uri.host.isNullOrBlank() && uri.userInfo == null
    }

    fun sanitizeCookie(rawCookie: String?): String? {
        val cookie = rawCookie.orEmpty().trim()
        if (cookie.isBlank() || cookie.length > MAX_COOKIE_LENGTH || cookie.contains('\r') || cookie.contains('\n')) {
            return null
        }
        return cookie
    }

    fun parseSub2Tokens(rawUrl: String): Sub2OAuthTokens? {
        val uri = runCatching { URI(rawUrl) }.getOrNull() ?: return null
        val values = parseFragment(uri.rawFragment.orEmpty())
        val accessToken = sanitizeToken(values["access_token"] ?: values["accessToken"]) ?: return null
        val refreshToken = sanitizeToken(values["refresh_token"] ?: values["refreshToken"]).orEmpty()
        return Sub2OAuthTokens(accessToken, refreshToken)
    }

    fun parseEvaluatedUserId(rawResult: String?): String {
        val value = rawResult.orEmpty().trim().removeSurrounding("\"")
            .replace("\\\"", "\"")
            .trim()
        return value.takeIf { it.matches(Regex("^[0-9]{1,20}$")) }.orEmpty()
    }

    private fun parseFragment(fragment: String): Map<String, String> = fragment
        .split('&')
        .asSequence()
        .mapNotNull { item ->
            val separator = item.indexOf('=')
            if (separator <= 0) return@mapNotNull null
            val key = decode(item.substring(0, separator))
            val value = decode(item.substring(separator + 1))
            key to value
        }
        .toMap()

    private fun sanitizeToken(rawToken: String?): String? {
        val token = rawToken.orEmpty().trim()
        if (token.isBlank() || token.length > MAX_TOKEN_LENGTH || token.contains('\r') || token.contains('\n')) return null
        return token
    }

    private fun decode(value: String): String = runCatching {
        URLDecoder.decode(value, StandardCharsets.UTF_8.name())
    }.getOrDefault(value)

    private fun effectivePort(uri: URI): Int = when {
        uri.port >= 0 -> uri.port
        uri.scheme.equals("https", true) -> 443
        uri.scheme.equals("http", true) -> 80
        else -> -1
    }

    private const val MAX_COOKIE_LENGTH = 16 * 1024
    private const val MAX_TOKEN_LENGTH = 64 * 1024
}
