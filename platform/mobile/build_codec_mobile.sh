#!/usr/bin/env bash
# Codec mobile parity (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3) — build the codec
# FFI for iOS + Android and generate the Swift/Kotlin bindings. Run from the
# repo root. Mirrors platform/mobile/build_mobile.sh (the engine FFI).
#
# Prereqs (see platform/mobile/README-codec.md): full Xcode + iOS SDK, Android
# SDK+NDK, kotlinc, the Rust targets. Fails loudly if a prereq is missing.
set -euo pipefail

FFI_DIR="protocol/codec-ffi"
OUT="$FFI_DIR/target/bindings"
LIB="pgc_protocol_ffi"

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
cargo build --manifest-path "$FFI_DIR/Cargo.toml"
mkdir -p "$OUT/swift" "$OUT/kotlin"
DYLIB_REL="target/debug/lib${LIB}.dylib"
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library "$DYLIB_REL" --language swift --out-dir target/bindings/swift )
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library "$DYLIB_REL" --language kotlin --out-dir target/bindings/kotlin )

echo "== package for the iOS XCTest target (clients/ios/CodecParityTests) =="
IOS_PKG="clients/ios/CodecParityTests"
mkdir -p "$IOS_PKG/Sources/PgcProtocolFfi" "$IOS_PKG/Frameworks"
cp "$OUT/swift/${LIB}.swift" "$IOS_PKG/Sources/PgcProtocolFfi/"

# --- C interop target: publish the `pgc_protocol_ffiFFI` Clang module ---------
# The generated ${LIB}.swift pulls its low-level FFI types via
#   #if canImport(pgc_protocol_ffiFFI)
# SwiftPM does NOT auto-promote a static-library xcframework's bundled headers to
# an importable Clang module, so without a dedicated C target that import is
# skipped and every FFI type is "cannot find type ... in scope". This target owns
# the header + module.modulemap; the matching symbols link from the .a.
C_INC="$IOS_PKG/Sources/PgcProtocolFfiC/include"
mkdir -p "$C_INC"
cp "$OUT/swift/${LIB}FFI.h" "$C_INC/"
cat > "$C_INC/module.modulemap" <<MODMAP
// Codec iOS parity — Clang module exposing the UniFFI C ABI to Swift.
// Module name MUST be \`${LIB}FFI\` (the generated Swift gates its FFI import on it).
// Symbols link from PgcProtocolFfiFFI.xcframework; this only publishes declarations.
module ${LIB}FFI {
    header "${LIB}FFI.h"
    export *
}
MODMAP
if [[ ! -f "$IOS_PKG/Sources/PgcProtocolFfiC/shim.c" ]]; then
  cat > "$IOS_PKG/Sources/PgcProtocolFfiC/shim.c" <<'SHIM'
// Intentionally empty — the real symbols link from PgcProtocolFfiFFI.xcframework.
// This target exists only to publish the pgc_protocol_ffiFFI Clang module.
SHIM
fi

# Build the xcframework from the device + simulator static libs. The headers dir
# passed to -create-xcframework must contain ONLY the C header: bundling the
# .modulemap too would declare the module a SECOND time and collide with the C
# target above (duplicate-module redefinition).
HDR="$(mktemp -d)"
cp "$OUT/swift/${LIB}FFI.h" "$HDR/"
rm -rf "$IOS_PKG/Frameworks/PgcProtocolFfiFFI.xcframework"
xcodebuild -create-xcframework \
  -library "$FFI_DIR/target/aarch64-apple-ios/release/lib${LIB}.a" -headers "$HDR" \
  -library "$FFI_DIR/target/aarch64-apple-ios-sim/release/lib${LIB}.a" -headers "$HDR" \
  -output "$IOS_PKG/Frameworks/PgcProtocolFfiFFI.xcframework" 2>&1 | tail -2 || \
  echo "WARN: xcframework build skipped/failed — ensure full Xcode is active." >&2
rm -rf "$HDR"

echo "== package for the Android instrumented test (clients/android/codec-parity) =="
AND_MOD="clients/android/codec-parity"
mkdir -p "$AND_MOD/src/main/jniLibs/arm64-v8a" "$AND_MOD/src/main/kotlin/uniffi/${LIB}"
cp "$FFI_DIR/target/aarch64-linux-android/release/lib${LIB}.so" \
   "$AND_MOD/src/main/jniLibs/arm64-v8a/"
cp "$OUT/kotlin/uniffi/${LIB}/${LIB}.kt" \
   "$AND_MOD/src/main/kotlin/uniffi/${LIB}/"

echo "OK — libs + bindings built and packaged into the codec test targets. Run:"
echo "  platform/mobile/run_codec_parity.sh                      # host-provable (Swift run + Kotlin compile)"
echo "  xcodebuild test -scheme CodecParityTests ...             # iOS Simulator (clients/ios/CodecParityTests)"
echo "  ./gradlew :codec-parity:connectedAndroidTest             # Android emulator (clients/android)"
