# Game-server GS-4a — codec event-encode/decode seam (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS4A (prerequisite slice for GS-4; extends PLAN-S2-CODEC §4.5 cgo seam) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-14 |
| Implements (master IDs) | X-2, NFR-5 (one codec — Go never hand-rolls §4A bytes), NFR-10 (byte-identical across consumers), ADR-14, ADR-15 (in-process C-ABI), ADR-16 (unblocks the DynamoDB store) |
| Depends on | PLAN-S2-CODEC (RATIFIED — the `codec-cgo` seam + `pgc-protocol` codec); SPEC-S2-PROTOCOL §4A (frozen byte layout); the existing `pgc_reencode_*` seam (PR #28) |
| Gates it feeds | Unblocks GS-4 (the durable event store needs a `game.Event → §4A bytes` path that the current seam does not expose). Makes no lettered gate green by itself. |

> **Why this slice exists — a gap GS-4 surfaced.** GS-4's Q-GS4B says the store
> serializes each event body "by reusing the §4A wire codec via the Go cgo
> binding." But the `codec-cgo` seam today exposes only
> `pgc_reencode_{client,server,event}(frame) → frame` — a **frame→frame**
> reencode that takes bytes already in §4A form. There is **no**
> `game.Event → §4A bytes` path reachable from Go, and nothing in the
> game-server serializes a domain `game.Event` at all (the GS-1 `MemStore` holds
> the Go struct in memory). The pure `pgc-protocol` crate already HAS
> `encode_event`/`decode_event`; only the cgo seam never exported an
> Event-level entry point. GS-4a adds exactly that — keeping §4A encoding 100%
> in Rust (NFR-5) — so GS-4 can store canonical wire bytes. No wire bytes
> change; this adds a seam, it does not alter the frozen §4A layout.

## 1. Goal

Add a `game.Event ⇄ §4A event-frame bytes` path to the `codec-cgo` seam and its
Go binding, so the GS-4 durable store can persist each event as its canonical
§4A frame (Q-GS4B) without the Go tier ever hand-encoding a §4A byte (NFR-5).
Done = the new seam round-trips every event variant byte-identically to the
existing golden vectors, survives an ABI fuzz sweep, and the Go binding
round-trips every `game.Event`.

## 2. The boundary representation (the one design decision)

The Rust `pgc_protocol::encode_event` takes an **`EventEnvelope`**
(`stream_id, seq, schema_ver, ts, event: GameEvent`) and emits the full §4A.8
frame (`stream_id, seq, type, schema_ver, ts, body`). To call it from Go, Go must
hand the envelope's *fields* across the C boundary. Two shapes were considered:

- **A per-event C struct graph** (one C struct per variant). **Rejected** — it is
  the wide, fragile `unsafe` surface the existing seam explicitly avoided
  ("No per-message C struct graph crosses the boundary — smallest `unsafe`
  surface", ADR-15 rationale). It would also duplicate the 12-variant shape in C.

- **A byte-oriented boundary (CHOSEN), reusing the project's own serializer.**
  `EventEnvelope`/`GameEvent` gain `#[derive(Serialize, Deserialize)]` (every
  leaf type — the id newtypes, `Termination`, `SeatColor`, `SeatRating`,
  `SeatResult`/`Color` — already derives them). The Go↔Rust intermediate is then
  simply the **postcard bytes of the `EventEnvelope`**. Go builds a mirror struct
  and postcard-encodes it; Rust postcard-decodes it to a real `EventEnvelope`,
  then calls `encode_event` to produce the canonical §4A frame. The reverse
  decodes a §4A frame to `EventEnvelope` and re-emits the postcard intermediate.

This keeps the §4A encoding entirely in Rust (NFR-5), adds no new hand-maintained
format (postcard is already the §4A serializer, PLAN-CODEC OPEN-C2 DECIDED), and
keeps the C surface byte-oriented and identical in shape to the existing
`pgc_reencode_*` seam. **The intermediate is NOT a second wire format** — it never
leaves the process boundary, carries no compatibility guarantee of its own, and
exists only to move an envelope's fields from Go to Rust and back. The §4A frame
Rust emits is the only durable artifact.

## 3. Design

### 3.1 Rust seam — `protocol/codec-cgo/src/lib.rs`

Two new `#[no_mangle] extern "C"` functions, following the existing
`reencode_impl` pattern exactly (same `(buf, len, out, out_len) → i32` ABI, same
`catch_unwind` panic guard, same `hand_out`/`pgc_free` ownership, same `code_of`
error taxonomy):

```
/* Encode: postcard(EventEnvelope) bytes IN  → canonical §4A event-frame bytes OUT. */
int32_t pgc_encode_event(const uint8_t *buf, size_t len,
                         uint8_t **out, size_t *out_len);

/* Decode: §4A event-frame bytes IN → postcard(EventEnvelope) bytes OUT. */
int32_t pgc_decode_event(const uint8_t *buf, size_t len,
                         uint8_t **out, size_t *out_len);
```

- `pgc_encode_event`: `postcard::from_bytes::<EventEnvelope>(buf)` → on success
  `pgc_protocol::encode_event(&env)` → hand out the §4A frame. A malformed
  intermediate (postcard error) returns a new typed code `PGC_ERR_BAD_INTERMEDIATE`
  (distinct from the §4A `PGC_ERR_MALFORMED`, so the Go side can tell "I built a
  bad envelope" from "the §4A bytes were bad").
- `pgc_decode_event`: `pgc_protocol::decode_event(buf)` → on success
  `postcard::to_allocvec(&env)` → hand out the intermediate. §4A decode failures
  return the existing `code_of` discriminants unchanged.
- Both reuse `hand_out`/`pgc_free` verbatim — Rust allocates, Go copies then
  frees; no new ownership rule.

`EventEnvelope` and `GameEvent` get `#[derive(Serialize, Deserialize)]` added in
`protocol/codec/src/event.rs`. This is additive and does **not** change any
existing `encode_event`/`decode_event` output (those already drive the frozen
§4A bytes; the derive only enables the *intermediate*, which is a separate code
path). The golden-vector freeze test (§5) proves the §4A frames are byte-for-byte
unchanged.

### 3.2 C header — `protocol/codec-cgo/include/pgc_protocol_cgo.h`

Two prototypes mirroring the existing `pgc_reencode_*` block, with doc comments
describing the IN/OUT byte shapes and the shared `pgc_free` ownership. Avoid the
`-Wcomment` nested-comment trap (no literal `/*` inside a block comment).

### 3.3 Go binding — `services/game-server/internal/codec`

- A pure-Go `EventEnvelope` mirror struct + `game.Event ⇄ envelope` mapping
  (no §4A knowledge — just field copies between the two Go representations), and
  a postcard marshal/unmarshal for it (the Go postcard shape is positional and
  must match the Rust struct field order — asserted by the parity test, not by
  trust).
- Two thin cgo wrappers, `EncodeEvent(game.Event, hdr) ([]byte, error)` and
  `DecodeEvent([]byte) (game.Event, hdr, error)`, reusing the existing
  `reencode` helper body (copy-out + `pgc_free`), returning the typed error for
  `PGC_ERR_BAD_INTERMEDIATE`.
- The seq/stream_id/ts/schema_ver header fields travel on the envelope; GS-4
  supplies them (it owns the stream id + seq) — this slice just carries them
  across faithfully.

## 4. Interfaces & contracts

- No change to the existing `pgc_reencode_*` functions, `pgc_free`, or
  `pgc_protocol_version` — purely additive C-ABI surface.
- No change to the §4A byte layout or any golden vector (§5 proves it).
- The Go `codec` package gains two exported functions; nothing else in the
  game-server changes in this slice (GS-4 consumes them next).
- The postcard-intermediate shape is an **internal cross-boundary convention**,
  frozen only insofar as the Rust struct and the Go mirror must agree — enforced
  by the parity test, versioned implicitly with the binary (both sides rebuild
  together; it never persists or crosses a network).

## 5. Test plan

- **§4A frame unchanged (freeze):** the existing `codec-cgo` golden-vector test
  still passes byte-for-byte after the `Serialize/Deserialize` derive is added —
  proves the derive did not perturb the frozen encoding.
- **Round-trip through the new seam (Rust):** for every committed golden event
  frame, `pgc_encode_event(intermediate_of(decode_event(golden))) == golden`
  and `pgc_decode_event(golden)` yields an intermediate that re-encodes to
  `golden`. Byte-identical, every one of the 12 variants.
- **Go round-trip parity (the real proof):** for every `game.Event` variant, Go
  `EncodeEvent` → bytes → `DecodeEvent` == the original event; AND the bytes Go
  produces equal the committed golden §4A frame for that event (so the Go mapping
  + Rust seam reproduce canonical wire bytes, not just a self-consistent loop).
  This is the test that would have caught the postcard field-order / type-mapping
  mistakes (UniFFI `Vec<u8>`→`Data` class of bug) — it runs the real seam, not an
  assumption.
- **ABI fuzz sweep (ADR-15 convention):** ~100k malformed / random / oversize
  inputs into `pgc_encode_event` (bad postcard intermediates) and
  `pgc_decode_event` (bad §4A frames) — assert: no crash, every `PGC_OK` buffer
  freed exactly once (leak check), every failure a typed non-zero code,
  `PGC_ERR_BAD_INTERMEDIATE` distinct from `PGC_ERR_MALFORMED`.
- **Null-arg / panic-guard:** null out-pointer → `PGC_ERR_NULL_ARG`; the
  `catch_unwind` guard maps any panic to `PGC_ERR_PANIC` (mirrors the existing
  seam tests).
- `cargo test`/`fmt`/`clippy` clean on `codec` + `codec-cgo`; `make test` green
  in the game-server (the Go binding + its parity test).

## 6. Non-functional handling

- **NFR-5:** Go never encodes a §4A byte — it postcard-marshals a field-copy
  mirror and Rust does all §4A work. The seam is the only §4A-byte producer.
- **NFR-10:** the stored frame is byte-identical to what the wire path produces
  (same `encode_event`), so a stored event replays to the exact bytes a live
  consumer saw — proven against the shared golden corpus.
- **NFR-1 (move latency):** the encode is one postcard decode + one §4A encode in
  Rust, both O(body). Measured at G-E, not asserted here; the store append (GS-4)
  dominates the move-path cost, not this encode.
- **Security (§7-style):** the fuzz sweep covers malformed input at the boundary;
  Rust `catch_unwind` prevents a panic crossing `extern "C"` (UB); no new memory
  ownership rule (reuses `pgc_free`).

## 7. Acceptance criteria (Definition of Done)

- [ ] `pgc_encode_event` / `pgc_decode_event` added to `codec-cgo` with the
      shared ABI + ownership, and `PGC_ERR_BAD_INTERMEDIATE` in the taxonomy.
- [ ] C header updated with both prototypes (no `-Wcomment` trap).
- [ ] `EventEnvelope`/`GameEvent` derive `Serialize/Deserialize`; the §4A
      golden-vector freeze test proves the frozen encoding is byte-for-byte
      unchanged.
- [ ] Go `codec.EncodeEvent`/`DecodeEvent` + the `game.Event ⇄ envelope`
      mapping; Go round-trip parity test green against the golden corpus.
- [ ] ABI fuzz sweep (~100k) green: no crash, no leak, typed errors only.
- [ ] `cargo test`/`fmt`/`clippy` clean (`codec`, `codec-cgo`); `make test`
      green; CI `services` + `changes` green on the hosted runner.
- [ ] No change to the §4A byte layout or any existing seam function; stated in
      the PR.
- [ ] Makes no lettered gate green; it unblocks GS-4. Stated in the PR.

## 8. Open questions

- **Q-GS4a-1 — intermediate representation — PROPOSED (postcard of
  `EventEnvelope`).** Use the project's own serializer (postcard, OPEN-C2
  DECIDED) over an envelope mirror as the Go↔Rust boundary payload, rather than a
  per-variant C struct graph. Lowest `unsafe` surface, no new hand-maintained
  format, matches the existing byte-oriented seam. Rejected: C struct graph
  (wide unsafe surface, rejected by the seam's own ADR-15 rationale).
  **Proposed: postcard-of-envelope.**
- **Q-GS4a-2 — does the stored frame include `stream_id`/`seq`? — DEFERRED to
  GS-4.** `encode_event` writes `stream_id` + `seq` into the frame, but the
  DynamoDB item ALSO keys on them (ADR-16). So GS-4 must decide whether the
  stored `payload` is the full frame (redundant key fields, but literally
  "== wire bytes") or the frame minus the key fields (smaller, but not literally
  the wire frame). GS-4a produces whichever GS-4 asks for — the seam encodes a
  full envelope; GS-4's Q-GS4B settles what it hands in. Flagged here so GS-4
  resolves it explicitly rather than inheriting it by accident.
