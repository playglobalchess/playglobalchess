# Game-server GS-7 — one canonical fold, takeback with consent, draw stream shape (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS7 (amends GS-3 live session, GS-3b endgame commands, PROTOCOL §4A, §5, §7) |
| Owner | S2 Real-time |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-11-09 |
| Fixes (issues) | #84, #85 (blockers); #94, #67, #89, #122, #107, #101, #123 (directly); enables #95, #88, #97, #98, #111 (see §10) |
| Depends on | GS-3, GS-3b, GS-4 (atomic `appendBatchAtHead`), PROTOCOL §4A / v1.1 / v1.2 |
| Gates it feeds | None lettered. Prerequisite for rated play (G-C) and restart recovery (#98). |

> **Why this exists.** Two blockers were reproduced on `main` (ad0626d) with
> failing-first tests: #84 (after a takeback the next move is re-acked as the
> undone move and the session FEN becomes empty) and #85 (every agreed draw
> persists a stream that `Fold` rejects). They share one cause: game state is
> computed by several readers that disagree. This slice removes the seam.

**Scope declared up front.** Beyond the two blockers, this slice also changes:
the takeback flow (real consent, #107), draw rules (explicit decline, cooldown,
which move declines an offer), MOVE idempotency (plies can recur), the full
`STATE` extension of #101 (owner decision Q-V2, §7.4), and a forward-compat rule
for unknown server messages (owner decision Q-V1, §7.6). Each is required for
the fold to stay single and correct or was chosen by the owner to avoid amending
`STATE` twice; none is optional polish.

**Review record.** v0.1 approved in direction by the owner in chat (2026-11-09:
takeback kept, clocks rewound, draw option A). A context-free independent review
of v0.1 returned **BLOCK** (2 blockers, 8 should-fix); v0.2 was re-reviewed and
returned **BLOCK** (1 blocker, introduced by v0.2's own timestamp choice, plus 8
should-fix). v0.3 replaces point fixes with a full per-field write-coverage audit
(§3.9) and simplifies: derived events removed, legacy streams dropped. v0.3's
review returned **RATIFY-WITH-CHANGES** (no blocker, 7 should-fix); v0.4 applied
them; a confirmation review of v0.4 returned **RATIFY-WITH-CHANGES** (no blocker,
3 should-fix), applied in v0.5. v0.6 records the owner's answers to Q-V1, Q-V2
and Q-V3 (2026-11-09) and adds the #101 `STATE` fields; a focused review of that addition returned
**RATIFY-WITH-CHANGES** (no blocker, 7 should-fix), applied in v0.7. Mapping in §13.

**Ratified 2026-11-09 (v1.0).** The owner attests that S2, S4, Platform-Eng, S6
and the architecture group reviewed and approved this spec, ADR-19 and the v1.3
amendment in a team review held outside GitHub (no GitHub review is attached to
PR #156). The owner also kept the single bundled plan after a PR review finding
that the blocker fix could ship without the protocol change; that tradeoff is
recorded in §13. Sign-offs are recorded by stream name in §14. **Nothing is
implemented yet**: ratifying approves the design and the v1.3 contract text, and
the build follows §12.

## 1. Goal and scope

**Goal.** No stream the server writes can be un-foldable, and the live session can
never disagree with a rebuild from the store.

**In scope:** the evolve/decide structure of the session and move pipeline; new
takeback and decline events; takeback consent, ply and clock rules; draw rules;
MOVE idempotency under recurring plies; the STATE pending-offer fields; the
invariant test harness.

**Out of scope:** persist-error ambiguity (#95), cross-node game ownership
(#98), flag-ticker redesign (#97), token persistence across reload (the web half
of #111), client UX. Two-seat games only: `decide` returns `ErrUnsupportedSeatCount` for takeback
and draw commands when `seatCount != 2`.

## 2. Root cause (evidence, verified by the independent reviewer)

- `applyTakebackAccepted` only clears the pending request (`game/statemachine.go`).
  The roll-back lives in `RefoldTakebackWithEngine`, called only by the session's
  takeback command (`session/commands.go`). The pipeline (`game/pipeline.go:167`)
  and `NewSession` (`FoldWithEngine`) fold the raw stream. Two cut algorithms exist.
- `TakebackAccepted{seat}` does not say what was undone.
- `findMove` returns the first `MovePlayed` for a ply; it is called on the
  idempotent re-ack path AND the `ErrSeqConflict` reconcile path in `pipeline.go`.
  After a takeback both can re-ack an undone move that the stream no longer means.
- The pipeline's raw fold reports a stale ply after a takeback, so it also emits
  spurious `NOT_YOUR_TURN` / `STALE_PLY` rejections.
- The session tells a re-ack from a new move by ply arithmetic (`out.Ply == s.ply+1`).
- Other raw-stream scanners carry the same flaw: `lastMover`, `lastMoveServerTS`,
  `trailingUnendedFlag` (`session/session.go`) and `LastMoveTerminal` in
  `FoldWithEngine`, which can wrongly end a live game on rebuild via
  `finishIfHalfTerminal`.
- GS-3b writes `DrawOffered{b}, DrawAccepted{b}` for a mutual offer; the fold
  forbids accepting your own offer (#85).
- No test folds a stream the session wrote and compares it with the live state.

## 3. Architecture: one evolve, one decide

### 3.1 State and history

`State` keeps today's fields plus:
- `History []PlyEntry` — one entry per LIVE (non-undone) ply: `{seat, uci,
  clockAfterMs, serverTS, seq, fen, zobrist, terminal}`. `fen`, `zobrist`, `terminal`
  are engine-derived and filled only when a verdict is supplied (§3.2).
- `NextSeq uint32` — the stream sequence number of the next append.
- `LastChangeTS uint64` — idle-clock baseline, read ONLY from event BODIES:
  `ClockStarted.server_ts` (new, §5.1), `MovePlayed.ServerTS` (exists today) or
  `TakebackRewound.server_ts` (new). Each is set from the same single `now` that
  `decide` uses for the clock arithmetic of that command (one read per `decide`
  call, including a re-decide after `ErrSeqConflict`), so live and rebuilt values
  are equal by construction. Units: epoch milliseconds, integer.
- `ClockStartedSet bool` — false until `ClockStarted` is applied. "Not started"
  is this flag, never `LastChangeTS == 0` (a fake clock at the Unix epoch writes a
  real `ServerTS` of 0).
- `PendingTakeback *{by, atPly}` (replaces `TakebackReqBy`), `DrawOfferBy`, and
  per-seat `LastDrawOfferPly` (cooldown, §6).
- `HalfTerminal *{seat}` — a `ClockFlagged` not yet followed by `GameEnded`
  (resign and abort are always appended atomically with `GameEnded`, so only a
  flag can be half-terminal). Replaces the `trailingUnendedFlag` stream scan;
  `finishIfHalfTerminal` reads it.

### 3.2 evolve (the only state transition)

```
evolve(state, event, verdict *EngineVerdict) (State, error)
```

- No timestamp parameter and no store header is read. Every time value the fold
  uses is in an event body (§3.1). The store's header TS stays an audit field only.
- `verdict` is `{fen, zobrist, terminal}` for a `MovePlayed`, nil otherwise.
  With nil verdicts, `evolve` still computes ply, turn, clocks, offers, history
  shape and `NextSeq` identically; only the engine fields stay empty. This is the
  ONE algorithm; "with engine" and "without engine" differ only in whether the
  engine fields are filled, never in which plies are live.
- `evolve` rejects takeback and draw events in a game whose seat count is not 2
  (the same guard as `decide`, §1).
- `Fold(events)` = `evolve` over the stream with nil verdicts (engine-free, NFR-5).
- `FoldWithEngine(events, oracle)` = `evolve` over the stream, obtaining a verdict
  for each `MovePlayed` that is LIVE at the moment it is applied, by calling the
  engine on the current history-tip FEN. Undone moves are truncated by the later
  `TakebackRewound` in stream order, so replay stays correct. Used on rebuild only.

### 3.3 decide (validation, no mutation)

```
decide(state, cmd, now, oracle) ([]Event, Outcome, error)
```

`decide` mutates nothing and appends nothing. For a MOVE it calls the engine once
(the hot path stays at one engine call) and returns the events plus the verdict.
It is not pure in the strict sense (one engine call), but it is deterministic and
side-effect-free; that is the property required.

### 3.4 The session loop

1. `events, out, err := decide(state, cmd, now, oracle)`.
2. `next := evolve*(state, events, verdicts)` BEFORE appending. An event
   the fold would reject is caught here and never persisted.
3. Append at `expectedSeq = state.NextSeq`: `Append` for a single event (the
   move hot path keeps the conditional `PutItem`), `AppendBatch` for several (a
   `TransactWriteItems`). Both exist on `MemStore` and the DynamoDB store. Not
   `appendBatchAtHead`: the expected sequence is the state's own.
4. On success: `state = next`.
5. On `ErrSeqConflict`: reload the stream, rebuild state with `FoldWithEngine`,
   then re-run `decide` for the same command (it may now be a re-ack, stale, or
   still valid). Bounded retries, then `INTERNAL`.
6. On an ambiguous persist error: same reload-and-redecide. Full design is #95.

### 3.5 Flag checks and the ticker

`TickFlagCheck` becomes a session command: `decide(TickFlag, now)` reads
`state.TurnIdx`, the side-to-move clock and `LastChangeTS` from the CURRENT state,
so after a rewind flips the side to move the next tick charges the right seat with
no separate re-baseline. The move-arrives-after-flag path (today
`Outcome.Flagged`) also moves into `decide`, as does the flag check before a
takeback accept (§5.2).

Elapsed time is computed the same way on every path: `elapsed_ms = now_ms -
LastChangeTS`, integer milliseconds, wall clock. This replaces today's monotonic
`time.Time.Sub` on the live path; a wall-clock baseline is needed anyway for a
durable, cross-node value. Consequence: an NTP step on a node mis-charges by the
step size, bounded by the skew requirement below.

**First-clock baseline.** Today the session seeds the baseline from `now()` at
`NewSession` when no move exists (`session.go`, `lastMoveAt := now()`), which is
wall-clock state outside the stream, so a restart or a build on another node
resets it. GS-7 makes TODAY's rule durable without moving it: when `NewSession`
builds a LIVE game whose state has `ClockStartedSet == false`, it appends
`ClockStarted{server_ts = now}` at `expectedSeq = NextSeq` before serving. The
first build (in `deliverMatched`, right after matchmaking's opening batch) is the
same instant as today. Every later build reads the persisted value. Two nodes
building at once: one append wins, the other gets `ErrSeqConflict`, reloads and
finds it set. Matchmaking's opening batch is unchanged (still 3 events).

Not decided here (#124): whether the clock should start at first session build,
at match time, or at a first-move timer as lichess does, and whether the join
window (`joinTTL`, 30 s) should drain a bullet clock. GS-7 keeps today's answer.

If `ClockStartedSet == false` (only possible before the first build has appended
it), no idle time is charged.

Clock baselines are cross-node wall-clock instants (already true for
`ServerTS`). Operational requirement: game-server nodes keep clock skew under
100 ms (NTP); elapsed time after failover includes that skew.

### 3.5a The pipeline

The pipeline stops loading and folding the stream per move. It runs inside the
session's `decide` against the session's state. Cross-node staleness is caught by
`expectedSeq` on the append (step 3), not by a per-move O(n) fold.

### 3.6 Deleted or ported

Deleted: `RefoldTakeback`, `RefoldTakebackWithEngine`, `takebackPrefix`,
`findMove`, `lastMover`, `lastMoveServerTS`, `trailingUnendedFlag`, the session's
direct field assignments, `State.TakebackReqBy`. Replaced by State fields:
`History[last].seat`, `LastChangeTS`, `HalfTerminal`, `History[last].terminal`
(was `LastMoveTerminal`), `PendingTakeback`. `finishIfHalfTerminal` reads
`HalfTerminal`.

### 3.7 Move-produced terminal

Move plus `GameEnded` is one batch (#67).

### 3.8 Stream consumers

Undone `MovePlayed` events stay in the append-only stream with recurring ply
numbers. Every consumer (PGN, replay, analysis, anti-cheat) MUST read the stream
through the canonical `evolve` and use `History`, never scan `MovePlayed` events.
Rating is unaffected: takeback is unrated-only.

### 3.9 Write-coverage audit (every State field, every path)

"Live" = session after a successful append; "rebuild" = `FoldWithEngine` over
the store; both run the same `evolve`, so the question per field is only which
events write it and whether that event carries everything needed.

| Field | Initial value | Written by (event) | Value source | Live == rebuild? |
|---|---|---|---|---|
| Phase, Seats, Rated, StartFEN, PoolID, TimeControl | `GameCreated` | same | event body | yes |
| Joined | all false | `PlayerJoined` | event body | yes |
| Ply, TurnIdx | 0 / seat of white | `MovePlayed` (+1), `TakebackRewound` (= `to_ply`) | event body | yes |
| Clock per seat | `BaseMs` | `MovePlayed` (mover), `TakebackRewound` (both, §5.4), `ClockFlagged` (flagged seat set to 0) | `ClockAfterMs` in History; 0 on flag | yes |
| Clock.FlaggedSeat | none | `ClockFlagged` | event body | yes |
| CurFEN, PriorKeys | start FEN / empty | derived from `History` tip and keys; not stored separately | verdicts | yes, when verdicts supplied |
| History | empty | `MovePlayed` (push), `TakebackRewound` (truncate) | body + verdict | yes; engine fields only with verdicts |
| NextSeq | 0 | every event (+1) | stream position | yes |
| LastChangeTS, ClockStartedSet | 0 / false | `ClockStarted` (sets both, appended by the first `NewSession`), `MovePlayed` (`ServerTS`), `TakebackRewound` (`server_ts`) | event body | yes (body, not header) |
| PendingTakeback | nil | `TakebackProposed` (set, `atPly` = Ply), cleared by `TakebackRewound`, `TakebackDeclined`, responder's `MovePlayed`, `GameEnded` | derived | yes |
| DrawOfferBy | -1 | `DrawOffered` (set), cleared by `DrawAccepted`, `DrawDeclined`, responder's `MovePlayed`, `TakebackRewound`, `GameEnded` | derived | yes |
| LastDrawOfferPly[s] | none | `DrawOffered{s}` (= Ply); entries > `to_ply` dropped by `TakebackRewound` | derived | yes |
| HalfTerminal | nil | `ClockFlagged` (set), `GameEnded` (clear) | event body | yes |
| Termination, ResultVector | none | `GameEnded` | event body | yes |
| RatingApplied | none | `RatingApplied` | event body | yes (unchanged) |

Every field has a writer on every path; none is read from a header or a stream
scan. The only wall-clock read is `now` inside `decide`, and its value reaches
state only through an event body. "Responder's `MovePlayed`" is derivable inside `evolve` (the
mover is not the seat that offered/proposed), so no extra declined event is
written for it.

## 4. MOVE idempotency (amends PROTOCOL §5, §7)

`(game_id, ply_expected)` is no longer unique because plies recur after a
takeback. New rule, applied by `decide` BEFORE the phase check (so a resent mating
move after the game ended is re-acked, #122, #89):

Rows are checked in order.

| Condition | Outcome |
|---|---|
| sender seat `>= seatCount` | `NOT_YOUR_SEAT` (unchanged) |
| `1 <= ply_expected <= state.Ply`, `History[ply_expected].seat` != sender | `NOT_YOUR_SEAT` (unchanged code) |
| `1 <= ply_expected <= state.Ply`, same seat AND same UCI | re-ack (`Outcome.Reacked = true`, `AppliedSeq` = `History[ply].seq`), append nothing |
| `1 <= ply_expected <= state.Ply`, same seat, different UCI | `MOVE_REJECTED{STALE_PLY}` with the server's ply; the client resyncs |
| game ended (any other `ply_expected`) | `GAME_OVER` (unchanged) |
| `ply_expected == state.Ply + 1`, sender is not the side to move | `NOT_YOUR_TURN` (unchanged) |
| `ply_expected == state.Ply + 1`, game live | validate and apply (new move) |
| otherwise (`0`, or `> Ply + 1`) | `STALE_PLY` (unchanged) |

`PlyEntry` also stores the event's stream `seq` for `AppliedSeq`.

Behaviour changes versus today (`pipeline.go`, phase check first): a resent move
after the game ended is now re-acked instead of `GAME_OVER` (#122, intended), and
an ended game with `ply_expected <= Ply`, same seat, different UCI now gets
`STALE_PLY` instead of `GAME_OVER`. Either tells the client to resync; the second
is accepted as harmless.

**Transport for a re-ack:** `MOVE_ACCEPTED` goes to the SENDER only (a re-ack is
not news to the opponent). If the re-acked move ended the game, the sender also
gets `GAME_END` again (#122).

**No client retransmission (new protocol rule, PROTOCOL §5).** A client MUST NOT
resend a `MOVE` frame it has already transmitted; a lost ack is recovered by
`RESYNC`/`STATE`. The web client already complies: `GameSocket.ts` drops a MOVE
when the socket is not open (only AUTHENTICATE and SEEK are queued), and
`useGame.move` blocks while a move is pending. Under this rule a stale MOVE after a rewind
always has `ply_expected` greater than `to_ply + 1` (a rewind only lowers the ply
below what the client has seen) and is rejected as `STALE_PLY`, never re-applied.
This relies on in-order delivery per connection (true for one WebSocket).

**Enforcement and residual risk.** The rule is recorded in ADR-19 and in the
client contract tests that every client (web now, mobile in P2) must pass. The
server cannot tell a forbidden resend from a legitimate replay of the same move
after a takeback, so it does not guard; it counts re-acks as a metric (a
compliant client produces none). A non-compliant client could redo an undone move
in an unrated game; game state stays consistent (the §9 invariant still holds),
so the impact is UX only.

This deliberately changes the v1.2 behaviour that re-acked a persisted move even
when the retry's UCI differed: after a takeback, a different UCI at that ply means
the client is stale, not retrying.

The session distinguishes re-ack from new move by `Outcome.Reacked`, never by ply
arithmetic.

## 5. Takeback

Unrated games only (F-01.4). Modelled on lichess `Takebacker.scala`,
`MovePlayer.scala`, `Rewind.scala` (read 2026-11-09). Deviations are marked **[dev]**.

### 5.1 Events (additive, new type ids; amends PROTOCOL §4A.9)

| Id | Event | Body | Notes |
|---|---|---|---|
| `0x0D` | `TakebackProposed` | `{seat_idx}` | `at_ply` = the fold's ply when applied (derivable, not stored) |
| `0x0E` | `TakebackRewound` | `{seat_idx, to_ply: u32, server_ts: u64}` | `seat_idx` = accepter. `server_ts` = the session's `now` (idle baseline, §3.1). `evolve` rejects it unless `to_ply` equals the §5.3 formula |
| `0x0F` | `TakebackDeclined` | `{seat_idx}` | decline by responder or cancel by proposer |
| `0x11` | `ClockStarted` | `{server_ts: u64}` | appended by the first `NewSession` of a LIVE game (§3.5) |

Existing `TakebackReq` (`0x08`) and `TakebackAccepted` (`0x09`) are **frozen,
still decodable, and never written again**. `evolve` rejects them with a typed
error (`ErrLegacyTakeback`). No event body changes, so the additive-only rule
holds.

**Pre-GS-7 streams are not supported (owner confirmed on 2026-11-09 that no
production data exists, Q-V3).** Two kinds of old stream stop folding:
- any stream with `TakebackReq`/`TakebackAccepted` (already inconsistent, #84;
  a "legacy meaning" would invent clocks the old session never showed);
- some VALID old draw streams: the old fold cleared an offer on any move, so
  `DrawOffered{W}, MovePlayed(W), DrawOffered{B}` folded, but under §6.2 W's offer
  is still pending and B's `DrawOffered` must have been an accept. `evolve`
  rejects it. (A repeated `DrawOffered{W}` while W's own offer is pending is
  accepted as idempotent, so that old shape still folds.)

Dev and test streams that fail to fold are discarded. There are no players and no
persisted production games. An existing live stream without `ClockStarted`
gets one appended on its next session build, which matches today's fallback of
charging from build time.

### 5.2 Flow

1. Seat p sends `TAKEBACK` (`0x07`, unchanged: request only). `decide` requires:
   live, unrated, both seats have moved (ply >= 2), no takeback pending. Appends
   `TakebackProposed{p}`; server pushes `TAKEBACK_OFFERED{game_id, seat_idx, at_ply}`.
   A repeat from p while its own request is pending is a no-op. **[dev]** A
   `TAKEBACK` from q while p's request is pending is a no-op plus `STATE` push,
   not an acceptance as in lichess: acceptance is only `TAKEBACK_ACCEPT`, so a
   repeated or late frame can never change meaning. Errors: rated game or seat
   count != 2 → `PROTOCOL_VIOLATION` (today's mapping); ply < 2 → no-op plus
   `STATE` push.
2. Opponent q sends `TAKEBACK_ACCEPT{game_id, at_ply}`. If a request from p is
   pending with that `at_ply`: `decide` first runs the flag check at `now`; if the
   side to move has flagged, the game ends by flag and the accept is dropped (no
   refund for a flagged player). Otherwise append `TakebackRewound{q, to_ply}` and
   push `STATE` to both seats.
3. `TAKEBACK_DECLINE{game_id, at_ply}` from q (decline) or p (cancel): append
   `TakebackDeclined{seat}`; push `TAKEBACK_DECLINED` to the other seat.
4. A move by the responder q declines: `evolve` clears the request on q's
   `MovePlayed` (derivable; no extra event). A move by the proposer p does not
   clear its own request.
5. Game end clears a pending request (the fold clears it on `GameEnded`).
6. **Stale or repeated accept/decline** (no request pending, or `at_ply` does not
   match): no state change, no error frame; the server pushes `STATE` to the sender
   so its UI converges. A resent accept after a lost ack therefore becomes a
   harmless STATE push, never a new proposal.

### 5.3 Plies undone

`k = 1` if, at proposal time, it was the accepter's turn (the proposer moved
last); otherwise `k = 2`. `to_ply = at_ply - k`. Since `at_ply >= 2`, `to_ply >= 0`.
Because the responder's move declines the request (§5.2.4), at most one proposer
move falls between request and accept, so up to 3 plies are undone. Matches
lichess `acceptedPlies`.

### 5.4 Clocks (re-derived from lichess `Rewind.scala`, confirmed by the reviewer)

After truncating `History` to `to_ply`:
- **Side to move after the rewind:** clock = `History_before[to_ply + 1].clockAfterMs`
  (its own move, now undone; it keeps that move's cost and increment and is refunded
  only its think time since).
- **Other seat:** clock = its last retained `clockAfterMs` in the truncated history,
  or `BaseMs` if it has no retained move.
- `LastChangeTS` = `TakebackRewound.server_ts`; the side to move's clock runs
  from there.

Worked table (5+3: Base 300000, Inc 3000; ply1 W 301000, ply2 B 298000, ply3 W
294000, ply4 B 297000, ply5 W 291000):

| Case | at_ply | k | to_ply | W after | B after |
|---|---|---|---|---|---|
| B proposes after ply 4, W accepts | 4 | 1 | 3 | 294000 | 297000 |
| W proposes after ply 4, B accepts | 4 | 2 | 2 | 294000 | 298000 |
| W proposes after ply 4, plays ply 5, B accepts | 4 | 2 | 2 | 294000 | 298000 |
| W proposes at ply 2, B accepts | 2 | 2 | 0 | 301000 | 300000 (Base) |

The same table is a required test (§9).

Known consequence (matches lichess as read, unrated only): at `to_ply = 0` the
side to move keeps `History[1].clockAfterMs`, which already includes the
increment, and replaying the move adds it again. A cooperating opponent can farm
increment this way. Accepted: takeback needs consent and is unrated-only.

### 5.5 Other effects of a rewind

- Pending draw offer is cleared. **[dev]** Lichess's `Rewind` prunes draw-offer
  history but does not clear a standing offer; we clear it because the offer was
  made about a position that no longer exists.
- `LastDrawOfferPly` entries above `to_ply` are dropped (lichess `beforePly`;
  whether lichess also drops an entry equal to `to_ply` is unverified, and here it
  is kept).
- Abort window: a rewind below `abortBeforePly` reopens abort, as lichess does
  (`playedPlies < 2`). Acceptable because takeback is unrated-only.

## 6. Draw (decision D5: option A)

### 6.1 Stream shape

Mutual agreement is `DrawOffered{a} [MovePlayed by a] DrawAccepted{b} GameEnded{DRAW_AGREE}`.
Seat b's `OFFER_DRAW` while a's offer is pending IS the acceptance. No extra
`DrawOffered{b}`. The pairing rule: every `DrawAccepted{X}` follows a pending
`DrawOffered{Y}` with Y != X.

This reverses GS-3b **Q-EG5** (extra `DrawOffered{other}`, the cause of #85) and
**Q-EG2** (the offer was ephemeral and not reconstructed on rebuild; with one
`evolve`, rebuild reconstructs it).

### 6.2 Fold changes (correcting v0.1's "fold is unchanged")

- `applyMovePlayed` clears `DrawOfferBy` only when the mover is NOT the offerer
  (the responder declines by moving). Today it clears on any move.
- `applyDrawOffered{s}` rejects an offer while the OTHER seat's offer is pending
  (that must be an accept); a repeat by s while its own offer is pending is
  idempotent (no change). Today it silently overwrites either way.
- New event `DrawDeclined` (`0x10`, `{seat_idx}`): explicit decline or withdraw;
  clears the pending offer.

### 6.3 Rules (from lichess `Drawer.scala`, `Game.scala`, `MovePlayer.scala`)

Order inside `decide` for `OFFER_DRAW` from seat s:
1. If the OTHER seat's offer is pending: accept (append `DrawAccepted{s}`,
   `GameEnded`). No ply or cooldown gate applies to an accept.
2. Else if s's own offer is pending: no-op.
3. Else offer, if `ply >= 2` and not (`LastDrawOfferPly[s]` exists and
   `int64(LastDrawOfferPly[s]) >= int64(ply) - 20`). Signed arithmetic: with
   `uint32`, `ply - 20` underflows for `ply < 20` and would allow re-offers. Otherwise a no-op with no error frame
   and a `STATE` push (the UI should not have offered).

- `DECLINE_DRAW` from the responder (decline) or the offerer (withdraw): append
  `DrawDeclined{seat}`; push `DRAW_DECLINED` to the other seat. With nothing
  pending: no-op plus `STATE` push.
- A move by the responder declines (in `evolve`, §6.2). A move by the offerer
  leaves the offer standing. This replaces GS-3b's "offer withdrawn by offerer's
  move".
- **[dev]** The offerer may withdraw with `DECLINE_DRAW`; lichess's `no` does
  nothing for the offerer.
- `LastDrawOfferPly[s]` is set by `evolve` on `DrawOffered{s}`.
- **[dev]** Lichess's `yes` also ends the game on a threefold-repetition claim.
  There is no claim path here; only the engine's automatic repetition draw exists.
  A claim path is out of scope.

### 6.4 Client state rules (both flows)

New reducer state in `clients/web/src/game/reducer.ts` (today it has only an
incoming `drawOffer`): `myDrawOffered: bool`, `incomingTakeback: {by, atPly} |
null`, `myTakebackSent: {atPly} | null`. Transitions:
- `MOVE_ACCEPTED` for an OPPONENT's move (first time for that ply): clear
  `myDrawOffered` and `myTakebackSent` (the opponent declined by moving).
- `MOVE_ACCEPTED` for my own move: clear the incoming `drawOffer` and
  `incomingTakeback` (I declined by moving). A duplicate `MOVE_ACCEPTED` of a ply
  already applied (a re-ack, sender-only per §4) is a no-op and clears nothing.
- `STATE`: replace all four from the §7.4 fields (`myDrawOffered` =
  `pending_draw_by == mySeat`; `drawOffer` = `pending_draw_by == opponent`; same
  for takeback). A `STATE` with a LOWER ply than the client's (after a rewind) is
  valid: drop any optimistic `pendingMove`, and `needsResync`/`pendingMove.prevPly`
  logic must accept the backwards ply.
- `TAKEBACK_OFFERED`: set `incomingTakeback`. `TAKEBACK_DECLINED` /
  `DRAW_DECLINED`: clear the matching sent flag.
- `GAME_END`: clear all four.
Reducer tests cover each transition.

## 7. Protocol amendment (v1.3, ADR-19)

### 7.0 Governance

The canonical event set is "exactly MASTER §VIII.3" (`MASTER-PLAN.md`, and
`event.rs` says "no additions"), and §7.4 mutates frozen frames. This slice
therefore needs **ADR-19** plus a MASTER §VIII.3/§VIII.4 amendment. Both are
recorded in `docs/MASTER-PLAN.md` (ADR-19 DECIDED 2026-11-09). The exact contract
text to fold into `PROTOCOL.md` is [PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md](PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md). Required reviewers per
PROTOCOL §4.2: S2, S4 (S5 for P2 clients), PE, S6 (fair-play veto: takeback) and
the architecture group.

### 7.1 Events (additive)

`0x0D TakebackProposed`, `0x0E TakebackRewound`, `0x0F TakebackDeclined`,
`0x10 DrawDeclined`, `0x11 ClockStarted`. Appended after `RatingApplied` in the Rust `GameEvent` enum,
because the cgo seam uses serde variant indices (`event_seam.go` positional
constants). `0x08`/`0x09` stay decodable and are never written.

### 7.2 Client messages

| Id | Message | Payload |
|---|---|---|
| `0x07` | TAKEBACK (now request only) | `game_id` (unchanged) |
| `0x0B` | TAKEBACK_ACCEPT | `game_id, at_ply: u32` |
| `0x0C` | TAKEBACK_DECLINE | `game_id, at_ply: u32` |
| `0x0D` | DECLINE_DRAW | `game_id` |

### 7.3 Server messages

Inserted BEFORE `Error` in the Rust and Go message enums (`message_seam.go`
depends on that order):

| Id | Message | Payload |
|---|---|---|
| `0x88` | TAKEBACK_OFFERED | `game_id, seat_idx: u8, at_ply: u32` |
| `0x89` | TAKEBACK_DECLINED | `game_id, seat_idx: u8` |
| `0x8A` | DRAW_DECLINED | `game_id, seat_idx: u8` |

No new error codes; stale actions are silent no-ops with a `STATE` push (§5.2.6,
§6.3), so the frozen §4A.7 error table is unchanged.

### 7.4 STATE body v1.3 (owner decision Q-V2: all of #101 in this amendment)

`StateBody` (`protocol/codec/src/types.rs`, today `fen, turn, clocks`) gains these
fields, appended in this order after `clocks`:

| Field | Type | Value |
|---|---|---|
| `you_seat` | `u8` | the RECIPIENT's seat; `0xFF` = no seat (reserved: transport has no spectator path today) |
| `ply` | `u32` | server ply (`State.Ply`). The next MOVE's `ply_expected` is `ply + 1`; clients stop deriving it from the FEN (`plyFromFen`) |
| `status` | `u8` | new frozen table §4A.10 (below) |
| `result_vector` | `[u8; seat_count]` | §4A result codes when `status == ENDED`; empty otherwise. For termination `ABORT` it is all `DRAW`, as GS-3b writes it |
| `termination` | `u8` | §4A.5 code when `ENDED`; `0x00` otherwise (`0x00` is unused in §4A.5) |
| `last_move_uci` | `str` (≤ 5) | `History[Ply].uci`; empty iff `ply == 0` (including after a rewind to 0) |
| `pending_draw_by` | `u8` | seat with a pending draw offer; `0xFF` none |
| `takeback_by` | `u8` | seat with a pending takeback request; `0xFF` none |
| `takeback_at_ply` | `u32` | that request's `at_ply`; `0` iff `takeback_by == 0xFF` |
| `running_seat` | `u8` | seat whose clock is running; `0xFF` if none (not started, or ended) |
| `server_t` | `u64` | server epoch ms at which `clocks` are valid |

**Clock semantics (fixes #123, enables #112).** `clocks` are charged UP TO
`server_t`: the running seat's value is `clockAfter - (server_t - LastChangeTS)`,
floored at 0. Today the side to move's clock is stale by its whole current think.
A client ticks `running_seat` down from `server_t`, mapped to its own clock with
the `PING`/`PONG` offset it already has (§4A.2). `MOVE_ACCEPTED` is unchanged.
Displaying a ticking clock is the web work in #112.

**§4A.10 `status` enum (new, frozen with v1.3):** `0x01 MATCHED` (`PhaseMatched`:
not every seat has joined), `0x02 LIVE`, `0x03 ENDED` (any termination, abort
included). `0x00` and other values are rejected on decode, as `is_known_color`
and `is_known_result` reject theirs. `PhaseSeeking`/`PhaseNone` never produce a
STATE.

**Decode-time invariants**, enforced identically in Rust, the Go seams and wasm
(a frame that breaks one is malformed):
- `result_vector` is non-empty iff `status == ENDED`, and then its length equals `clocks.len()`;
- `termination != 0x00` iff `status == ENDED`;
- `you_seat`, `pending_draw_by`, `takeback_by`, `running_seat` are each `< clocks.len()` or `0xFF`;
- `takeback_at_ply == 0` iff `takeback_by == 0xFF`;
- `last_move_uci` is at most 5 bytes, and empty iff `ply == 0`;
- `running_seat == 0xFF` when `status == ENDED`.

Sentinels, not optionals: the hand-rolled Go postcard seams have no `Option`
support (the web client goes through the wasm serde seam, `seam.ts`); `0xFF` as
"no seat" already exists (`Aborted.by`). Not added: `rating_delta` (stays on
`GAME_END`; no rating service yet) and **opponent identity**, deferred because it
needs the handle that `MATCHED.opponent` does not carry today (#131); a reloaded
client shows no opponent name until then.

**Per-recipient construction.** `you_seat` differs per connection. One builder,
`stateFor(snapshot, recipientSeat, now)`, is used by every push site:
- MATCHED loop (`transport/handlers.go`, seat known);
- `handleResync` (seat from `seatOf`);
- `broadcastState` (today builds one message for both seats with `seat < 2`
  hard-coded; must build one per seat);
- `resyncUser` on AUTHENTICATE (`transport/dispatch.go`, seat known);
- the new stale-action pushes (§5.2.6, §6.3), which reuse the same builder.

**MATCHED keeps the shared body** (Q-P4, one state format). `start_state` is built
by the same `stateFor` from the snapshot, never hard-coded: in today's flow the
opening batch joins both seats, so it carries `status = LIVE`, `ply = 0`, empty
result and last move, no pending offers, and `you_seat == MATCHED.seat_idx`.

**This is NOT additive.** `de_exact` rejects trailing bytes, so both `0x81` and
`0x84` change for every decoder and the golden vectors regenerate (the ADR-18
situation). ADR-19 must say so.

**Web reducer (`clients/web/src/game/reducer.ts`).** On MATCHED take `ply` from
`start_state.ply` (not `plyFromFen`). On STATE: set `mySeat` from `you_seat`
(`0xFF` → null), `ply` from `ply`, `phase = game_over` when `status == ENDED`
(with result and termination), `lastMove` from `last_move_uci`, the pending-offer
state of §6.4, and the clock anchor. The inline TS shape types and
`codec/messages.ts` change with it.

**What it fixes.** A reconnecting or reloaded client learns its seat, side to
move, ply, live-or-over, result, last move, pending prompts and a correct running
clock from `STATE` alone (#101, #123). Not fixed here: the web client keeps its
token in memory, so a reload still needs re-authentication (#111); and once
`GAME_END` drops the session no `STATE` can be served at all (#102), so an ENDED
`STATE` is reachable today only for a session that has not been dropped.

### 7.5 Ripple (all change together; parity must pass before merge)

Rust codec (`protocol/codec`), wasm binding, FFI (iOS/Android), Go cgo seams
(`event_seam.go`, `message_seam.go`, both hand-rolled postcard readers), the Go
`Snapshot` struct (gains result vector, last move, pending fields) and a
per-recipient `snapshotToStateBody`, every STATE push site in `transport`, `NewSession` (`ClockStarted` append on first build), the per-recipient `stateFor`
builder and all five STATE push sites (§7.4), `PROTOCOL.md` (§4A.3 STATE line,
§4A.6 "exactly fen, turn, clocks", the Q-P4 text, new §4A.10), the `types.rs`
`StateBody` doc comment, `message_seam.go` (the STATE body writer and reader),
the TS mirror
(`clients/web/src/codec/messages.ts`), the web reducer, `protocol/vectors/`
(including `vectors.json`), and the corpus and property tests.

### 7.6 Version compatibility (owner decision Q-V1: ignore unknown server types)

There is no minor-version negotiation. **Rule (v1.3, frozen):** a client that
receives a server-to-client frame it cannot decode (an unknown type `>= 0x80`, or
any other decode failure) MUST drop it, log it, and keep the connection; it MUST
NOT treat it as fatal or reconnect. The only fatal server frame stays
`ERROR{UNSUPPORTED_VERSION}`. The web client already does this
(`GameSocket.ts` catches every decode failure, logs and continues; only
`UNSUPPORTED_VERSION` is fatal), so the rule records today's behaviour; iOS and
Android (P2) must match. The wasm layer reports every codec error as a message
string, so the rule is deliberately "any undecodable frame", which needs no error
discriminator. A frame with type `< 0x80` at a client is wrong-direction and is
dropped the same way. The server keeps rejecting unknown or wrong-direction
CLIENT frames with `PROTOCOL_VIOLATION` (unchanged).

Scope: the rule makes NEW server message types safe for N-1 clients (MASTER Part
XIII). It does not make changes to EXISTING frames safe, nor new values in frozen
enums (those decode as malformed, and the frame is dropped). In particular an old
client facing the v1.3 `STATE`/`MATCHED` silently drops them and stays stuck
with no `UNSUPPORTED_VERSION`. So v1.3 server and web client ship together;
pre-launch, web is the only client.

## 8. Field-lifetime audit

| Value | Written | Read | Window and outcome if stale |
|---|---|---|---|
| Takeback `at_ply` | at proposal (fold ply) | at accept/decline | Survives the proposer's own move only. Cleared by the responder's move, decline, cancel, rewind, game end. A mismatched or absent `at_ply` on accept/decline is a no-op plus STATE push. |
| `to_ply` | at accept | every reader | Stored in the event; `evolve` rejects it if it differs from the formula. |
| Clocks after rewind | at accept | after | From stored `clockAfterMs` (§5.4), never wall time. Flag is checked before the accept is applied. |
| Idle baseline | each move or rewind | flag ticker, next move, rebuild | `LastChangeTS` from event bodies (§3.1); rebuild reads the same value, and an undone move's `ServerTS` is no longer used because the later `TakebackRewound.server_ts` supersedes it. First-clock-start: #124. |
| Pending draw offer | on offer | on accept | Cleared by the responder's move or decline, rewind, game end. In `STATE` (§7.4). |
| Draw cooldown | on offer | on next offer | Per seat; pruned on rewind; rebuilt by fold. |
| Pending takeback across reconnect | on proposal | after reconnect | In `STATE` (§7.4). |
| Seat, ply, status, result, last move across reconnect or reload | on match / each event | after reconnect | In `STATE` (§7.4), built per recipient; `last_move_uci` follows the rewind. |
| Pending request across restart | on proposal | after rebuild | Rebuilt by `evolve`. Live sessions only survive restart once #98 lands. |
| Concurrent actions (accept vs resign, accept vs move, two accepts) | — | — | Serialized by the session actor; each re-decided against current state; the loser becomes a no-op or a rejection per §4, §5.2.6. Cross-node: `expectedSeq` conflict, reload, re-decide (§3.4.5). |
| Proposer disconnects | on proposal | by opponent | Request stands; opponent may accept (lichess behaviour). |

## 9. Tests (failing first)

1. `TestRepro84_MoveAfterTakeback` and `TestRepro85_MutualDrawStreamFolds`
   (written, failing on `main`) become regression tests.
2. **Invariant harness (built first).** Random command sequences (move, resend,
   takeback propose/accept/decline/cancel, draw offer/accept/decline, resign,
   flag via injected clock, simulated restart, simulated seq conflict). After each
   step assert: live state == `FoldWithEngine(store)` == `NewSession(store)`,
   compared on every field in §3.9 (including after a flag); and `Fold(store)`
   succeeds. Uses an injected clock for `now`, so `LastChangeTS` equality is exact.
3. The §5.4 clock table, both `k` values and the 3-ply case.
4. Draw: offerer moves and offer survives; responder moves and the offer is
   cleared with no extra event; accept allowed inside the requester's cooldown; re-offer
   at `ply - 20` rejected and at `ply - 21` allowed; offer at ply < 2 ignored;
   explicit decline and withdraw; re-offer at ply < 20 inside the cooldown
   rejected (unsigned-underflow regression); exact boundary
   `LastDrawOfferPly == ply - 20` rejected and `== ply - 21` allowed at plies 20 and
   21; client reducer rules of §6.4. `ClockStarted`: first build appends it; a
   second build does not; concurrent builds append exactly one; the baseline
   survives a rebuild. Transport tests inject ONE shared fake clock into both
   `Matchmaker.now` and `Registry.now`.
5. MOVE: re-ack after takeback with same UCI; different UCI at a recurring ply
   gives `STALE_PLY`; resent mating move after end is re-acked.
6. Takeback: propose at ply < 2 is a no-op; `TAKEBACK` from the responder while a
   request is pending is a no-op; repeated accept after a lost ack is a
   STATE push; accept by a flagged side ends by flag; second takeback after a first.
7. `STATE` v1.3 (#101 acceptance): a transport test where seat A plays a move,
   drops the socket and reconnects on a fresh connection, then derives seat, turn,
   ply and status from the decoded `STATE` alone; the same for an ended game
   (status, result, termination) built at session level through `stateFor`,
   since transport cannot serve it after `GAME_END` until #102; the running
   seat's clock is charged to `server_t` (#123) and `running_seat` is correct
   before and after a move and a rewind; every §7.4 decode invariant has a
   rejection test in Rust, Go and wasm; a takeback broadcast gives each seat its own
   `you_seat` and the rewound `last_move_uci`; golden vectors `server/state` and
   `server/matched` regenerated and only those (plus the new messages and events)
   change; a web reducer test where `STATE` arrives with no prior `MATCHED` and
   the game is playable; a web socket test where an unknown server type is dropped
   and the connection stays up (Q-V1).
8. A stream containing `TakebackReq`/`TakebackAccepted` fails with
   `ErrLegacyTakeback` (not a panic, not a silent fold).
9. `-race` under the CI service gate.

**Existing tests that change** (update in the implementing PR):
`TestDrawOfferAccept`, `TestAcceptOwnDrawOfferRejected`,
`TestDrawOfferWithdrawnByOffererMove` (behaviour reversed),
`TestDrawOfferDeclinedByOpponentMove`, `TestMutualDrawOfferPairsInStream`
(shape reversed), `TestRepeatOfferSameSeatIsNoop`; transport
`TestDrawOfferAndAccept`, `TestOfferDrawPushesDrawOfferedToOpponent`,
`TestRepeatOfferDrawNoDuplicateNotify` (all offered at ply 0; must play 2 plies
first); `TestTakebackUnratedRollsBack`, `TestDoubleTakeback`,
`TestTakebackPushesStateToBoth` (old `Takeback(ctx, by, plies)` API);
`TestRefoldTakeback`, `TestRefoldTakebackErrors` (deleted with the function);
`game/statemachine_test.go` takeback and draw cases; `game/pipeline_test.go`,
`game/pipeline_seqconflict_test.go` (the `findMove` reconcile path),
`engine/pipeline_integration_test.go`, `session/session_test.go`, and the
`FoldWithEngine` tests; `transport/flow_test.go` `TestReAckEchoesPersistedUCI`
(behaviour reversed: a different UCI at a persisted ply now gets `STALE_PLY`);
`session_test.go` `TestIdempotentReackDoesNotCorrupt`; `game/pipeline_test.go`
`TestPipelineIdempotentRetry`; `endgame_test.go` `TestTakebackVoidsPendingDrawOffer`;
`session_test.go` `seedLive` helper, `TestFlagOnTimeout`,
`TestFlagUsesAdvancedClock` and every test whose expected seq or event count
now includes the appended `ClockStarted` (matchmaking `create_test.go` is NOT
affected: the opening batch stays 3 events).

## 10. Issue coverage

Fixed: #84, #85, #94, #67, #89, #122, #107, #101 (protocol half), #123. Made tractable,
not closed: #95, #88, #97, #98, #99 (harness replaces vacuous tests), #100, #111
(still needs token persistence), #102 (still needs result delivery after the
session is dropped). Not touched: #124.

## 11. Open questions

| Id | Question | Status |
|---|---|---|
| Q-TB1 | Takeback preference and decline cooldown | Deferred past the closed alpha. |
| Q-TB2 | Does a request survive later moves | Resolved: responder's move declines (§5.2.4). |
| Q-TB3 | Takeback in rated games | Out of scope (F-01.4). |
| Q-TB4 | Clock rule | Resolved by source reading, confirmed by the independent reviewer (§5.4). |
| Q-DR1 | Draw decline and cooldown | Resolved (§6.3). Owner may veto. |
| Q-DR2 | Which move declines an offer | Resolved: responder's only (§6.3). |
| Q-V1 | Clients ignore unknown server types (§7.6) | **Decided by owner, 2026-11-09: yes.** Rule text approved in the 2026-11-09 team review (PE included). |
| Q-V2 | Ship the rest of #101 in the same v1.3 amendment (§7.4) | **Decided by owner, 2026-11-09: yes.** Field set taken from #101's acceptance criteria. |
| Q-V3 | Pre-GS-7 streams unsupported (§5.1) | **Confirmed by owner, 2026-11-09:** no production data exists. |

## 12. Definition of Done

- [x] This spec re-reviewed (context-free) with no BLOCKER, then RATIFIED (2026-11-09).
- [x] ADR-19 + MASTER §VIII.3/§VIII.4 amendment + protocol v1.3 reviewed by S2,
      S4, PE, S6 and the architecture group (2026-11-09, owner attestation).
- [x] Those documents merged to `main` before any implementation PR (#155, #156).
- [x] Invariant harness merged and failing on `main` before the fix (#158).
- [x] Refactor merged; repro tests pass; `-race` clean (#163: the harness passes with nothing excused).
- [x] All codec consumers (§7.5) pass parity, including the v1.3 `STATE`/`MATCHED` (#161).
- [x] #101 acceptance criteria met (§9 test 7): server in #163 (`TestReconnectDerivesEverythingFromState`), web reducer in the GS-7 web PR (a STATE with no prior MATCHED gives a playable game).
- [x] GS-3b Q-EG2, Q-EG5, §3.3, §6 amended (#163); `PROJECT-STATUS.md` updated (GS-7 web PR).
- [x] Independent review of the FIX round before merge (#163: APPROVE-WITH-CHANGES, addressed).

## 13. Review findings addressed (v0.1 → v0.2)

| Finding | Resolution |
|---|---|
| B1 evolve not engine-free | §3.2 one `evolve` with optional verdict; §3.3 decide makes one engine call; §3.5a pipeline stops refolding |
| B2 schema_ver bump unimplementable | §5.1, §7.1 new event types; old bodies untouched; waiver dropped |
| S1 clock rule wrong in 3-ply case | §5.4 corrected rule and worked table |
| S2 TAKEBACK toggle; MOVE idempotency | §5.2, §7.2 separate ACCEPT/DECLINE with `at_ply`; §4 new idempotency rule |
| S3 second `findMove` and other scanners | §2, §3.6 |
| S4 idle baseline unreadable | v0.2 used header TS (wrong, see Round 2 B-1); now body timestamps; flag check before accept §3.5 |
| S5 draw rules | §6.2, §6.3; deviation marked §5.5; Q-EG2 reversal noted; claim path removed |
| S6 tests under-listed | §9 |
| S7 protocol gaps | §7.1–§7.6 ids, enum order, no new error code, ripple, versioning, STATE |
| S8 appendBatchAtHead blind re-sequence | §3.4 `expectedSeq = NextSeq`, re-decide on conflict |
| N1 nits | §1 seat count; §5.2.6, §6.3 empty decline; §5.5 abort window; §3.8 consumers; §5.3 dead `max`; §5.1 `to_ply` validated; §9 harness fields |
| **Round 2** B-1 header TS baseline | §3.1, §3.2 body timestamps only; `TakebackRewound.server_ts`; §3.9 audit |
| R2 N1 LastChangeTS initial writer | §3.1 `0` = none; §3.5 no idle charge, fallback kept, #124 |
| R2 N2 STATE not additive, no Option | §7.4 sentinels, stated as frame mutation, ADR-19 |
| R2 N3 MASTER event set, architecture review | §7.0, §12 |
| R2 N4 stale duplicate re-applies undone move | §4 no-retransmission rule (web already complies); full row table; re-ack transport |
| R2 N5 new strictness breaks old draw streams | §5.1 pre-GS-7 streams unsupported (Q-V3); §6.2 responder move derived in evolve |
| R2 N6 legacy takeback rule contradicts | §5.1 legacy rule removed; legacy events rejected |
| R2 N7 test list | §9 |
| R2 N8 single Append, skew, ticker, fields | §3.4, §3.5, §3.6 |
| R2 N9 unmarked deviations | §6.3 [dev] markers |
| **Round 3** F1 false claim about old draw streams | §5.1 corrected; §6.2 same-seat repeat idempotent |
| R3 F2 first-move baseline outside the stream | §3.5 `ClockStarted` event keeps today's rule durable; #124 still decides the rule |
| R3 F3 Clock after flag | §3.9 `ClockFlagged` writes Clock and FlaggedSeat |
| R3 F4 missing fields; ms vs monotonic | §3.9 rows added; §3.5 integer ms wall clock, NTP consequence |
| R3 F5 client clearing for derived events | §6.4 reducer rules |
| R3 F6 no-retransmission unenforceable | §4 ADR-19 + contract tests + re-ack metric; residual risk stated |
| R3 F7 tests | §9 |
| **Round 4** S1 ClockStarted moved the start to match time | §3.5 appended by the first `NewSession` instead; today's rule truly kept; matchmaking unchanged; open start-rule questions left to #124 |
| R4 S2 zero sentinel collides with fake-epoch ServerTS | §3.1 explicit `ClockStartedSet`; §9 affected tests listed |
| R4 S3 reducer state and missing transitions | §6.4 fields enumerated; re-ack, backwards STATE, GAME_END |
| R4 nits | §4 seat-range row and stated behaviour changes; §9 exact cooldown boundary and shared fake clock |
| **Owner decisions** Q-V1, Q-V2, Q-V3 (2026-11-09) | §7.6 unknown-type rule; §7.4 full #101 STATE body; §5.1 no production data |
| **Round 5** (focused, v0.6) F1 no clock anchor | §7.4 `running_seat`, `server_t`, clocks charged to `server_t` (#123) |
| R5 F2 status contradiction | §4A.10 table; `start_state` built from the snapshot |
| R5 F3 invariants, ABORT result | §7.4 decode-time invariants; ABORT = all DRAW |
| R5 F4 ended STATE unreachable | §7.4 and §9 test 7 scoped to session level, #102 dependency stated |
| R5 F5 reducer, opponent | §7.4 reducer changes; opponent deferred to #131 |
| R5 F6 push sites | §7.4 one `stateFor`, five sites, `broadcastState` per seat |
| R5 F7 unknown vs malformed | §7.6 "any undecodable frame", matches current web behaviour; stuck-old-client consequence |
| R5 F8 documents | §7.5 |
| **PR review** (#156): blocker fix bundled with v1.3 scope | Owner kept the bundled plan, 2026-11-09. Reason: no production games exist, so the blockers do no harm before the plan ships. Accepted cost: the #84/#85 fix is complete only after the v1.3 codec lands in every consumer, and takeback stays unconsented (#107) until then. Mitigation: implementation still lands as separate PRs in the §12 order, each independently reviewed |
| **PR review** (#156): v1.1/v1.2 drift rows mixed into the ADR-19 hunk | Moved to PR #155; this PR is stacked on it and carries only ADR-19 rows |
| R3 nits | §6.3 signed arithmetic; §5.2 [dev] responder TAKEBACK no-op and error mapping; §3.2 evolve 2-seat guard; §3.1 PlyEntry seq; §4 NOT_YOUR_TURN and GAME_OVER rows; §3.1 HalfTerminal flag-only; §7.4 seam claim and at_ply 0; §5.4 increment-farming note |

## 14. Sign-off

Recorded on the owner's attestation of a team review held 2026-11-09 outside
GitHub. Attributed by stream name, as the owner has chosen for earlier
ratifications; no individual reviewer names are recorded.

| Role | Name | Date | Verdict |
|---|---|---|---|
| Owner | Ashish Kumar | 2026-11-09 | Approved (design direction, Q-V1, Q-V2, Q-V3, bundled plan, v1.0 text) |
| S2 Real-time | S2 stream | 2026-11-09 | Approved |
| S4 Web | S4 stream | 2026-11-09 | Approved |
| Platform-Eng (protocol) | PE stream | 2026-11-09 | Approved |
| S6 Trust & Safety (fair-play veto) | S6 stream | 2026-11-09 | Approved |
| Architecture | Architecture group | 2026-11-09 | Approved |
| Independent context-free review | | 2026-11-09 | v0.1: BLOCK. v0.2: BLOCK. v0.3: RATIFY-WITH-CHANGES (applied in v0.4). v0.4: RATIFY-WITH-CHANGES (applied in v0.5). v0.6 focused: RATIFY-WITH-CHANGES (applied in v0.7) |
