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
mkdir -p "$OUT/swift" "$OUT/kotlin"
# Path relative to $FFI_DIR (the subshells cd there). Single source of truth.
DYLIB_REL="target/debug/libpgc_engine_ffi.dylib"
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library "$DYLIB_REL" --language swift --out-dir target/bindings/swift )
( cd "$FFI_DIR" && cargo run --bin uniffi-bindgen -- generate \
    --library "$DYLIB_REL" --language kotlin --out-dir target/bindings/kotlin )

echo "OK — libs + bindings built. Run the parity tests:"
echo "  platform/mobile/run_parity.sh"
