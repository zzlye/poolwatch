package com.zzlye.poolwatch.update

import android.content.Context
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.os.Build
import java.io.File
import java.security.MessageDigest

/**
 * 在调用系统安装器前校验安装包身份，避免完整但属于其他应用的 APK 被打开。
 */
class AppUpdatePackageVerifier(context: Context) {
    private val appContext = context.applicationContext
    private val packageManager = appContext.packageManager

    fun verify(file: File, expectedVersionCode: Int): Result<Unit> = runCatching {
        val candidate = archivePackageInfo(file)
            ?: error("系统未识别该安装包，请重新下载")
        require(candidate.packageName == appContext.packageName) {
            "安装包应用标识不匹配"
        }

        val installed = installedPackageInfo()
        require(candidate.compatVersionCode() == expectedVersionCode.toLong()) {
            "安装包版本与更新信息不一致"
        }
        require(candidate.compatVersionCode() > installed.compatVersionCode()) {
            "安装包版本未高于当前版本"
        }
        val installedIdentity = signingIdentity(installed)
        val candidateIdentity = signingIdentity(candidate)
        require(
            AppUpdateSigningPolicy.isCompatible(
                installedCurrentSigners = installedIdentity.currentSigners,
                candidateCurrentSigners = candidateIdentity.currentSigners,
                installedHasMultipleSigners = installedIdentity.hasMultipleSigners,
                candidateHasMultipleSigners = candidateIdentity.hasMultipleSigners,
            ),
        ) { "安装包签名与当前应用不一致" }
    }

    @Suppress("DEPRECATION")
    private fun archivePackageInfo(file: File): PackageInfo? {
        val flags = signingFlags()
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            packageManager.getPackageArchiveInfo(
                file.absolutePath,
                PackageManager.PackageInfoFlags.of(flags.toLong()),
            )
        } else {
            packageManager.getPackageArchiveInfo(file.absolutePath, flags)
        }
    }

    @Suppress("DEPRECATION")
    private fun installedPackageInfo(): PackageInfo {
        val flags = signingFlags()
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            packageManager.getPackageInfo(
                appContext.packageName,
                PackageManager.PackageInfoFlags.of(flags.toLong()),
            )
        } else {
            packageManager.getPackageInfo(appContext.packageName, flags)
        }
    }

    @Suppress("DEPRECATION")
    private fun signingFlags(): Int = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
        PackageManager.GET_SIGNING_CERTIFICATES
    } else {
        PackageManager.GET_SIGNATURES
    }

    @Suppress("DEPRECATION")
    private fun signingIdentity(info: PackageInfo): SigningIdentity {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) {
            val signers = info.signatures.orEmpty().mapTo(linkedSetOf()) { signature -> signature.sha256() }
            return SigningIdentity(signers, signers.size > 1)
        }

        val signingInfo = info.signingInfo ?: return SigningIdentity(emptySet(), false)
        val current = signingInfo.apkContentsSigners.orEmpty()
            .mapTo(linkedSetOf()) { signature -> signature.sha256() }
        return SigningIdentity(current, signingInfo.hasMultipleSigners())
    }

    private fun Signature.sha256(): String = MessageDigest.getInstance("SHA-256")
        .digest(toByteArray())
        .joinToString("") { byte -> "%02x".format(byte.toInt() and 0xff) }

    private data class SigningIdentity(
        val currentSigners: Set<String>,
        val hasMultipleSigners: Boolean,
    )

    @Suppress("DEPRECATION")
    private fun PackageInfo.compatVersionCode(): Long =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) longVersionCode else versionCode.toLong()
}
