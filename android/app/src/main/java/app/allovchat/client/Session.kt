package app.allovchat.client

import android.content.Context
import android.content.Intent

/** What the app remembers about the user's server and sign-in. Stored in the app's private storage. */
object Session {
    private fun prefs(c: Context) = c.getSharedPreferences("session", Context.MODE_PRIVATE)

    fun server(c: Context): String = prefs(c).getString("server", "") ?: ""
    fun token(c: Context): String = prefs(c).getString("token", "") ?: ""
    fun lastServer(c: Context): String = prefs(c).getString("last", "") ?: ""
    fun signedIn(c: Context): Boolean = server(c).isNotEmpty() && token(c).isNotEmpty()

    fun save(c: Context, server: String, token: String) {
        prefs(c).edit().putString("server", server).putString("token", token).putString("last", server).apply()
    }

    fun notifyOn(c: Context): Boolean = prefs(c).getBoolean("notify", false)

    fun setNotify(c: Context, on: Boolean) {
        prefs(c).edit().putBoolean("notify", on).apply()
    }

    /** Signs out on this phone: forgets the token, drops queued messages and stops the background connection. */
    fun clear(c: Context) {
        prefs(c).edit().remove("token").remove("server").putBoolean("notify", false).apply()
        Outbox.clear(c)
        c.stopService(Intent(c, EventService::class.java))
    }
}

/** True while the app is on screen, so the background service doesn't announce what the person is already looking at. */
object AppState {
    @Volatile var foreground = false
}
