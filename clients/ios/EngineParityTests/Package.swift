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
//   xcodebuild test -scheme EngineParityTests-Package \
//     -destination 'platform=iOS Simulator,name=iPhone 16'
//   (SwiftPM auto-names the scheme `<package>-Package`; `xcodebuild -list` confirms.)

import PackageDescription

let package = Package(
    name: "EngineParityTests",
    platforms: [.iOS(.v15)],
    targets: [
        // The native engine as a prebuilt xcframework (device + simulator
        // slices). Produced by the build script; not committed (large binary).
        // Supplies the compiled UniFFI symbols (the static .a); its headers are
        // NOT auto-promoted to an importable Clang module by SwiftPM, so the
        // module itself is published by PgcEngineFfiC below.
        .binaryTarget(
            name: "PgcEngineFfiFFI",
            path: "Frameworks/PgcEngineFfiFFI.xcframework"
        ),
        // C interop target publishing the `pgc_engine_ffiFFI` Clang module.
        // The generated pgc_engine_ffi.swift does `#if canImport(pgc_engine_ffiFFI)`
        // for its low-level FFI types (RustBuffer, RustCallStatus, the
        // uniffi_*_checksum_* / ffi_*_contract_version symbols); without this
        // module that import is skipped and every FFI type is out of scope.
        // Header + module.modulemap are placed by platform/mobile/build_mobile.sh.
        .target(
            name: "PgcEngineFfiC",
            path: "Sources/PgcEngineFfiC"
        ),
        // The generated UniFFI Swift bindings + a module wrapper over the FFI.
        .target(
            name: "PgcEngineFfi",
            dependencies: ["PgcEngineFfiFFI", "PgcEngineFfiC"],
            path: "Sources/PgcEngineFfi" // populated by the bindgen step
        ),
        .testTarget(
            name: "EngineParityTests",
            dependencies: ["PgcEngineFfi"],
            path: "Tests/EngineParityTests"
        ),
    ]
)
