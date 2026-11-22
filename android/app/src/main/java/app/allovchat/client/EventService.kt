package app.allovchat.client

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject
import java.io.BufferedReader
import java.io.IOException
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL

/**
 * The live connection, like the Gotify app: one open request to the user's own server. The server sends a small generic
 * event whenever something needs attention, and this shows a notification. No message text ever travels this way.
 */
class EventService : Service() {
    @Volatile private var running = false
    private var worker: Thread? = null
    private var callback: ConnectivityManager.NetworkCallback? = null
    @Volatile private var conn: HttpURLConnection? = null
    private val nap = Object()

    private class SignedOut : Exception()

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        createChannels(this)
        startForeground(ONGOING_ID, ongoing())
        if (!Session.signedIn(this)) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (!running) {
            running = true
            worker = Thread({ loop() }, "allov-events").also { it.start() }
            watchNetwork()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        callback?.let { try { (getSystemService(CONNECTIVITY_SERVICE) as ConnectivityManager).unregisterNetworkCallback(it) } catch (e: Exception) {} }
        conn?.disconnect()
        wake()
        super.onDestroy()
    }

    private fun wake() = synchronized(nap) { nap.notifyAll() }

    private fun watchNetwork() {
        val cm = getSystemService(CONNECTIVITY_SERVICE) as ConnectivityManager
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                Outbox.schedule(applicationContext) // a connection is back: send anything written while offline
                wake() // and reconnect now instead of waiting out the back-off
            }
        }
        try { cm.registerDefaultNetworkCallback(cb); callback = cb } catch (e: Exception) {}
    }

    private fun loop() {
        var backoff = 1000L
        while (running) {
            try {
                connectOnce()
                backoff = 1000L
            } catch (e: SignedOut) {
                // The token was revoked on the server. Forget it, so the app asks for a sign-in next time.
                Session.clear(applicationContext)
                notifyText("Signed out", "Open allovchat to sign in again.", "signed-out")
                stopSelf()
                return
            } catch (e: Exception) {
                // no connection or the server is restarting: wait and try again
            }
            if (!running) return
            synchronized(nap) { try { nap.wait(backoff) } catch (e: InterruptedException) { return } }
            backoff = minOf(backoff * 2, 60000L)
        }
    }

    private fun connectOnce() {
        val c = URL(Session.server(this) + "/api/app/events").openConnection() as HttpURLConnection
        conn = c
        try {
            c.connectTimeout = 15000
            c.readTimeout = 75000 // the server sends a ping every 25 seconds, so silence for this long means the link is dead
            c.setRequestProperty("Authorization", "Bearer " + Session.token(this))
            c.setRequestProperty("Accept", "text/event-stream")
            val code = c.responseCode
            if (code == 401) throw SignedOut()
            if (code != 200) throw IOException("status $code")
            Outbox.schedule(applicationContext)
            BufferedReader(InputStreamReader(c.inputStream, Charsets.UTF_8)).use { r ->
                while (running) {
                    val line = r.readLine() ?: break
                    if (line.startsWith("data:")) handle(line.substring(5).trim())
                }
            }
        } finally {
            c.disconnect()
            conn = null
        }
    }

    private fun handle(json: String) {
        val o = try { JSONObject(json) } catch (e: Exception) { return }
        if (AppState.foreground) return // the person is looking at the app, which refreshes itself
        val (title, body) = when (o.optString("k")) {
            "request" -> "New chat request" to "Someone wants to chat. Open the app to see it."
            "accepted" -> "Request accepted" to "Your chat request was accepted. Open the app to continue."
            "reveal" -> "Contact details ready" to "You both agreed to share. Open the app to see them."
            else -> "New message" to "Open the app to read it."
        }
        notifyText(title, body, o.optString("g", "chat"))
    }

    private fun notifyText(title: String, text: String, tag: String) {
        if (Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val n = NotificationCompat.Builder(this, CH_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_MESSAGE)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .build()
        // The same tag gives the same id, so a newer alert for a chat replaces the older one.
        (getSystemService(NOTIFICATION_SERVICE) as NotificationManager).notify(tag.hashCode(), n)
    }

    private fun ongoing(): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Builder(this, CH_CONNECTION)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(R.string.listening))
            .setContentIntent(open)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()
    }

    companion object {
        const val CH_MESSAGES = "messages"
        const val CH_CONNECTION = "connection"
        private const val ONGOING_ID = 1

        fun createChannels(c: Context) {
            val nm = c.getSystemService(NOTIFICATION_SERVICE) as NotificationManager
            nm.createNotificationChannel(NotificationChannel(CH_MESSAGES, c.getString(R.string.channel_messages), NotificationManager.IMPORTANCE_HIGH))
            nm.createNotificationChannel(NotificationChannel(CH_CONNECTION, c.getString(R.string.channel_connection), NotificationManager.IMPORTANCE_MIN))
        }

        fun start(c: Context) {
            ContextCompat.startForegroundService(c, Intent(c, EventService::class.java))
        }
    }
}
