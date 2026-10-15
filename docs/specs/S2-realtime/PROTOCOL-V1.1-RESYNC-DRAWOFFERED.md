# Protocol §4A v1.1 — RESYNC + DRAW_OFFERED (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-PROTOCOL-V1.1 (amends SPEC-S2-PROTOCOL §4A; implements ADR-17) |
| Owner | S2 Real-time + Architecture |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-16 |
| Implements (master IDs) | X-2, X-13, ADR-14, ADR-17, NFR-5, NFR-10 |
| Depends on | ADR-17 (PROPOSED — this slice does not implement until ADR-17 is owner-approved); the merged codec + GS-6a/GS-6 seams + GS-6 transport |
| Gates it feeds | None directly. Closes the two §4A v1.0 gaps GS-6 documented (no client RESYNC, no server DRAW_OFFERED). Re-runs the G-D freeze corpus with the two added codes. |

> **Why this slice exists.** ADR-17 adds two additive §4A codes. This slice is the
> code: the Rust codec variants + encode/decode, the regenerated frozen
> golden-vector corpus, the Go seam extension (GS-6a-style), and the GS-6 transport
> wiring that uses them. Additive only — no existing code moves, so the v1.0
> golden vectors remain valid and are a REGRESSION check (they must still
> reproduce byte-for-byte alongside the two new vectors).

## 1. Goal

Add `RESYNC` (client, 0x0A, `{game_id}`) and `DRAW_OFFERED` (server, 0x87,
`{game_id, by}`) end-to-end — codec, cgo seam, Go binding, transport behavior —
keeping §4A encoding in Rust (NFR-5) and all three clients on the one shared
codec (NFR-10). Done = both new frames round-trip byte-identically through the
real seam, the v1.0 corpus still reproduces (no drift), the ABI fuzz still holds,
and the transport serves a client RESYNC with a STATE and pushes DRAW_OFFERED on
a pending offer.

## 2. Codec (Rust `pgc-protocol`)

- Add `ClientMsg::Resync { game_id: GameId }` and `ServerMsg::DrawOffered {
  game_id: GameId, by: u8 }` — appended at the END of each enum (so existing
  serde variant indices are unchanged; the §4A frame uses the frozen type byte,
  not the variant index, but keeping declaration order stable also keeps the
  GS-6a intermediate indices stable).
- Frozen type codes: `client_code::RESYNC = 0x0A`, `server_code::DRAW_OFFERED =
  0x87`. Payload: RESYNC reuses the shared bare-`game_id` encode/decode arm (like OFFER_DRAW — byte-identical to a one-field struct in postcard, so no `ResyncPayload` type is introduced); DRAW_OFFERED uses `DrawOfferedPayload {
  game_id, by }` in §4A.3 field order.
- `encode_client`/`decode_client`/`encode_server`/`decode_server` gain the new
  arms (decode dispatches on the type byte; the direction check already covers
  0x0A as client, 0x87 as server).

## 3. Golden corpus (the freeze)

- Add `client/resync` and `server/draw_offered` to `tests/corpus/mod.rs`;
  regenerate `protocol/vectors/vectors.json` via `cargo run --example
  gen_vectors`. The `golden_freeze` test then locks the two new frames AND
  re-asserts every v1.0 frame is byte-unchanged (the regression guard — an
  additive change must not perturb a single existing byte).

## 4. Seam + Go binding

- The GS-6a/GS-6 seam already round-trips whatever `ClientMsg`/`ServerMsg` the
  Go mirror and Rust agree on; extend the Go mirror (`message_seam.go`) with the
  two variants + their postcard marshal/unmarshal, and the Go `ClientMsg`/
  `ServerMsg` tagged unions with `CMResync` / `SMDrawOffered` + payload structs.
- The shared-corpus Go tests (`message_seam_test.go` driving the real cgo seam)
  then cover the two new codes automatically once they are in `vectors.json`.

## 5. Transport wiring (GS-6)

- **RESYNC:** route `CMResync` → if the conn is seated in the game, reply `STATE`
  from `StateSnapshot` (the same path as the Option-A auto-push); else
  `ERROR{UNKNOWN_GAME}`. On-demand complement to the auth-time push.
- **DRAW_OFFERED:** in `handleEnding`'s OFFER_DRAW branch, when the offer is a
  first (non-ending) pending offer, push `DRAW_OFFERED{game_id, by}` to the
  OPPONENT's connection (if present on this node). The mutual-offer /
  agreement path still ends via `GAME_END` as before.
- Update GS-6 spec §3.7 to remove the two items from "v1 limitations" (they are
  now implemented) and §3.4 to note RESYNC as the on-demand resync path.

## 6. Test plan

- Rust: `golden_freeze` reproduces v1.0 + the 2 new frames; `negative` covers
  wrong-direction (0x0A as server, 0x87 as client) and malformed; the ABI fuzz
  sweep still passes.
- Go: the shared-corpus seam tests cover the 2 new codes; a transport test for
  RESYNC→STATE and OFFER_DRAW→DRAW_OFFERED-to-opponent.
- Mobile parity re-run (protocol changed) — Swift/Kotlin reproduce the 2 new
  frames; on-host Swift as fallback.
- `make test` + docs-check green; context-free review (a frozen-contract change
  is load-bearing).

## 7. Acceptance criteria

- [ ] ADR-17 owner-approved BEFORE any code (frozen-contract change, approval-gated).
- [ ] Rust codec + corpus + freeze (v1.0 unchanged + 2 new); ABI fuzz green.
- [ ] Go seam mirror + shared-corpus tests cover both codes.
- [ ] Transport: RESYNC→STATE; OFFER_DRAW→DRAW_OFFERED to the opponent.
- [ ] Mobile parity green (CI + on-host). GS-6 spec §3.4/§3.7 updated.
- [ ] `make test` + docs-check green; context-free review clean; makes no lettered
      gate green (re-freezes G-D's realtime half with the two added codes).

## 8. Open questions

- **Q-V11a — RESYNC rate-limit.** A client could spam RESYNC to force repeated
  folds. **Proposed:** a basic per-conn rate limit (reuse the §7 seek/move-flood
  limiter noted in GS-6); tuning later. Not a blocker for the frame itself.
- **Q-V11b — DRAW_OFFERED on reconnect.** Should a reconnecting opponent be
  (re)told of a still-pending offer via DRAW_OFFERED after their STATE? **Proposed:
  no** — the offer is ephemeral session state (GS-3b Q-EG2) not resurrected on
  rebuild, so a reconnect that missed the live DRAW_OFFERED simply does not see it
  until a fresh offer; keeping it ephemeral avoids re-plumbing offer state into the
  fold. (A client that wants it can be told via a future STATE flag — out of scope.)
