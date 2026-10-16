# Protocol §4A v1.2 — MOVE_ACCEPTED carries `uci` + broadcast to both seats (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-PROTOCOL-V1.2 (amends SPEC-S2-PROTOCOL §4A; implements ADR-18) |
| Owner | S2 Real-time + S4 Web + Architecture |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-16 |
| Implements (master IDs) | X-2, X-13, ADR-14, ADR-18, NFR-1, NFR-5, NFR-10 |
| Depends on | ADR-18 (PROPOSED — this slice does not implement until ADR-18 is owner-approved); the merged codec + GS-6a/GS-6 seams + GS-6 transport + §4A v1.1 (ADR-17) |
| Gates it feeds | None directly. Unblocks the S4 web client's reducer/move path (SPEC-S4-WEB-F01 §3.3), which is currently blocked: a two-human game cannot show the opponent's moves. Re-runs the G-D freeze corpus with the changed MOVE_ACCEPTED frame. |

> **Why this slice exists.** Building the S4 web client surfaced that the §4A
> move hot path cannot drive a real two-human game: `MOVE_ACCEPTED` goes to the
> mover only and carries no move, so the opponent's board never advances and no
> client can render the resulting position (no web engine binding exists).
> ADR-18 fixes it by adding `uci` to `MOVE_ACCEPTED` and broadcasting it to both
> seats. **Unlike the v1.1 slice (additive codes in reserved slots, zero existing
> bytes changed), this MUTATES a frozen frame** — the `server/move_accepted`
> golden vector changes. Safe only because all clients bind the one codec and
> none is deployed yet (ADR-18 context). Additive-at-the-tail field order keeps
> the change minimal, but the vector regenerates and the freeze test re-locks it.

## 1. Goal

End-to-end: `MOVE_ACCEPTED` carries the `uci` of the move just applied, and an
accepted non-terminal move is delivered to BOTH seats. Done =
- the codec `ServerMsg::MoveAccepted` gains `uci`, round-trips byte-identically
  through the real seam (Rust + cgo + wasm), with the regenerated golden vector;
- the GS-6 transport broadcasts `MOVE_ACCEPTED` to both seats on an accepted
  non-terminal move;
- a regression test proves the OPPONENT receives the move (the gap this closes);
- the web client (SPEC-S4-WEB-F01) can apply the authoritative UCI to its board.

## 2. Codec (Rust `pgc-protocol`)

- `ServerMsg::MoveAccepted` becomes `{ game_id: GameId, ply: u32, uci: String,
  clock_after_ms: u32 }` — `uci` inserted after `ply` (mirrors the `MovePlayed`
  event body order `ply, seat_idx, uci, clock_after_ms`; §4A.3/§4A.9). The
  per-variant payload struct `MoveAcceptedPayload` gains `uci` in the same
  position; postcard is positional so field order IS the wire — `uci` is a
  length-prefixed `str` (≤ 5 B, like `MovePlayed.uci` and `C MOVE.uci`).
- `encode_server`/`decode_server` need no new arm (same type code `0x82`); the
  payload struct change flows through. `de_exact` still rejects trailing bytes.
- No change to `C MOVE`, `MovePlayed`, or any other message.

## 3. Golden corpus (the freeze — this is the delicate part)

- `server/move_accepted` in `protocol/vectors/vectors.json` **changes** (the
  frame now includes `uci`). Regenerate the WHOLE corpus via `cargo run
  --example gen_vectors` with the `move_accepted` fixture updated to carry a
  `uci` (e.g. `e2e4`), so the vector reflects the v1.2 frame.
- The `golden_freeze` test re-locks the new `server/move_accepted` bytes AND
  re-asserts every OTHER v1.0/v1.1 frame is byte-UNCHANGED (only this one frame
  moves — a diff touching any other vector is a mistake and must fail review).
- Call this out EXPLICITLY in the PR as a deliberate frozen-frame mutation
  (ADR-18), not an accidental corpus drift — a reviewer must see one and only
  one vector changed, on purpose.

## 4. Seam + Go binding + wasm

- cgo seam (`codec-cgo`) + Go mirror (`message_seam.go`): the Go `MoveAccepted`
  payload struct gains `uci`; the shared-corpus Go test picks up the regenerated
  vector automatically.
- wasm seam (`codec-wasm`): no code change (it serializes the typed struct); the
  node-parity test's `server/move_accepted` hex updates with the regenerated
  corpus. The web client's TS `ServerMsg` type (`MoveAccepted`) gains `uci:
  string` (SPEC-S4-WEB-F01 §3.1 message types).
- Mobile parity (Swift/Kotlin) re-runs against the regenerated corpus.

## 5. Transport (GS-6, `services/game-server/internal/transport`)

- `handleMove`, on `out.Accepted` and NOT terminal: build the `MOVE_ACCEPTED`
  with `uci = p.UCI` (the move just persisted) and send it to **both seats**
  (the mover and the opponent via `connFor(gameID, otherSeat)`), replacing the
  mover-only `s.send(ctx, c, …)`. The terminal path still broadcasts GAME_END.
- The idempotent re-ack path (duplicate MOVE on the same `ply_expected`,
  PROTOCOL §4A duplicate-MOVE rule) must also carry the real `uci` for that ply
  (the re-ack echoes the SAME move), not an empty string — so a client that
  missed the first ack still learns the move. (This is the GS-3 "re-ack with the
  real seq/fields, not a blank" lesson applied to `uci`.)
- The opponent connection may be absent (disconnected mid-game); send is
  best-effort to a present seat exactly as DRAW_OFFERED/GAME_END already are —
  the absent seat resyncs via STATE on reconnect (Option A), which already
  carries the full FEN, so no move is lost.

## 6. Test plan

- **Codec:** `MoveAccepted` round-trip + the regenerated golden vector; the
  freeze test proves exactly one vector changed.
- **Seam:** cgo + wasm node-parity reproduce the new `server/move_accepted`.
- **Transport regression (the gap closer):** a test where seat A plays a move
  and **seat B receives `MOVE_ACCEPTED` with the correct `uci`** — the assertion
  the current `TestE2E_GameEndsByResign` never made. Add an explicit
  `TestMoveBroadcastToBothSeats` (A moves → both A and B see MOVE_ACCEPTED{uci});
  and extend the e2e so black actually consumes white's move before proceeding.
- **Idempotent re-ack:** a duplicate MOVE on the same ply re-acks with the SAME
  `uci` (regression against the blank-field class of bug).

## 7. Acceptance criteria (Definition of Done)

- [ ] `ServerMsg::MoveAccepted` carries `uci`; round-trips through the real
      Rust/cgo/wasm seam; golden corpus regenerated with exactly one changed
      vector (`server/move_accepted`), freeze test green.
- [ ] GS-6 broadcasts MOVE_ACCEPTED (with `uci`) to both seats on an accepted
      non-terminal move; terminal + re-ack + disconnected-opponent paths correct.
- [ ] `TestMoveBroadcastToBothSeats` proves the opponent receives the move; the
      e2e consumes the opponent's move.
- [ ] cgo + wasm node-parity + mobile parity green against the regenerated corpus.
- [ ] `cargo fmt --check` on every touched Rust crate; go.mod ↔ CI setup-go
      unchanged (no new dep); the mobile parity harness re-run (protocol/** changed).
- [ ] A context-free independent reviewer has reviewed the diff (load-bearing:
      frozen-frame mutation + hot-path broadcast) BEFORE merge.

## 8. Open questions

- **Q-V12a — field position of `uci`.** `{game_id, ply, uci, clock_after_ms}`
  (uci after ply, matching the `MovePlayed` event body) vs appended at the end
  `{…, clock_after_ms, uci}`. **Proposed: after `ply`** — reads 1:1 against the
  event body and groups the move identity (`ply`+`uci`) before the clock. Either
  is a one-time frozen-frame change; pick the one that matches the event body.
- **Q-V12b — does the mover's ack and the opponent's notification need to
  differ?** **Proposed: no** — one identical `MOVE_ACCEPTED{uci}` to both seats.
  The mover uses it to confirm/replace its optimistic move; the opponent uses it
  to apply the move. One frame, one code path, no divergence.
