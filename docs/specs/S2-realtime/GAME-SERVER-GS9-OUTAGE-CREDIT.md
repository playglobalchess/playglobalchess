# Game-server GS-9 — clock credit and abort after an outage (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.1 |
| Last updated | 2026-11-27 |
| Fixes (issues) | #207; also supplies the restart-downtime rule GS-8 stage 1 needs (GS-8 Q1) |
| Depends on | GS-4 (event store), GS-6 (ticker, #97 retry), GS-7 (fold, `LastChangeTS`, first-move rule) |
| Needs from the owner | the abort threshold, the wire change (section 8) |

> **Why this exists.** While the store is down no move can be saved, but clocks
> keep running. When the store returns, the flag ticker flags every game whose
> clock ran out meanwhile, so players lose on time for a fault that was ours.
> The same happens when a node restarts: the downtime is charged to the player on
> move. The owner decided on 2026-11-26: **credit time for short outages and
> abort games after long ones** (Lichess adds time after an interruption;
> Chess.com aborts live games with no rating change).

## 1. Scope

**In scope**
- Detecting an outage, with a start and an end, on the node that owns the games.
- A durable event that gives the time back, so a rebuilt session has the same
  clocks (GS-7 invariant I3, #203).
- Aborting every affected live game when the outage is longer than a threshold.
- Telling clients.

**Out of scope**
- Moving a game to another node (GS-8 stage 2 reuses this event for the failover
  gap, section 7).
- Lichess-style fixed bonus time. The credit is exactly the lost time.
- Rating (NFR-4): an outage abort is unrated, the same as any abort.

## 2. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-4 rated games scored | An outage abort ends with no result and no rating change (section 5). |
| NFR-7 persist-before-ack | The credit is a stored event written before any flag decision. |
| NFR-9 replayable | The fold reads only stored events; the outage window is never needed to rebuild (section 4). |
| GS-7 I3 (rebuild equals live) | The credit moves the stored clock baseline, so live and rebuilt clocks agree. |
| #95 | One failed write, even one that committed, never opens an outage (section 3). |

## 3. Detecting an outage

A game's own failures cannot tell an outage from a bad moment, and the flag timer
is deadline-driven (#182): an idle game's first failed check would be at its flag
deadline, after the time was already lost. So detection is node-wide and
independent of game traffic.

- **Health heartbeat.** Every node writes one small item every 2 s to the event
  table, under a reserved stream id `node/{node_id}/health`, as an unconditional
  put of `{node_id, incarnation, ok_ms}` (it overwrites; it never grows). It
  exercises the same write path as a move. A node's store-health state is
  *healthy* until two consecutive heartbeats fail, then *outage*.
- **Window.** An outage window is `[start, end]` in the node's wall-clock
  milliseconds. `start` is the time of the **last successful heartbeat** before
  the first failure, so it is backdated by at most one heartbeat period (2 s), a
  bounded advantage to the player on move. `end` is the first successful
  heartbeat afterwards. A window gets an id, its `start` in milliseconds. Windows
  less than 5 s apart merge (a flapping store is one window).
- **A single failed or ambiguous write opens nothing** (#95). A write that
  committed but reported an error is handled by the session's existing re-read,
  and it cannot fail two heartbeats.
- **Downtime of the node itself.** At start, a node reads its previous
  incarnation's heartbeat item. The window `[ok_ms, now]` is the downtime of a
  restart, in the same form as an observed outage. This is what makes GS-8 stage
  1's restart rule (its Q1) the same rule as this one. If there is no previous
  item (first start) there is no window.
- **Retention.** A node keeps its windows for 1 hour and at most 16. They are
  node memory only, never needed to rebuild, because applying one writes an event
  (section 4).

## 4. The credit (window length at most T)

**Event.** `ClockCredited{credit_ms: u32, window_id: u64, server_ts: u64,
reason: u8}`, new code `0x12`. `reason` is `1` store outage, `2` node downtime.

**Fold.** `LastChangeTS += credit_ms`, and `LastCreditWindow = window_id`.
Nothing else changes. This works for every clock state with one rule: a running
clock is charged `now - LastChangeTS`, so moving the baseline forward gives that
time back; before the first two plies no clock runs, and the first-move timeout
is also measured from `LastChangeTS` (GS-7 §3.5), so it is shifted the same way.
The fold needs no seat, no clamp and no knowledge of the window: it replays from
stored events alone.

**What is credited.** For a window `[start, end]` the credit is the time the game
was actually waiting through it: `credit_ms = end - max(start, LastChangeTS)`. It
is never negative and never exceeds `now - LastChangeTS`, so the baseline never
moves past the present. A game whose last change is after `end` gets nothing.
Only the side to move is affected, because only that clock (or the first-move
timeout) runs; the opponent's clock was not running and is not touched.

**When it is applied.** Before any other decision for that game, in the same
atomic write as the command's own events:

- A node tells every live session about a window the moment it closes, so an idle
  game gets its credit at once and not at its next deadline.
- A session built or rebuilt after the window closed applies the node's retained
  windows at build time.
- The tick and the move path both apply any unapplied window first, then decide.
  A flag is judged on the credited baseline, never the old one.

**No double credit.** A window is applied to a game at most once: the decision
skips a window whose id equals `LastCreditWindow`, which is stored, so a rebuilt
session, a retried write and a second node all see it. A window older than the
last stored change is ignored.

**Example.** Black has 10 s left and is thinking. The store is down for 15 s.
Without this spec Black flags at recovery. With it, 15 s is credited, Black
still has about 10 s, and the opponent's clock never started.

## 5. The abort (window longer than T)

When the window is longer than **T (proposed 2 minutes, owner to confirm)**,
every game that was live during it is aborted, not credited, at the same points
as section 4:

- The session appends `Aborted{by: SystemActor}` and `GameEnded{termination:
  ABORT_OUTAGE, result: draw-shaped}` in one atomic write.
- **Unrated.** A consumer treats `ABORT` and `ABORT_OUTAGE` as unrated; the draw
  shaped result vector must never count as a draw (as GS-7 §3.5 already says).
- **Exception to the early-abort rule.** `Aborted` is otherwise valid only early
  in a game (`abortBeforePly`). A system abort with termination `ABORT_OUTAGE` is
  valid at any ply, and only from the system actor.
- **Every live game aborts**, including ones where nobody's clock was near zero.
  A long outage means players have left, so this is the Chess.com behaviour the
  owner chose, and the first-move rule already aborts below ply 2.

## 6. Clients

- **Credit.** After a credit the node pushes a `STATE` to both seats (a server
  push of the existing message; no new message). Clients already replace their
  clocks from `STATE`; WEB-F01 §3 gets one sentence saying so.
- **Abort.** Both players get `GAME_END` with the new termination, and the
  client shows "Game aborted: server problem, no rating change".
- **A MOVE during the outage** is answered `ERROR{INTERNAL}` as today. The client
  does not resend (ADR-19 (a)); after recovery it moves again, and the credit
  covers the time.

## 7. Restarts and failover (how GS-8 uses this)

- **Restart on one node (GS-8 stage 1).** The new incarnation's downtime window
  (section 3) goes through the same credit-or-abort rule, with `reason = 2`. A
  restart no longer flags the player on move, so GS-8 Q1 is answered by this spec
  and stage 1 no longer has to ship with rated play off, **once this spec is
  implemented**. A restart longer than T aborts every live game, the same as an
  outage.
- **Failover to another node (GS-8 stage 2).** The adopting node reads the dead
  node's last heartbeat item and builds the window `[ok_ms, adoption time]`. GS-8
  §6.4's `gap_ms` field on the claim is replaced by `ClockCredited{reason: 2}`;
  GS-8 is amended to say so when stage 2 is designed in detail. The clock
  skew bound in GS-7 §3.5 applies, because the window mixes two nodes' clocks.
- **Limits, stated.** A node that dies and is never replaced produces no window
  until someone adopts its games. A window is only as accurate as the 2 s
  heartbeat: the start is backdated by at most that, so the player on move can
  gain up to 2 s, never lose time.

## 8. Wire and codec change (needs the owner, ADR-20)

PROTOCOL §4A changes only additively:

| Change | Detail |
|---|---|
| Event `0x12 ClockCredited` | `credit_ms: u32, window_id: u64, server_ts: u64, reason: u8` |
| Termination `0x0B ABORT_OUTAGE` | a new value of the frozen u8 termination table; `GameEnded` and `Aborted` keep their shapes |
| `Aborted.by` | unchanged; the outage abort uses `SystemActor` (`0xFF`) |

This follows ADR-19's process: a new protocol version, review by the affected
streams (S2, S4; S5 for the clients) and the architecture group, golden vectors,
and decoders in Rust, Go, TypeScript, Swift and Kotlin. It also needs #187 first
(the fold must tolerate an event type an older node does not know). Code `0x12`
is taken here; GS-8's claim event moves to the next free code.

**Rolling deploy.** A node that does not know `0x12` cannot rebuild a game that
holds one. All nodes must understand the event before any node writes it, and
rolling back below that point is a one-way door. There are no deployed nodes yet.

## 9. Tests (the #203 harness gets an outage phase)

- A store outage of D at most T: no flag for time lost; the mover's clock after
  recovery equals the clock before it; a rebuilt session agrees (I3).
- An outage longer than T: every live game ends `ABORT_OUTAGE`, unrated, both
  players notified, and a game already past `abortBeforePly` still aborts.
- Idle game: the credit lands at recovery, not at the next deadline.
- First-move window: a game below ply 2 keeps its full first-move timeout.
- No double credit: replaying the same window, a rebuild, and a retried write
  leave one `ClockCredited`. Two windows 6 s apart credit separately; 4 s apart
  merge.
- One failed or ambiguous write opens no window (the #95 fault store).
- A heartbeat failing once does not open a window; twice does.
- Restart: a stored heartbeat from a previous incarnation yields a window of the
  right length, and a first start yields none.
- Codec: golden vectors for `0x12` and termination `0x0B`, and parity in every
  decoder. Mutation checks: credit ignored, window not deduplicated, flag judged
  before credit, window start not clamped to `LastChangeTS`.

## 10. Decisions for the owner

- **T, the abort threshold.** Recommended 2 minutes. Chess.com aborts on any
  restart; Lichess never aborts but adds 10 to 20 s. A longer T keeps more games
  alive but makes players wait.
- **Exact credit, no bonus.** Recommended. Lichess adds a fixed grace on top, but
  that gives one side time the other never gets.
- **The wire change** in section 8 (ADR-20), including the new termination so
  clients can explain the abort instead of showing a plain abort.
- **The heartbeat item** in the event table under `node/{id}/health`, rather than
  a fourth table. It is the same ADR-16 scope (event store), one overwritten item
  per node.

## 11. Specs that change when this is accepted

GS-6 §3.5 and Q-GS6f (decided); GS-7 §3.5 (the baseline can move forward by a
credit; the abort exception); GS-8 (Q1 answered; §6.4 `gap_ms` replaced);
PROTOCOL §4A and WEB-F01 (section 8 and 6); MASTER-PLAN (ADR-20).
