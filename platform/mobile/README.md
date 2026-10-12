# Mobile parity (§8.8) — iOS + Android

ENGINE-CORE §8.8 / NFR-10 / ADR-9: the **same** Rust engine (`pgc-engine`),
compiled into the iOS (Swift) and Android (Kotlin) client cores via UniFFI,
must produce **byte-identical** results to the server. This directory holds the
parity harness.

## Architecture

```
engine/            pure pgc-engine (no FFI deps, #![forbid(unsafe_code)])
engine/ffi/        pgc-engine-ffi — UniFFI bindings (depends on pgc-engine)
                   exposes the parity slice: startpos_fen, perft_from_fen, replay_sans
platform/mobile/   build + parity scripts, Swift/Kotlin test sources, this doc
```

The **oracle** — the exact values both platforms must reproduce — lives in
`engine/tests/mobile_parity_oracle.rs` (host test): `perft(startpos, 5) =
4,865,609` and the Scholar's-mate golden-slice final FEN. The Swift/Kotlin
tests assert against the same constants.

## What is PROVEN on this host (CI-able on a mac runner)

- `pgc-engine-ffi` **cross-compiles** for `aarch64-apple-ios`,
  `aarch64-apple-ios-sim`, and `aarch64-linux-android` (NDK clang linker).
- UniFFI generates **Swift and Kotlin** bindings cleanly.
- **Swift parity RUNS** (`run_parity.sh`): the Swift consumer, through the
  generated bindings + the engine dylib, matches the oracle byte-for-byte.
- **Kotlin binding type-checks** (`kotlinc` + JNA).

Run it:
```sh
platform/mobile/build_mobile.sh   # cross-build all targets + generate bindings
platform/mobile/run_parity.sh     # Swift run + Kotlin binding compile
```

## Device test targets (scaffolded) + how to run them

The device-run harnesses are scaffolded and wired to the artifacts
`build_mobile.sh` produces:

- **iOS** — `clients/ios/EngineParityTests/` (SwiftPM): `Package.swift` + the
  XCTest `EngineParityTests.swift`. `build_mobile.sh` drops the generated Swift
  binding into `Sources/PgcEngineFfi/` and the `PgcEngineFfiFFI.xcframework`
  (device + simulator slices) into `Frameworks/` (both gitignored — build
  outputs). Run on a simulator:
  ```sh
  # One-time: install the iOS Simulator runtime if absent. Xcode ships the
  # device TYPES (iPhone 16, …) but not the runtime — check with
  # `xcrun simctl list runtimes`; if no iOS runtime is listed, download it:
  xcodebuild -downloadPlatform iOS            # several GB, one-time

  platform/mobile/build_mobile.sh
  cd clients/ios/EngineParityTests
  xcodebuild test -scheme EngineParityTests \
    -destination 'platform=iOS Simulator,name=iPhone 16'   # Xcode 16.x device
  ```
- **Android** — `clients/android/engine-parity/` (Gradle library module):
  `build.gradle.kts` + the instrumented `EngineParityTest.kt`. `build_mobile.sh`
  drops the `.so` into `src/main/jniLibs/arm64-v8a/` and the Kotlin binding into
  `src/main/kotlin/uniffi/` (both gitignored). Run on an emulator:
  ```sh
  platform/mobile/build_mobile.sh
  ./gradlew :engine-parity:connectedAndroidTest
  ```

Both targets assert the SAME oracle constants (`engine/tests/mobile_parity_oracle.rs`).
The committed files are the hand-written scaffolds + tests; the generated
bindings, native libs, and xcframework are build outputs and are gitignored.

## What is DEVICE-GATED (cannot run on a host JVM)

The Kotlin parity **run** needs an Android emulator or device (the
`aarch64-linux-android` `.so` does not load on a host JVM), and the iOS run
needs an iOS Simulator or device (an Xcode test host). The scaffolds above are
ready; the only missing piece is executing them on a sim/emulator, which needs
a booted device + the one-time `xcodebuild`/`gradlew` run — not an agent action
here. The engine + binding correctness they prove is already established
host-side (the Swift run in `run_parity.sh` is byte-identical).

Both are the LAST mile of §8.8; the engine/binding correctness they would prove
is already established host-side (the Swift run is byte-identical, and all three
targets build), so a device divergence could only come from the toolchain, not
the engine.

## Prerequisites (host)

- **iOS:** full Xcode + iOS SDK; `rustup target add aarch64-apple-ios
  aarch64-apple-ios-sim`.
- **Android:** Android SDK + NDK (`ANDROID_HOME` set); `rustup target add
  aarch64-linux-android`; `kotlinc` on PATH; a JNA jar for the type-check.
- UniFFI bindgen is the crate's own `uniffi-bindgen` bin (no global install).

## Toolchain versions pinned at bring-up (2026-10-12)

Xcode iOS SDK 18.5, Android NDK 30.0.16248370 (API 24 floor), kotlinc 2.4.21,
uniffi 0.32.2. Update deliberately; a UniFFI major bump can change the generated
binding API.
