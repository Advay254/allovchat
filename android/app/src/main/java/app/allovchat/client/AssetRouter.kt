package app.allovchat.client

/**
 * Decides what each address inside the app resolves to. It knows nothing about Android, so it is tested on its own
 * against the real files. Everything on the app's own host is answered from the APK, never from the network: a request
 * that matches nothing gets a clear 404, not a trip to the internet (which fails with a DNS error for this made-up host).
 *
 * Layout inside the APK:
 *   assets/setup.html       the sign-in screen (committed)
 *   assets/static/...       the website's interface files, copied from ../static when the app is built
 */
class AssetRouter(private val exists: (String) -> Boolean) {
    sealed class Route {
        /** Serve this file from assets/. */
        class Asset(val path: String, val mime: String, val page: Boolean) : Route()

        /** A page of the website that isn't bundled (About, Privacy, ...). Open it on the user's server, in the browser. */
        class Website(val path: String) : Route()

        class Missing(val reason: String) : Route()
    }

    fun route(path: String, signedIn: Boolean): Route {
        val p = if (path.isEmpty()) "/" else path
        if (p == "/" || p == "/app" || p == "/app/" || p.startsWith("/app/")) {
            // One address, two screens: the sign-in form until there is a session, then the app itself.
            val file = if (signedIn) APP else SETUP
            return if (exists(file)) Route.Asset(file, "text/html", true)
            else Route.Missing("This build of the app is missing $file. The web interface wasn't bundled into it.")
        }
        if (p == "/favicon.svg") return asset("static/logo-mark.svg")
        if (p.startsWith("/static/")) {
            val rel = p.removePrefix("/static/")
            if (rel.isEmpty() || rel.contains("..") || rel.contains('\\') || rel.startsWith("/")) return Route.Missing("Bad path")
            return asset("static/$rel")
        }
        if (p in WEBSITE_PAGES) return Route.Website(p)
        return Route.Missing("Not part of the app")
    }

    private fun asset(file: String): Route =
        if (exists(file)) Route.Asset(file, mime(file), false) else Route.Missing("$file is not in the app")

    companion object {
        const val APP = "static/app.html"
        const val SETUP = "setup.html"
        val WEBSITE_PAGES = setOf("/about", "/blog", "/privacy", "/terms", "/feed.xml", "/llms.txt")

        fun mime(file: String): String = when (file.substringAfterLast('.', "").lowercase()) {
            "html" -> "text/html"
            "js" -> "text/javascript"
            "css" -> "text/css"
            "svg" -> "image/svg+xml"
            "png" -> "image/png"
            "json" -> "application/json"
            "woff2" -> "font/woff2"
            else -> "application/octet-stream"
        }
    }
}
