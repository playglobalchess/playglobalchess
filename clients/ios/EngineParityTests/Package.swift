// swift-tools-version:5.9
// §8.8 iOS parity test package (ENGINE-CORE §8.8, ADR-9).
//
// Wires the EngineParityTests XCTest target to the engine's UniFFI Swift module
// + the native xcframework. The xcframework (PgcEngineFfiFFI.xcframework) and the
// generated Swift source are produced by platform/mobile/build_mobile.sh +
// `xcodebuild -create-xcframework` (see platform/mobile/README.md); this manifest
// is the stable scaffold they slot into.
//
// Run (needs the xcframework present under ./Frameworks):
//   xcodebuild test -scheme EngineParityTests \
//     -destination 'platform=iOS Simulator,name=iPhone 15'

import PackageDescription

let package = Package(
    name: "EngineParityTests",
    platforms: [.iOS(.v15)],
    targets: [
        // The native engine as a prebuilt xcframework (device + simulator
        // slices). Produced by the build script; not committed (large binary).
        .binaryTarget(
            name: "PgcEngineFfiFFI",
            path: "Frameworks/PgcEngineFfiFFI.xcframework"
        ),
        // The generated UniFFI Swift bindings + a module wrapper over the FFI.
        .target(
            name: "PgcEngineFfi",
            dependencies: ["PgcEngineFfiFFI"],
            path: "Sources/PgcEngineFfi" // populated by the bindgen step
        ),
        .testTarget(
            name: "EngineParityTests",
            dependencies: ["PgcEngineFfi"],
            path: "Tests/EngineParityTests"
        ),
    ]
)
