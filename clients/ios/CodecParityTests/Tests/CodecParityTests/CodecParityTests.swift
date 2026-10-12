// Codec iOS parity — XCTest target (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3,
// NFR-10, ADR-9).
//
// Runs the SAME parity assertions as platform/mobile/SwiftCodecParityTest.swift,
// but as XCTest cases so they execute in an iOS Simulator (or on-device) run via
// `xcodebuild test`. The codec is linked as the UniFFI xcframework built by
// platform/mobile/build_codec_mobile.sh (aarch64-apple-ios + -ios-sim).
//
// Frames are a subset of protocol/vectors/vectors.json and mirror
// protocol/codec-ffi/tests/codec_parity_oracle.rs — change in lockstep (the
// parity contract). This is the DEVICE-GATED last mile; the same codec already
// passes byte-identically on the host (run_codec_parity.sh) and in the pure
// crate's golden_freeze test.

import XCTest
@testable import PgcProtocolFfi // the generated UniFFI Swift module

final class CodecParityTests: XCTestCase {
    private let clientMovePromoHex =
        "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
    private let serverGameEndHex =
        "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
    private let eventRatingAppliedHex =
        "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"

    private func fromHex(_ h: String) -> Data {
        var out = Data()
        var i = h.startIndex
        while i < h.endIndex {
            let j = h.index(i, offsetBy: 2)
            out.append(UInt8(h[i..<j], radix: 16)!)
            i = j
        }
        return out
    }

    func testClientDecodeReencodeIdentity() throws {
        let b = fromHex(clientMovePromoHex)
        XCTAssertEqual(try encodeClient(msg: decodeClient(frame: b)), b)
    }

    func testServerDecodeReencodeIdentity() throws {
        let b = fromHex(serverGameEndHex)
        XCTAssertEqual(try encodeServer(msg: decodeServer(frame: b)), b)
    }

    func testEventDecodeReencodeIdentity() throws {
        let b = fromHex(eventRatingAppliedHex)
        XCTAssertEqual(try encodeEvent(env: decodeEvent(bytes: b)), b)
    }

    func testMalformedFrameThrowsNotCrash() {
        // A panic across the FFI boundary crashes the app (§5): must throw.
        XCTAssertThrowsError(try decodeClient(frame: Data()))
    }
}
