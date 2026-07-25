package com.zzlye.poolwatch.auth

import java.net.URI
import java.net.URLDecoder
import java.nio.charset.StandardCharsets
import java.security.MessageDigest

data class Sub2OAuthTokens(
    val accessToken: String,
    val refreshToken: String,
)

class Sub2CaptureGuard {
    private var lastSubmittedFingerprint: ByteArray? = null

    fun shouldCapture(tokens: Sub2OAuthTokens, manual: Boolean): Boolean {
        val fingerprint = fingerprint(tokens)
        // 自动复查跳过已经提交过的同一组令牌；手工点击始终允许再次验证。
        if (!manual && lastSubmittedFingerprint?.contentEquals(fingerprint) == true) return false
        lastSubmittedFingerprint = fingerprint
        return true
    }

    private fun fingerprint(tokens: Sub2OAuthTokens): ByteArray {
        val digest = MessageDigest.getInstance("SHA-256")
        digest.update(tokens.accessToken.toByteArray(StandardCharsets.UTF_8))
        digest.update(byteArrayOf(0))
        return digest.digest(tokens.refreshToken.toByteArray(StandardCharsets.UTF_8))
    }
}

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
        val accessToken = sanitizeToken(values["access_token"] ?: values["accessToken"]).orEmpty()
        val refreshToken = sanitizeToken(values["refresh_token"] ?: values["refreshToken"]).orEmpty()
        return if (accessToken.isNotBlank() || refreshToken.isNotBlank()) Sub2OAuthTokens(accessToken, refreshToken) else null
    }

    fun parseEvaluatedSub2Tokens(rawResult: String?): Sub2OAuthTokens? {
        val encoded = rawResult.orEmpty().trim().removeSurrounding("\"")
        val separator = encoded.indexOf('|')
        if (separator < 0) return null
        val accessToken = sanitizeToken(decode(encoded.substring(0, separator))).orEmpty()
        val refreshToken = sanitizeToken(decode(encoded.substring(separator + 1))).orEmpty()
        return if (accessToken.isNotBlank() || refreshToken.isNotBlank()) Sub2OAuthTokens(accessToken, refreshToken) else null
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
