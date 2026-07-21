package com.zzlye.poolwatch.update

import org.json.JSONObject
import java.io.File
import java.io.FileInputStream
import java.net.URI
import java.security.MessageDigest
import java.util.Locale

/**
 * 服务器发布的安卓安装包信息。
 */
data class AppUpdateInfo(
    val versionCode: Int,
    val versionName: String,
    val tag: String,
    val downloadUrl: String,
    val sha256: String,
    val sizeBytes: Long,
    val mandatory: Boolean,
    val releaseUrl: String?,
    val releaseNotes: String?,
    val publishedAt: String?,
)

/**
 * 更新区域的显示状态。
 */
sealed interface AppUpdateState {
    data object Idle : AppUpdateState
    data object Checking : AppUpdateState
    data object Latest : AppUpdateState
    data class Available(val info: AppUpdateInfo) : AppUpdateState
    data class Downloading(
        val info: AppUpdateInfo,
        val downloadId: Long,
        val progress: Int?,
        val downloadedBytes: Long,
        val totalBytes: Long,
        val pausedReason: Int? = null,
    ) : AppUpdateState
    data class Ready(val info: AppUpdateInfo) : AppUpdateState
    data class Error(val message: String, val info: AppUpdateInfo? = null) : AppUpdateState
}

/**
 * 解析并校验更新元数据，避免把不可信地址直接交给下载器。
 */
object AppUpdateMetadataParser {
    const val MAX_APK_BYTES = 256L * 1024L * 1024L
    private val sha256Pattern = Regex("^[0-9a-fA-F]{64}$")
    private val versionNamePattern = Regex("^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$")
    fun parse(raw: String, metadataUrl: String? = null): Result<AppUpdateInfo> = runCatching {
        require(raw.toByteArray(Charsets.UTF_8).size <= 256 * 1024) { "更新信息过大" }
        val json = JSONObject(raw)
        val versionCode = json.getInt("versionCode")
        require(versionCode > 0) { "版本号无效" }
        val versionName = json.getString("versionName").trim()
        require(versionNamePattern.matches(versionName)) { "版本名称无效" }
        val tag = json.optString("tag", "android-v$versionName").trim()
        require(tag.isNotEmpty() && tag.length <= 128) { "发布标签无效" }
        val downloadUrl = validateDownloadUrl(json.getString("downloadUrl"), metadataUrl)
        val sha256 = json.getString("sha256").trim().lowercase()
        require(sha256Pattern.matches(sha256)) { "安装包校验值无效" }
        val sizeBytes = json.getLong("sizeBytes").also {
            require(it in 1..MAX_APK_BYTES) { "安装包大小无效" }
        }
        val releaseUrl = json.optNullableString("releaseUrl")?.let(::validateHttpsUrl)
        val releaseNotes = json.optNullableString("releaseNotes")?.take(10_000)
        val publishedAt = json.optNullableString("publishedAt")?.take(128)
        AppUpdateInfo(
            versionCode = versionCode,
            versionName = versionName,
            tag = tag,
            downloadUrl = downloadUrl,
            sha256 = sha256,
            sizeBytes = sizeBytes,
            mandatory = json.optBoolean("mandatory", false),
            releaseUrl = releaseUrl,
            releaseNotes = releaseNotes,
            publishedAt = publishedAt,
        )
    }

    fun toJson(info: AppUpdateInfo): String = JSONObject().apply {
        put("versionCode", info.versionCode)
        put("versionName", info.versionName)
        put("tag", info.tag)
        put("downloadUrl", info.downloadUrl)
        put("sha256", info.sha256)
        put("sizeBytes", info.sizeBytes)
        put("mandatory", info.mandatory)
        info.releaseUrl?.let { put("releaseUrl", it) }
        info.releaseNotes?.let { put("releaseNotes", it) }
        info.publishedAt?.let { put("publishedAt", it) }
    }.toString()

    fun isNewer(info: AppUpdateInfo, currentVersionCode: Int): Boolean =
        info.versionCode > currentVersionCode

    private fun validateDownloadUrl(raw: String, metadataUrl: String?): String {
        val source = metadataUrl?.let { URI(validateHttpsUrl(it)) }
        val resolved = if (source == null) {
            URI(validateHttpsUrl(raw))
        } else {
            source.resolve(raw.trim()).also {
                require(sameOrigin(source, it)) { "安装包地址必须与更新服务同源" }
            }
        }
        return validateHttpsUrl(resolved.toString())
    }

    private fun validateHttpsUrl(raw: String): String {
        val value = raw.trim()
        val uri = URI(value)
        require(uri.isAbsolute && uri.scheme.equals("https", ignoreCase = true)) {
            "更新地址必须使用 HTTPS"
        }
        require(uri.userInfo == null && !uri.host.isNullOrBlank()) { "更新地址无效" }
        return value
    }

    private fun sameOrigin(first: URI, second: URI): Boolean {
        fun effectivePort(uri: URI): Int = when {
            uri.port >= 0 -> uri.port
            uri.scheme.equals("https", ignoreCase = true) -> 443
            else -> -1
        }
        return first.scheme.equals(second.scheme, ignoreCase = true) &&
            first.host.equals(second.host, ignoreCase = true) &&
            effectivePort(first) == effectivePort(second)
    }

    private fun JSONObject.optNullableString(name: String): String? =
        if (!has(name) || isNull(name)) null else optString(name).trim().takeIf(String::isNotEmpty)
}

/**
 * 计算安装包摘要并与发布清单比对。
 */
object AppUpdateDigest {
    fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        FileInputStream(file).use { input ->
            val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
            while (true) {
                val count = input.read(buffer)
                if (count < 0) break
                if (count > 0) digest.update(buffer, 0, count)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }

    fun matches(file: File, expectedSha256: String, expectedSizeBytes: Long): Boolean {
        if (!file.isFile || file.length() <= 0L) return false
        if (file.length() != expectedSizeBytes) return false
        return runCatching { sha256(file).equals(expectedSha256, ignoreCase = true) }.getOrDefault(false)
    }
}

/**
 * 保存更新界面状态，Activity 重建后可以继续显示下载、安装或错误信息。
 */
object AppUpdateStateCodec {
    fun encode(state: AppUpdateState): String = JSONObject().apply {
        when (state) {
            AppUpdateState.Idle -> put("type", "idle")
            AppUpdateState.Checking -> put("type", "checking")
            AppUpdateState.Latest -> put("type", "latest")
            is AppUpdateState.Available -> {
                put("type", "available")
                putInfo(state.info)
            }
            is AppUpdateState.Downloading -> {
                put("type", "downloading")
                putInfo(state.info)
                put("downloadId", state.downloadId)
                state.progress?.let { put("progress", it) }
                put("downloadedBytes", state.downloadedBytes)
                put("totalBytes", state.totalBytes)
                state.pausedReason?.let { put("pausedReason", it) }
            }
            is AppUpdateState.Ready -> {
                put("type", "ready")
                putInfo(state.info)
            }
            is AppUpdateState.Error -> {
                put("type", "error")
                put("message", state.message.take(1_000))
                state.info?.let { info -> putInfo(info) }
            }
        }
    }.toString()

    fun decode(raw: String): AppUpdateState? = runCatching {
        val json = JSONObject(raw)
        when (json.getString("type")) {
            "idle" -> AppUpdateState.Idle
            // 检查协程不会随 Activity 一起恢复，重建后交给自动检查重新开始。
            "checking" -> AppUpdateState.Idle
            "latest" -> AppUpdateState.Latest
            "available" -> AppUpdateState.Available(json.getInfo())
            "downloading" -> AppUpdateState.Downloading(
                info = json.getInfo(),
                downloadId = json.getLong("downloadId"),
                progress = json.optIntOrNull("progress"),
                downloadedBytes = json.getLong("downloadedBytes"),
                totalBytes = json.getLong("totalBytes"),
                pausedReason = json.optIntOrNull("pausedReason"),
            )
            "ready" -> AppUpdateState.Ready(json.getInfo())
            "error" -> AppUpdateState.Error(
                message = json.getString("message"),
                info = if (json.has("info") && !json.isNull("info")) json.getInfo() else null,
            )
            else -> error("未知更新状态")
        }
    }.getOrNull()

    private fun JSONObject.putInfo(info: AppUpdateInfo) {
        put("info", JSONObject(AppUpdateMetadataParser.toJson(info)))
    }

    private fun JSONObject.getInfo(): AppUpdateInfo = AppUpdateMetadataParser.parse(
        getJSONObject("info").toString(),
    ).getOrThrow()

    private fun JSONObject.optIntOrNull(name: String): Int? =
        if (!has(name) || isNull(name)) null else getInt(name)
}

/**
 * 判断当前应用签名是否可以由待安装包安全接续。
 */
object AppUpdateSigningPolicy {
    fun isCompatible(
        installedCurrentSigners: Set<String>,
        candidateCurrentSigners: Set<String>,
        installedHasMultipleSigners: Boolean,
        candidateHasMultipleSigners: Boolean,
    ): Boolean {
        if (installedCurrentSigners.isEmpty() || candidateCurrentSigners.isEmpty()) return false
        return installedHasMultipleSigners == candidateHasMultipleSigners &&
            installedCurrentSigners == candidateCurrentSigners
    }
}

/**
 * 控制同一进程内的自动检查频率，并阻止 Activity 重建造成并发检查。
 */
class AutomaticUpdateCheckGate(
    private val intervalMillis: Long = 6L * 60L * 60L * 1_000L,
    private val retryMillis: Long = 15L * 60L * 1_000L,
) {
    private val nextAllowedAt = mutableMapOf<String, Long>()
    private val running = mutableSetOf<String>()

    @Synchronized
    fun begin(serverUrl: String, nowMillis: Long = System.currentTimeMillis()): Boolean {
        val key = normalizedServerKey(serverUrl)
        if (key in running) return false
        val nextAllowed = nextAllowedAt[key]
        if (nextAllowed != null && nowMillis < nextAllowed) return false
        running += key
        return true
    }

    @Synchronized
    fun finish(
        serverUrl: String,
        completed: Boolean,
        nowMillis: Long = System.currentTimeMillis(),
    ) {
        val key = normalizedServerKey(serverUrl)
        running -= key
        nextAllowedAt[key] = nowMillis + if (completed) intervalMillis else retryMillis
    }

    @Synchronized
    fun cancel(serverUrl: String) {
        running -= normalizedServerKey(serverUrl)
    }

    @Synchronized
    fun delayUntilNextCheck(
        serverUrl: String,
        nowMillis: Long = System.currentTimeMillis(),
    ): Long {
        val nextAllowed = nextAllowedAt[normalizedServerKey(serverUrl)] ?: return 0L
        return (nextAllowed - nowMillis).coerceAtLeast(0L)
    }

    private fun normalizedServerKey(serverUrl: String): String =
        serverUrl.trim().trimEnd('/').lowercase(Locale.ROOT)
}

/**
 * 把更新信息转换为适合小屏展示的文字。
 */
object AppUpdateFormatting {
    fun sizeLabel(sizeBytes: Long): String {
        return String.format(Locale.US, "%.1f MB", sizeBytes.toDouble() / 1024.0 / 1024.0)
    }
}
