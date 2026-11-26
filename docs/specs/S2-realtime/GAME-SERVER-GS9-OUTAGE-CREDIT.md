# Game-server GS-9 — a clock that never charges players for our downtime (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.4 (v0.3 design kept; fixes the four blockers and ten should-fix points of the v0.3 review, section 14) |
| Last updated | 2026-11-27 |
| Fixes (issues) | #207; also supplies the restart-downtime rule GS-8 stage 1 needs (GS-8 Q1) |
| Depends on | GS-4 (event store), GS-6 (ticker, #97, #182), GS-7 (fold, `LastChangeTS`, first-move rule, #183 receipt time) |
| Owner decisions | 2026-11-27: exact credit, T = 2 min, no bonus time |

## 1. What the player gets

| What happens | What the player sees |
|---|---|
| A hiccup under 1.5 s | Nothing. This is network lag (#82). |
| We cannot serve for 1.5 s to 2 min (store outage, pause, restart, deploy) | A "server problem, clocks paused" notice. Nobody flags. The clock display stops where it was when we stopped, and when service returns both clocks show exactly that. A move sent meanwhile is held and played when service returns (section 6). |
| We cannot serve for more than 2 min | The game is aborted with no rating change and a message saying why. A held move is answered with an error that says it was not played. |

Lichess gives a fixed +10 to +20 s on a planned restart and nothing for a crash,
and it never aborts. Chess.com aborts live games on a restart. This design
gives back the recorded lost time, in every case, and aborts only when a game
cannot sensibly continue. "Exact" means exact for every recorded gap. A gap
shorter than S (1.5 s) is not recorded and counts as lag, and a flag decided
under a valid proof can still precede a gap that began after the last renewal,
so the unrecorded error is at most S plus the renewal timeout (section 6).

## 2. The idea in one paragraph

Earlier drafts tried to detect an outage after it happened and then patch a
credit into every game's clock. That produced double credits, ordering
problems, a write storm at recovery and a race between a flag and the credit.
The root cause is that the clock was a running total that someone had to
correct. Here the clock is **a pure function of two stored facts**: the game's
own events, and the **service record**, a small list of the intervals in which
the cluster could not prove it was serving. A game is never patched. Nothing
needs deduplicating, because the credit is the length of the overlap of two
stored intervals, and the same inputs give the same answer on any node, live or
rebuilt. A decision that reads a clock takes a snapshot of those inputs and
carries it into the pure `Decide`, so the proof and the clock reading cannot
come apart.

## 3. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-4 | An outage abort ends with no result and no rating change (section 8). |
| NFR-7 | A flag or an abort is decided only inside a proof snapshot (section 6). The residual error is at most S plus the timeout, stated, not hidden. |
| NFR-2 | A held move is played or answered with an explicit not-applied error, never dropped silently (section 6). |
| NFR-9 | A live clock is replayable from the game stream and the service record, both durable (section 5). |
| GS-7 I3 | Live and rebuilt sessions read the same two inputs, so they agree. |
| #95 | One failed or ambiguous write opens no gap. Only a missed renewal does (section 4). |

## 4. The service record: lease and gaps

**Scope: one lease per cluster.** Stage 1 of GS-8 has one node, so the lease key
is `lease/{cluster}`, not per node. A restart under a different node id (pod or
instance name) still finds it. `node_id` is stored as information only.
Per-owner scoping for several nodes is forbidden until GS-8 stage 2 defines it
(section 12).

**Lease.** One item in the event table, reserved key space, same table and
failure mode as game events: `{incarnation, renewed_ms, node_id}`. It probes the
exact dependency games need. The node renews it every **P = 500 ms** with a
timeout of 500 ms. Defaults, tuned in the G-E tests, must keep `S >= 2P +
timeout`. Cost: two small writes per second per node.

**Incarnation.** A counter, stored as a decimal string so it matches
`owner_incarnation` in the live-games index (GS-8). At start the node reads the
lease with a consistent read and writes `incarnation + 1` with a condition on
the old value. A renewal is a conditional write on the node's own incarnation.
A failed condition means a newer process exists: the node learns it is
superseded and **must stop serving**, not just stop writing the lease.

**Stamps.** `renewed_ms` is the node's wall clock when it **sent** the
renewal. The proof age is measured from that stamp, so the up to 500 ms between
send and acknowledgement is charged to the player on move as lag. This is a
deliberate, bounded cost; the other choice is a credit that can be late.

**Gap.** A renewal sent at `t2` after the node's last acknowledged renewal sent
at `t1`, with `t2 - t1 > S = 1.5 s`, means the node was not provably serving in
`(t1, t2)`. One rule covers a store outage, a pause, a restart (the new
incarnation compares its first renewal with the stored `renewed_ms`) and a slow
deploy. There is no failure counting. The credit can start up to one period
early, a bounded advantage for the player on move, and is never late.

**Service record item.** One item per gap: partition `svc/{cluster}`, numeric
sort key `start_ms`, attributes `{end_ms, node_id, incarnation}`. It is not a
game event and has no codec payload, so no ADR-19 decoders are needed. It is
idempotent by its key: a second put with the same `start_ms` fails the
condition, and the writer reads it back and compares. Two gaps with different
starts that cover the same time are harmless, because `Overlap` is a union.

**The gap and the lease commit together (blocker 1).** The renewal that
crosses `S` is one `TransactWriteItems`: put the lease (condition: own
incarnation) and put the gap item (condition: key absent). The stored
`renewed_ms` therefore never advances past an unrecorded gap. The node's
in-memory proof advances only after the transaction commits. If the transaction
fails or the answer is ambiguous, nothing changed in memory. The next attempt
takes the gap from the node's last acknowledged `renewed_ms`, not from the
stored one, so an ambiguous commit that left the stored value ahead produces an
overlapping gap and not a hole. An ordinary renewal with no gap is a single
conditional put.

**Start.** The first renewal of a new incarnation is the same transaction,
with `t1` = the stored `renewed_ms`. A lease read that fails means the node
does not serve. A missing lease item means a first start only if the service
record is empty and `live_games` shows no live game for the cluster. Otherwise
the node refuses to serve and alarms, so a deleted lease or table is never read
as "no gap".

**Clock source.** Stamps are wall-clock milliseconds, since a freeze stops the
monotonic clock. A wall step is dangerous in both directions:
- **Backward step.** Proof age must satisfy `0 <= now - renewed_ms <= S`. A
  negative age is no proof. A renewal sent with `t2 < t1` records no gap.
- **Forward step or freeze.** Each renewal also compares the wall delta with
  the monotonic delta. When the wall delta exceeds the monotonic delta by more
  than S the gap is a **suspect gap** (a step or a VM freeze, which cannot be
  told apart). It is credited, but clamped to T and **never aborts** a game.
  A suspect gap therefore costs nobody a game. Operations must slew clocks, not
  step them, and the node alarms on every suspect gap.

## 5. The clock projection

`Gaps.Overlap(a, b)` is the length of the union of recorded gaps inside
`[a, b]`. The game fold and the stored events do not change: `LastChangeTS` stays
a real timestamp and `ClockAfterMs` is stored per move as today. These
functions take a `Gaps` value and the proof snapshot of section 6:
`ClocksAt`, `SnapshotOf`, `NextTickMs`, `firstMoveTimedOut`,
`sideToMoveFlagged`, `moveTimeMs` and the join-timeout check.

- **Elapsed.** `elapsed(now) = max(0, (now - LastChangeTS) - Overlap(LastChangeTS, now))`.
- **Display without proof (S3).** `ClocksAt` and `SnapshotOf` take
  `effNow = now` with a proof and `effNow = renewed_ms` without one. A STATE sent
  during an unrecorded gap therefore shows the clock frozen at the last proof.
  When the gap is recorded every affected game gets a fresh `STATE`.
- **Move charge (#183).** A move received at `r` is charged
  `(r - LastChangeTS) - Overlap(LastChangeTS, r)`, with `r` clamped as today.
  A gap before `r` is not charged. A gap after `r`, while the move waited, is
  charged to nobody. The comment on `MovePlayed.ServerTS` and
  `SubmitMoveReceived` is corrected: the stored `ServerTS` is the decide time
  `nowMs`, and the receipt time `r` is the clamp input.
- **Next tick (#182).** The deadline is a fixed point: start with
  `d = LastChangeTS + remaining`, then repeat `d = LastChangeTS + remaining +
  Overlap(LastChangeTS, d)` until it stops growing. It ends in at most the
  number of gaps steps. When a gap is appended, `publishTick` is woken for every
  live session, so no timer sleeps through a credit.
- **Only the running clock gains.** The opponent's clock was not running, so
  nothing changes for it.
- **Takeback and first moves.** A rewind sets `LastChangeTS` to the rewind time,
  so older gaps fall outside `[LastChangeTS, now]`.
- **Join timer.** Its baseline is `GameCreated.ServerTS`, a stored value, and it
  is a command to the actor (`CmdJoinTimeout`) that `Decide` evaluates with
  `elapsed` and the proof, like a flag. The process-local `time.Timer` only
  wakes the actor.
- **Ended games** need no gaps: `GameEnded` and the per-move clocks are stored.
  Fair-play think time is the `ServerTS` difference minus `Overlap` over the
  same interval.
- **Retention.** A clock needs only gaps that end after the oldest live game
  was created. At start the node queries `svc/{cluster}` for `start_ms` from
  that time on (the sort key makes it one range read), and on each append it
  adds the new item. Old items are never deleted.

## 6. The serving proof: one snapshot for every clock decision

**The snapshot (blocker 2).** The node keeps one immutable value
`Proof{renewedMs, gapsVersion, incarnation}`, replaced atomically after each
committed renewal. Every command that reads a running clock carries the
snapshot **into `Decide`**, and `Decide` itself checks
`0 <= nowMs - renewedMs <= S` and uses the gaps of that snapshot. `nowMs` is read
**after** the snapshot, and both are re-taken on every seq-conflict retry and
after every wait. A pause between the check and the decision therefore makes the
age exceed `S`, and `Decide` returns "no proof" instead of flagging. The check
is not done in a timer or transport goroutine, where it could go stale.

**Without a proof the clock decision waits.** The proof returns when the next
renewal commits. This replaces the earlier hold, ordering and corroboration
rules:

- **Flag tick, first-move abort, join abort.** The timer wakes the actor with a
  command. `Decide` flags only inside a valid snapshot. Otherwise the command
  waits for a new proof and recomputes with the new gaps.
- **Residual error.** A gap that began after the last renewal and is shorter
  than `S + timeout` can lie inside a valid proof. It is charged as lag. That is
  the stated bound on the unrecorded error.
- **Resign, draw offer and accept, abort.** They do not read the clock and are
  never held. A MOVE held earlier is not replayed ahead of a later resign: the
  resign applies, and the held MOVE then fails with `game over`. This is the
  same race as any move that was not yet acknowledged.
- **No starvation.** The proof depends only on the lease and the service record,
  never on a game's stream, so one poisoned game cannot hold the others.
- **No write storm.** Recovery is one transaction. Games need no write to get
  their credit. A herd of timers and held commands at recovery is bounded by the
  per-game parked queue and measured in an N-game recovery test.

**Where the hold lives (blocker 4 and S2).** A held command waits in a **gate in
front of the game's actor**, not inside `exec`, so a held MOVE never blocks a
resign, a draw offer or a `STATE` for the same game. The gate keeps the receipt
time `r` and arrival order and has its own deadline.
- **Deadline.** `T_hold = T` (2 min), not the 10 s command timeout. The command
  timeout starts when the command enters the actor.
- **Bound.** At most 8 parked commands per game. A ninth gets `ERROR{BUSY}`
  immediately, with the guarantee that it was **not applied**.
- **Release.** With a proof back, parked commands enter the actor in arrival
  order and are decided with the gaps, so the outage is not charged.
- **Expiry.** If no proof returns by `T_hold`, the command gets `ERROR{BUSY}`
  with the same "not applied" guarantee. A gap over T aborts the game when
  service returns, so the player's next frame is `GAME_END`. The client does not
  resend (ADR-19 (a)). A command that reached the actor and is then cut by the
  command timeout keeps today's `ERROR{INTERNAL}` and is resolved by RESYNC.
- **Ambiguous commit.** A re-decided command after a conflict is decided from a
  new snapshot, and the existing re-ack rule (the ply is already in the stream)
  keeps it from applying twice.

## 7. Rebuild and restart

- A rebuilt session loads the game stream and the in-memory service record.
  Live and rebuilt agree because they read the same two durable inputs. Tests
  compare their clocks at a gap boundary.
- **Restart.** The new incarnation's first renewal is the transaction of section
  4, with the gap `(stored renewed_ms, first renewal)`. It is the node's
  downtime, whatever the number of games or when each is rebuilt. A slow or lazy
  rebuild adds no gap and aborts nothing. Games are rebuilt eagerly at start
  (GS-8 stage 1) so the gap between start and the first decision stays small.
- A node never serves while the lease read, the lease write or the gap
  transaction is failing.
- **Planned deploy.** It is the same gap, with the same credit. A drain that
  hands games to another node first is GS-8 stage 2 (D5).

## 8. The abort (gap longer than T)

T is **2 minutes**. A **real** gap longer than T aborts, for every cause. A
suspect gap (section 4) never aborts.

- After the gap transaction commits, the node aborts every live game created
  before the gap's end, in memory and from the `live_games` index (a hint, so
  the repair below is the guarantee). The command is `CmdAbortOutage`. It
  appends `Aborted{by: SystemActor}` and `GameEnded{ABORT_OUTAGE}` in one
  write.
- **Who may issue it (S8).** `Decide` today accepts `CmdAbort` with
  `By == SystemActor` from any caller. v0.4 requires an internal-only marker on
  the command that the transport cannot set, and a test sends a client frame
  that tries it. The fold requires `GameEnded{ABORT_OUTAGE}` to be immediately
  preceded by `Aborted{0xFF}`, so the pair is enforced from the events.
- **No lost abort.** A crash between the gap and the aborts is repaired at
  build, **before the first STATE or MOVE of the game is served**: a live game
  created before the end of a real gap longer than T aborts from stored facts
  alone.
- **No cumulative cap.** v0.3 aborted a game whose total credit exceeded 10
  minutes. The reviewer showed that this breaks I3 at the boundary, because a
  late-found gap can abort a game that was live before. The cap is removed.
  The credit is exactly the recorded non-serving time, so there is no free time
  to bound, and a suspect gap is clamped to T. The alternative is a cap that
  limits credit instead of aborting. It is not needed now and is not built.
- **Unrated, and not a draw.** `ResultVector` for an abort is Draw,Draw today.
  Every consumer keys on the termination code. `knownTermination` adds `0x0B`,
  and a test checks that rating, fair play, replay and the ended-game view treat
  it as unrated.
- **A slow deploy.** A deploy that takes longer than T aborts every live game.
  This follows from the owner's choice of T.

## 9. Clients and wire

- **No new game event and no new codec payload.** The service record is a
  node-internal item, not a game event, so the frozen event set is unchanged and
  rating and fair-play readers ignore non-`game/` streams and the `lease/` and
  `svc/` key space (no consumer exists in this tree; a test pins it).
- **Notice.** While a node has no proof but can still talk to clients, it
  sends `SERVICE_STATUS{paused: true}`, then `{paused: false}` and a `STATE` for
  each affected game when service returns. Clients stop their local clock tick
  while paused. The message is additive, and the client notice can ship after
  the server rule.
- **Abort.** Both players get `GAME_END` with the new termination `0x0B
  ABORT_OUTAGE`. The u8 table uses `0x01` to `0x0A` today, so `0x0B` is free,
  but `protocol/codec/tests/negative.rs` uses `0x0B` as an invalid code and
  must change. The client shows "Game aborted: server problem, no rating
  change".
- **Fail closed.** A node that does not know `0x0B` refuses to fold the game, as
  today.
- **Rollout.** Stage 1 has one node, so no mixed versions exist. The gap rule
  ships behind a per-node switch, off until the clients show the notice. After a
  game ends `0x0B` there is no rollback to a build that does not know it, so
  the switch is turned on only after the codec and decoders have shipped.
- **Consumers.** Think-time readers subtract `Overlap`.
- ADR-19 process applies to `0x0B` and `SERVICE_STATUS`: version bump, review,
  golden vectors, decoders in Rust, Go, TypeScript, Swift and Kotlin.

## 10. Tests (the #203 harness gets an outage phase)

- **Credit.** A store outage of D under T: no flag for time lost, the mover's
  clock afterwards equals the clock before, a rebuilt session agrees, and the
  two agree at the exact gap boundary (I3).
- **Atomic gap (blocker 1).** Crash after the lease write but before the gap
  write cannot happen: fail the transaction at each step and check the lease
  never advances past an unrecorded gap. An ambiguous commit gives an
  overlapping gap, not a hole.
- **Snapshot (blocker 2).** A fake clock jumps between the proof check and the
  `nowMs` read: `Decide` returns no proof. A pause between a timer and `exec`,
  and a seq-conflict retry, are re-checked. A backward wall step gives no proof.
- **Pause.** A pause with no failed write: a tick right after resume does not
  flag. A pause shorter than `S` is not credited (boundary test).
- **Restart id (blocker 3).** A restart under a different node id still finds the
  lease. A missing lease with a non-empty service record or live games refuses
  to serve. A first start gives no gap. A failing lease read means no serving.
  A zombie fails renewal and stops serving.
- **Held command (blocker 4).** An outage of 30 s: a MOVE is held past 10 s and
  then played with no outage charge. An outage past `T_hold`: the MOVE gets
  `ERROR{BUSY}` and the game then aborts. A ninth parked command is refused.
- **Gate (S2).** A resign while a MOVE is held proceeds, and the held MOVE then
  fails with `game over`. `STATE` is served while a MOVE is held.
- **STATE during a gap (S3).** The displayed clock is frozen at the last proof.
- **Gap race.** Writes recover before the lease: a MOVE and a TAKEBACK_ACCEPT in
  that gap are held and charged nothing for the outage.
- **Queued move.** Received before the gap, inside it, and after it, with a
  reload between receipt and decision (the stored `AppliedSeq` stays correct).
- **Poisoned stream.** One game that always fails does not delay another game's
  flag. A throttled event table at 30% gives false gaps that pause clocks and
  never flag; flags fire after the proof returns.
- **Idempotence (S5).** The same gap seen twice, overlapping gaps and
  out-of-order appends give the same clock. Two writers racing on one start key
  leave one item.
- **Abort.** Every live game ends `ABORT_OUTAGE`, unrated; a game past
  `abortBeforePly` still aborts; a client frame cannot produce `SystemActor` or
  `ABORT_OUTAGE`; a crash between the gap and the aborts is repaired at build
  before any STATE or MOVE is served; a suspect gap credits and never aborts;
  every consumer treats `0x0B` as unrated.
- **Takeback, first moves, join.** An older gap does not affect a rewound clock;
  the first-move and join timeouts move with a gap; `publishTick` is woken for
  every session when a gap is appended.
- **Wall step.** A forward step of S or more is a suspect gap, credited and
  clamped to T; a backward step records none.
- **Recovery herd.** N live games, each with a timer and a parked command,
  recover without a second outage.
- **Codec.** Golden vectors for `0x0B` and `SERVICE_STATUS`, parity in every
  decoder, an old node fails closed, `negative.rs` updated.
- **Mutations.** Overlap ignored, flag decided without proof, proof checked
  outside `Decide`, move charged without the gap, lease written without its
  gap, gap not appended before serving, resign held behind a MOVE.

## 11. Designs rejected, and why

| Design | Why not |
|---|---|
| A `ClockCredited` event per game (v0.1, v0.2) | N writes at recovery, a race with the flag, double credit on rebuild, a watermark with ordering rules, and a stored baseline shift. Two review rounds found 12 blockers, all from here. |
| Detecting an outage by counting failed writes | It misses pauses and partial throttling and can starve flags. |
| A separate health table | It can be healthy while the event table is down, so players would be flagged unfairly. |
| Lichess-style fixed bonus | It cannot cover a crash, and it gives one side time the other never gets. |
| A gap stream with sequence numbers (v0.3) | Needs a conditional overwrite and a seq conflict handler and cannot be pruned by time. A key by `start_ms` is idempotent by itself and gives a range read. |
| A cumulative credit cap that aborts (v0.3) | Breaks I3 at the boundary and answers a problem that exact credit does not have. |

## 12. Specs that change when this is accepted

GS-6 §3.5 and Q-GS6f (decided); GS-7 §3.5 (clock projection takes the service
record and the proof snapshot; `CmdAbortOutage`; the `SystemActor` rule); GS-4
(lease item, `svc/` key space, `TransactWriteItems` for the renewal, `GetItem`
or a consistent query on the store interface); GS-8 (Q1 answered; **stage 2 must
scope gaps by the game's owner and ownership interval, and may not start
before that is defined**; node clock skew; its liveness can use the lease);
PROTOCOL §4A and WEB-F01 (`0x0B`, `SERVICE_STATUS`, `ERROR{BUSY}`);
MASTER-PLAN (ADR-20).

## 13. Build order

1. Store: `GetItem` or consistent query, the lease and gap transaction, the
   `svc/` range read. No behaviour change.
2. Node state: incarnation, renewal loop, the proof snapshot, suspect-gap
   classification, the stop-serving rule.
3. The clock projection with `Gaps`, the proof check inside `Decide`, the gate,
   `CmdJoinTimeout`.
4. `CmdAbortOutage`, build-time repair, `0x0B`, the notice and the clients, then
   the per-node switch.

## 14. Review mapping (v0.3 review, 4 blockers and 10 should-fix points)

| Finding | Where answered |
|---|---|
| B1 gap lost between renewal and append | §4 single transaction; §10 atomic gap |
| B2 proof and `nowMs` read apart | §6 snapshot into `Decide`; §10 snapshot |
| B3 restart needs a stable id | §4 cluster lease, first-start rule; §10 restart id |
| B4 hold vs 10 s timeout | §6 gate with `T_hold`, bound, expiry |
| S1 wall steps | §4 clock source, suspect gap |
| S2 hold blocks the actor | §6 gate in front of the actor |
| S3 STATE during a gap | §5 display without proof |
| S4 stamping, ambiguous renewal, slack | §4 stamps, gap, P and S rule |
| S5 gap idempotence | §4 key by `start_ms` |
| S6 store fit, retention | §4, §5 retention, §9, §12 |
| S7 cap breaks I3 | §8 cap removed |
| S8 abort rules | §8 |
| S9 join timer, `NextTickMs`, `publishTick` | §5 |
| S10 multi-node scoping | §4 scope, §12 |
| Nits | §5 comment, §6 incarnation current, §9 rollout and `0x0B` |
