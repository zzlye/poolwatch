package com.zzlye.poolwatch.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.nio.file.Files

class AppUpdateModelsTest {
    @Test
    fun `相对下载地址解析为当前更新服务的同源绝对地址`() {
        val info = AppUpdateMetadataParser.parse(
            validMetadata(downloadUrl = "/api/app/update/android/download?tag=android-v1.1.3"),
            "https://custom.example/api/app/update/android",
        ).getOrThrow()

        assertEquals(
            "https://custom.example/api/app/update/android/download?tag=android-v1.1.3",
            info.downloadUrl,
        )
    }

    @Test
    fun `拒绝更新服务返回的跨源下载地址`() {
        val result = AppUpdateMetadataParser.parse(
            validMetadata(downloadUrl = "https://other.example/poolwatch.apk"),
            "https://custom.example/api/app/update/android",
        )

        assertTrue(result.isFailure)
    }

    @Test
    fun `安装包大小必填且不得超过二百五十六兆字节`() {
        val missing = validMetadata().replace("\"sizeBytes\": 1572864,", "")
        val oversized = validMetadata().replace(
            "1572864",
            (AppUpdateMetadataParser.MAX_APK_BYTES + 1L).toString(),
        )

        assertTrue(AppUpdateMetadataParser.parse(missing).isFailure)
        assertTrue(AppUpdateMetadataParser.parse(oversized).isFailure)
    }

    @Test
    fun `使用版本编号判断是否需要更新`() {
        val info = AppUpdateMetadataParser.parse(validMetadata()).getOrThrow()

        assertTrue(AppUpdateMetadataParser.isNewer(info, 4))
        assertFalse(AppUpdateMetadataParser.isNewer(info, 5))
        assertFalse(AppUpdateMetadataParser.isNewer(info, 6))
    }

    @Test
    fun `安装包摘要和大小必须同时匹配`() {
        val directory = Files.createTempDirectory("poolwatch-update-test").toFile()
        val file = File(directory, "update.apk").apply { writeText("abc") }
        try {
            val sha256 = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
            assertTrue(AppUpdateDigest.matches(file, sha256, 3L))
            assertFalse(AppUpdateDigest.matches(file, sha256, 4L))
            assertFalse(AppUpdateDigest.matches(file, "0".repeat(64), 3L))
        } finally {
            directory.deleteRecursively()
        }
    }

    @Test
    fun `下载进度处理未知总量并限制到百分之百`() {
        assertEquals(
            50,
            AppUpdateManager.DownloadSnapshot(
                AppUpdateManager.DownloadStatus.RUNNING,
                50L,
                100L,
                null,
            ).progress,
        )
        assertNull(
            AppUpdateManager.DownloadSnapshot(
                AppUpdateManager.DownloadStatus.RUNNING,
                50L,
                -1L,
                null,
            ).progress,
        )
        assertEquals(
            100,
            AppUpdateManager.DownloadSnapshot(
                AppUpdateManager.DownloadStatus.SUCCESSFUL,
                120L,
                100L,
                null,
            ).progress,
        )
        assertTrue(
            AppUpdateManager.DownloadSnapshot(
                AppUpdateManager.DownloadStatus.RUNNING,
                101L,
                -1L,
                null,
            ).exceedsExpectedSize(100L),
        )
        assertTrue(
            AppUpdateManager.DownloadSnapshot(
                AppUpdateManager.DownloadStatus.RUNNING,
                10L,
                101L,
                null,
            ).exceedsExpectedSize(100L),
        )
    }

    @Test
    fun `签名集合必须非空并与当前签名完全一致`() {
        assertTrue(
            AppUpdateSigningPolicy.isCompatible(
                setOf("a", "b"),
                setOf("b", "a"),
                installedHasMultipleSigners = true,
                candidateHasMultipleSigners = true,
            ),
        )
        assertFalse(
            AppUpdateSigningPolicy.isCompatible(
                setOf("a"),
                setOf("a", "b"),
                installedHasMultipleSigners = false,
                candidateHasMultipleSigners = true,
            ),
        )
        assertFalse(
            AppUpdateSigningPolicy.isCompatible(
                emptySet(),
                emptySet(),
                installedHasMultipleSigners = false,
                candidateHasMultipleSigners = false,
            ),
        )
    }

    @Test
    fun `自动检查成功间隔六小时失败十五分钟重试`() {
        val gate = AutomaticUpdateCheckGate(intervalMillis = 100L, retryMillis = 20L)

        assertTrue(gate.begin("https://example.com/", nowMillis = 0L))
        assertFalse(gate.begin("https://example.com", nowMillis = 1L))
        gate.finish("https://example.com", completed = true, nowMillis = 10L)
        assertFalse(gate.begin("https://example.com", nowMillis = 109L))
        assertTrue(gate.begin("https://example.com", nowMillis = 110L))
        gate.finish("https://example.com", completed = false, nowMillis = 120L)
        assertFalse(gate.begin("https://example.com", nowMillis = 139L))
        assertTrue(gate.begin("https://example.com", nowMillis = 140L))
        gate.cancel("https://example.com")
        assertTrue(gate.begin("https://example.com", nowMillis = 141L))
    }

    @Test
    fun `暂停下载状态可在界面重建后恢复`() {
        val info = AppUpdateMetadataParser.parse(validMetadata()).getOrThrow()
        val state = AppUpdateState.Downloading(info, 9L, 35, 350L, 1_000L, 2)

        assertEquals(state, AppUpdateStateCodec.decode(AppUpdateStateCodec.encode(state)))
    }

    @Test
    fun `更新文件路径固定在专用目录`() {
        assertEquals("updates/poolwatch-5.apk", AppUpdateManager.updateRelativePath(5))
    }

    @Test
    fun `安装包大小按兆字节显示`() {
        assertEquals("1.5 MB", AppUpdateFormatting.sizeLabel(1_572_864L))
    }

    private fun validMetadata(downloadUrl: String = "https://custom.example/update.apk"): String = """
        {
          "versionCode": 5,
          "versionName": "1.1.3",
          "tag": "android-v1.1.3",
          "downloadUrl": "$downloadUrl",
          "sha256": "${"a".repeat(64)}",
          "sizeBytes": 1572864,
          "mandatory": false,
          "releaseUrl": "https://github.com/zzlye/poolwatch/releases/tag/android-v1.1.3",
          "releaseNotes": "修复问题",
          "publishedAt": "2026-07-21T00:00:00Z"
        }
    """.trimIndent()
}
