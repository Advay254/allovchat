# allovchat for Android

A native Android app for any allovchat server. Open source means every server is run by someone, so the app asks for the
address of yours the first time, then a username and password. After that it works like a chat app should:

- **Notifications without Google.** Like the Gotify app, it keeps one live connection to your server and shows an alert
  when something arrives. Alerts are generic ("New message") and never contain names or message text.
- **Works offline.** The whole interface is inside the app, so it opens with no connection. Chats you have already loaded
  stay readable. They are kept on the phone only in the form the server has them (encrypted, for end-to-end chats), and are
  removed when you sign out.
- **Messages sent offline go through later.** A message written with no connection is encrypted, kept on the phone and
  sent as soon as a connection returns, even if the app is closed. The server ignores a repeat of a message it already
  has, so it can never arrive twice.

## Install

1. Open the **Actions** tab of this repository, choose **Android app** and run it (or push a tag like `android-v0.5.0`
   to also publish a release).
2. Download `allovchat.apk` from the run (or from the release) and open it on the phone. Android will ask you to allow
   installs from your browser or file manager once.
3. Open the app, enter your server address (it must start with `https://`), your username and password.
4. In **Settings**, turn on **Notifications**. Android asks for permission, and once for an exception from battery saving
   so the connection survives while the phone sleeps.

Sign-ups happen on your server's website. The app signs in only.

## Updates and signing

Without a signing key the APK is signed with a temporary debug key, which installs but can't be updated by a later build
(Android would ask you to uninstall first). For updates, make a key once and add four secrets to the repository:

```bash
keytool -genkeypair -v -keystore release.jks -alias allovchat -keyalg RSA -keysize 2048 -validity 10000
base64 -w0 release.jks   # paste into the secret ANDROID_KEYSTORE_BASE64
```

Secrets: `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`. Keep the
keystore file somewhere safe. Losing it means users must reinstall.

## Server settings

The app talks to the server's `/api/app/*` endpoints with a bearer token. The only server setting involved is
`APP_ORIGINS` (optional, comma separated, `https://` only), which adds origins allowed to call the API with a token. The
default, `https://appassets.androidplatform.net`, is the app's own and needs no setting.

## How it is built

`../static/` (the same interface the website uses) is copied into `app/src/main/assets/static/` when the app is built,
and `native.js` adapts it to run inside the app. `setup.html` is the sign-in screen. Kotlin code is in
`app/src/main/java/app/allovchat/client/`:

| File | Job |
|---|---|
| `MainActivity.kt` | Hosts the interface in a WebView and exposes a small bridge |
| `AssetRouter.kt` | Decides what every address inside the app resolves to (tested on its own) |
| `EventService.kt` | The live connection and the notifications |
| `Outbox.kt` | The offline send queue and the background worker that empties it |
| `Session.kt` | Server address and token, kept in the app's private storage |
| `BootReceiver.kt` | Restores the live connection after a restart |

## Tests

- `gradle -p android testDebugUnitTest` checks the app's address mapping (`AssetRouter`) against the real files, laid out as they
  are inside the APK. It fails if `/app` would point at a file that isn't there, or if a page references a file that isn't bundled.
- `bash android/check-assets.sh source` and `bash android/check-assets.sh apk allovchat.apk` fail if interface files are
  missing from the assets folder or from the built APK. The Gradle build refuses to start without them too.
- `node android/test/flow.mjs` drives the real screens in a browser with a stand-in for the Android bridge: sign-in form,
  dashboard, chat, sending, going offline, reopening offline, delivery on return, sign-out. It needs a built server binary and a
  Postgres database (see the `flow` job in `.github/workflows/android.yml`).

Everything on the app's own address (`https://appassets.androidplatform.net`) is answered from the APK. Nothing there ever goes
to the network, because that name doesn't exist on the internet. Only the user's server (always `https://`) does.

## Limits

- Servers must use `https://`. The app refuses plain `http://`.
- Notifications are announcements only. Opening the app shows the content.
- The **Share card** download from a link's menu doesn't work inside the app yet. Copying the link and the QR code do.
- The website's About, Privacy, Terms and Blog pages open on your server in the browser. They aren't part of the app.
- It can't create accounts, and it doesn't show the human check some servers ask for after many failed sign-ins. Sign in
  once on the website, then try again.
- The sign-in token lasts 180 days. After that, or if you sign out on the server, the app asks you to sign in again.
- Built and tested here with a browser harness and a type check against the Android API, but not run on a phone. The
  first CI build may need small fixes from its log.
