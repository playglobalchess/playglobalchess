# protocol

Shared API/WS + event-store schema (ADR-10). One source of truth; cited by every
client/service, never copied.

The contract itself is frozen in the specs (`docs/specs/S2-realtime/PROTOCOL.md`,
SPEC-S2-PROTOCOL §4A); this folder holds the **one implementation** of it that
every consumer links (ADR-14 / PLAN-S2-CODEC — "one codec, many consumers").

## Layout

| Path | What |
|------|------|
| `codec/` | `pgc-protocol` — the pure Rust wire + event codec: the §4A message/event types and `encode`/`decode` over the `ver \| type \| payload` frame (§4A.1) and the event-store envelope (§4A.8). Dependency-light (serde + postcard + thiserror), `#![forbid(unsafe_code)]`, no chess logic / no I/O / no networking. |
| `vectors/` | The frozen golden-vector corpus (`vectors.json`) — committed `(value, bytes)` pairs that are the cross-language oracle and the field-order freeze guard (SPEC-S2-PROTOCOL §8, PLAN-CODEC §3). See `vectors/README.md`. |

## Status

Implements PLAN-S2-CODEC steps **4.1 (codec core) + 4.2 (golden vectors)** — the
Rust codec and its corpus. Deferred to later chunks: `codec-ffi/` (Swift/Kotlin
UniFFI, 4.3), the web wasm binding (4.4), Go server cgo consumption (4.5), and
the `protocol` CI job (4.6). **G-D is not yet green** — it needs the codec bound
into all three clients and the corpus passing on each (SPEC-S2-PROTOCOL §9).

## Building the codec

```sh
cd protocol/codec
cargo test            # round-trip + negative + golden-freeze + property + ordering
cargo fmt --all -- --check
cargo clippy --all-targets -- -D warnings
cargo run --example gen_vectors   # regenerate vectors/ (deliberate changes only)
```
