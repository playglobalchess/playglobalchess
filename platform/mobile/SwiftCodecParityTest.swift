// Codec mobile parity test (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3, NFR-10,
// ADR-9). Runs on the HOST (macOS) against the generated Swift bindings + the
// host dylib — proving the Swift consumer binds the codec and reproduces the
// frozen bytes byte-for-byte. The on-device run uses the SAME file against the
// aarch64-apple-ios build.
//
// The frames below are a subset of protocol/vectors/vectors.json and mirror
// protocol/codec-ffi/tests/codec_parity_oracle.rs — change in lockstep (the
// parity contract). The test decodes each frame through the binding and asserts
// the decoded value re-encodes to the identical bytes (NFR-10 proof on mobile).
//
// Run via platform/mobile/run_codec_parity.sh.

import Foundation

// Hex-encoded committed golden frames (ver|type|payload).
let CLIENT_MOVE_PROMO_HEX =
    "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
let SERVER_GAME_END_HEX =
    "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
let SERVER_MOVE_ACCEPTED_HEX =
    "01822466343761633130622d353863632d343337322d613536372d3065303262326333643437391204653265349c9412"
let SERVER_STATE_PENDING_HEX =
    "01842466343761633130622d353863632d343337322d613536372d3065303262326333643437393b726e62716b626e722f70707070707070702f382f382f3450332f382f50505050315050502f524e42514b424e522062204b516b7120653320302031010290a10fa0ef0f0112020000046731663300011201f483cea6a532"
let EVENT_RATING_APPLIED_HEX =
    "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"

func fromHex(_ h: String) -> Data {
    var out = Data()
    var i = h.startIndex
    while i < h.endIndex {
        let j = h.index(i, offsetBy: 2)
        out.append(UInt8(h[i..<j], radix: 16)!)
        i = j
    }
    return out
}

var failures = 0
func check(_ cond: Bool, _ msg: String) {
    if cond { print("OK  \(msg)") } else { print("FAIL \(msg)"); failures += 1 }
}

do {
    // Client: decode → re-encode is byte-identical.
    let c = fromHex(CLIENT_MOVE_PROMO_HEX)
    let cMsg = try decodeClient(frame: c)
    check(try encodeClient(msg: cMsg) == c, "client move (e7e8q) decode→re-encode identical")

    // Server.
    let s = fromHex(SERVER_GAME_END_HEX)
    let sMsg = try decodeServer(frame: s)
    check(try encodeServer(msg: sMsg) == s, "server game_end decode→re-encode identical")

    // Server: §4A v1.2 MOVE_ACCEPTED carries uci (ADR-18).
    let ma = fromHex(SERVER_MOVE_ACCEPTED_HEX)
    let maMsg = try decodeServer(frame: ma)
    check(try encodeServer(msg: maMsg) == ma, "server move_accepted (§4A v1.2, carries uci) decode→re-encode identical")

    // Server: §4A v1.3 STATE (pending draw + takeback). Decode, READ the new
    // fields through the binding, and re-encode identically.
    let st = fromHex(SERVER_STATE_PENDING_HEX)
    let stMsg = try decodeServer(frame: st)
    if case let .state(_, body) = stMsg {
        check(body.youSeat == 1 && body.ply == 18 && body.status == 2 && body.lastMoveUci == "g1f3"
            && body.pendingDrawBy == 0 && body.takebackBy == 1 && body.takebackAtPly == 18
            && body.runningSeat == 1, "server state (§4A v1.3) v1.3 fields read through the binding")
    } else {
        check(false, "server state (§4A v1.3) decodes as .state")
    }
    check(try encodeServer(msg: stMsg) == st, "server state (§4A v1.3) decode→re-encode identical")

    // Event.
    let e = fromHex(EVENT_RATING_APPLIED_HEX)
    let env = try decodeEvent(bytes: e)
    check(try encodeEvent(env: env) == e, "event rating_applied decode→re-encode identical")

    // Negative: a malformed frame throws rather than crashing (no panic across FFI).
    var threw = false
    do { _ = try decodeClient(frame: Data()) } catch { threw = true }
    check(threw, "empty frame throws (no FFI panic)")
} catch {
    print("FAIL threw unexpectedly: \(error)")
    failures += 1
}

if failures == 0 {
    print("Swift codec parity: all checks pass")
    exit(0)
} else {
    print("Swift codec parity: \(failures) FAILED")
    exit(1)
}
