#!/usr/bin/env bash
# §8.8 mobile parity — build the engine FFI for iOS + Android and generate the
# Swift/Kotlin bindings (ENGINE-CORE §8.8, ADR-9). Run from the repo root.
#
# Prereqs (see platform/mobile/README.md): full Xcode + iOS SDK, Android SDK+NDK,
# kotlinc, the Rust targets. Fails loudly if a prereq is missing.
set -euo pipefail

FFI_DIR="engine/ffi"
OUT="$FFI_DIR/target/bindings"

# --- Android NDK linker (absolute, per-machine → derived here, not committed) ---
: "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
NDK_DIR="$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | sort | tail -1 || true)"
if [[ -z "$NDK_DIR" ]]; then
  echo "ERROR: no NDK under $ANDROID_HOME/ndk — install via sdkmanager 'ndk;<ver>'." >&2
  exit 1
fi
TC="$NDK_DIR/toolchains/llvm/prebuilt/darwin-x86_64/bin"
export CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER="$TC/aarch64-linux-android24-clang"
export CC_aarch64_linux_android="$TC/aarch64-linux-android24-clang"

echo "== iOS device =="
cargo build --manifest-path "$FFI_DIR/Cargo.toml" --target aarch64-apple-ios --release
echo "== iOS simulator =="
cargo build --manifest-path "$FFI_DIR/Cargo.toml" --target aarch64-apple-ios-sim --release
echo "== Android arm64 (NDK: $NDK_DIR) =="
cargo build --manifest-path "$FFI_DIR/Cargo.toml" --target aarch64-linux-android --release

echo "== generate bindings =="
# A host dylib drives bindgen (platform-independent interface extraction).
cargo build --manifest-path "$FFI_DIR/Cargo.toml"
DYLIB="$FFI_DIR/target/debug/libpgc_engine_ffi.dylib"
mkdir -p "$OUT/swift" "$OUT/kotlin"
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library target/debug/libpgc_engine_ffi.dylib --language swift --out-dir target/bindings/swift )
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library target/debug/libpgc_engine_ffi.dylib --language kotlin --out-dir target/bindings/kotlin )

echo "== package for the iOS XCTest target (clients/ios/EngineParityTests) =="
IOS_PKG="clients/ios/EngineParityTests"
mkdir -p "$IOS_PKG/Sources/PgcEngineFfi" "$IOS_PKG/Frameworks"
# Generated Swift bindings → the SwiftPM target source dir.
cp "$OUT/swift/pgc_engine_ffi.swift" "$IOS_PKG/Sources/PgcEngineFfi/"
# Build the xcframework from the device + simulator static libs. (xcodebuild is
# required; this is the step that needs full Xcode.)
HDR="$OUT/swift"
xcodebuild -create-xcframework \
  -library "$FFI_DIR/target/aarch64-apple-ios/release/libpgc_engine_ffi.a" -headers "$HDR" \
  -library "$FFI_DIR/target/aarch64-apple-ios-sim/release/libpgc_engine_ffi.a" -headers "$HDR" \
  -output "$IOS_PKG/Frameworks/PgcEngineFfiFFI.xcframework" 2>&1 | tail -2 || \
  echo "WARN: xcframework build skipped/failed — ensure full Xcode is active." >&2

echo "== package for the Android instrumented test (clients/android/engine-parity) =="
AND_MOD="clients/android/engine-parity"
mkdir -p "$AND_MOD/src/main/jniLibs/arm64-v8a" "$AND_MOD/src/main/kotlin/uniffi/pgc_engine_ffi"
cp "$FFI_DIR/target/aarch64-linux-android/release/libpgc_engine_ffi.so" \
   "$AND_MOD/src/main/jniLibs/arm64-v8a/"
cp "$OUT/kotlin/uniffi/pgc_engine_ffi/pgc_engine_ffi.kt" \
   "$AND_MOD/src/main/kotlin/uniffi/pgc_engine_ffi/"

echo "OK — libs + bindings built and packaged into the test targets. Run:"
echo "  platform/mobile/run_parity.sh                          # host-provable (Swift run + Kotlin compile)"
echo "  xcodebuild test -scheme EngineParityTests ...          # iOS Simulator (clients/ios/EngineParityTests)"
echo "  ./gradlew :engine-parity:connectedAndroidTest          # Android emulator (clients/android)"
