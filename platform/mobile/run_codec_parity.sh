#!/usr/bin/env bash
# Codec mobile parity — the HOST-PROVABLE half (SPEC-S2-PROTOCOL §8, PLAN-CODEC
# §4.3). Run from the repo root. Proves:
#   - Swift consumer RUNS against the codec and reproduces the frozen bytes;
#   - Kotlin binding COMPILES (kotlinc + JNA) — the type-check half.
# The Kotlin RUN on an emulator/device is the one step this script cannot do
# (the aarch64 .so will not load on a host JVM) — see README-codec.md.
set -euo pipefail

FFI="protocol/codec-ffi"
BIND="$FFI/target/bindings"
LIB="pgc_protocol_ffi"

echo "== build host dylib + regenerate bindings =="
cargo build --manifest-path "$FFI/Cargo.toml" >/dev/null
( cd "$FFI" && cargo run --bin uniffi-bindgen -- generate \
    --library "target/debug/lib${LIB}.dylib" --language swift --out-dir target/bindings/swift >/dev/null
  cargo run --bin uniffi-bindgen -- generate \
    --library "target/debug/lib${LIB}.dylib" --language kotlin --out-dir target/bindings/kotlin >/dev/null )

echo "== Swift codec parity (host run) =="
SW="$BIND/swift"
cp platform/mobile/SwiftCodecParityTest.swift "$SW/main.swift"
swiftc -O -I "$SW" -L "$FFI/target/debug" -l"$LIB" \
  -import-objc-header "$SW/${LIB}FFI.h" \
  "$SW/${LIB}.swift" "$SW/main.swift" -o "$FFI/target/swift_codec_parity"
rm -f "$SW/main.swift"
DYLD_LIBRARY_PATH="$FFI/target/debug" "$FFI/target/swift_codec_parity"

echo "== Kotlin binding type-check (host) =="
JNA="$(find "$HOME/.ivy2" "$HOME/.gradle" -name 'jna-*.jar' 2>/dev/null | sort | tail -1 || true)"
if [[ -z "$JNA" ]]; then
  echo "ERROR: no JNA jar found — the Kotlin binding type-check is a REQUIRED" >&2
  echo "host-parity step (README lists JNA as a prerequisite), not optional." >&2
  echo "Install JNA (via Gradle/Maven, or run the Android target via Gradle" >&2
  echo "which supplies it) and re-run. Refusing to report PASS with a step skipped." >&2
  exit 1
fi
kotlinc -classpath "$JNA" \
  "$BIND/kotlin/uniffi/${LIB}/${LIB}.kt" \
  -d "$FFI/target/kotlin_codec_binding.jar" >/dev/null
echo "OK  Kotlin codec binding compiles"

echo
echo "Host-provable codec parity: PASS (Swift run byte-identical; Kotlin binding compiles)."
echo "DEVICE-GATED (not run here): the Kotlin parity RUN on an Android"
echo "emulator/device, and the iOS XCTest run on a Simulator — see"
echo "platform/mobile/README-codec.md."
