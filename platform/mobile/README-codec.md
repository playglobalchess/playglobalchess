# Codec mobile parity (§8 / PLAN-CODEC §4.3) — iOS + Android

SPEC-S2-PROTOCOL §8 / NFR-10 / ADR-9 / ADR-14: the **same** Rust codec
(`pgc-protocol`), compiled into the iOS (Swift) and Android (Kotlin) client
cores via UniFFI, must produce **byte-identical** wire/event bytes to the
server. This is the codec parity harness — the sibling of the engine's §8.8
harness (`platform/mobile/README.md`).

## Architecture

```
protocol/codec/      pure pgc-protocol codec (no FFI deps, #![forbid(unsafe_code)])
protocol/codec-ffi/  pgc-protocol-ffi — UniFFI bindings (depends on pgc-protocol)
                     exposes encode_client/decode_client, encode_server/decode_server,
                     encode_event/decode_event + the §4A mirror types
platform/mobile/     build + parity scripts, Swift/Kotlin codec test sources, this doc
```

The **oracle** — the exact bytes both platforms must reproduce — is the frozen
golden-vector corpus `protocol/vectors/vectors.json`, with a fast representative
subset pinned in `protocol/codec-ffi/tests/codec_parity_oracle.rs` (host test).
The Swift/Kotlin tests decode each committed frame through the generated binding
and assert the decoded value **re-encodes to the identical bytes**.

## What is PROVEN on this host (CI-able on a mac runner)

- `pgc-protocol-ffi` **cross-compiles** for `aarch64-apple-ios`,
  `aarch64-apple-ios-sim`, and `aarch64-linux-android` (NDK clang linker).
- UniFFI generates **Swift and Kotlin** bindings cleanly.
- **Swift codec parity RUNS** (`run_codec_parity.sh`): the Swift consumer,
  through the generated bindings + the codec dylib, decodes→re-encodes the
  frozen frames byte-for-byte.
- **Kotlin binding type-checks** (`kotlinc` + JNA).
- The pure crate's `golden_freeze` test already proves the full 33-vector corpus
  byte-for-byte in Rust (`cargo test -p pgc-protocol`).

Run it:
```sh
platform/mobile/build_codec_mobile.sh   # cross-build all targets + generate bindings
platform/mobile/run_codec_parity.sh     # Swift run + Kotlin binding compile
```

## Device test targets (scaffolded) + how to run them

- **iOS** — `clients/ios/CodecParityTests/` (SwiftPM): `Package.swift` + the
  XCTest `CodecParityTests.swift`. `build_codec_mobile.sh` drops the generated
  Swift binding into `Sources/PgcProtocolFfi/` and the
  `PgcProtocolFfiFFI.xcframework` (device + simulator slices) into `Frameworks/`
  (both gitignored — build outputs). Run on a simulator:
  ```sh
  # One-time: install an iOS Simulator runtime if absent
  # (`xcrun simctl list runtimes`; if none, `xcodebuild -downloadPlatform iOS`).
  platform/mobile/build_codec_mobile.sh
  cd clients/ios/CodecParityTests
  # SwiftPM auto-names the scheme `<package>-Package` — list with `xcodebuild -list`.
  xcodebuild test -scheme CodecParityTests-Package \
    -destination 'platform=iOS Simulator,name=iPhone 16'
  ```
- **Android** — `clients/android/codec-parity/` (Gradle library module):
  `build.gradle.kts` + the instrumented `CodecParityTest.kt`.
  `build_codec_mobile.sh` drops the `.so` into `src/main/jniLibs/arm64-v8a/` and
  the Kotlin binding into `src/main/kotlin/uniffi/` (both gitignored). Run on an
  emulator:
  ```sh
  platform/mobile/build_codec_mobile.sh
  ./gradlew :codec-parity:connectedAndroidTest
  ```

Both targets assert the SAME frames (`protocol/codec-ffi/tests/codec_parity_oracle.rs`).
The committed files are the hand-written scaffolds + tests; the generated
bindings, native libs, and xcframework are build outputs and are gitignored.

## What is DEVICE-GATED (cannot run on a host JVM / in this sandbox)

The Kotlin parity **run** needs an Android emulator or device (the aarch64 `.so`
does not load on a host JVM), and the iOS XCTest run needs an iOS Simulator (an
Xcode test host). On the KiroCrew sandbox box, `xcodebuild test` for a SwiftPM
package additionally cannot run (macOS forbids nesting a second sandbox), so the
Simulator/emulator runs are a **human Terminal action** — the same last mile the
engine's §8.8 parity needed. The scaffolds above are ready; the codec/binding
correctness they prove is already established host-side (the Swift run is
byte-identical, the Kotlin binding compiles, and the Rust golden-freeze covers
the full corpus), so a device divergence could only come from the toolchain, not
the codec.

## Prerequisites (host)

- **iOS:** full Xcode + iOS SDK; `rustup target add aarch64-apple-ios
  aarch64-apple-ios-sim`.
- **Android:** Android SDK + NDK (`ANDROID_HOME` set); `rustup target add
  aarch64-linux-android`; `kotlinc` on PATH; a JNA jar for the type-check.
- UniFFI bindgen is the crate's own `uniffi-bindgen` bin (no global install).

## Toolchain versions pinned at bring-up (2026-10-12)

uniffi 0.32.2 (matches the engine FFI). A UniFFI major bump can change the
generated binding API — update deliberately and in lockstep with the engine FFI.
