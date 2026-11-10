# Protocol §4A v1.3 — takeback consent, draw decline, ClockStarted, full resync STATE (amendment + implementation slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-PROTOCOL-V1.3 (amends SPEC-S2-PROTOCOL §3.1, §3.2, §4A, §5; implements ADR-19) |
| Owner | S2 Real-time + Architecture |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-11-09 |
| Implements (master IDs) | X-2, X-13, ADR-14, ADR-19, NFR-2, NFR-5, NFR-10 |
| Depends on | ADR-19 (DECIDED 2026-11-09 after review by S2, S4, PE, S6 and the architecture group; owner attestation); SPEC-S2-GAME-SERVER-GS7 (the server behaviour these frames carry); PROTOCOL.md at text version 1.2 (#106) |
| Gates it feeds | Re-runs the G-D freeze corpus. Required by GS-7 (fixes #84, #85, #101, #107, #123). |

> **Why this document exists.** It is the exact contract text that will be folded
> into `PROTOCOL.md` when v1.3 is implemented, kept separate (as v1.1 and v1.2 were)
> so the frozen contract never describes behaviour the shipped codec does not
> have. The reasons, alternatives and scope boundary are in ADR-19
> (`docs/MASTER-PLAN.md`); server behaviour is in
> [GAME-SERVER-GS7-FOLD-TAKEBACK-DRAW.md](GAME-SERVER-GS7-FOLD-TAKEBACK-DRAW.md).
> This document pins only bytes, codes, tables and client rules.
>
> **Wire implemented; behaviour pending.** Approved 2026-11-09. The codec, every
> binding, the Go seams and the web mirror implement §2–§6, and §2–§6 are folded
> into `PROTOCOL.md` (2026-11-10). The server still behaves as v1.2 for takeback,
> draw decline and duplicate MOVE: the §7 rows describe the GS-7 reducer and fold
> into `PROTOCOL.md` §5 with it.

## 1. Summary of changes

| Kind | Change | Additive? |
|---|---|---|
| Client messages | `0x0B TAKEBACK_ACCEPT`, `0x0C TAKEBACK_DECLINE`, `0x0D DECLINE_DRAW`; `0x07 TAKEBACK` now means "request" only | yes |
| Server messages | `0x88 TAKEBACK_OFFERED`, `0x89 TAKEBACK_DECLINED`, `0x8A DRAW_DECLINED` | yes |
| Events | `0x0D TakebackProposed`, `0x0E TakebackRewound`, `0x0F TakebackDeclined`, `0x10 DrawDeclined`, `0x11 ClockStarted`; `0x08`/`0x09` frozen, never written | yes |
| Enum | new §4A.10 `status` | yes |
| `STATE` body | 11 fields appended (also changes `MATCHED.start_state`) | **no** (mutates `0x81` and `0x84`) |
| Semantics | MOVE idempotency with recurring plies; two client rules | rule change |

Major `ver` stays `0x01`. All new codes use reserved slots; no existing code moves.

## 2. §4A.2 message type codes (rows added)

| Code | Dir | Message |   | Code | Dir | Message |
|---|---|---|---|---|---|---|
| 0x0B | C | TAKEBACK_ACCEPT | | 0x88 | S | TAKEBACK_OFFERED |
| 0x0C | C | TAKEBACK_DECLINE | | 0x89 | S | TAKEBACK_DECLINED |
| 0x0D | C | DECLINE_DRAW | | 0x8A | S | DRAW_DECLINED |

Rust and Go enum order: new client variants append after `Resync`; new server
variants go before `Error` (`message_seam.go` depends on the Rust enum order,
which also shifted when `DRAW_OFFERED` was added).

## 3. §4A.3 payload layouts (rows added or changed)

```
TAKEBACK            game_id:str36                         -- request only (meaning narrowed)
TAKEBACK_ACCEPT     game_id:str36, at_ply:u32
TAKEBACK_DECLINE    game_id:str36, at_ply:u32             -- decline (responder) or cancel (proposer)
DECLINE_DRAW        game_id:str36                         -- decline (responder) or withdraw (offerer)
TAKEBACK_OFFERED    game_id:str36, seat_idx:u8, at_ply:u32
TAKEBACK_DECLINED   game_id:str36, seat_idx:u8
DRAW_DECLINED       game_id:str36, seat_idx:u8
STATE               game_id:str36, STATE-body(§4A.6)
```

## 4. §4A.6 STATE body (replaced)

The `STATE` body, reused verbatim as `MATCHED.start_state` (Q-P4 unchanged):

```
fen:str, turn:u8, clocks:[u32; seat_count],
you_seat:u8, ply:u32, status:enum8(§4A.10),
result_vector:[u8], termination:u8,
last_move_uci:str5,
pending_draw_by:u8, takeback_by:u8, takeback_at_ply:u32,
running_seat:u8, server_t:u64
```

| Field | Meaning |
|---|---|
| `you_seat` | the recipient's seat; `0xFF` = none (reserved; no spectator path exists) |
| `ply` | server ply; the next `MOVE.ply_expected` is `ply + 1` |
| `status` | §4A.10 |
| `result_vector` | §4A.3 result codes when `ENDED`, empty otherwise; termination `ABORT` = all draw |
| `termination` | §4A.5 code when `ENDED`, `0x00` otherwise |
| `last_move_uci` | the last live move; empty iff `ply == 0` |
| `pending_draw_by` | seat with a pending draw offer, `0xFF` none |
| `takeback_by`, `takeback_at_ply` | pending takeback request; `0xFF` / `0` when none |
| `running_seat` | seat whose clock runs, `0xFF` if none |
| `server_t` | server epoch ms at which `clocks` are valid; the running seat's clock is charged up to it |

**Decode-time invariants** (a frame that violates one is malformed; enforced
identically by the Rust codec, the Go seams and wasm):
1. `result_vector` non-empty iff `status == ENDED`, and then `len == clocks.len()`.
2. `termination != 0x00` iff `status == ENDED`.
3. `you_seat`, `pending_draw_by`, `takeback_by`, `running_seat` each `< clocks.len()` or `0xFF`.
4. `takeback_at_ply == 0` iff `takeback_by == 0xFF`.
5. `last_move_uci` ≤ 5 bytes, empty iff `ply == 0`.
6. `running_seat == 0xFF` when `status == ENDED`.

`STATE` is built **per recipient** (`you_seat` differs per connection).

## 5. §4A.8 event bodies and §4A.9 event types (rows added)

```
TakebackProposed   seat_idx:u8
TakebackRewound    seat_idx:u8, to_ply:u32, server_ts:u64
TakebackDeclined   seat_idx:u8
DrawDeclined       seat_idx:u8
ClockStarted       server_ts:u64
```

| 0x0D TakebackProposed | 0x0E TakebackRewound | 0x0F TakebackDeclined | 0x10 DrawDeclined | 0x11 ClockStarted |

Rust `GameEvent` variants append after `RatingApplied` (the cgo seam uses serde
variant indices). `0x08 TakebackReq` and `0x09 TakebackAccepted` remain decodable
by the codec and are never written by a v1.3 server; the game-server fold rejects
them (GS-7 §5.1; no production data exists).

## 6. §4A.10 `status` enum (new) — u8, frozen

| 0x01 MATCHED | 0x02 LIVE | 0x03 ENDED |

`0x00` and unlisted values are rejected on decode.

## 7. §5 edge cases (rows replaced or added)

| Case | Contract behavior |
|---|---|
| **Duplicate MOVE** (replaces v1.2 row) | After a takeback a ply number can recur, so `(game_id, ply_expected)` alone no longer identifies a move. For `1 <= ply_expected <= ply`: same seat and same `uci` as the move live at that ply → re-send `MOVE_ACCEPTED` to the sender only (and `GAME_END` again if that move ended the game); same seat, different `uci` → `MOVE_REJECTED{STALE_PLY}`; different seat → `NOT_YOUR_SEAT`. This applies even after the game ended (v1.2 answered `GAME_OVER`). |
| **Reconnect** (amends) | The pushed `STATE` carries seat, ply, status, result, last move, pending prompts and a clock anchor, so a client resumes from `STATE` alone. |
| **Takeback request / accept / decline** | Request needs a live unrated 2-seat game with both seats moved and nothing pending. Accept and decline carry `at_ply`; an accept or decline that matches no pending request changes nothing and the server pushes `STATE` to the sender (no `ERROR`). Rated game or seat count ≠ 2 → `ERROR{PROTOCOL_VIOLATION}`. |
| **Draw decline** | `DECLINE_DRAW` with nothing pending changes nothing; the server pushes `STATE`. |
| **Client must not retransmit MOVE** (new rule) | A client MUST NOT resend a `MOVE` frame it has already sent; a lost ack is recovered with `RESYNC`/`STATE`. |
| **Undecodable server frame at a client** (new rule) | A client MUST drop and log any server frame it cannot decode (unknown type, wrong direction, malformed) and keep the connection. Only `ERROR{UNSUPPORTED_VERSION}` is fatal. This does not make changes to existing frames safe: an old client drops a v1.3 `STATE`/`MATCHED` and stays stuck, so v1.3 server and clients ship together. |

## 8. Golden corpus

- New vectors: the 6 new messages and 5 new events; at least one `STATE` per
  `status` value, one with pending offers, one ENDED with a result.
- Changed vectors: `server/state` and `server/matched` (the mutated body). Every
  other v1.2 vector must still reproduce byte-for-byte (regression check).
- Negative vectors: one per §4 decode invariant, and `status` `0x00`/`0x04`.

## 9. Consumers that change

Rust codec (`protocol/codec`), wasm (`protocol/codec-wasm`), UniFFI
(`protocol/codec-ffi`) and the iOS/Android parity suites, Go cgo seams
(`event_seam.go`, `message_seam.go`), the web TS mirror
(`clients/web/src/codec/messages.ts`) and reducer, `protocol/vectors/`, and on
fold-in `PROTOCOL.md` §3.1, §3.2, §4A.2, §4A.3, §4A.6, §4A.8, §4A.9, new §4A.10,
§5, and the §11 version history. `platform/ci/check_docs.py` (#106) fails until
the new codes are in `PROTOCOL.md` §4A.2.

## 10. Acceptance criteria

- [x] ADR-19 reviewed by S2, S4, PE, S6 and the architecture group; this document RATIFIED (2026-11-09).
- [x] Codec, all bindings and the Go seams implement §2–§6; decode invariants enforced in the Rust codec on encode and decode, which every binding and the Go seams call.
- [x] Golden corpus per §8; all consumers byte-identical; v1.2 vectors other than the three STATE-body vectors (`state`, `state_max_fen`, `matched`) unchanged.
- [ ] Client rules (§7) covered by client contract tests (web now; iOS/Android in P2). With the GS-7 web reducer.
- [ ] §2–§6 folded into `PROTOCOL.md` (wire 1.3) and docs-check green — done; §7 rows fold in with the GS-7 reducer.

## 11. Open questions

None blocking. Decided by the owner on 2026-11-09: Q-V1 (drop undecodable server
frames), Q-V2 (full #101 STATE in this amendment), Q-V3 (no production data). Deferred
by ADR-19: opponent identity in `STATE` (#131), first-clock rule (#124).
