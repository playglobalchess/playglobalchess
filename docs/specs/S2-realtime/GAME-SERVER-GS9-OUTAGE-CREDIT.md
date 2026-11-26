# Game-server GS-9 — clock credit and abort after an outage (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.2 (answers the first independent review, section 12) |
| Last updated | 2026-11-27 |
| Fixes (issues) | #207; also supplies the restart-downtime rule GS-8 stage 1 needs (GS-8 Q1) |
| Depends on | GS-4 (event store), GS-6 (ticker, #97 retry), GS-7 (fold, `LastChangeTS`, first-move rule, #183 receipt time) |
| Owner decisions | 2026-11-27: exact credit, T = 2 min, no bonus time, additive wire change (section 9), heartbeat in its own table (section 3) |

> **Why this exists.** While the store is down no move can be saved, but clocks
> keep running. When the store returns, the flag ticker flags every game whose
> clock ran out meanwhile, so players lose on time for a fault that was ours.
> A node restart does the same. The owner decided on 2026-11-26: **credit time
> for short outages and abort games after long ones**.

## 1. Scope

**In scope**
- Detecting an outage, a node pause or a restart, with a start and an end.
- A durable event that gives the time back, so a rebuilt session has the same
  clocks (GS-7 invariant I3, #203).
- Aborting every affected live game when the outage is longer than T.
- Not flagging anyone while a credit may still be owed.
- Telling clients.

**Out of scope**
- Moving a game to another node (GS-8 stage 2 reuses the event, section 8).
- Lichess-style fixed bonus time. The credit is exactly the lost time.
- Rating (NFR-4): an outage abort is unrated, like any abort.

## 2. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-4 | An outage abort ends with no result and no rating change (section 6). |
| NFR-7 | The credit is a stored event written before any flag decision. |
| NFR-9 | The fold reads only stored events and real timestamps (section 4). |
| GS-7 I3 | Live and rebuilt clocks agree because the credit is stored. |
| #95 | One failed or ambiguous write never opens a window (section 3). |

## 3. Detecting a window

A game's own failures cannot tell an outage from a bad moment, and the flag
timer is deadline-driven (#182), so detection is node-wide. A window is
`{start_ts, end_ts, reason}` in wall-clock milliseconds. Its length is measured on
the monotonic clock, so an NTP step cannot change it.

**Heartbeat.** Each node writes one item every 2 s (timeout 1 s) to a small
table `node_health`, keyed by `node_id`: `{incarnation, ok_ms, windows}`. It is
a separate table so that throttling of the event table cannot starve it and
the event table's change feed carries no heartbeat noise. The put is
conditional on `incarnation <= mine`, so a zombie older process cannot
overwrite a newer one's item and learns it is superseded. One item per node, no
growth. This is inside ADR-16's amended scope (index and hint tables only, no
game state).

**Three signals open a window.**
1. **Store failure.** Two consecutive failed heartbeats, or three consecutive
   store errors (not seq conflicts) on game writes with no success between. One
   failed or ambiguous write opens nothing (#95). `start_ts` is the last
   successful heartbeat or game write, so it is backdated by at most 2 s plus the
   timeout. `end_ts` is the first success of both a heartbeat and a game write.
2. **Pause.** A gap between two successful heartbeats of more than 4 s (a GC
   stop, VM freeze or sleep produces no failed write) is the window
   `[previous ok, now]`, reason 3.
3. **Restart.** At start the node reads its own previous item with a consistent
   read. The window is `[previous ok_ms, build time of each game]`, reason 2.
   The end is per game, because a game is rebuilt after the node starts. If the
   read fails, the node does not serve and retries; it treats "no item" as a
   first start only when a consistent read confirms the item is absent.

**Durable windows.** A closed window is written into the item's `windows` list
(at most 16, kept 1 hour) before any game credits it. A node that crashes
between a window closing and the credits being written therefore loses nothing:
the next incarnation reads the list and applies it. Applying is idempotent
(section 4), so reading a window twice is harmless.

**Limits, stated.** An outage shorter than one missed heartbeat (about 3 s) is
not credited, and the cliff is deliberate. Windows are not merged: each is
applied on its own, and overlap is removed by the watermark.

**False windows.** A heartbeat that fails while game writes succeed (a
permissions fault, a stuck goroutine) opens a window of at most T and never an
abort: a window longer than T aborts games only if a real game write or read
also failed inside it. Otherwise the node alarms and credits at most T.

## 4. The credit: real timestamps and a watermark

v0.1 shifted `LastChangeTS` by the credit. That made it a non-real timestamp,
so comparing a window with it was wrong, and a rebuilt session could credit an
older window a second time. v0.2 keeps `LastChangeTS` real.

**State added to the fold (GS-7).**
- `CreditedMs`: time credited since the last `LastChangeTS` write. Reset to 0
  every time `LastChangeTS` is written (a move, a takeback rewind, the start).
- `CreditedThrough`: a watermark in wall-clock milliseconds. It never goes down
  and is not reset.

**Elapsed time.** `elapsed = max(0, now - LastChangeTS - CreditedMs)`. Flag
decisions, the first-move timeout and `NextTickMs` (#182) all use it.

**Event.** `ClockCredited{start_ts: u64, end_ts: u64, reason: u8}`, code `0x12`.
It carries real times only. There is no `credit_ms` and no `server_ts`, so
re-deciding the same window gives byte-identical events (needed for the #95
ambiguous-commit comparison).

**Fold rule.** With `s = max(start_ts, LastChangeTS, CreditedThrough)`:
`CreditedMs += max(0, end_ts - s)` and
`CreditedThrough = max(CreditedThrough, end_ts)`. The fold rejects an event with
`end_ts <= start_ts`, and rejects `ClockCredited` unless the game is live with
its clock started. Because of the watermark, an overlapping or repeated window
adds nothing the second time, a rebuild cannot double credit, and two nodes
that saw the same outage credit it once. A credit is never negative.

**Who is credited.** Only the clock that is running: the side to move after
ply 2, or the first-move timeout before it. The opponent's clock is not
running, so it gets nothing.

**A queued move (#183).** A MOVE is charged only up to its receipt time `r`.
Before the move is decided, an unapplied window is clipped to `end_ts = min(end,
r)`, so credit covers only the part of the window the mover spent waiting. A
window that ended after `r` is credited to nobody: the opponent's clock starts at
processing time, which is after the window. A move received before the window
starts gets no credit for it.

**When it is applied.** Before any other decision for that game, in the same
atomic write as the command's own events, in window order. A node tells idle
sessions the moment a window closes. Credit writes are spread over up to 1 s
with jitter and retried on the game's own backoff (at most 5 s), so a node with
many games does not throttle its own recovery. A takeback or a move in the
gap resets the baseline, so an older window is clamped away by `LastChangeTS`.

## 5. No flag while a credit may be owed

A store write can recover before the heartbeat closes the window, so a tick in
that gap would flag a player for outage time. The rule:

- **Hold.** While the node is in a window, or within **H = 5 s** after the last
  failed heartbeat or game write, or while any closed window is not yet applied
  to a game, `decideTick` does not flag that game and does not abort it for the
  first-move timeout or the join window. The deadlines of both are measured
  from `LastChangeTS` plus `CreditedMs`, so they move with the credit.
- **Order on a tick.** Abort for a long window, then credit, then flag. An abort
  on an ended game is a no-op.
- **Cost.** A flag can be late by at most H plus the credit retry. That is an
  acceptable cost against an unfair flag, and it is only paid after a fault.
- **Join window.** The join abort is held and its deadline extended by the
  overlap in the same way, so an outage during the join wait does not abort a
  game nobody could join.

## 6. The abort (window longer than T)

When a window is longer than **T = 2 minutes** (monotonic), and was corroborated
by a failed game write or read (section 3), every game that was live during it is
aborted instead of credited:

- A new command `CmdAbortOutage` is decided by the session, never by a client.
  It appends `Aborted{by: SystemActor}` and `GameEnded{termination:
  ABORT_OUTAGE}` in one atomic write. A client command can never produce
  `SystemActor` or `ABORT_OUTAGE`. The fold accepts `ABORT_OUTAGE` only
  directly after `Aborted{0xFF}` and rejects it otherwise.
- `abortBeforePly` is enforced in `CmdAbort` and stays so. `CmdAbortOutage` is
  valid at any ply.
- **Unrated.** A consumer must treat `ABORT` and `ABORT_OUTAGE` as unrated. The
  draw-shaped result vector must never count as a draw (GS-7 §3.5).
- **A slow deploy.** A restart that takes longer than T aborts every live game.
  This is the owner's chosen trade-off, and it means a deploy must finish well
  inside T.

## 7. Clients

- **Credit.** After a credit the node pushes a `STATE` to both seats (the
  existing message). WEB-F01 §3 gets one sentence saying clients replace their
  clocks from it.
- **Abort.** Both players get `GAME_END` with the new termination; the client
  shows "Game aborted: server problem, no rating change".
- **A MOVE during the outage** gets `ERROR{INTERNAL}` as today and the client
  does not resend (ADR-19 (a)). A move that committed but whose reply was lost
  (the #95 case) shows an error until the client resyncs; the client already
  resyncs on `INTERNAL` (#171), and the credited `STATE` then shows the move.
- **Think-time consumers** (fair play) read `ServerTS` differences between
  plies. They must subtract the `ClockCredited` overlap between those plies, or
  outage time will look like thinking time.

## 8. Restarts and failover (how GS-8 uses this)

- **Restart on one node (GS-8 stage 1).** A restart is signal 3 of section 3, so
  its downtime is credited exactly, or aborts the game if longer than T. GS-8
  Q1 is answered, and stage 1 may ship with rated play on **only once this spec
  is implemented and #203 has an outage phase**. Until then GS-8 §4.5 holds.
- **Rebuild timing.** A node rebuilds adopted games eagerly at start, so the gap
  between node start and a game's rebuild is small. The window's end for a game
  is its own build time, and a game built later still gets the credit.
- **Failover to another node (GS-8 stage 2).** The adopting node reads the dead
  node's `node_health` item and uses `[ok_ms, adoption time]` with reason 4
  (reserved now). The two nodes' clocks differ, so the credit is clamped at 0
  and the GS-7 skew bound (100 ms) applies. The watermark makes a window seen by
  both nodes credit once. GS-8 §6.4's `gap_ms` is replaced by this event.
- **Limits, stated.** A node that dies and is never replaced produces no window
  until someone adopts its games. A window is only as accurate as the 2 s
  heartbeat; the backdate favours the player on move by at most that much.

## 9. Wire and codec change (needs ADR-20)

PROTOCOL §4A changes only additively:

| Change | Detail |
|---|---|
| Event `0x12 ClockCredited` | `start_ts: u64, end_ts: u64, reason: u8` (1 store, 2 restart, 3 pause, 4 adoption reserved) |
| Termination `0x0B ABORT_OUTAGE` | a new value in the frozen u8 table (0x01-0x0A used) |
| `Aborted.by` | unchanged; the outage abort uses `SystemActor` (`0xFF`) |

`0x12` is reserved here and `0x13` for GS-8's `OwnerClaimed`. Process per
ADR-19: a protocol version bump, review by S2, S4 and S5 and the architecture
group, golden vectors, and decoders in Rust, Go, TypeScript, Swift and Kotlin.

**Fail closed, not skip.** #187 must classify events as skippable or
safety-relevant. `ClockCredited` and `ABORT_OUTAGE` are safety-relevant: a node
that does not know them must refuse to fold the game, not skip the event,
because skipping a credit would flag the player. All nodes must be able to read
the event before any node writes it, so the rollout has two steps (readers,
then writers), and rolling back below the reader version is a one-way door.

## 10. Tests (the #203 harness gets an outage phase)

- **Credit.** An outage of D at most T gives no flag for time lost; the mover's
  clock after recovery equals the clock before it; a rebuilt session agrees.
- **Watermark.** Two windows in order, then a rebuild that sees the older one;
  overlapping windows; a window extended after it was applied; a crash between
  window close and credit. Each credits exactly once.
- **Flag hold.** A tick in the 2 s gap after the store recovers, for a game with
  a nearly spent clock, does not flag. The same with a window longer than T.
- **Queued move.** A move received inside the window, one received before it,
  and one received after it. Mutation check: charging up to receipt after a
  shift.
- **Detection.** Heartbeat healthy while moves fail; heartbeat failing while
  moves succeed (no abort); a transaction path throttled; a process pause with
  no failed write; a forward and a backward clock step across a window.
- **States.** A credit for a game that is matched, not yet clock-started, ended;
  a takeback before and after a credit.
- **Abort.** Every live game ends `ABORT_OUTAGE`, unrated, both players told,
  including a game past `abortBeforePly`; a client cannot produce it; abort on
  an ended game is a no-op.
- **Restart.** A previous heartbeat gives the right window; first start gives
  none; store down at start does not serve; a zombie cannot overwrite the item;
  a crash loop; lazy rebuild after start.
- **Ambiguous commit** on the credit-plus-move batch with a rebuild between.
- **Herd.** N games credited at once while the heartbeat keeps succeeding.
- **Codec.** Golden vectors for `0x12` and `0x0B` and parity in every decoder.
  Mixed version: an old node fails closed on both.
- **Mutations.** Credit ignored, watermark not applied, flag judged before
  credit, window start not clamped, hold removed.

## 11. Decisions

Decided by the owner on 2026-11-27, on the v0.1 recommendations:
- **T = 2 minutes.** Chess.com aborts on any restart; Lichess never aborts but
  adds 10 to 20 s.
- **Exact credit, no bonus.** A bonus would give one side time the other never
  gets.
- **The wire change** in section 9 (ADR-20).
- **Heartbeat store.** v0.1 proposed an item in the event table. The review
  showed that this shares the event table's failure modes and change feed, so
  v0.2 uses a separate `node_health` table, inside the ADR-16 index-table scope.
  This is a change after the owner's approval and is flagged for confirmation.

## 12. How the first review's findings are answered

| Finding | Answer |
|---|---|
| B1 shifted baseline, double credit | Real `LastChangeTS`, `CreditedMs`, `CreditedThrough` (section 4) |
| B2 merge vs dedup | No merging; watermark (sections 3, 4) |
| B3 flag race at recovery | Hold H and order abort, credit, flag (section 5) |
| B4 queued move | Window clipped to receipt time (section 4) |
| B5 detection | Three signals, corroborated abort, separate table, jitter (sections 3, 4) |
| B6 restart and adoption | Per-game window end, durable windows, consistent read, conditional put (sections 3, 8) |
| Should-fix 1-9 | Fail closed (9), `CmdAbortOutage` (6), ordering (5), idempotent event (4), states (4), codes (9), think time (7), client resync (7), heartbeat cost (3) |

## 13. Specs that change when this is accepted

GS-6 §3.5 and Q-GS6f (decided); GS-7 §3.5 (`CreditedMs`, `CreditedThrough`, the
abort exception); GS-8 (Q1 answered; §6.4 `gap_ms` replaced; code `0x13`);
PROTOCOL §4A and WEB-F01 (sections 9 and 7); MASTER-PLAN (ADR-20; ADR-16
`node_health` table); #187 (event classes).
