package com.zzlye.poolwatch.auth

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.os.Message
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebStorage
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import com.zzlye.poolwatch.BuildConfig
import com.zzlye.poolwatch.config.ServerUrlValidator
import com.zzlye.poolwatch.ui.PoolWatchTheme
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.net.URI

class ChannelAuthActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val attemptId = intent.getStringExtra(EXTRA_ATTEMPT_ID).orEmpty()
        val serverUrl = ServerUrlValidator.normalize(intent.getStringExtra(EXTRA_SERVER_URL).orEmpty())
            .getOrElse { "" }
        setContent {
            PoolWatchTheme {
                ChannelAuthRoute(attemptId = attemptId, serverUrl = serverUrl, onClose = ::finish)
            }
        }
    }

    companion object {
        private const val EXTRA_ATTEMPT_ID = "target_auth_attempt_id"
        private const val EXTRA_SERVER_URL = "target_auth_server_url"

        fun createIntent(context: Context, attemptId: String, serverUrl: String): Intent =
            Intent(context, ChannelAuthActivity::class.java)
                .putExtra(EXTRA_ATTEMPT_ID, attemptId)
                .putExtra(EXTRA_SERVER_URL, serverUrl)
    }
}

@Composable
private fun ChannelAuthRoute(attemptId: String, serverUrl: String, onClose: () -> Unit) {
    val client = remember { ChannelAuthClient() }
    var config by remember { mutableStateOf<ChannelAuthConfig?>(null) }
    var error by remember { mutableStateOf("") }

    LaunchedEffect(attemptId, serverUrl) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) {
            error = "当前安卓版本请在渠道网页登录后，回到向导手工粘贴令牌或 Cookie。"
            return@LaunchedEffect
        }
        if (!ChannelAuthSecurity.isValidAttemptId(attemptId)) {
            error = "网页登录任务格式无效。"
            return@LaunchedEffect
        }
        if (serverUrl.isBlank()) {
            error = "号池监控服务器地址无效。"
            return@LaunchedEffect
        }
        runCatching { client.loadConfig(serverUrl, attemptId) }
            .onSuccess { config = it }
            .onFailure { error = it.message ?: "读取网页登录任务失败。" }
    }

    when {
        error.isNotBlank() -> ChannelAuthMessage(error, onClose)
        config == null -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            CircularProgressIndicator()
        }
        else -> ChannelAuthBrowser(
            serverUrl = serverUrl,
            config = requireNotNull(config),
            client = client,
            onClose = onClose,
        )
    }
}

@Composable
private fun ChannelAuthMessage(message: String, onClose: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(28.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(message, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(18.dp))
        Button(onClick = onClose) { Text("返回渠道向导") }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun ChannelAuthBrowser(
    serverUrl: String,
    config: ChannelAuthConfig,
    client: ChannelAuthClient,
    onClose: () -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var webView by remember { mutableStateOf<WebView?>(null) }
    var currentHost by remember { mutableStateOf(hostLabel(config.loginUrl)) }
    var message by remember { mutableStateOf("请在渠道页面选择 GitHub、Linux.do 或其他可用方式登录。") }
    var loading by remember { mutableStateOf(true) }
    var capturePending by remember { mutableStateOf(false) }
    var completed by remember { mutableStateOf(false) }
    var visitedExternalProvider by remember { mutableStateOf(false) }

    fun closeAndClean() {
        val closingWebView = webView
        webView = null
        clearTemporaryBrowser(closingWebView)
        onClose()
    }

    fun finishSuccess() {
        completed = true
        message = "网页登录成功，正在返回渠道向导。"
        scope.launch {
            delay(700)
            closeAndClean()
        }
    }

    fun submitSub2(currentUrl: String, manual: Boolean) {
        if (capturePending || completed) return
        if (!ChannelAuthSecurity.sameOrigin(currentUrl, config.baseUrl)) {
            if (manual) message = "请先完成登录并返回渠道站点。"
            return
        }
        val tokens = ChannelAuthSecurity.parseSub2Tokens(currentUrl)
        if (tokens == null) {
            if (manual) message = "当前页面没有发现登录令牌，请确认授权已经完成。"
            return
        }
        capturePending = true
        message = "正在验证网页登录状态…"
        scope.launch {
            runCatching { client.captureSub2API(serverUrl, config, tokens) }
                .onSuccess { finishSuccess() }
                .onFailure { message = it.message ?: "验证网页登录状态失败。" }
            capturePending = false
        }
    }

    fun submitNewAPI(view: WebView, currentUrl: String, manual: Boolean) {
        if (capturePending || completed) return
        if (!ChannelAuthSecurity.sameOrigin(currentUrl, config.baseUrl)) {
            if (manual) message = "请先完成登录并返回渠道站点。"
            return
        }
        val cookie = ChannelAuthSecurity.sanitizeCookie(
            CookieManager.getInstance().getCookie(config.baseUrl),
        )
        if (cookie == null) {
            if (manual) message = "当前渠道还没有登录会话，请先完成授权。"
            return
        }
        capturePending = true
        message = "正在验证网页登录状态…"
        // 只在目标渠道同源页面读取少量白名单键，用户 ID 仍会由服务器再次校验。
        view.evaluateJavascript(NEW_API_USER_ID_SCRIPT) { rawUserId ->
            val userId = ChannelAuthSecurity.parseEvaluatedUserId(rawUserId)
            scope.launch {
                runCatching { client.captureNewAPI(serverUrl, config, cookie, userId) }
                    .onSuccess { finishSuccess() }
                    .onFailure { message = it.message ?: "验证网页登录状态失败。" }
                capturePending = false
            }
        }
    }

    fun submit(view: WebView?, manual: Boolean) {
        val readyView = view ?: return
        val currentUrl = readyView.url.orEmpty()
        if (config.kind == "sub2api") submitSub2(currentUrl, manual)
        else submitNewAPI(readyView, currentUrl, manual)
    }

    BackHandler {
        if (webView?.canGoBack() == true && !completed) webView?.goBack() else closeAndClean()
    }

    DisposableEffect(Unit) {
        onDispose { clearTemporaryBrowser(webView) }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = ::closeAndClean) {
                        Icon(Icons.Default.Close, contentDescription = "关闭网页登录")
                    }
                },
                title = {
                    Column {
                        Text("渠道网页登录", style = MaterialTheme.typography.titleMedium)
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Icon(Icons.Default.Lock, contentDescription = null)
                            Text(currentHost, style = MaterialTheme.typography.labelSmall)
                        }
                    }
                },
            )
        },
        bottomBar = {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(MaterialTheme.colorScheme.surface)
                    .padding(14.dp),
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                Text(message, style = MaterialTheme.typography.bodySmall)
                Button(
                    onClick = { submit(webView, true) },
                    enabled = !capturePending && !completed,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    if (capturePending) {
                        CircularProgressIndicator(
                            modifier = Modifier.padding(end = 10.dp),
                            strokeWidth = 2.dp,
                        )
                    }
                    Text(if (completed) "登录成功" else "我已完成登录")
                }
            }
        },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            AndroidView(
                modifier = Modifier.fillMaxSize(),
                factory = { viewContext ->
                    val cookieManager = CookieManager.getInstance().apply { setAcceptCookie(true) }
                    WebStorage.getInstance().deleteAllData()
                    WebView(viewContext).apply {
                        configureChannelAuthSettings(this)
                        cookieManager.setAcceptThirdPartyCookies(this, true)
                        webViewClient = object : WebViewClient() {
                            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                                val url = request.url.toString()
                                if (!request.isForMainFrame) return false
                                if (!ChannelAuthSecurity.isAllowedHttpsUrl(url)) {
                                    message = "已阻止渠道尝试打开不安全的链接。"
                                    return true
                                }
                                if (config.kind == "sub2api" &&
                                    ChannelAuthSecurity.sameOrigin(url, config.baseUrl) &&
                                    ChannelAuthSecurity.parseSub2Tokens(url) != null
                                ) {
                                    // URL 片段不会发送到服务器，因此在页面脚本清理地址前立即采集。
                                    submitSub2(url, false)
                                }
                                return false
                            }

                            override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
                                loading = true
                                val currentUrl = url.orEmpty()
                                currentHost = hostLabel(currentUrl)
                                if (ChannelAuthSecurity.isAllowedHttpsUrl(currentUrl) &&
                                    !ChannelAuthSecurity.sameOrigin(currentUrl, config.baseUrl)
                                ) {
                                    visitedExternalProvider = true
                                }
                            }

                            override fun onPageFinished(view: WebView, url: String?) {
                                loading = false
                                CookieManager.getInstance().flush()
                                val currentUrl = url.orEmpty()
                                currentHost = hostLabel(currentUrl)
                                val returnedToTarget = ChannelAuthSecurity.sameOrigin(currentUrl, config.baseUrl)
                                val hasSub2Tokens = config.kind == "sub2api" &&
                                    ChannelAuthSecurity.parseSub2Tokens(currentUrl) != null
                                if (returnedToTarget && (visitedExternalProvider || hasSub2Tokens)) {
                                    submit(view, false)
                                }
                            }

                            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                                handler.cancel()
                                loading = false
                                message = "渠道证书校验失败，网页登录已经停止。"
                                view.stopLoading()
                            }

                            override fun onReceivedError(
                                view: WebView,
                                request: WebResourceRequest,
                                error: WebResourceError,
                            ) {
                                if (request.isForMainFrame) {
                                    loading = false
                                    message = "登录页面加载失败，请检查网络后重试。"
                                }
                            }
                        }
                        webChromeClient = channelAuthChromeClient(this)
                        webView = this
                        // 清理完成后再载入页面，避免上一次未完成授权留下的 Cookie 被新任务复用。
                        cookieManager.removeAllCookies {
                            cookieManager.flush()
                            post {
                                if (webView === this && !completed) loadUrl(config.loginUrl)
                            }
                        }
                    }
                },
            )
            if (loading || capturePending) {
                LinearProgressIndicator(Modifier.fillMaxWidth())
            }
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
private fun configureChannelAuthSettings(webView: WebView) {
    webView.settings.apply {
        javaScriptEnabled = true
        domStorageEnabled = true
        cacheMode = WebSettings.LOAD_NO_CACHE
        allowFileAccess = false
        allowContentAccess = false
        javaScriptCanOpenWindowsAutomatically = false
        setSupportMultipleWindows(true)
        mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        safeBrowsingEnabled = true
        userAgentString = "$userAgentString PoolWatchChannelAuth/${BuildConfig.VERSION_NAME}"
    }
}

private fun channelAuthChromeClient(owner: WebView): WebChromeClient = object : WebChromeClient() {
    override fun onCreateWindow(
        view: WebView,
        isDialog: Boolean,
        isUserGesture: Boolean,
        resultMsg: Message,
    ): Boolean {
        if (!isUserGesture) return false
        val popup = WebView(view.context)
        configureChannelAuthSettings(popup)
        CookieManager.getInstance().setAcceptThirdPartyCookies(popup, true)
        popup.webViewClient = object : WebViewClient() {
            private fun forward(url: String): Boolean {
                if (!ChannelAuthSecurity.isAllowedHttpsUrl(url)) {
                    popup.stopLoading()
                    popup.destroy()
                    return true
                }
                owner.loadUrl(url)
                popup.stopLoading()
                popup.destroy()
                return true
            }

            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean =
                forward(request.url.toString())

            override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
                if (!url.isNullOrBlank() && url != "about:blank") forward(url)
            }
        }
        val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
        transport.webView = popup
        resultMsg.sendToTarget()
        return true
    }
}

private fun clearTemporaryBrowser(webView: WebView?) {
    webView?.apply {
        stopLoading()
        clearHistory()
        clearFormData()
        clearCache(true)
        webChromeClient = null
        webViewClient = WebViewClient()
        destroy()
    }
    WebStorage.getInstance().deleteAllData()
    CookieManager.getInstance().removeAllCookies { CookieManager.getInstance().flush() }
}

private fun hostLabel(rawUrl: String): String = runCatching { URI(rawUrl).host }
    .getOrNull()
    .orEmpty()
    .ifBlank { "安全登录窗口" }

private const val NEW_API_USER_ID_SCRIPT = """
    (function(){try{var keys=['user','new-api-user','user_info','userInfo'];for(var i=0;i<keys.length;i++){var raw=localStorage.getItem(keys[i]);if(!raw)continue;try{var value=JSON.parse(raw);var id=value&&((value.id)||(value.user_id)||(value.userId)||(value.data&&value.data.id));if(id!==undefined&&id!==null)return String(id);}catch(e){}}var simple=['user_id','userId'];for(var j=0;j<simple.length;j++){var direct=localStorage.getItem(simple[j]);if(direct&&/^\d+$/.test(direct))return direct;}}catch(e){}return '';})()
"""
