// Root build script — declares the plugin versions the subprojects apply.
// Pinned (engineering-conventions: pin dependency versions). AGP 8.5 pairs with
// Gradle 8.7 (see gradle/wrapper/gradle-wrapper.properties).
plugins {
    id("com.android.library") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "2.0.20" apply false
}
