// Codec Android parity test module (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3,
// ADR-9).
//
// An instrumented-test module: it carries the generated UniFFI Kotlin bindings
// (src/main/kotlin, populated by the bindgen step) and the parity test
// (src/androidTest). The codec .so is placed under src/main/jniLibs/<abi>/ by
// platform/mobile/build_codec_mobile.sh. Run on an emulator/device:
//   ./gradlew :codec-parity:connectedAndroidTest
//
// minSdk 24 matches the Rust aarch64-linux-android24 build (platform/mobile).

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.playglobalchess.codecparity"
    compileSdk = 34

    defaultConfig {
        minSdk = 24
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    // src/main/jniLibs/arm64-v8a/libpgc_protocol_ffi.so — placed by build_codec_mobile.sh.
    sourceSets["main"].jniLibs.srcDirs("src/main/jniLibs")

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // UniFFI's Kotlin runtime needs JNA (the aar variant for Android).
    implementation("net.java.dev.jna:jna:5.14.0@aar")
    androidTestImplementation("androidx.test.ext:junit:1.1.5")
    androidTestImplementation("androidx.test:runner:1.5.2")
}
