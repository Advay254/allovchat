package app.allovchat.client

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Brings the background connection back after a restart or an app update, if the person had turned notifications on. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (Session.signedIn(context) && Session.notifyOn(context)) {
            try { EventService.start(context) } catch (e: Exception) {}
        }
        Outbox.schedule(context)
    }
}
