package com.zzlye.poolwatch.update

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

/**
 * 在网页外壳之上显示更新提示，避免用户必须先打开设置抽屉。
 */
@Composable
fun AppUpdateDialog(
    state: AppUpdateState,
    onDismiss: () -> Unit,
    onDownload: () -> Unit,
    onInstall: () -> Unit,
    onRetry: () -> Unit,
    onRetryDownload: () -> Unit,
    onCancelDownload: () -> Unit,
) {
    val info = when (state) {
        is AppUpdateState.Available -> state.info
        is AppUpdateState.Downloading -> state.info
        is AppUpdateState.Ready -> state.info
        is AppUpdateState.Error -> state.info
        else -> return
    } ?: return
    AlertDialog(
        onDismissRequest = { if (!info.mandatory) onDismiss() },
        title = { Text("发现号池监控更新") },
        text = {
            Column(
                modifier = Modifier
                    .heightIn(max = 360.dp)
                    .verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Text("新版本 ${info.versionName}")
                Text("安装包大小：${AppUpdateFormatting.sizeLabel(info.sizeBytes)}")
                info.releaseNotes?.takeIf(String::isNotBlank)?.let {
                    Text(it)
                }
                when (state) {
                    is AppUpdateState.Downloading -> {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            if (state.pausedReason == null) {
                                CircularProgressIndicator(modifier = Modifier.size(22.dp), strokeWidth = 2.dp)
                            }
                            Text(
                                if (state.pausedReason != null) {
                                    "下载已暂停，请检查网络后重试"
                                } else {
                                    state.progress?.let { "正在下载 $it%" } ?: "正在下载…"
                                },
                            )
                        }
                    }
                    is AppUpdateState.Ready -> Text("下载完成，校验通过，可以安装。")
                    is AppUpdateState.Error -> Text(state.message)
                    else -> Text("更新会保留服务器地址、登录状态和监控设置。")
                }
                Spacer(Modifier.height(1.dp))
            }
        },
        confirmButton = {
            when (state) {
                is AppUpdateState.Available -> Button(onClick = onDownload) { Text("下载更新") }
                is AppUpdateState.Ready -> Button(onClick = onInstall) { Text("安装更新") }
                is AppUpdateState.Error -> Button(onClick = onRetry) { Text("重新检查") }
                is AppUpdateState.Downloading -> if (state.pausedReason != null) {
                    Button(onClick = onRetryDownload) { Text("重新下载") }
                }
                else -> Unit
            }
        },
        dismissButton = {
            if (state is AppUpdateState.Downloading) {
                TextButton(onClick = onCancelDownload) { Text("取消下载") }
            } else if (!info.mandatory) {
                TextButton(onClick = onDismiss) {
                    Text(if (state is AppUpdateState.Ready) "稍后安装" else "稍后提醒")
                }
            }
        },
    )
}
