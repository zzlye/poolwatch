package com.zzlye.poolwatch

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Message
import android.os.PowerManager
import android.provider.Settings
import android.webkit.CookieManager
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.BatterySaver
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DrawerValue
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.material3.ModalNavigationDrawer
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.core.net.toUri
import com.zzlye.poolwatch.config.AppSettings
import com.zzlye.poolwatch.config.MonitorStatus
import com.zzlye.poolwatch.config.ServerUrlValidator
import com.zzlye.poolwatch.monitoring.MonitoringScheduler
import com.zzlye.poolwatch.monitoring.NotificationHelper
import com.zzlye.poolwatch.monitoring.RealtimeMonitorService
import com.zzlye.poolwatch.monitoring.SeenAlertStore
import com.zzlye.poolwatch.ui.PoolWatchTheme
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    private var requestedAlertId by mutableStateOf<String?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        requestedAlertId = intent.getStringExtra(EXTRA_ALERT_ID)
        setContent {
            PoolWatchTheme {
                PoolWatchApp(
                    requestedAlertId = requestedAlertId,
                    onAlertOpened = { requestedAlertId = null },
                )
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        requestedAlertId = intent.getStringExtra(EXTRA_ALERT_ID)
    }

    companion object {
        const val EXTRA_ALERT_ID = "alert_id"
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun PoolWatchApp(
    requestedAlertId: String?,
    onAlertOpened: () -> Unit,
) {
    val context = LocalContext.current
    val activity = context as Activity
    val settings = remember { AppSettings(context) }
    val drawerState = androidx.compose.material3.rememberDrawerState(DrawerValue.Closed)
    val scope = rememberCoroutineScope()
    val snackbar = remember { SnackbarHostState() }
    var serverUrl by remember { mutableStateOf(settings.serverUrl) }
    var serverDraft by remember { mutableStateOf(serverUrl) }
    var monitoringEnabled by remember { mutableStateOf(settings.monitoringEnabled) }
    var monitorStatus by remember {
        mutableStateOf(MonitorStatus.fromValue(settings.monitorStatus))
    }
    var reloadSignal by remember { mutableIntStateOf(0) }
    var recreateSignal by remember { mutableIntStateOf(0) }
    var webView by remember { mutableStateOf<WebView?>(null) }
    var pendingTestNotification by remember { mutableStateOf(false) }

    val notificationPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted && pendingTestNotification) {
            NotificationHelper.showTest(context)
        } else if (!granted) {
            scope.launch { snackbar.showSnackbar("通知权限未开启，请在系统设置中允许通知") }
        }
        pendingTestNotification = false
    }

    DisposableEffect(Unit) {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(receiverContext: Context?, intent: Intent?) {
                monitorStatus = MonitorStatus.fromValue(
                    intent?.getStringExtra(RealtimeMonitorService.EXTRA_STATUS).orEmpty(),
                )
                if (intent?.hasExtra(RealtimeMonitorService.EXTRA_ENABLED) == true) {
                    monitoringEnabled = intent.getBooleanExtra(RealtimeMonitorService.EXTRA_ENABLED, false)
                }
            }
        }
        ContextCompat.registerReceiver(
            context,
            receiver,
            IntentFilter(RealtimeMonitorService.ACTION_STATUS_CHANGED),
            ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        onDispose { context.unregisterReceiver(receiver) }
    }

    LaunchedEffect(Unit) {
        if (monitoringEnabled) {
            MonitoringScheduler.schedulePeriodic(context)
            MonitoringScheduler.startRealtimeService(context)
        }
    }

    LaunchedEffect(monitoringEnabled, monitorStatus, serverUrl) {
        if (!monitoringEnabled) return@LaunchedEffect
        // 网页登录和退出均由前端请求完成，不一定触发整页跳转，因此观察会话变化并同步原生监听。
        val previousSession = sessionCookieValue(serverUrl)
        while (true) {
            delay(3_000)
            val currentSession = sessionCookieValue(serverUrl)
            val loggedInAgain = monitorStatus == MonitorStatus.LOGIN_REQUIRED &&
                currentSession != null && currentSession != previousSession
            val loggedOut = monitorStatus != MonitorStatus.LOGIN_REQUIRED &&
                previousSession != null && currentSession == null
            if (loggedInAgain || loggedOut) {
                MonitoringScheduler.reconnect(context)
                break
            }
        }
    }

    LaunchedEffect(requestedAlertId, webView) {
        val alertId = requestedAlertId ?: return@LaunchedEffect
        val readyWebView = webView ?: return@LaunchedEffect
        readyWebView.loadUrl("$serverUrl/alerts?focus=${Uri.encode(alertId)}")
        onAlertOpened()
    }

    BackHandler {
        when {
            drawerState.isOpen -> scope.launch { drawerState.close() }
            webView?.canGoBack() == true -> webView?.goBack()
            else -> activity.finish()
        }
    }

    ModalNavigationDrawer(
        drawerState = drawerState,
        drawerContent = {
            ModalDrawerSheet(modifier = Modifier.width(340.dp)) {
                NativeSettingsPanel(
                    serverDraft = serverDraft,
                    onServerDraftChange = { serverDraft = it },
                    monitoringEnabled = monitoringEnabled,
                    monitorStatus = monitorStatus,
                    onMonitoringChange = { enabled ->
                        monitoringEnabled = enabled
                        if (enabled) {
                            MonitoringScheduler.enable(context)
                            monitorStatus = MonitorStatus.CONNECTING
                            val permissionRequested = requestNotificationPermissionIfNeeded(
                                context,
                                notificationPermissionLauncher::launch,
                            )
                            if (!permissionRequested && !NotificationHelper.canPostAlerts(context)) {
                                scope.launch { snackbar.showSnackbar("持续监控已开启，请在系统设置中允许通知") }
                            }
                        } else {
                            MonitoringScheduler.disable(context)
                            monitorStatus = MonitorStatus.STOPPED
                        }
                    },
                    onSaveServer = {
                        ServerUrlValidator.normalize(serverDraft).fold(
                            onSuccess = { normalized ->
                                if (normalized != serverUrl) {
                                    val previousUrl = serverUrl
                                    CookieManager.getInstance().setCookie(
                                        previousUrl,
                                        "poolwatch_session=; Max-Age=0; Path=/; Secure; HttpOnly; SameSite=Strict",
                                    ) {
                                        CookieManager.getInstance().flush()
                                        settings.serverUrl = normalized
                                        settings.resetForServerChange()
                                        SeenAlertStore(context).clear()
                                        serverUrl = normalized
                                        serverDraft = normalized
                                        recreateSignal++
                                        if (monitoringEnabled) MonitoringScheduler.reconnect(context)
                                        scope.launch { snackbar.showSnackbar("服务器地址已保存") }
                                    }
                                } else {
                                    scope.launch { snackbar.showSnackbar("服务器地址已保存") }
                                }
                            },
                            onFailure = { error ->
                                scope.launch { snackbar.showSnackbar(error.message ?: "服务器地址无效") }
                            },
                        )
                    },
                    onTestNotification = {
                        if (NotificationHelper.canPostAlerts(context)) {
                            NotificationHelper.showTest(context)
                        } else {
                            pendingTestNotification = true
                            val permissionRequested = requestNotificationPermissionIfNeeded(
                                context,
                                notificationPermissionLauncher::launch,
                            )
                            if (!permissionRequested) {
                                pendingTestNotification = false
                                openNotificationSettings(context)
                                scope.launch { snackbar.showSnackbar("请在系统设置中允许通知后再测试") }
                            }
                        }
                    },
                    onOpenNotificationSettings = { openNotificationSettings(context) },
                    onOpenBatterySettings = { openBatteryOptimizationSettings(context) },
                    onReconnect = {
                        MonitoringScheduler.reconnect(context)
                        monitorStatus = MonitorStatus.CONNECTING
                    },
                    onClose = { scope.launch { drawerState.close() } },
                )
            }
        },
    ) {
        Scaffold(
            snackbarHost = { SnackbarHost(snackbar) },
            topBar = {
                TopAppBar(
                    navigationIcon = {
                        IconButton(onClick = { scope.launch { drawerState.open() } }) {
                            Icon(Icons.Default.Menu, contentDescription = "打开应用设置")
                        }
                    },
                    title = {
                        Column {
                            Text("号池监控", style = MaterialTheme.typography.titleMedium)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Box(
                                    Modifier
                                        .size(7.dp)
                                        .background(statusColor(monitorStatus), CircleShape),
                                )
                                Spacer(Modifier.width(6.dp))
                                Text(monitorStatus.label, style = MaterialTheme.typography.labelSmall)
                            }
                        }
                    },
                    actions = {
                        IconButton(onClick = { reloadSignal++ }) {
                            Icon(Icons.Default.Refresh, contentDescription = "刷新当前页面")
                        }
                        IconButton(onClick = { scope.launch { drawerState.open() } }) {
                            Icon(Icons.Default.Settings, contentDescription = "打开应用设置")
                        }
                    },
                )
            },
        ) { padding ->
            key(recreateSignal) {
                PoolWatchWebView(
                    serverUrl = serverUrl,
                    reloadSignal = reloadSignal,
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(padding),
                    onWebViewReady = { readyWebView ->
                        webView = readyWebView
                        requestedAlertId?.let {
                            readyWebView.loadUrl("$serverUrl/alerts?focus=${Uri.encode(it)}")
                            onAlertOpened()
                        }
                    },
                    onAuthenticatedPageLoaded = {
                        CookieManager.getInstance().flush()
                        if (monitoringEnabled) MonitoringScheduler.reconnect(context)
                    },
                )
            }
        }
    }
}

@Composable
private fun NativeSettingsPanel(
    serverDraft: String,
    onServerDraftChange: (String) -> Unit,
    monitoringEnabled: Boolean,
    monitorStatus: MonitorStatus,
    onMonitoringChange: (Boolean) -> Unit,
    onSaveServer: () -> Unit,
    onTestNotification: () -> Unit,
    onOpenNotificationSettings: () -> Unit,
    onOpenBatterySettings: () -> Unit,
    onReconnect: () -> Unit,
    onClose: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxHeight()
            .verticalScroll(rememberScrollState())
            .padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("安卓应用设置", style = MaterialTheme.typography.titleLarge)
            TextButton(onClick = onClose) { Text("关闭") }
        }
        Text(
            "网页中的渠道、阈值和账号管理保持不变；这里控制手机端的持续监听与原生通知。",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        HorizontalDivider()
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text("持续监控", style = MaterialTheme.typography.titleMedium)
                Text(
                    monitorStatus.label,
                    style = MaterialTheme.typography.bodySmall,
                    color = statusColor(monitorStatus),
                )
            }
            Switch(checked = monitoringEnabled, onCheckedChange = onMonitoringChange)
        }
        Text(
            "开启后会显示一条低优先级常驻通知，并通过实时连接接收告警；十五分钟检查用于断线补偿。",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        OutlinedButton(
            onClick = onReconnect,
            enabled = monitoringEnabled,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Icon(Icons.Default.Refresh, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("立即重新连接")
        }
        HorizontalDivider()
        Text("服务器", style = MaterialTheme.typography.titleMedium)
        OutlinedTextField(
            value = serverDraft,
            onValueChange = onServerDraftChange,
            modifier = Modifier.fillMaxWidth(),
            label = { Text("HTTPS 地址") },
            singleLine = true,
        )
        Button(onClick = onSaveServer, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.Default.CheckCircle, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("保存并重新加载")
        }
        HorizontalDivider()
        Text("通知与后台运行", style = MaterialTheme.typography.titleMedium)
        OutlinedButton(onClick = onTestNotification, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.Default.Notifications, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("发送测试通知")
        }
        OutlinedButton(onClick = onOpenNotificationSettings, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.Default.Settings, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("打开通知设置")
        }
        OutlinedButton(onClick = onOpenBatterySettings, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.Default.BatterySaver, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("设置忽略电池优化")
        }
        Text(
            "部分手机还需要在系统管家中允许自启动和后台运行。强行停止应用后，需要再次打开应用才能恢复。",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Spacer(Modifier.height(12.dp))
        Text(
            "版本 ${BuildConfig.VERSION_NAME}",
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun PoolWatchWebView(
    serverUrl: String,
    reloadSignal: Int,
    modifier: Modifier = Modifier,
    onWebViewReady: (WebView) -> Unit,
    onAuthenticatedPageLoaded: () -> Unit,
) {
    val context = LocalContext.current
    var webView by remember { mutableStateOf<WebView?>(null) }
    var loading by remember { mutableStateOf(true) }
    var pageError by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(reloadSignal) {
        if (reloadSignal > 0) webView?.reload()
    }

    DisposableEffect(Unit) {
        onDispose {
            webView?.apply {
                stopLoading()
                webChromeClient = null
                webViewClient = WebViewClient()
                destroy()
            }
        }
    }

    Box(modifier = modifier) {
        AndroidView(
            modifier = Modifier.fillMaxSize(),
            factory = { viewContext ->
                CookieManager.getInstance().setAcceptCookie(true)
                WebView(viewContext).apply {
                    settings.javaScriptEnabled = true
                    settings.domStorageEnabled = true
                    settings.cacheMode = WebSettings.LOAD_DEFAULT
                    settings.allowFileAccess = false
                    settings.allowContentAccess = false
                    settings.javaScriptCanOpenWindowsAutomatically = false
                    settings.setSupportMultipleWindows(true)
                    settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                    settings.safeBrowsingEnabled = true
                    settings.userAgentString = "${settings.userAgentString} PoolWatchAndroid/${BuildConfig.VERSION_NAME}"
                    CookieManager.getInstance().setAcceptThirdPartyCookies(this, false)
                    webViewClient = object : WebViewClient() {
                        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                            val uri = request.url
                            return if (sameOrigin(uri, serverUrl.toUri())) {
                                false
                            } else {
                                openExternalUrl(context, uri)
                                true
                            }
                        }

                        override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) {
                            loading = true
                            pageError = null
                        }

                        override fun onPageFinished(view: WebView, url: String?) {
                            loading = false
                            CookieManager.getInstance().flush()
                            onAuthenticatedPageLoaded()
                        }

                        override fun onReceivedError(
                            view: WebView,
                            request: WebResourceRequest,
                            error: WebResourceError,
                        ) {
                            if (request.isForMainFrame) {
                                loading = false
                                pageError = "页面加载失败，请检查网络后重试"
                            }
                        }
                    }
                    webChromeClient = popupWebChromeClient(context, this, serverUrl)
                    loadUrl(serverUrl)
                    webView = this
                    onWebViewReady(this)
                }
            },
        )

        if (loading) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                CircularProgressIndicator()
            }
        }
        pageError?.let { message ->
            Column(
                modifier = Modifier
                    .fillMaxSize()
                    .background(MaterialTheme.colorScheme.background)
                    .padding(28.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center,
            ) {
                Text(message, style = MaterialTheme.typography.titleMedium)
                Spacer(Modifier.height(16.dp))
                Button(onClick = { webView?.reload() }) { Text("重新加载") }
            }
        }
    }
}

private fun popupWebChromeClient(context: Context, owner: WebView, serverUrl: String): WebChromeClient =
    object : WebChromeClient() {
        override fun onCreateWindow(
            view: WebView,
            isDialog: Boolean,
            isUserGesture: Boolean,
            resultMsg: Message,
        ): Boolean {
            if (!isUserGesture) return false
            val popup = WebView(context)
            popup.webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(popupView: WebView, request: WebResourceRequest): Boolean {
                    openPopupDestination(context, owner, serverUrl, request.url)
                    popupView.destroy()
                    return true
                }

                override fun onPageStarted(popupView: WebView, url: String, favicon: android.graphics.Bitmap?) {
                    openPopupDestination(context, owner, serverUrl, url.toUri())
                    popupView.stopLoading()
                    popupView.destroy()
                }
            }
            val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
            transport.webView = popup
            resultMsg.sendToTarget()
            return true
        }
    }

private fun openPopupDestination(context: Context, owner: WebView, serverUrl: String, uri: Uri) {
    if (sameOrigin(uri, serverUrl.toUri())) owner.loadUrl(uri.toString()) else openExternalUrl(context, uri)
}

private fun sameOrigin(first: Uri, second: Uri): Boolean {
    fun effectivePort(uri: Uri): Int = when {
        uri.port != -1 -> uri.port
        uri.scheme.equals("https", true) -> 443
        uri.scheme.equals("http", true) -> 80
        else -> -1
    }
    return first.scheme.equals(second.scheme, true) &&
        first.host.equals(second.host, true) &&
        effectivePort(first) == effectivePort(second)
}

private fun openExternalUrl(context: Context, uri: Uri) {
    if (uri.scheme != "https" && uri.scheme != "http") return
    runCatching {
        context.startActivity(Intent(Intent.ACTION_VIEW, uri).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}

private fun requestNotificationPermissionIfNeeded(
    context: Context,
    launch: (String) -> Unit,
): Boolean {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
        ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) !=
        PackageManager.PERMISSION_GRANTED
    ) {
        launch(Manifest.permission.POST_NOTIFICATIONS)
        return true
    }
    return false
}

private fun sessionCookieValue(serverUrl: String): String? = CookieManager.getInstance()
    .getCookie(serverUrl)
    .orEmpty()
    .split(';')
    .asSequence()
    .map(String::trim)
    .firstOrNull { it.startsWith("poolwatch_session=") }
    ?.substringAfter('=')
    ?.takeIf(String::isNotBlank)

private fun openNotificationSettings(context: Context) {
    context.startActivity(
        Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
            .putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName),
    )
}

@SuppressLint("BatteryLife")
private fun openBatteryOptimizationSettings(context: Context) {
    val powerManager = context.getSystemService(PowerManager::class.java)
    val intent = if (powerManager.isIgnoringBatteryOptimizations(context.packageName)) {
        Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
    } else {
        Intent(
            Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
            "package:${context.packageName}".toUri(),
        )
    }
    runCatching { context.startActivity(intent) }
        .onFailure { context.startActivity(Intent(Settings.ACTION_BATTERY_SAVER_SETTINGS)) }
}

@Composable
private fun statusColor(status: MonitorStatus): Color = when (status) {
    MonitorStatus.CONNECTED -> Color(0xFF177A4A)
    MonitorStatus.CONNECTING, MonitorStatus.RETRYING -> Color(0xFFB7770B)
    MonitorStatus.LOGIN_REQUIRED, MonitorStatus.ERROR -> MaterialTheme.colorScheme.error
    MonitorStatus.STOPPED -> MaterialTheme.colorScheme.onSurfaceVariant
}
