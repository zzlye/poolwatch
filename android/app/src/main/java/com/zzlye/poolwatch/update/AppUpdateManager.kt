package com.zzlye.poolwatch.update

import android.app.DownloadManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.provider.Settings
import androidx.core.content.FileProvider
import androidx.core.net.toUri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

/**
 * 负责把更新检查、系统下载器、文件校验和安装器串联起来。
 */
class AppUpdateManager(
    context: Context,
    private val updateClient: AppUpdateClient = AppUpdateClient(),
) {
    private val appContext = context.applicationContext
    private val downloadManager = appContext.getSystemService(DownloadManager::class.java)
    private val preferences = appContext.getSharedPreferences(PREFERENCES_NAME, Context.MODE_PRIVATE)
    private val packageVerifier = AppUpdatePackageVerifier(appContext)

    suspend fun checkLatest(currentVersionCode: Int, serverUrl: String): Result<AppUpdateInfo?> =
        updateClient.fetchLatest(serverUrl).map { info ->
            info?.takeIf { AppUpdateMetadataParser.isNewer(it, currentVersionCode) }
        }

    /** 同一进程冷启动立即检查，之后每六小时允许一次。 */
    fun beginAutomaticCheck(serverUrl: String, nowMillis: Long = System.currentTimeMillis()): Boolean =
        automaticCheckGate.begin(serverUrl, nowMillis)

    fun finishAutomaticCheck(
        serverUrl: String,
        completed: Boolean,
        nowMillis: Long = System.currentTimeMillis(),
    ) = automaticCheckGate.finish(serverUrl, completed, nowMillis)

    fun cancelAutomaticCheck(serverUrl: String) = automaticCheckGate.cancel(serverUrl)

    fun delayUntilAutomaticCheck(
        serverUrl: String,
        nowMillis: Long = System.currentTimeMillis(),
    ): Long = automaticCheckGate.delayUntilNextCheck(serverUrl, nowMillis)

    fun enqueue(info: AppUpdateInfo): Long {
        cancelStoredDownload(removeFile = true)
        val destination = apkFile(info)
        destination.parentFile?.mkdirs()
        if (destination.exists()) destination.delete()
        val request = DownloadManager.Request(info.downloadUrl.toUri())
            .setTitle("号池监控 ${info.versionName}")
            .setDescription("正在下载应用更新")
            .setMimeType(APK_MIME_TYPE)
            .setAllowedOverMetered(true)
            .setAllowedOverRoaming(false)
            .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
            .setDestinationInExternalFilesDir(
                appContext,
                Environment.DIRECTORY_DOWNLOADS,
                updateRelativePath(info.versionCode),
            )
        val downloadId = downloadManager.enqueue(request)
        preferences.edit()
            .putLong(KEY_DOWNLOAD_ID, downloadId)
            .putString(KEY_DOWNLOAD_INFO, AppUpdateMetadataParser.toJson(info))
            .apply()
        return downloadId
    }

    fun storedDownload(): StoredDownload? {
        val downloadId = preferences.getLong(KEY_DOWNLOAD_ID, -1L)
        val rawInfo = preferences.getString(KEY_DOWNLOAD_INFO, null)
        if (downloadId <= 0L || rawInfo.isNullOrBlank()) return null
        val info = AppUpdateMetadataParser.parse(rawInfo).getOrElse {
            clearStoredDownload()
            return null
        }
        return StoredDownload(downloadId, info)
    }

    /**
     * 从持久化下载记录恢复界面状态，Activity 重建或进程恢复后仍能继续显示进度。
     */
    suspend fun restoreStoredDownload(currentVersionCode: Int): AppUpdateState? {
        val stored = storedDownload() ?: return null
        if (stored.info.versionCode <= currentVersionCode) {
            cancelStoredDownload(removeFile = true)
            return null
        }
        val snapshot = query(stored.downloadId)
        if (snapshot.exceedsExpectedSize(stored.info.sizeBytes)) {
            cancelStoredDownload(removeFile = true)
            return AppUpdateState.Error("安装包超过允许大小，已取消下载", stored.info)
        }
        return when (snapshot.status) {
            DownloadStatus.SUCCESSFUL -> verify(stored.info).fold(
                onSuccess = { AppUpdateState.Ready(stored.info) },
                onFailure = { AppUpdateState.Error(it.message ?: "安装包校验失败，请重新下载", stored.info) },
            )
            DownloadStatus.PENDING,
            DownloadStatus.RUNNING,
            DownloadStatus.PAUSED,
            -> AppUpdateState.Downloading(
                info = stored.info,
                downloadId = stored.downloadId,
                progress = snapshot.progress,
                downloadedBytes = snapshot.downloadedBytes,
                totalBytes = snapshot.totalBytes,
                pausedReason = snapshot.reason.takeIf { snapshot.status == DownloadStatus.PAUSED },
            )
            DownloadStatus.FAILED,
            DownloadStatus.UNKNOWN,
            -> {
                cancelStoredDownload(removeFile = true)
                AppUpdateState.Error("上次更新下载未完成，请重新下载", stored.info)
            }
        }
    }

    fun query(downloadId: Long): DownloadSnapshot {
        return runCatching {
            val cursor = downloadManager.query(DownloadManager.Query().setFilterById(downloadId))
            cursor.use {
                if (!it.moveToFirst()) return@runCatching DownloadSnapshot(DownloadStatus.UNKNOWN, 0L, -1L, null)
                val status = when (it.getInt(it.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS))) {
                    DownloadManager.STATUS_PENDING -> DownloadStatus.PENDING
                    DownloadManager.STATUS_RUNNING -> DownloadStatus.RUNNING
                    DownloadManager.STATUS_PAUSED -> DownloadStatus.PAUSED
                    DownloadManager.STATUS_SUCCESSFUL -> DownloadStatus.SUCCESSFUL
                    DownloadManager.STATUS_FAILED -> DownloadStatus.FAILED
                    else -> DownloadStatus.UNKNOWN
                }
                val downloadedBytes = it.getLong(
                    it.getColumnIndexOrThrow(DownloadManager.COLUMN_BYTES_DOWNLOADED_SO_FAR),
                )
                val totalBytes = it.getLong(
                    it.getColumnIndexOrThrow(DownloadManager.COLUMN_TOTAL_SIZE_BYTES),
                )
                val reason = if (status == DownloadStatus.FAILED || status == DownloadStatus.PAUSED) {
                    it.getInt(it.getColumnIndexOrThrow(DownloadManager.COLUMN_REASON))
                } else {
                    null
                }
                DownloadSnapshot(status, downloadedBytes, totalBytes, reason)
            }
        }.getOrDefault(DownloadSnapshot(DownloadStatus.UNKNOWN, 0L, -1L, null))
    }

    /**
     * 校验摘要、文件大小、包名和签名。清理动作全部吞掉异常，确保调用方始终拿到 Result。
     */
    suspend fun verify(info: AppUpdateInfo): Result<File> = withContext(Dispatchers.IO) {
        val fileResult = runCatching { apkFile(info) }
        val file = fileResult.getOrNull()
        if (file == null) {
            runCatching { clearStoredDownload() }
            return@withContext Result.failure(
                fileResult.exceptionOrNull() ?: Exception("下载目录不可用"),
            )
        }

        val result = runCatching {
            require(AppUpdateDigest.matches(file, info.sha256, info.sizeBytes)) {
                "安装包校验失败，请重新下载"
            }
            packageVerifier.verify(file, info.versionCode).getOrThrow()
            file
        }
        if (result.isFailure) {
            runCatching { file.delete() }
            runCatching { clearStoredDownload() }
        }
        result
    }

    fun canRequestPackageInstalls(): Boolean = appContext.packageManager.canRequestPackageInstalls()

    fun unknownSourcesSettingsIntent(): Intent = Intent(
        Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
        "package:${appContext.packageName}".toUri(),
    )

    fun installerIntent(info: AppUpdateInfo): Intent {
        val file = apkFile(info)
        val uri = FileProvider.getUriForFile(
            appContext,
            "${appContext.packageName}.fileprovider",
            file,
        )
        return Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, APK_MIME_TYPE)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
    }

    fun cancelStoredDownload(removeFile: Boolean) {
        val stored = storedDownload()
        if (stored != null) {
            runCatching { downloadManager.remove(stored.downloadId) }
            if (removeFile) runCatching { apkFile(stored.info).delete() }
        }
        clearStoredDownload()
    }

    fun clearStoredDownload() {
        runCatching {
            preferences.edit()
                .remove(KEY_DOWNLOAD_ID)
                .remove(KEY_DOWNLOAD_INFO)
                .apply()
        }
    }

    fun sameArtifact(first: AppUpdateInfo, second: AppUpdateInfo): Boolean =
        first.versionCode == second.versionCode &&
            first.sha256.equals(second.sha256, ignoreCase = true) &&
            first.sizeBytes == second.sizeBytes &&
            first.downloadUrl == second.downloadUrl

    private fun apkFile(info: AppUpdateInfo): File {
        val root = requireNotNull(appContext.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)) {
            "设备暂时没有可用的下载目录"
        }
        return File(root, updateRelativePath(info.versionCode))
    }

    data class StoredDownload(val downloadId: Long, val info: AppUpdateInfo)

    data class DownloadSnapshot(
        val status: DownloadStatus,
        val downloadedBytes: Long,
        val totalBytes: Long,
        val reason: Int?,
    ) {
        val progress: Int?
            get() = if (totalBytes > 0L) {
                ((downloadedBytes.coerceAtLeast(0L) * 100L) / totalBytes).coerceIn(0L, 100L).toInt()
            } else {
                null
            }

        fun exceedsExpectedSize(expectedBytes: Long): Boolean =
            downloadedBytes > expectedBytes || (totalBytes > expectedBytes && totalBytes > 0L)
    }

    enum class DownloadStatus {
        PENDING,
        RUNNING,
        PAUSED,
        SUCCESSFUL,
        FAILED,
        UNKNOWN,
    }

    companion object {
        private const val PREFERENCES_NAME = "poolwatch_updates"
        private const val KEY_DOWNLOAD_ID = "download_id"
        private const val KEY_DOWNLOAD_INFO = "download_info"
        private const val UPDATE_DIRECTORY = "updates"
        private const val APK_MIME_TYPE = "application/vnd.android.package-archive"
        private val automaticCheckGate = AutomaticUpdateCheckGate()

        fun updateRelativePath(versionCode: Int): String {
            require(versionCode > 0) { "版本号无效" }
            return "$UPDATE_DIRECTORY/poolwatch-$versionCode.apk"
        }
    }
}
