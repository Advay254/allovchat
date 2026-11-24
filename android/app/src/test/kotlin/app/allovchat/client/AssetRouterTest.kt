package app.allovchat.client

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * Checks the app's address mapping against the real files, laid out exactly as they are inside the APK: the website's
 * static/ folder copied to assets/static/, plus the committed assets/setup.html. This is the check that would have caught
 * "/app opens assets/app.html" while the file is really assets/static/app.html.
 */
class AssetRouterTest {
    private val root: File = generateSequence(File("").absoluteFile) { it.parentFile }
        .first { File(it, "static/app.html").exists() && File(it, "android/app").exists() }

    private val files: Set<String> = buildSet {
        val st = File(root, "static")
        st.walkTopDown().filter { it.isFile }.forEach { add("static/" + it.relativeTo(st).path.replace('\\', '/')) }
        val a = File(root, "android/app/src/main/assets")
        a.walkTopDown().filter { it.isFile }.forEach {
            val rel = it.relativeTo(a).path.replace('\\', '/')
            if (!rel.startsWith("static/")) add(rel)
        }
    }

    private val router = AssetRouter { it in files }

    private fun asset(path: String, signedIn: Boolean): AssetRouter.Route.Asset {
        val r = router.route(path, signedIn)
        assertTrue("$path should be served from the app, got $r", r is AssetRouter.Route.Asset)
        return r as AssetRouter.Route.Asset
    }

    @Test fun appOpensTheBundledInterfaceWhenSignedIn() {
        for (p in listOf("/app", "/app/", "/", "/app/anything/deep")) {
            val a = asset(p, true)
            assertEquals("static/app.html", a.path)
            assertTrue("the file the app opens must exist in the APK", a.path in files)
            assertTrue(a.page)
        }
    }

    @Test fun appOpensTheSignInScreenWhenSignedOut() {
        val a = asset("/app", false)
        assertEquals("setup.html", a.path)
        assertTrue(a.path in files)
    }

    @Test fun everyFileThePagesReferenceIsBundled() {
        val ref = Regex("""(?:src|href)="(/static/[^"?#]+)""")
        for (page in listOf("static/app.html", "setup.html")) {
            val text = File(root, if (page == "setup.html") "android/app/src/main/assets/setup.html" else page).readText()
            val refs = ref.findAll(text).map { it.groupValues[1] }.toSet()
            assertTrue("$page should reference files", refs.isNotEmpty())
            for (r in refs) {
                val route = router.route(r, true)
                assertTrue("$page needs $r, but the app can't serve it ($route)", route is AssetRouter.Route.Asset)
            }
        }
        assertTrue("native.js must be part of the app interface", "static/native.js" in files)
    }

    @Test fun mimeTypesAreRight() {
        assertEquals("text/javascript", asset("/static/app.js", true).mime)
        assertEquals("text/css", asset("/static/style.css", true).mime)
        assertEquals("image/svg+xml", asset("/static/logo-mark.svg", true).mime)
    }

    @Test fun nothingOnTheAppHostEverGoesToTheNetwork() {
        for (p in listOf("/static/nope.js", "/static/../secret", "/static/", "/icons/192.png", "/manifest.webmanifest", "/l/abcd", "/zeus/")) {
            val r = router.route(p, true)
            assertTrue("$p should be a clear miss, got $r", r is AssetRouter.Route.Missing)
        }
    }

    @Test fun websitePagesOpenOnTheServerInstead() {
        for (p in listOf("/about", "/blog", "/privacy", "/terms")) {
            val r = router.route(p, true)
            assertTrue("$p should open on the user's server", r is AssetRouter.Route.Website)
            assertEquals(p, (r as AssetRouter.Route.Website).path)
        }
    }

    @Test fun aMissingInterfaceGivesAnExplanationNotANetworkRequest() {
        val empty = AssetRouter { false }
        val r = empty.route("/app", true)
        assertTrue(r is AssetRouter.Route.Missing)
        assertTrue((r as AssetRouter.Route.Missing).reason.contains("static/app.html"))
    }
}
