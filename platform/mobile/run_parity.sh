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
# Resolve JNA DETERMINISTICALLY to the version the engine-parity Android module
# pins (net.java.dev.jna:jna:5.14.0). The classpath only needs the plain Java
# `jna.jar` (NOT the Android @aar) to TYPE-CHECK the generated binding on the
# host. Fetch that exact jar from Maven Central into a build-local path so this
# step works on a CLEAN clone / CI runner with no primed Gradle/Ivy cache —
# never by scanning the user's caches for whatever jna-*.jar happens to exist.
JNA_VERSION="5.14.0"
JNA_DIR="$FFI/target/jna"
JNA="$JNA_DIR/jna-${JNA_VERSION}.jar"
JNA_URL="https://repo1.maven.org/maven2/net/java/dev/jna/jna/${JNA_VERSION}/jna-${JNA_VERSION}.jar"
JNA_SHA256="34ed1e1f27fa896bca50dbc4e99cf3732967cec387a7a0d5e3486c09673fe8c6"

# Verify a file against the pinned checksum. Returns non-zero on mismatch.
jna_ok() { [[ -f "$1" ]] && [[ "$(shasum -a 256 "$1" | awk '{print $1}')" == "$JNA_SHA256" ]]; }

# Atomic, verified fetch: download to a temp file, checksum it BEFORE moving it
# into place, and clean the temp up on any failure. A partial/truncated download
# never becomes $JNA.
jna_fetch() {
  mkdir -p "$JNA_DIR"
  local tmp; tmp="$(mktemp "$JNA_DIR/jna.XXXXXX.tmp")"
  echo "   fetching JNA ${JNA_VERSION} from Maven Central (pinned, verified)…"
  if ! curl -fsSL "$JNA_URL" -o "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  if ! jna_ok "$tmp"; then
    echo "ERROR: JNA ${JNA_VERSION} checksum mismatch on download (got $(shasum -a 256 "$tmp" | awk '{print $1}'))." >&2
    rm -f "$tmp"
    return 1
  fi
  mv -f "$tmp" "$JNA"
}

# ALWAYS verify the jar we are about to use, no matter how it got here. A cache
# HIT is re-checked (a stale/partial/tampered build-local jar from a prior run is
# not trusted on its mere existence); only an unverified-or-absent jar triggers a
# single re-fetch. This is what makes "tamper-checked" true on every path.
if ! jna_ok "$JNA"; then
  rm -f "$JNA"   # discard a bad/partial cached jar before re-fetching
  if ! jna_fetch || ! jna_ok "$JNA"; then
    echo "ERROR: could not obtain a verified JNA ${JNA_VERSION} jar." >&2
    echo "The Kotlin binding type-check is a REQUIRED host-parity step; refusing" >&2
    echo "to report PASS with it skipped. Check network access to repo1.maven.org" >&2
    echo "or pre-place the verified jar at $JNA." >&2
    exit 1
  fi
fi
kotlinc -classpath "$JNA" \
  "$BIND/kotlin/uniffi/pgc_engine_ffi/pgc_engine_ffi.kt" \
  -d "$FFI/target/kotlin_binding.jar" >/dev/null
echo "OK  Kotlin binding compiles"

echo
echo "Host-provable parity: PASS (Swift run byte-identical to oracle; Kotlin binding compiles)."
echo "DEVICE-GATED (not run here): the Kotlin parity RUN on an Android"
echo "emulator/device — see platform/mobile/README.md."
