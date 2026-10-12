// §8.8 Android parity — instrumented test (ENGINE-CORE §8.8, NFR-10, ADR-9).
//
// Runs the SAME parity assertions as platform/mobile/KotlinParityTest.kt, but as
// a JUnit instrumented test so it executes on an Android emulator/device via
// `./gradlew connectedAndroidTest` — where the aarch64 .so loads (it cannot on a
// host JVM). The engine .so + the generated UniFFI Kotlin bindings are placed by
// platform/mobile/build_mobile.sh (see platform/mobile/README.md).
//
// Oracle constants mirror engine/tests/mobile_parity_oracle.rs — change in
// lockstep (the parity contract). DEVICE-GATED last mile; the engine already
// passes byte-identically on the host.

package com.playglobalchess.engineparity

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import uniffi.pgc_engine_ffi.perftFromFen
import uniffi.pgc_engine_ffi.replaySans
import uniffi.pgc_engine_ffi.startposFen

@RunWith(AndroidJUnit4::class)
class EngineParityTest {
    private val startposPerftD5: ULong = 4_865_609u
    private val goldenSans = listOf("e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#")
    private val goldenFinalFen =
        "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4"

    @Test
    fun startposPerftD5MatchesOracle() {
        assertEquals(startposPerftD5, perftFromFen(startposFen(), 5u))
    }

    @Test
    fun goldenSliceFinalFenMatchesOracle() {
        val fens = replaySans(goldenSans)
        assertEquals(7, fens.size)
        assertEquals(goldenFinalFen, fens.last())
    }
}
