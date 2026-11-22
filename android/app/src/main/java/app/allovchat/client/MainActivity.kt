package app.allovchat.client

import android.Manifest
import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.webkit.WebViewAssetLoader
import java.io.IOException

/**
 * The app is the allovchat interface, bundled inside the APK, running against the server the person typed in.
 * Everything it shows comes from the files in assets/, so it opens with no connection.
 */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView
    private lateinit var loader: WebViewAssetLoader

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) startListening() else Session.setNotify(this, false)
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        current = this
        loader = WebViewAssetLoader.Builder()
            .setDomain(HOST)
            .addPathHandler("/static/", StaticHandler(this))
            .build()

        web = WebView(this)
        setContentView(web)
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true // also what IndexedDB needs, which keeps the offline copy of chats
            allowFileAccess = false
            allowContentAccess = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            setSupportMultipleWindows(false)
        }
        web.addJavascriptInterface(Bridge(), "AndroidApp")
        web.webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                val u = request.url
                if (u.host == HOST && u.path == "/app") {
                    // One address, two screens: the sign-in form until there is a session, then the app.
                    return page(if (Session.signedIn(this@MainActivity)) "app.html" else "setup.html")
                }
                return loader.shouldInterceptRequest(u)
            }

            // Only the app's own pages load inside the app. Anything else opens in the browser.
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                if (request.url.host == HOST) return false
                try { startActivity(Intent(Intent.ACTION_VIEW, request.url)) } catch (e: Exception) {}
                return true
            }
        }
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                // In a chat, Back returns to the list. Anywhere else it leaves the app.
                val js = "(function(){var b=document.querySelector('.chat-head .back');if(b&&b.offsetParent!==null){b.click();return true}return false})()"
                web.evaluateJavascript(js) { r -> if (r != "true") finish() }
            }
        })
        if (savedInstanceState == null) web.loadUrl("https://$HOST/app")
    }

    override fun onResume() {
        super.onResume()
        AppState.foreground = true
        (getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager).cancelAll()
        if (Session.signedIn(this) && Session.notifyOn(this) && permissionOk()) startListening()
        Outbox.schedule(this)
    }

    override fun onPause() {
        AppState.foreground = false
        super.onPause()
    }

    override fun onDestroy() {
        if (current === this) current = null
        web.destroy()
        super.onDestroy()
    }

    private fun page(name: String): WebResourceResponse? = try {
        WebResourceResponse("text/html", "utf-8", 200, "OK", mapOf("Cache-Control" to "no-store"), assets.open(name))
    } catch (e: IOException) { null }

    private fun permissionOk() =
        Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private fun startListening() {
        try { EventService.start(this) } catch (e: Exception) {}
        // Phones stop background connections to save battery. Ask once, in plain words, for an exception.
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        val seen = getSharedPreferences("hints", MODE_PRIVATE)
        if (!pm.isIgnoringBatteryOptimizations(packageName) && !seen.getBoolean("battery", false)) {
            seen.edit().putBoolean("battery", true).apply()
            AlertDialog.Builder(this)
                .setTitle("Keep alerts reliable")
                .setMessage("To hear about messages while your phone sleeps, allow allovchat to run in the background. You can change this later in the phone's settings.")
                .setPositiveButton("Open settings") { _, _ -> try { startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)) } catch (e: Exception) {} }
                .setNegativeButton("Not now", null)
                .show()
        }
    }

    /** What the interface can ask the app to do. These run on a background thread, so each one hands UI work to the main thread. */
    inner class Bridge {
        @JavascriptInterface fun server(): String = Session.server(this@MainActivity)
        @JavascriptInterface fun token(): String = Session.token(this@MainActivity)
        @JavascriptInterface fun lastServer(): String = Session.lastServer(this@MainActivity)

        @JavascriptInterface fun saveSession(server: String, token: String) {
            // Only a secure address, and only a token of the expected shape.
            if (!server.startsWith("https://") || !Regex("^[a-f0-9]{48}$").matches(token)) return
            Session.save(this@MainActivity, server.trimEnd('/'), token)
        }

        @JavascriptInterface fun signedOut() {
            runOnUiThread {
                Session.clear(this@MainActivity)
                web.loadUrl("https://$HOST/app")
            }
        }

        @JavascriptInterface fun enqueue(path: String, body: String): Boolean = Outbox.enqueue(this@MainActivity, path, body)
        @JavascriptInterface fun pendingFor(path: String): String = Outbox.pending(this@MainActivity, path)
        @JavascriptInterface fun flush() { Thread { Outbox.flushNow(applicationContext) }.start() }

        @JavascriptInterface fun notificationsOn(): Boolean = Session.notifyOn(this@MainActivity) && permissionOk()

        @JavascriptInterface fun enableNotifications() {
            runOnUiThread {
                Session.setNotify(this@MainActivity, true)
                if (permissionOk()) startListening() else askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        }

        @JavascriptInterface fun disableNotifications() {
            runOnUiThread {
                Session.setNotify(this@MainActivity, false)
                stopService(Intent(this@MainActivity, EventService::class.java))
            }
        }
    }

    /** Serves the interface files bundled in assets/static/ at https://appassets.androidplatform.net/static/. */
    private class StaticHandler(val ctx: Context) : WebViewAssetLoader.PathHandler {
        override fun handle(path: String): WebResourceResponse? {
            if (path.contains("..")) return null
            return try {
                val ext = path.substringAfterLast('.', "")
                val mime = when (ext) {
                    "js" -> "text/javascript"
                    "css" -> "text/css"
                    "svg" -> "image/svg+xml"
                    "html" -> "text/html"
                    "png" -> "image/png"
                    "json" -> "application/json"
                    "woff2" -> "font/woff2"
                    else -> "application/octet-stream"
                }
                WebResourceResponse(mime, null, ctx.assets.open("static/$path"))
            } catch (e: IOException) { null }
        }
    }

    companion object {
        const val HOST = "appassets.androidplatform.net"
        @Volatile private var current: MainActivity? = null

        /** Called after the queue sent something: tells an open chat to refresh. */
        fun notifyFlushed() {
            val a = current ?: return
            a.runOnUiThread { a.web.evaluateJavascript("window.dispatchEvent(new Event('allov:flushed'))", null) }
        }
    }
}
