package app.allovchat.client

import android.content.Context
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.TimeUnit

/**
 * Messages written without a connection. The interface has already sealed each one (end-to-end encryption) and given it
 * a key, so this code only has to post the same bytes later. The server ignores a repeat of a key it has seen, so a retry
 * can never post a message twice.
 */
object Outbox {
    private const val KEY = "queue"
    private const val MAX = 200
    private const val MAX_AGE_MS = 7L * 24 * 3600 * 1000 // chats are gone after 7 days anyway
    private val sendPath = Regex("^/api/(t|m)/\\d+/send$")
    private val lock = Any()

    private fun prefs(c: Context) = c.getSharedPreferences("outbox", Context.MODE_PRIVATE)

    private fun read(c: Context): JSONArray =
        try { JSONArray(prefs(c).getString(KEY, "[]")) } catch (e: Exception) { JSONArray() }

    private fun write(c: Context, a: JSONArray) = prefs(c).edit().putString(KEY, a.toString()).apply()

    fun enqueue(c: Context, path: String, body: String): Boolean {
        if (!sendPath.matches(path) || body.length > 20000) return false
        synchronized(lock) {
            val a = read(c)
            if (a.length() >= MAX) return false
            a.put(JSONObject().put("path", path).put("body", body).put("ts", System.currentTimeMillis()))
            write(c, a)
        }
        schedule(c)
        return true
    }

    /** The queued entries for one chat, as JSON, so the interface can show them in place. */
    fun pending(c: Context, path: String): String = synchronized(lock) {
        val out = JSONArray()
        val a = read(c)
        for (i in 0 until a.length()) {
            val e = a.getJSONObject(i)
            if (e.getString("path") == path) out.put(e)
        }
        out.toString()
    }

    fun clear(c: Context) {
        synchronized(lock) { prefs(c).edit().remove(KEY).apply() }
        WorkManager.getInstance(c).cancelUniqueWork("flush")
    }

    /** Ask Android to send the queue as soon as there is a connection, even if the app is closed. */
    fun schedule(c: Context) {
        if (synchronized(lock) { read(c).length() } == 0) return
        val req = OneTimeWorkRequest.Builder(FlushWorker::class.java)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
            .setBackoffCriteria(BackoffPolicy.LINEAR, 30, TimeUnit.SECONDS)
            .build()
        WorkManager.getInstance(c).enqueueUniqueWork("flush", ExistingWorkPolicy.APPEND_OR_REPLACE, req)
    }

    /** Sends what it can, in order. Returns true when nothing is left that should be retried. */
    fun flushNow(c: Context): Boolean {
        if (!Session.signedIn(c)) { clear(c); return true }
        val server = Session.server(c)
        val token = Session.token(c)
        var sentAny = false
        var retry = false
        val snapshot = synchronized(lock) { read(c) }
        val done = ArrayList<JSONObject>()
        for (i in 0 until snapshot.length()) {
            val e = snapshot.getJSONObject(i)
            val old = System.currentTimeMillis() - e.getLong("ts") > MAX_AGE_MS
            val code = if (old) 404 else try { post(server, token, e.getString("path"), e.getString("body")) } catch (x: IOException) { -1 }
            when {
                code in 200..299 -> { done.add(e); sentAny = true }
                // The server said no for good (chat ended, blocked, message refused): keeping it would only repeat the answer.
                code in intArrayOf(400, 403, 404, 409, 410, 413, 422) -> done.add(e)
                else -> { retry = true; break } // no connection, signed out, busy or a server error: try again later, in order
            }
        }
        if (done.isNotEmpty()) synchronized(lock) {
            val a = read(c)
            val keep = JSONArray()
            for (i in 0 until a.length()) {
                val e = a.getJSONObject(i)
                if (done.none { it.getLong("ts") == e.getLong("ts") && it.getString("body") == e.getString("body") }) keep.put(e)
            }
            write(c, keep)
        }
        if (sentAny) MainActivity.notifyFlushed()
        return !retry
    }

    private fun post(server: String, token: String, path: String, body: String): Int {
        val conn = URL(server + path).openConnection() as HttpURLConnection
        try {
            conn.requestMethod = "POST"
            conn.connectTimeout = 10000
            conn.readTimeout = 15000
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.setRequestProperty("Authorization", "Bearer $token")
            conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            return conn.responseCode
        } finally {
            conn.disconnect()
        }
    }
}

class FlushWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result = if (Outbox.flushNow(applicationContext)) Result.success() else Result.retry()
}
