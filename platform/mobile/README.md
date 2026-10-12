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

## What is DEVICE-GATED (cannot run on a host JVM)

The Kotlin parity **run** needs an Android emulator or device: the
`aarch64-linux-android` `.so` does not load on the host JVM. Wire
`KotlinParityTest.kt` into an Android instrumented-test target (or a Robolectric
+ arm64 translation setup) and run on an emulator. The iOS run on a simulator
likewise uses `SwiftParityTest.swift` against the `aarch64-apple-ios-sim` build
in an Xcode test target.

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
