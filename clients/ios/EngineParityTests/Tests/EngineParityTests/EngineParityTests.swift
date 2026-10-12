// §8.8 iOS parity — XCTest target (ENGINE-CORE §8.8, NFR-10, ADR-9).
//
// Runs the SAME parity assertions as platform/mobile/SwiftParityTest.swift, but
// as XCTest cases so they execute in an iOS Simulator (or on-device) test run
// via `xcodebuild test`. The engine is linked as the UniFFI xcframework built
// by platform/mobile/build_mobile.sh (aarch64-apple-ios + -ios-sim).
//
// Oracle constants mirror engine/tests/mobile_parity_oracle.rs — change in
// lockstep (the parity contract). This is the DEVICE-GATED last mile; the same
// engine already passes byte-identically on the host (see run_parity.sh).

import XCTest
@testable import PgcEngineFfi // the generated UniFFI Swift module

final class EngineParityTests: XCTestCase {
    private let startposPerftD5: UInt64 = 4_865_609
    private let goldenSans = ["e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#"]
    private let goldenFinalFen =
        "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4"

    func testStartposPerftD5() throws {
        let d5 = try perftFromFen(fen: startposFen(), depth: 5)
        XCTAssertEqual(d5, startposPerftD5, "startpos perft d5 must match the host oracle")
    }

    func testGoldenSliceFinalFen() throws {
        let fens = try replaySans(sans: goldenSans)
        XCTAssertEqual(fens.count, 7, "golden slice must be 7 plies")
        XCTAssertEqual(fens.last, goldenFinalFen, "golden slice final FEN must match the oracle")
    }

    func testBadFenIsErrorNotCrash() {
        // A panic across the FFI boundary crashes the app (§5): must be a thrown
        // error, not a trap.
        XCTAssertThrowsError(try perftFromFen(fen: "not a fen", depth: 1))
    }
}
