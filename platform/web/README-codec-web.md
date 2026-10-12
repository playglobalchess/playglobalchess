# Web codec parity (§8 / PLAN-CODEC §4.4) — WebAssembly

SPEC-S2-PROTOCOL §8 / NFR-10 / ADR-9 / ADR-14, OPEN-C1 = **wasm** DECIDED: the
web client runs the **same compiled `pgc-protocol` Rust codec** as the server
(cgo) and mobile (UniFFI), via `wasm-pack`/WebAssembly — parity by construction,
not a hand-written TS re-implementation (which would reintroduce the NFR-10
drift ADR-14 exists to kill).

## Architecture

```
protocol/codec/       pure pgc-protocol codec (no wasm deps, #![forbid(unsafe_code)])
protocol/codec-wasm/  pgc-protocol-wasm — wasm-bindgen binding (depends on pgc-protocol)
                      exposes reencode_client/server/event + protocol_version
platform/web/         build + host-parity scripts, this doc
```

The binding is **delegate-only**: it compiles the pure codec to wasm and calls
its `encode`/`decode`. It exposes a byte-oriented `reencode_*(bytes) -> bytes`
surface (`encode(decode(bytes))`), which is exactly what the parity test needs —
nothing crosses the JS boundary but bytes, so parity can't be weakened by a
JS-side type-mapping bug.

> The **typed TS wrapper** that exposes the §4A message/event types as JS objects
> is intentionally a later, web-app (S4) concern: it would require adding serde
> derives to the frozen `pgc-protocol` top-level enums (postcard-encoded by
> manual dispatch today). It layers cleanly on top of these byte functions.

## What is PROVEN on this host

- `pgc-protocol-wasm` **compiles to wasm** via `wasm-pack` (web + nodejs targets).
- The **shipped wasm artifact** decodes→re-encodes the committed golden frames
  (a subset of `protocol/vectors/vectors.json`, mirroring the mobile oracle)
  **byte-identically** in node, and a malformed frame **throws** (no wasm trap).

Run it:
```sh
platform/web/build_codec_web.sh   # wasm-pack build (web + nodejs) + node parity check
```

## CI form vs host-provable form

- **Canonical CI test:** `protocol/codec-wasm/tests/web_parity.rs` via
  `wasm-pack test --node` — runs the parity assertions in a real wasm VM on a CI
  runner (the `protocol` job can gate it once wired).
- **Host-provable companion:** `platform/web/parity_node_check.cjs` loads the
  built `pkg-node/` and asserts the same thing. This exists because the
  `wasm-bindgen-test` runner does a filesystem realpath walk that some sandboxes
  (incl. the KiroCrew dev sandbox) block — the node check does not, so parity is
  provable here. Both assert the SAME frames; change them in lockstep.

## Prerequisites (host)

- `rustup target add wasm32-unknown-unknown`
- `wasm-pack` (pinned bring-up version 0.13.1) on PATH
- `node` (18+; tested on 20.x)

## Toolchain versions pinned at bring-up (2026-10-13)

wasm-bindgen 0.2.95 (crate), wasm-pack 0.13.1, node 20.19. A wasm-bindgen major
bump can change the generated JS/TS API — update deliberately.
