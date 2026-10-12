// Root Gradle settings for the PlayGlobalChess Android build surface.
// Currently includes only the §8.8 engine-parity test module; the full Android
// client (clients/android/app, S5) is added here as it lands.
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
