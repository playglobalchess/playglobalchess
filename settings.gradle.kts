// Root Gradle settings for the PlayGlobalChess Android build surface.
// Includes the parity test modules (§8.8 engine, PLAN-CODEC §4.3 codec); the
// full Android client (clients/android/app, S5) is added here as it lands.
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "playglobalchess-android"

include(":engine-parity")
project(":engine-parity").projectDir = file("clients/android/engine-parity")

include(":codec-parity")
project(":codec-parity").projectDir = file("clients/android/codec-parity")
