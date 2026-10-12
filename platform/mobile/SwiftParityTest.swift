// §8.8 Swift parity test (ENGINE-CORE §8.8, ADR-9). Runs on the HOST (macOS)
// against the generated Swift bindings + the host dylib — proving the Swift
// consumer binds the engine and reproduces the host oracle byte-for-byte. The
// on-device run uses the SAME file against the aarch64-apple-ios build.
//
// Oracle constants mirror engine/tests/mobile_parity_oracle.rs. If those change,
// change these in lockstep (the parity contract).
//
// Run via platform/mobile/run_parity.sh (which compiles this against the
// generated pgc_engine_ffi.swift + links libpgc_engine_ffi.dylib).

import Foundation

let STARTPOS_PERFT_D5: UInt64 = 4_865_609
let GOLDEN_SANS = ["e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#"]
let GOLDEN_FINAL_FEN =
    "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4"

var failures = 0
func check(_ cond: Bool, _ msg: String) {
    if cond { print("OK  \(msg)") } else { print("FAIL \(msg)"); failures += 1 }
}

// `perftFromFen` / `startposFen` / `replaySans` are the UniFFI-exported funcs
// (camelCased by the Swift binding).
do {
    let d5 = try perftFromFen(fen: startposFen(), depth: 5)
    check(d5 == STARTPOS_PERFT_D5, "startpos perft d5 == \(STARTPOS_PERFT_D5) (got \(d5))")

    let fens = try replaySans(sans: GOLDEN_SANS)
    check(fens.count == 7, "golden slice has 7 plies (got \(fens.count))")
    check(fens.last == GOLDEN_FINAL_FEN, "golden slice final FEN matches oracle")
} catch {
    print("FAIL threw: \(error)")
    failures += 1
}

if failures == 0 {
    print("Swift parity: all checks pass")
    exit(0)
} else {
    print("Swift parity: \(failures) FAILED")
    exit(1)
}
