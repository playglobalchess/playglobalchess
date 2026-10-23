// Root build script — declares the plugin versions the subprojects apply.
// Pinned (engineering-conventions: pin dependency versions).
//
// Bumped from AGP 8.5.2 / Kotlin 2.0.20 (Gradle 8.7) to AGP 9.4.1 (Gradle 9.8.2)
// to clear the Dependabot alerts on the AGP buildscript transitive tree. AGP 9
// has built-in Kotlin, so the org.jetbrains.kotlin.android plugin is no longer
// applied (see https://kotl.in/gradle/agp-built-in-kotlin).
//
// AGP 9.4.1 already ships patched netty (dropped), protobuf-java 3.25.5,
// commons-io 2.16.1 and commons-compress 1.27.1. The constraints below force the
// few transitive deps AGP still pins below their Dependabot fix floor
// (bouncycastle, jose4j, jdom2, kotlin-gradle-plugin) up to patched versions in
// the plugin/buildscript classpath. These are BUILD-TIME ONLY deps (they run
// Gradle/AGP; nothing here ships in an APK), so this is defence-in-depth to clear
// the security tab, not a runtime exposure.
buildscript {
    dependencies {
        constraints {
            classpath("org.bouncycastle:bcprov-jdk18on:1.85")
            classpath("org.bouncycastle:bcpkix-jdk18on:1.85")
            classpath("org.bouncycastle:bcutil-jdk18on:1.85")
            classpath("org.bitbucket.b_c:jose4j:0.9.6")
            classpath("org.jdom:jdom2:2.0.6.1")
            classpath("org.jetbrains.kotlin:kotlin-gradle-plugin:2.4.20")
        }
    }
}

plugins {
    id("com.android.library") version "9.4.1" apply false
}
