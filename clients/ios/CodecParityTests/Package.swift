// swift-tools-version:5.9
// Codec iOS parity test package (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3, ADR-9).
//
// Wires the CodecParityTests XCTest target to the codec's UniFFI Swift module +
// the native xcframework. The xcframework (PgcProtocolFfiFFI.xcframework) and
// the generated Swift source are produced by
// platform/mobile/build_codec_mobile.sh + `xcodebuild -create-xcframework` (see
// platform/mobile/README-codec.md); this manifest is the stable scaffold they
// slot into.
//
// Run (needs the xcframework present under ./Frameworks):
//   xcodebuild test -scheme CodecParityTests-Package \
//     -destination 'platform=iOS Simulator,name=iPhone 16'
//   (SwiftPM auto-names the scheme `<package>-Package`; `xcodebuild -list` confirms.)

import PackageDescription

let package = Package(
    name: "CodecParityTests",
    platforms: [.iOS(.v15)],
    targets: [
        // The native codec as a prebuilt xcframework (device + simulator
        // slices). Produced by the build script; not committed (large binary).
        .binaryTarget(
            name: "PgcProtocolFfiFFI",
            path: "Frameworks/PgcProtocolFfiFFI.xcframework"
        ),
        // C interop target publishing the `pgc_protocol_ffiFFI` Clang module —
        // the generated pgc_protocol_ffi.swift gates its low-level FFI types on
        // `#if canImport(pgc_protocol_ffiFFI)`; without this they are out of
        // scope. Header + module.modulemap are placed by build_codec_mobile.sh.
        .target(
            name: "PgcProtocolFfiC",
            path: "Sources/PgcProtocolFfiC"
        ),
        // The generated UniFFI Swift bindings + a module wrapper over the FFI.
        .target(
            name: "PgcProtocolFfi",
            dependencies: ["PgcProtocolFfiFFI", "PgcProtocolFfiC"],
            path: "Sources/PgcProtocolFfi" // populated by the bindgen step
        ),
        .testTarget(
            name: "CodecParityTests",
            dependencies: ["PgcProtocolFfi"],
            path: "Tests/CodecParityTests"
        ),
    ]
)
