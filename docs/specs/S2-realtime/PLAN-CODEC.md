# Wire Codec — Implementation Plan (ADR-14) — S2 Real-time

| Field | Value |
|---|---|
| Doc ID | PLAN-S2-CODEC |
| Owner | S2 Real-time |
| Status | RATIFIED |
| Version | 1.1 |
| Last updated | 2026-10-13 (v1.1 amendment: Go consumption via a sibling `codec-cgo` crate, not the core — §4.5, OPEN-C4; corrects the v1.0 "pure crate emits the staticlib" wording. v1.0 2026-10-12: OPEN-C1=wasm, OPEN-C2=postcard, Go consumption=cgo/staticlib DECIDED.) |
| Implements | ADR-14 (DECIDED); SPEC-S2-PROTOCOL §3.1 + §4A (RATIFIED); NFR-5, NFR-10, NFR-1 |
| Depends on | SPEC-S2-PROTOCOL (frozen byte layout §4A); the existing `engine/ffi` UniFFI pattern (pgc-engine-ffi) |

> This is an **implementation plan**, not a spec and not code. It sequences the build of the ADR-14 wire codec so a reviewer can approve the approach (and the one open decision in §6) before any Rust/TS is written. Nothing here is implemented yet.

## 1. Goal

Build the **single wire + event codec** mandated by ADR-14: one encoder/decoder for the SPEC-S2-PROTOCOL §4A byte layout, living in the shared Rust core, consumed byte-identically by the Go server and all three clients (web/iOS/Android). "One codec, many consumers" — a client or service hand-rolling bytes is a bug (NFR-5 spirit, mirroring "one rules engine").

Done = the §8 golden-vector corpus (SPEC-S2-PROTOCOL §8) passes byte-for-byte on Rust + web + iOS + Android. That corpus passing is a G-D input.

## 2. Where it lives — proposed crate layout

Mirror the engine's proven discipline: a **pure, dependency-light codec crate**, wrapped for FFI exactly like `pgc-engine` → `pgc-engine-ffi`.

```
engine/
  (pgc-engine)              pure chess rules — UNCHANGED, stays codec-free
  ffi/ (pgc-engine-ffi)     existing UniFFI wrapper — UNCHANGED
protocol/
  codec/  (pgc-protocol)    NEW — the pure codec crate: §4A types + encode/decode
  codec-ffi/ (pgc-protocol-ffi)  NEW — UniFFI wrapper (Swift/Kotlin), mirrors engine/ffi
  codec-wasm/ (pgc-protocol-wasm) NEW — wasm-bindgen wrapper (web, OPEN-C1), via wasm-pack
  codec-cgo/ (pgc-protocol-cgo)  NEW — C-ABI (staticlib + cbindgen header) for the Go server (§4.5, cgo)
  vectors/                  NEW — the frozen golden-vector corpus (shared by all consumers)
```

**Rationale for a new `pgc-protocol` crate (not folding into `pgc-engine`):** the engine crate is deliberately dependency-free and "adds no chess logic / nothing but chess" (its Cargo.toml). The codec is not chess logic; putting it in `pgc-engine` would pull a serialization dependency into the rules core and blur the single-responsibility line G-A was built on. A sibling crate keeps the engine pristine and matches the repo's existing `protocol/` folder (currently a placeholder). **Path DECIDED: `protocol/codec/` (OPEN-C3, owner 2026-10-12).**

**One binding pattern for every consumer (v1.1).** The pure `pgc-protocol` crate stays `#![forbid(unsafe_code)]`, dependency-light, and a plain `lib` — it owns the §4A types + encode/decode and nothing ABI-specific. **Every foreign-ABI consumer is a thin sibling wrapper** that depends on it and adds only its own `crate-type` + binding machinery: `codec-ffi` (UniFFI, mobile), `codec-wasm` (wasm, web), and `codec-cgo` (C-ABI, the Go server). This mirrors `pgc-engine → pgc-engine-ffi` and keeps the single unavoidable `unsafe` seam (the `extern "C"` C-ABI) **quarantined in `codec-cgo`**, so the core's forbid-unsafe guarantee — a property G-D's single-source-of-truth leans on — is never relaxed, and the `staticlib`/`cdylib` build cost never lands on the consumers that don't need it. (v1.0 §4.5 wrongly said the Go path links the *core* crate's staticlib "the crate already emits it"; the core emits no staticlib and no C-ABI, and adding them there would break forbid-unsafe. v1.1 corrects this to the sibling pattern — the architecturally consistent reading of this very section.)

## 3. Codec choice (ADR-14 sub-choice) — DECIDED: `postcard` + golden-vector freeze guard

ADR-14 DECIDED "one schema-defined binary codec in the shared Rust core" and left the exact library as a sub-choice. **DECIDED: `postcard`** (serde-based, `no_std`-capable, compact varint framing).
- Smallest frames of the serde options (varint ints) → best for the §IX.1 move hot path.
- Pure Rust, no external IDL/codegen toolchain (unlike Protobuf/FlatBuffers) → clients get it through the existing UniFFI path (mobile) and `wasm-pack` (web), no second build system.
- `#![forbid(unsafe_code)]` compatible, like the engine crates.

**The §4A byte tables are a LOGICAL contract, not a hand-fixed hex layout.** They pin field *type, order, and max* — the *physical* bytes are whatever the codec emits (`postcard` encodes a `u32` as 1–5 varint bytes, not a fixed 4). This is the ADR-14 reading: the codec owns framing. Fixed byte offsets would only buy something for a non-owner parsing the wire with no shared library (hardware, a third party, zero-copy mmap) — we have none; every consumer links the one codec.

**Sharp edge made explicit:** `postcard` identifies fields **by position, not by tag** — so **field ORDER is the contract**, and an accidental reorder/insert silently corrupts the wire with no error. The guard against this is the **§4.2 golden-vector corpus**: committed `(value, bytes)` pairs that a CI test replays, so any field reorder/addition fails loudly as a changed-bytes test rather than shipping a silent wire break. The vectors *are* the freeze enforcement.

**Fallback (documented, not chosen):** if a non-FFI consumer ever needs tag-based, reorder-safe fields, switch to Protobuf/FlatBuffers. That is a codec-crate-internal change behind the same `encode`/`decode` API — **not** a protocol change. Re-validate the §4A 4 KB max-frame cap against whatever codec is in force (varint output differs from fixed-width).

## 4. Build sequence (each step = one reviewable PR, spec-first already satisfied)

**4.1 `pgc-protocol` core crate — types + codec.**
- Define the §4A message enums (`ClientMsg`, `ServerMsg`), the event enum (`GameEvent` + envelope), and the value structs (ids as newtypes, `TimeControlId(String)` per Q-A4), with serde derives.
- `encode(msg) -> Vec<u8>` / `decode(&[u8]) -> Result<msg, CodecError>` over the §4A frame envelope (`ver | type | payload`), dispatching on the frozen type codes (§4A.2).
- No panics on decode (every malformed input → `CodecError`, mirroring the engine FFI's no-panic §5 rule — a panic across FFI is a mobile hard-crash).
- Tests: round-trip (`decode(encode(x)) == x`) for every message + event; `proptest` property tests (same tool the engine uses); negative-decode (truncated, unknown type, unsupported ver, wrong-direction) → defined error.

**4.2 Golden-vector corpus (`protocol/vectors/`).**
- Generate `(message, bytes)` and `(event, bytes)` pairs from 4.1 for every type, including edge values (max-length FEN in STATE, 5-char promotion UCI, N-seat-ready result_vector).
- Commit as the frozen reference. These are the cross-language oracle (SPEC-S2-PROTOCOL §8).
- A Rust test asserts the live codec reproduces every vector byte-for-byte (catches an accidental wire change as a failing test = the freeze guard).

**4.3 `pgc-protocol-ffi` — Swift + Kotlin bindings.**
- Mirror `engine/ffi`: UniFFI 0.32, `crate-type = ["cdylib","staticlib","lib"]`, a `uniffi-bindgen` bin, `#![forbid(unsafe_code)]`, delegate-only (no logic beyond calling `pgc-protocol`).
- Expose `encode`/`decode` for the message + event types to Swift/Kotlin.
- Parity test (mirrors `mobile_parity_oracle.rs`): decode every golden vector on iOS + Android, assert the decoded struct re-encodes to the identical bytes. This is the NFR-10 proof on mobile.

**4.4 Web/TypeScript binding — `wasm-pack` (OPEN-C1 = (a) DECIDED).** Compile `pgc-protocol` to WebAssembly via `wasm-pack`; the web client imports the generated JS/wasm and calls the *same* `encode`/`decode` the server and mobile run. A thin typed TS wrapper exposes the message/event types to the web app. Its parity test runs the §4.2 golden vectors in the web test runner and asserts byte-identical decode→re-encode.

**4.5 Go server consumption — DECIDED: cgo over a sibling `codec-cgo` static library, in-process (v1.1).** The Go game-server (X-2, ADR-12) links the **`pgc-protocol-cgo`** sibling crate's **`staticlib`** — a thin, delegate-only C-ABI wrapper over `pgc-protocol` (the `codec/` pure crate, mirroring how `codec-ffi` and `codec-wasm` wrap it) — and calls `encode`/`decode` as C-ABI functions **in-process** — no network hop, no second serialization boundary. `codec-cgo` emits `crate-type = ["staticlib", "rlib"]`, a `#[no_mangle] extern "C"` surface (the one place `unsafe` lives — carries safety comments), and a `cbindgen`-generated C header; the pure core stays `#![forbid(unsafe_code)]` (see §2 "one binding pattern"). Rationale: the codec is on the hottest path (every move = decode-intent → validate → encode-event, at IX.2's 50k moves/sec within the §IX.1 50 ms p50 / 300 ms ceiling); a **sidecar would add a network hop + a failure mode to the one operation that must be tiny and reliable** — backwards. cgo's per-call overhead (~tens of ns) is negligible against a move's budget; the Go↔Rust toolchain coupling was already accepted when the engine became Rust-consumed-by-Go (ADR-12).

**ADR-12 "FFI/gRPC boundary or sidecar" read by path:** the codec + engine (hot, per-move, latency-critical) → **in-process FFI (cgo/staticlib)**; a service that scales independently with its own lifecycle (e.g. a heavy analysis/NNUE worker) → gRPC/sidecar. Do not sidecar the move path. **This plan owns the existence + shape of the `codec-cgo` sibling crate and its C-ABI surface** (§2, §4.5); the `services/game-server` spec (OPEN-C4) owns only the *consumption* detail on the Go side — link flags, cross-compile, buffer-ownership rules, and the Go contract test. Its contract test decodes the golden vectors too.

> **Amendment (2026-10-14, GS-4a).** The initial `codec-cgo` surface was
> `reencode`-shaped only (`pgc_reencode_{client,server,event}`: frame→frame),
> which serves the hot-path relay but cannot serialize a domain `game.Event` for
> the durable store. SPEC-S2-GAME-SERVER-GS4A **additively** extends the C-ABI
> with `pgc_encode_event` (postcard-of-`EventEnvelope` intermediate → canonical
> §4A frame) and `pgc_decode_event` (the inverse), keeping §4A encoding entirely
> in Rust (NFR-5). No existing function, byte layout, or golden vector changes;
> the header stays hand-written (not cbindgen) as it already is. This is the
> additive seam extension this section anticipates for new store needs, not a
> change to any §4.5 decision.

**4.6 Wire the CI job.** Add a path-filtered `protocol` job to `.github/workflows/ci.yml` (mirrors the engine job): `cargo test` on `pgc-protocol` + `pgc-protocol-ffi`, and the golden-vector cross-check. Add the web + mobile vector checks as those toolchains are wired.

## 5. Test strategy (ties to SPEC-S2-PROTOCOL §8)

| Layer | What | Tool |
|---|---|---|
| Unit/property | round-trip, every type; random values | Rust + `proptest` |
| Negative | malformed/truncated/unknown-type/bad-ver → defined `CodecError`, never panic | Rust |
| Golden freeze | live codec reproduces every committed vector byte-for-byte | Rust |
| Cross-language parity | iOS/Android/web decode+re-encode the SAME vectors → identical bytes | UniFFI (Swift/Kotlin) + web runner |
| Ordering/immutability | event `seq` strictly increasing, no-gap; append-only | Rust |

The golden-vector corpus is the single source of truth across languages — the same discipline that proved the engine byte-identical on real devices.

## 6. Decisions

**OPEN-C1 — Web/TypeScript codec path — DECIDED (a) wasm, owner, 2026-10-12.** The web client runs the **same compiled `pgc-protocol` Rust codec via `wasm-pack`/WebAssembly** — one implementation everywhere (server, iOS, Android, web), parity by construction rather than by test. Accepted cost: a wasm build step in the web pipeline and a ~tens-of-KB wasm payload in the bundle. Step 4.4 builds this; the hand-written-TS alternative is rejected (it reintroduces the NFR-10 drift risk ADR-14 exists to kill).

Secondary: **OPEN-C2 — codec library — DECIDED `postcard`** (§3), with the golden-vector corpus as the field-order freeze guard; Protobuf/FlatBuffers kept as a documented API-compatible fallback. **OPEN-C3 — `pgc-protocol` crate placement — DECIDED `protocol/codec/` (owner, 2026-10-12).** **OPEN-C4 — Go-server cgo consumption — DECIDED (v1.1): a sibling `protocol/codec-cgo` crate (staticlib + cbindgen C-ABI) wrapping the pure core, consumed in-process via cgo.** This plan owns the crate + C-ABI shape (§4.5); the Go-side build wiring (link flags, cross-compile, buffer ownership, Go contract test) is owned by the `services/game-server` spec. (v1.0 scoped OPEN-C4 to "wiring detail, not a new crate" over the core's own staticlib — corrected here because the core emits none and a sibling preserves forbid-unsafe; see §2.)

## 7. What this plan does NOT do
- No chess logic (engine owns it); no server behavior (game-server owns clocks/matchmaking/reconnection); no REST (that's JSON, SPEC-PE-API-CONTRACT); no persistence engine. The codec serializes the frozen §4A shapes and nothing more.

## 8. Sequence summary
4.1 core crate + codec → 4.2 golden vectors → 4.3 Swift/Kotlin FFI + mobile parity → 4.4 web (wasm-pack, OPEN-C1 DECIDED) → 4.5 Go consumption → 4.6 CI. Steps 4.1–4.4 are unblocked now; 4.5 pairs with the `services/game-server` spec.
