#!/usr/bin/env bash
# Web codec parity (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.4, OPEN-C1=wasm) — build
# the codec wasm package and prove parity on the web runtime. Run from the repo
# root. Mirrors the mobile build_codec_mobile.sh in intent.
#
# Prereqs: rustup target wasm32-unknown-unknown; wasm-pack; node. The pinned
# toolchain versions are in protocol/codec-wasm/Cargo.toml.
set -euo pipefail

WASM_DIR="protocol/codec-wasm"

echo "== wasm-pack build (web target — what clients/web imports) =="
( cd "$WASM_DIR" && wasm-pack build --target web --out-dir pkg )

echo "== wasm-pack build (nodejs target — for the host-provable parity check) =="
( cd "$WASM_DIR" && wasm-pack build --target nodejs --out-dir pkg-node )

echo "== host-provable web parity: run the committed golden frames through the"
echo "   SHIPPED wasm artifact in node, assert decode→re-encode byte-identity =="
# The canonical CI test is protocol/codec-wasm/tests/web_parity.rs via
# `wasm-pack test --node` (runs on a CI runner). This node check proves the same
# thing against the built artifact and runs anywhere node + the pkg exist — it is
# the host-provable half (the wasm-bindgen test runner's realpath walk is blocked
# inside some sandboxes; this is not).
node platform/web/parity_node_check.cjs

echo
echo "Host-provable web parity: PASS (wasm decode→re-encode byte-identical to the"
echo "committed golden frames; malformed input throws, no wasm trap)."
