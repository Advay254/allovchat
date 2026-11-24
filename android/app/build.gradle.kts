plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "app.allovchat.client"
    compileSdk = 34

    defaultConfig {
        applicationId = "app.allovchat.client"
        minSdk = 26
        targetSdk = 34
        versionCode = (System.getenv("APP_VERSION_CODE") ?: "1").toInt()
        versionName = System.getenv("APP_VERSION_NAME") ?: "0.5.0"
    }

    // A release key comes from the environment (see android/README.md). Without one, the build is signed with the
    // debug key, which installs fine but can't be updated by a build signed with a different key.
    val keystore = System.getenv("ANDROID_KEYSTORE_FILE")
    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = if (keystore != null) signingConfigs.getByName("release") else signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity:1.9.2")
    implementation("androidx.work:work-runtime:2.9.1")
    testImplementation("junit:junit:4.13.2")
}

// The interface is copied in from ../static before the build (see README). A build without it would open to an error, so refuse to build.
val checkWebAssets = tasks.register("checkWebAssets") {
    doLast {
        val dir = file("src/main/assets/static")
        val need = listOf("app.html", "app.js", "chat.js", "ui.js", "brand.js", "push.js", "native.js", "ux.js", "style.css", "ui.css", "app.css", "logo-mark.svg")
        val missing = need.filter { !File(dir, it).exists() }
        if (!File(dir, "../setup.html").exists()) throw GradleException("assets/setup.html is missing")
        if (missing.isNotEmpty()) throw GradleException("The web interface is not bundled into the app. Missing in ${dir}: ${missing.joinToString()}. Copy ../static/. into it first (see android/README.md).")
    }
}
tasks.matching { it.name == "preBuild" }.configureEach { dependsOn(checkWebAssets) }
