// §8.8 Kotlin parity test (ENGINE-CORE §8.8, ADR-9). Asserts the Android
// consumer reproduces the host oracle byte-for-byte through the generated
// UniFFI Kotlin bindings. The binding COMPILES on the host (CI type-checks it
// with kotlinc + JNA); this test RUNS on an Android emulator/device, where the
// aarch64 .so loads (it cannot load on a host JVM).
//
// Oracle constants mirror engine/tests/mobile_parity_oracle.rs — change in
// lockstep (the parity contract).

import uniffi.pgc_engine_ffi.perftFromFen
import uniffi.pgc_engine_ffi.startposFen
import uniffi.pgc_engine_ffi.replaySans

private const val STARTPOS_PERFT_D5: ULong = 4_865_609u
private val GOLDEN_SANS = listOf("e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#")
private const val GOLDEN_FINAL_FEN =
    "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4"

fun main() {
    var failures = 0
    fun check(cond: Boolean, msg: String) {
        if (cond) println("OK  $msg") else { println("FAIL $msg"); failures++ }
    }

    val d5 = perftFromFen(startposFen(), 5u)
    check(d5 == STARTPOS_PERFT_D5, "startpos perft d5 == $STARTPOS_PERFT_D5 (got $d5)")

    val fens = replaySans(GOLDEN_SANS)
    check(fens.size == 7, "golden slice has 7 plies (got ${fens.size})")
    check(fens.last() == GOLDEN_FINAL_FEN, "golden slice final FEN matches oracle")

    if (failures == 0) println("Kotlin parity: all checks pass")
    else { println("Kotlin parity: $failures FAILED"); kotlin.system.exitProcess(1) }
}
