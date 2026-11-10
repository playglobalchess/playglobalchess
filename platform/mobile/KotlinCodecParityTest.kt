// Codec mobile parity test (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3, NFR-10,
// ADR-9). Asserts the Android consumer reproduces the frozen codec bytes
// byte-for-byte through the generated UniFFI Kotlin bindings. The binding
// COMPILES on the host (run_codec_parity.sh type-checks it with kotlinc + JNA);
// this test RUNS on an Android emulator/device, where the aarch64 .so loads (it
// cannot on a host JVM).
//
// Frames mirror protocol/codec-ffi/tests/codec_parity_oracle.rs and are a subset
// of protocol/vectors/vectors.json — change in lockstep (the parity contract).

import uniffi.pgc_protocol_ffi.decodeClient
import uniffi.pgc_protocol_ffi.decodeEvent
import uniffi.pgc_protocol_ffi.decodeServer
import uniffi.pgc_protocol_ffi.encodeClient
import uniffi.pgc_protocol_ffi.encodeEvent
import uniffi.pgc_protocol_ffi.encodeServer

private const val CLIENT_MOVE_PROMO_HEX =
    "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
private const val SERVER_GAME_END_HEX =
    "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
private const val SERVER_MOVE_ACCEPTED_HEX =
    "01822466343761633130622d353863632d343337322d613536372d3065303262326333643437391204653265349c9412"
private const val SERVER_STATE_PENDING_HEX =
    "01842466343761633130622d353863632d343337322d613536372d3065303262326333643437393b726e62716b626e722f70707070707070702f382f382f3450332f382f50505050315050502f524e42514b424e522062204b516b7120653320302031010290a10fa0ef0f0112020000046731663300011201f483cea6a532"
private const val EVENT_RATING_APPLIED_HEX =
    "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"

private fun fromHex(h: String): ByteArray =
    ByteArray(h.length / 2) { ((h[it * 2].digitToInt(16) shl 4) or h[it * 2 + 1].digitToInt(16)).toByte() }

fun main() {
    var failures = 0
    fun check(cond: Boolean, msg: String) {
        if (cond) println("OK  $msg") else { println("FAIL $msg"); failures++ }
    }

    val c = fromHex(CLIENT_MOVE_PROMO_HEX)
    check(encodeClient(decodeClient(c)).contentEquals(c), "client move (e7e8q) decode→re-encode identical")

    val s = fromHex(SERVER_GAME_END_HEX)
    check(encodeServer(decodeServer(s)).contentEquals(s), "server game_end decode→re-encode identical")

    val ma = fromHex(SERVER_MOVE_ACCEPTED_HEX)
    check(encodeServer(decodeServer(ma)).contentEquals(ma), "server move_accepted (§4A v1.2, carries uci) decode→re-encode identical")

    val st = fromHex(SERVER_STATE_PENDING_HEX)
    val stMsg = decodeServer(st)
    val body = (stMsg as? uniffi.pgc_protocol_ffi.ServerMsg.State)?.state
    check(
        body != null && body.youSeat == 1.toUByte() && body.ply == 18u && body.status == 2.toUByte() &&
            body.lastMoveUci == "g1f3" && body.pendingDrawBy == 0.toUByte() && body.takebackBy == 1.toUByte() &&
            body.takebackAtPly == 18u && body.runningSeat == 1.toUByte(),
        "server state (§4A v1.3) v1.3 fields read through the binding",
    )
    check(encodeServer(stMsg).contentEquals(st), "server state (§4A v1.3) decode→re-encode identical")

    val e = fromHex(EVENT_RATING_APPLIED_HEX)
    check(encodeEvent(decodeEvent(e)).contentEquals(e), "event rating_applied decode→re-encode identical")

    var threw = false
    try { decodeClient(ByteArray(0)) } catch (ex: Exception) { threw = true }
    check(threw, "empty frame throws (no FFI panic)")

    if (failures == 0) println("Kotlin codec parity: all checks pass")
    else { println("Kotlin codec parity: $failures FAILED"); kotlin.system.exitProcess(1) }
}
