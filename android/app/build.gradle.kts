plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.multislow.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.multislow.app"
        minSdk = 24
        targetSdk = 34
        versionCode = 1
        versionName = "0.1"
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    buildTypes {
        release { isMinifyEnabled = false }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }

    // libdnstt.so is really an executable; it must be extracted to disk to run.
    packaging { jniLibs { useLegacyPackaging = true } }
}

dependencies {
    // Built by gomobile (see ../build.sh and the CI workflow).
    implementation(fileTree("libs") { include("*.aar") })
}
