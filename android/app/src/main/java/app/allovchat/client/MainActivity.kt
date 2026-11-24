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
import java.io.IOException

/**
 * The app is the allovchat interface, bundled inside the APK, running against the server the person typed in.
 * Everything it shows comes from the files in assets/, so it opens with no connection.
 */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView
    private lateinit var router: AssetRouter

    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) startListening() else Session.setNotify(this, false)
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        current = this
        router = AssetRouter { name -> assetExists(name) }

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
            // Everything on the app's own host is answered from the APK. Returning null here would send the request to the
            // network, and this host doesn't exist there. Other hosts (the user's server) return null and go out as normal.
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                val u = request.url
                if (u.host != HOST) return null
                return when (val r = router.route(u.path ?: "/", Session.signedIn(this@MainActivity))) {
                    is AssetRouter.Route.Asset -> try {
                        WebResourceResponse(r.mime, if (r.mime.startsWith("text/")) "utf-8" else null, 200, "OK",
                            mapOf("Cache-Control" to if (r.page) "no-store" else "no-cache"), assets.open(r.path))
                    } catch (e: IOException) { notFound("${r.path} could not be read") }
                    is AssetRouter.Route.Website -> notFound("Opens in your browser")
                    is AssetRouter.Route.Missing -> notFound(r.reason)
                }
            }

            // Only the app's own pages load inside the app. Pages of the website that aren't bundled open on the user's own
            // server, and any other address opens in the browser.
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val u = request.url
                var target = u
                if (u.host == HOST) {
                    val r = router.route(u.path ?: "/", Session.signedIn(this@MainActivity))
                    if (r !is AssetRouter.Route.Website) return false
                    val server = Session.server(this@MainActivity)
                    if (server.isEmpty()) return true
                    target = android.net.Uri.parse(server + r.path)
                }
                try { startActivity(Intent(Intent.ACTION_VIEW, target)) } catch (e: Exception) {}
                return true
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: android.webkit.WebResourceError) {
                // The interface itself failed to load. Say so plainly instead of showing a browser error page.
                if (request.isForMainFrame) view.loadDataWithBaseURL(null, errorPage(error.description.toString()), "text/html", "utf-8", null)
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

    private fun assetExists(name: String): Boolean = try { assets.open(name).close(); true } catch (e: IOException) { false }

    private fun notFound(reason: String) = WebResourceResponse("text/plain", "utf-8", 404, "Not Found", emptyMap(), reason.byteInputStream())

    private fun errorPage(detail: String) =
        "<meta name=viewport content='width=device-width,initial-scale=1'><body style='font:16px sans-serif;padding:24px'><h2>Couldn't open the app</h2><p>The app's own interface failed to load, which should never need the internet.</p><p style='color:#666'>" +
            android.text.TextUtils.htmlEncode(detail) + "</p>"

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
