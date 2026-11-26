# Game-server GS-9 — a clock that never charges players for our downtime (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.5 (v0.3 design kept; fixes the v0.3 review and the v0.4 review, section 14) |
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
The lease probes the event table only. A throttled index table, an engine
failure or one poisoned game partition can still fail a game while the lease
passes. That is outside this spec and belongs to the load and fault tests
(#138, #203). A gap also starts up to one renewal period early, a stated
bonus of at most 500 ms per gap to the player on move.

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
is `lease/{cluster}`, not per node. A restart under a different node id still
finds it. `node_id` is stored as information only. Per-owner scoping for several
nodes is forbidden until GS-8 stage 2 defines it (section 12).

**Lease.** One item in the event table, reserved key space, same table and
failure mode as game events: `{incarnation, nonce, renewed_ms, enabled_at_ms,
node_id}`. The node sends a renewal every **P = 500 ms** at a fixed rate, with
**at most one in flight**, and a timeout of 500 ms. Defaults, tuned in the G-E
tests, must keep `S >= 2P + timeout`. One lost renewal is tolerated. Two
consecutive lost renewals give a spacing of about 1.5 s, which is a gap (the
test is `t2 - t1 >= S`).

**Incarnation.** A counter stored as a decimal string, matching
`owner_incarnation` in the live-games index (GS-8). Each process also draws a
random **nonce**. At start the node reads the lease with a consistent read and
writes `incarnation + 1` and its nonce, conditional on the old value. A renewal
is conditional on `incarnation = own AND nonce = own AND renewed_ms < :t`. The
nonce lets a node recognise its own commit after an ambiguous answer (a lost
reply to the start write) and tell it from a competitor's. A lease condition
that fails with another nonce means a newer process exists: the node is
superseded and **must stop serving**.

**Stamps.** `renewed_ms` is the node's wall clock when it **sent** the renewal.
Proof age is measured from it, so the up to 500 ms between send and
acknowledgement is charged to the player on move as lag. This is a deliberate
bounded cost; the alternative is a credit that can be late.

**Gap.** A renewal sent at `t2`, after the node's last acknowledged renewal sent
at `t1`, with `t2 - t1 >= S = 1.5 s`, means the node was not provably serving
in `(t1, t2)`. One rule covers a store outage, a pause, a restart and a slow
deploy. There is no failure counting. The credit can start up to one period
early, so "exact" carries a stated bonus of at most P per gap to the player on
move. It is never late. T is compared against the measured `t2 - t1`.

**Service record item.** One item per gap: partition `svc/{cluster}`, numeric
sort key `start_ms`, attributes `{end_ms, node_id, incarnation, suspect}`. It is
not a game event and has no codec payload, so no ADR-19 decoders. A gap put
succeeds if `attribute_not_exists(start_ms) OR end_ms < :newEnd`, so a retry
**extends** the same gap and never collides with its own earlier commit. Two
gaps with different starts that cover the same time are harmless, because
`Overlap` is a union.

**The gap and the lease commit together.** The renewal that crosses `S` is one
`TransactWriteItems`: the lease put and the gap put. The stored `renewed_ms`
therefore never advances past an unrecorded gap. The in-memory proof advances
only after a commit. On a failed or ambiguous answer memory does not change,
and the retry builds the gap from the node's **last acknowledged** `renewed_ms`
with a new end. Per-item `CancellationReasons` decide the reaction: a lease
failure with a foreign nonce means superseded, a lease failure with the own
nonce means the commit already happened (adopt it), and a gap failure means
read back and compare. `appendTx` maps every conditional failure to a seq
conflict today (`store/index.go`), so this needs its own mapping. An ordinary
renewal with no gap is a single conditional put.

**Start.**
- The first renewal of a new incarnation is the same transaction, with `t1` =
  the stored `renewed_ms`. If `t2 < t1` (the host clock is behind the old
  lease) the node **refuses to serve and alarms** until its clock passes `t1`.
- A failed lease read means no serving.
- A missing lease is a first start only if the service record is empty **and**
  every `live_games` shard reads empty. A failed shard read refuses to serve.
  `live_games` is a hint (an ambiguous opening has no row), so a missing lease
  with any doubt is resolved by an operator, never by the node.
- **First deploy of this build** has live games and no lease. An operator
  seeding command writes `lease/{cluster}` with `incarnation 0` and
  `renewed_ms = now`. The downtime of that first deploy is not creditable, and
  the spec says so to the owner.

**Switch.** Recording gaps is always on. Applying them is behind a per-node
switch, and flipping it writes `enabled_at_ms` into the lease. A projection and
an abort ignore every gap with `start_ms < enabled_at_ms`, so turning the
switch on never credits or aborts a game for history.

**Clock source and steps.** Stamps are wall-clock milliseconds, because a
hypervisor freeze stops the monotonic clock.
- **Backward step.** Proof age must satisfy `0 <= now - renewed_ms <= S`, so a
  backward step gives no proof until the wall clock passes the last stamp. No
  decision is made in that time, and no gap is recorded for it. Both clocks are
  frozen meanwhile, which is symmetric.
- **Forward step or freeze.** Each renewal compares the wall delta with the
  monotonic delta. When the wall delta exceeds the monotonic delta by more than
  S, the gap is **suspect**: a step, or a VM freeze or migration. They cannot be
  told apart. A suspect gap is credited **in full** (the wall length), so no
  mover is charged for a step, and it **never aborts**. A real hypervisor
  freeze longer than T therefore credits and does not abort. This is accepted:
  the credit is correct, and the players are not harmed.
- **Process pause.** SIGSTOP, a GC stop or a cgroup freeze advance both clocks,
  so the gap is real, and it aborts above T.
- Operations must slew clocks, not step them, and the node alarms on every
  suspect gap.

## 5. The clock projection

`Gaps.Overlap(a, b)` is the length of the union of recorded gaps inside
`[a, b]`. The game fold and the stored events do not change: `LastChangeTS` stays
a real timestamp and `ClockAfterMs` is stored per move as today. These
functions take a `Gaps` value and the proof snapshot of section 6:
`ClocksAt`, `SnapshotOf`, `NextTickMs`, `firstMoveTimedOut`,
`sideToMoveFlagged`, `moveTimeMs` and the join-timeout check.

- **Elapsed.** `elapsed(now) = max(0, (now - LastChangeTS) - Overlap(LastChangeTS, now))`.
- **Display without proof.** `ClocksAt` and `SnapshotOf` take the proof and use
  `effNow = now` with one and `effNow = renewed_ms` without one. `Snapshot.ServerT`
  is `effNow` too, so a client does not tick from a wrong instant. A STATE sent
  during an unrecorded gap shows the clock frozen at the last proof. When the gap
  is recorded every affected game gets a fresh `STATE`. Ended games (`ended.go`)
  pass no gaps.
- **Move charge (#183).** A move received at `r` is charged
  `(r - LastChangeTS) - Overlap(LastChangeTS, r)`, with `r` clamped as today.
  A gap before `r` is not charged. A gap after `r`, while the move waited, is
  charged to nobody. The comment on `MovePlayed.ServerTS` and
  `SubmitMoveReceived` is corrected: the stored `ServerTS` is the decide time
  `nowMs`, and the receipt time `r` is the clamp input.
- **Next tick (#182).** The deadline walks the sorted, merged gaps: start with
  `d = LastChangeTS + remaining`, and whenever `d` falls inside a gap
  `[g1, g2)` set `d = g2 + (d - g1)`, then continue with the next gap. It is one
  pass over the gaps and ends in at most their number of steps. The first-move
  deadline (`NextTickMs`) uses the same walk. A gap only pushes deadlines later,
  so a timer that fires early self-heals when the actor re-checks and
  republishes (`applyRecovered`). No wake of every session is needed.
- **Only the running clock gains.** The opponent's clock was not running, so
  nothing changes for it.
- **Takeback and first moves.** A rewind sets `LastChangeTS` to the rewind time,
  so older gaps fall outside `[LastChangeTS, now]`.
- **Creation time.** `GameCreated` carries no timestamp, and adding one would
  change a frozen event. The store already stamps `committed_at` on the opening
  batch. v0.5 makes the event store return it as `Created` with a stream load
  (an addition to the store interface, no wire or event change), and the live
  session keeps it. GS-8 section 4.3 needs the same value. It is the baseline
  for the join timer and for "created before the gap's end".
- **Join timer.** It is a command to the actor (`CmdJoinTimeout`) that `Decide`
  evaluates with `elapsed` from `Created` and the proof, like a flag. The
  process-local join signal is not durable, so the transport passes a flag "no
  seat has ever been bound" in the command, and on a timer fire the transport
  re-checks the signal first, so a pause cannot pick the timer by chance.
- **Ended games** need no gaps: `GameEnded` and the per-move clocks are stored.
  Fair-play think time is the `ServerTS` difference minus `Overlap` over the
  same interval.
- **Retention.** Gap items are tiny, so a node loads **all** of
  `svc/{cluster}` at start and adds each new item on commit. Pruning is a later
  concern.

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
  command. `Decide` flags only inside a valid snapshot. **No proof is a normal
  outcome, not an error:** `Decide` returns `NoProof`, the command returns to
  the gate with its arrival position, and the ticker waits on a
  "proof restored" signal. It does not log, back off or spin.
- **Residual error.** A gap that began after the last renewal and is shorter
  than `S + timeout` can lie inside a valid proof. It is charged as lag. That is
  the stated bound on the unrecorded error.
- **Resign.** It does not read the clock and is never held. A MOVE held earlier
  is not replayed ahead of a later resign: the resign applies, and the held MOVE
  then fails with `game over`. This is the same race as any move that was not
  yet acknowledged, and a resign is the player's own final word.
- **Everything else keeps arrival order.** Draw offer, draw accept, takeback
  request, accept and decline, and abort wait **behind** a held MOVE in the same
  FIFO. Otherwise an opponent's draw accept could overtake a held mating move
  and turn a win into a draw. `STATE` and `RESYNC` are read-only and are served
  at once.
- **Abort precedence.** On recovery from a gap longer than T the node enqueues
  `CmdAbortOutage` for each live game **before** it publishes the proof to the
  gate and ahead of every parked command and tick. A released flag can then
  never end a game that a rebuild would have aborted (I3).
- **No starvation.** The proof depends only on the lease and the service record,
  never on a game's stream, so one poisoned game cannot hold the others.
- **No write storm.** Recovery is one transaction. Games need no write to get
  their credit. A herd of timers and held commands at recovery is bounded by the
  per-game parked queue and measured in an N-game recovery test.

**Where the hold lives.** A held command waits in a **gate in front of the
game's actor**, not inside `exec`, and the connection's read loop is **not**
blocked. Today `Handle` calls `route` inline per frame (`dispatch.go`), so
`handleMove` must park the command and return, and the reply is sent later from
the gate. Otherwise a held MOVE would stop that connection's own RESIGN and
RESYNC frames from being read. The gate keeps the receipt time `r` and arrival
order and has its own deadline.
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
- **Closed connection.** A parked command whose connection closes is dropped,
  the same as an unacknowledged command today. The client never resends, and
  RESYNC shows the truth.
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
- **What "serve" means.** Before the first proof, and while no proof holds, the
  node answers no SEEK, creates no game, runs no matchmaking and decides no
  clock. It still answers read-only `STATE` and `RESYNC` (frozen, section 5) and
  accepts a resign.
- **Planned deploy.** It is the same gap, with the same credit. A drain that
  hands games to another node first is GS-8 stage 2 (D5).

## 8. The abort (gap longer than T)

T is **2 minutes**. A **real** gap longer than T aborts, for every cause. A
suspect gap (section 4) never aborts.

- After the gap transaction commits, the node aborts every live game whose
  `Created` (section 5) is before the gap's end, in memory and from the
  `live_games` index (a hint, so the repair below is the guarantee). Aborts run
  with a concurrency bound (64 at a time) so recovery is not a second outage. The command is `CmdAbortOutage`. It
  appends `Aborted{by: SystemActor}` and `GameEnded{ABORT_OUTAGE}` in one
  write.
- **Who may issue it.** `Decide` accepts `CmdAbort` with
  `By == SystemActor` from any in-process caller. v0.5 requires an
  internal-only marker on the command that the transport cannot set. The fold requires `GameEnded{ABORT_OUTAGE}` to be immediately
  preceded by `Aborted{0xFF}`. `State` gets a field `AbortedBySystem`, set by
  the fold on `Aborted{0xFF}`, so `applyGameEnded` can check the pair. A client
  frame cannot reach `SystemActor` today, because `Abort` receives the
  authenticated seat (`handlers.go`). The join timer aborts as seat 0, not as
  `SystemActor`.
- **No lost abort.** A crash between the gap and the aborts is repaired at
  build, **before the first STATE or MOVE of the game is served**: a live game
  created before the end of a real gap longer than T aborts from stored facts
  alone.
- **No cumulative cap.** v0.3 aborted a game whose total credit exceeded 10
  minutes. The reviewer showed that this breaks I3 at the boundary, because a
  late-found gap can abort a game that was live before. The cap is removed.
  The credit is exactly the recorded non-serving time, so there is no free time
  to bound, and a suspect gap is credited in full but never aborts. The alternative is a cap that
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
- **Mixed versions.** Stage 1 has one node, so none exist. The gap rule ships
  behind a per-node switch, off until the clients show the notice. After a
  game ends `0x0B` there is no rollback to a build that does not know it, so
  the switch is turned on only after the codec and decoders have shipped.
- **Consumers.** Think-time readers subtract `Overlap`.
- **`ERROR{BUSY}`** is a new error code. PROTOCOL freezes six today, so BUSY
  joins the ADR-19 list with `0x0B` and `SERVICE_STATUS`.
- **Rollout.** The per-node switch also writes `enabled_at_ms` (section 4), so
  turning it on never applies a gap from before the flip.
- ADR-19 process applies to `0x0B`, `ERROR{BUSY}` and `SERVICE_STATUS`: version bump, review,
  golden vectors, decoders in Rust, Go, TypeScript, Swift and Kotlin.

## 10. Tests (the #203 harness gets an outage phase)

- **Credit.** A store outage of D under T: no flag for time lost, the mover's
  clock afterwards equals the clock before, a rebuilt session agrees, and the
  two agree at the exact gap boundary (I3).
- **Ambiguous commits.** The gap transaction commits and the reply is lost: the
  retry extends the same gap and renews. The same for the first-start write,
  recognised by its nonce. A lease failure and a gap failure are told apart from
  `CancellationReasons`. Out-of-order acks never move `renewed_ms` backward.
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
  the first-move and join timeouts move with a gap and re-arm after an early
  fire.
- **Wall and freeze.** A forward step of an hour is a suspect gap, credited in
  full, so no mover flags and no game aborts. A monotonic-stopped VM freeze
  credits and does not abort. SIGSTOP and a GC pause are real gaps and abort
  above T. A backward step gives no proof and records no gap. `t2 < t1` at
  start refuses to serve.
- **First start.** A first deploy with live games and no lease is refused until
  seeded. A deleted lease at runtime, and a failed `live_games` shard read,
  refuse to serve.
- **Switch.** Flipping it on with an old recorded gap over T neither credits nor
  aborts any live game.
- **No proof.** The ticker with no proof logs nothing, backs off nothing and
  resumes on the proof signal. A resign sent on the same connection as a held
  MOVE is read and applied. A held MOVE whose connection closes is dropped.
- **Ordering.** A draw accept behind a held MOVE waits and does not overtake it.
  The abort is enqueued before the gate opens, so a parked flag never wins over
  it, live or rebuilt.
- **Fixed point.** A clock expiring 1 ms after a gap start, and several adjacent
  gaps, finish in one pass.
- **Join.** The join timeout with the signal set during a pause does not abort.
- **STATE in a gap.** `server_t` equals the last proof time.
- **Recovery herd.** N live games, each with a timer and a parked command,
  recover without a second outage, and N aborts run under the concurrency
  bound.
- **Codec.** Golden vectors for `0x0B` and `SERVICE_STATUS`, parity in every
  decoder, an old node fails closed, `negative.rs` updated.
- **Mutations.** Overlap ignored, flag decided without proof, proof checked
  outside `Decide`, move charged without the gap, lease written without its
  gap, gap not appended before serving, resign held behind a MOVE, the gap-extension
condition removed, the `renewed_ms` monotonic condition removed, abort enqueued
after the gate opens.

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
or a consistent query, the opening batch's `Created` time returned by `Load`); GS-8 (Q1 answered; **stage 2 must
scope gaps by the game's owner and ownership interval, and may not start
before that is defined**; node clock skew; its liveness can use the lease);
PROTOCOL §4A and WEB-F01 (`0x0B`, `SERVICE_STATUS`, `ERROR{BUSY}`);
MASTER-PLAN (ADR-20).

## 13. Build order

1. Store: `GetItem` or consistent query, `Created` on `Load`, the lease and gap
   transaction with per-item cancellation reasons, the `svc/` read, and the
   operator seeding command. No behaviour change.
2. Node state: incarnation, renewal loop, the proof snapshot, suspect-gap
   classification, the stop-serving rule.
3. The clock projection with `Gaps`, the proof check inside `Decide`, the gate,
   `CmdJoinTimeout`.
4. `CmdAbortOutage`, build-time repair, `0x0B`, the notice and the clients, then
   the per-node switch.

## 14. Review mapping

### v0.3 review (4 blockers, 10 should-fix points)

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

### v0.4 review (3 blockers, 11 should-fix points, 7 nits)

| Finding | Where answered |
|---|---|
| N1 ambiguous commit wedges the lease | §4 extend condition, nonce, per-item reasons |
| N2 suspect gap clamp, misclassified freezes, restart, backward step | §4 clock source and steps, start rule |
| N3 `GameCreated.ServerTS` does not exist | §5 creation time (store `Created`, no event change) |
| N6 held MOVE blocks the read loop | §6 where the hold lives |
| S1 switch flip applies history | §4 switch and `enabled_at_ms` |
| S2 no-proof flow, ticker spin | §6 `NoProof` is not an error |
| S3 first deploy, deleted lease | §4 start, operator seeding |
| S4 `ServerT` in a gap | §5 display without proof |
| S5 loop shape, tolerance | §4 lease |
| S6 join signal not durable | §5 join timer |
| S7 gate ordering, draw accept overtakes | §6 FIFO behind a held MOVE |
| S8 abort precedence | §6 abort precedence |
| S9 lease probes only the event table | §1 stated limit |
| S10 abort write storm | §8 concurrency bound |
| S11 meaning of "serve" | §7 |
| N4, N7, N8, N9, N10, N5 | §9 BUSY, §8 `AbortedBySystem`, §1 and §4 bonus, §5 walk, §8 wording, §5 no wake |
