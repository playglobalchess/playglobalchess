#!/usr/bin/env bash
# §8.8 mobile parity — the HOST-PROVABLE half (ENGINE-CORE §8.8, ADR-9). Run
# from the repo root AFTER platform/mobile/build_mobile.sh (or it builds the
# host dylib + bindings itself). Proves:
#   - Swift consumer RUNS against the engine and matches the host oracle;
#   - Kotlin binding COMPILES (kotlinc + JNA) — the type-check half.
# The Kotlin RUN on an emulator/device is the one step this script cannot do
# (the aarch64 .so will not load on a host JVM) — see README.md.
set -euo pipefail

FFI="engine/ffi"
BIND="$FFI/target/bindings"

echo "== build host dylib + regenerate bindings =="
cargo build --manifest-path "$FFI/Cargo.toml" >/dev/null
( cd "$FFI" && cargo run --bin uniffi-bindgen -- generate \
    --library target/debug/libpgc_engine_ffi.dylib --language swift --out-dir target/bindings/swift >/dev/null
  cargo run --bin uniffi-bindgen -- generate \
    --library target/debug/libpgc_engine_ffi.dylib --language kotlin --out-dir target/bindings/kotlin >/dev/null )

echo "== Swift parity (host run) =="
SW="$BIND/swift"
cp platform/mobile/SwiftParityTest.swift "$SW/main.swift"
swiftc -O -I "$SW" -L "$FFI/target/debug" -lpgc_engine_ffi \
  -import-objc-header "$SW/pgc_engine_ffiFFI.h" \
  "$SW/pgc_engine_ffi.swift" "$SW/main.swift" -o "$FFI/target/swift_parity"
rm -f "$SW/main.swift"
DYLD_LIBRARY_PATH="$FFI/target/debug" "$FFI/target/swift_parity"

echo "== Kotlin binding type-check (host) =="
JNA="$(find "$HOME/.ivy2" "$HOME/.gradle" -name 'jna-*.jar' 2>/dev/null | sort | tail -1 || true)"
if [[ -z "$JNA" ]]; then
  echo "WARN: no JNA jar found — skipping Kotlin type-check. Install JNA or run via Gradle." >&2
else
  kotlinc -classpath "$JNA" \
    "$BIND/kotlin/uniffi/pgc_engine_ffi/pgc_engine_ffi.kt" \
    -d "$FFI/target/kotlin_binding.jar" >/dev/null
  echo "OK  Kotlin binding compiles"
fi

echo
echo "Host-provable parity: PASS."
echo "DEVICE-GATED (not run here): the Kotlin parity RUN on an Android"
echo "emulator/device — see platform/mobile/README.md."
