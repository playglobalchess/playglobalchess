# Game-server GS-9 — a clock that never charges players for our downtime (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, GS-8 §4.1, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.8 (v0.7 plus the v0.7 review: the ticker's no-proof wait, the client release rule, the proof gate after the clock-free rows, merged gaps, bounded abort writes) |
| Last updated | 2026-11-27 |
| Fixes (issues) | #207; also supplies the restart-downtime rule GS-8 stage 1 needs (GS-8 Q1) |
| Depends on | GS-8 stage 1 step 2 (`startGame`, adoption, UUIDv7 game ids); GS-4; GS-6 (ticker, #97, #182); GS-7 (fold, `LastChangeTS`, #124, #183) |
| Owner decisions | 2026-11-27: exact credit, T = 2 min, no bonus time. 2026-11-27: the client, not the server, holds a move made during a pause |

Paths in this spec are relative to `services/game-server/internal/` and were
read at commit 248b9ab (the code is identical at 9f0e8e9, and v0.7 re-read the
ticker, `handleMove`, `handleEnding`, `NewSession` and `NextTickMs`). A claim about existing code cites its line. Anything
without a citation is new work.

## 0. What the code already decides

v0.1 to v0.5 were written from memory of the code, and each review found code
facts they had wrong. v0.6 started from this table, and its review confirmed
every citation. v0.7 adds the rows marked new.

| Code fact | Where | Consequence |
|---|---|---|
| Every running-clock read goes through `State.ElapsedMs` (4 callers: the 3 below and `ClocksAt`, `game/statemachine.go:199`), `NextTickMs` (2 uses of `LastChangeTS`) and `ClocksAt` | `game/statemachine.go:185,196`; `game/pipeline.go:274,352,361,398` | The credit is a change to these functions, not a new mechanism (section 5). |
| `Decide` is pure and gets `nowMs` from its caller; `exec` reads `nowMs` again on every conflict retry | `game/pipeline.go:185`; `session/session.go:218-222` | The proof check and the abort rule live inside `Decide`, on the same `nowMs` (section 6). |
| `exec` returns early when a decision has no events, and `handleMove` has no case for an empty outcome | `session/session.go:222`; `transport/handlers.go:526-588` | "No proof" is an error, `game.ErrNoProof`, never an empty decision. |
| The session's command channel is unbuffered | `session/session.go:169` | Nothing may depend on "enqueued before X". Precedence is a rule in `Decide`. |
| `Handle` routes each frame inline on the connection goroutine | `transport/dispatch.go:141-150` | The server never parks a command. A command it cannot decide is answered at once (section 7). |
| The flag ticker logs every error and backs off | `transport/handlers.go:335-346` | `ErrNoProof` is not a failure. The ticker waits on a restored signal instead (section 6). |
| The first-move timeout already aborts as `SystemActor`; the join timer calls `sess.Abort(ctx, 0)`, a plain `CmdAbort{By: 0}` | `game/pipeline.go:330-332,387`; `transport/handlers.go:475` | `SystemActor` alone cannot mark an outage abort. It gets its own termination and fold check (section 8). |
| The event table has fixed keys `stream_id` (S) and `seq` (N). `DynamoAPI` has `Query` and `TransactWriteItems`, no `GetItem`; reads are consistent | `store/store.go:8-15,50-53,152-168` | The lease and gaps reuse these keys, and every read is a consistent `Query` (section 4). |
| `appendTx` maps condition failures by scanning `CancellationReasons`, without `ReturnValuesOnConditionCheckFailure` | `store/index.go:101-160` | An ambiguous renewal is resolved by reading the lease back, not from cancellation reasons. |
| Game ids are UUIDv4 today | `matchmaking/create.go:163` | Creation time comes from the UUIDv7 id GS-8 step 2 introduces. No store change. |
| A termination the fold does not know is rejected | `game/statemachine.go:652-665,715` | An old node fails closed on `ABORT_OUTAGE` with no new event type, so #187 is not a prerequisite. |
| Error codes `0x01`-`0x06` and server message codes up to `0x8A` and `0x8F` are used | `codec/message_seam.go:127-132`; PROTOCOL §4A table | `0x07` is free for `SERVER_BUSY`, and `0x8B` is free for `SERVICE_STATUS`. |
| New in v0.7. Server messages are encoded by serde variant index, in declaration order, and `SMError` is last (index 10) | `codec/message_seam.go:135-148` | `SERVICE_STATUS` is declared **before** `Error`, as ADR-17 and ADR-19 did, and `Error`'s index moves from 10 to 11 in Go and Rust together. The golden corpus and the seam test guard it. |
| New in v0.7. The flag ticker's `select` has four cases: shutdown, session closed, `TickChanged`, and the timer. When no clock can run out the timer is not armed and the goroutine sleeps | `transport/handlers.go:285-326` | An unarmed or long-sleeping ticker needs its own wake from `Restored()` (section 6). |
| New in v0.7. `handleMove` replies for `Rejected`, `Flagged`, `Accepted` and `Accepted+Reacked`, and for no other outcome. A non-nil error becomes `ERROR{INTERNAL}` | `transport/handlers.go:526-588`; `game/pipeline.go:51-69` | `MoveOutcome` needs a new `Voided` outcome for the outage abort (section 6). |
| New in v0.7. `handleEnding` broadcasts GAME_END whenever the command's snapshot says `Ended`, for resign and abort (it re-reads the state) and for every draw and takeback result | `transport/handlers.go:608-692` | Resign and the draw and takeback commands are answered by the outage abort with no new handler code. |
| New in v0.7. `NewSession` returns an error if `CmdStartClock` fails, and `CmdStartClock` sets `ClockStarted` only for a `Live` game that has not started its clock | `session/session.go:187-192`; `game/pipeline.go:191-197` | `NewSession` must accept `ErrNoProof`, and something must start the clock later (section 6). |

## 1. What the player gets

| What happens | What the player sees |
|---|---|
| A hiccup under 1.5 s | Nothing. It counts as network lag (#82). |
| We cannot serve for 1.5 s to 2 min (store outage, process pause, restart, deploy) | A "server problem, clocks paused" notice. Both clocks stop where they were and nobody flags. A move made meanwhile shows as pending on the board and is sent when the notice ends (`paused: false` and a matching STATE). The clocks then show exactly what they showed before. |
| We cannot serve for more than 2 min | The game is aborted with no rating change and a message saying why. A pending move is discarded. |

Lichess gives +10 to +20 s on a planned restart, nothing for a crash, and never
aborts. Chess.com aborts live games on a restart. This design gives back the
recorded lost time in every case and aborts only when a game cannot sensibly
continue.

**What "exact" means, stated once.**
- A recorded gap is credited in full.
- A gap shorter than S (1.5 s) is not recorded and counts as lag.
- Proof age is measured from when a renewal was **sent**, so the credit can
  start up to P (500 ms) early. That is a bonus of at most 500 ms per gap to the
  player on move. It is never late. The owner accepts this at ratification
  (section 12).
- A decision made in the first S of an outage is made under a still-valid
  proof, so up to S plus the renewal timeout can be charged as lag.
- The early start is at most P plus the renewal timeout (1 s with the defaults)
  when a committed reply is lost and the read-back also fails, not P alone.
- A forward wall-clock step of more than S inside a real outage makes the whole
  gap suspect, so it credits in full and never aborts (section 4). Harmless to
  players; the alarm fires.
- A backward wall-clock step stalls play: moves get `SERVER_BUSY` until the
  clock passes the last acknowledged stamp. There is no abort and the stall is
  not bounded by this spec. The pause notice shows (section 7), and operations
  must slew clocks.
- A move that reached the game's actor before the outage and then waits on the
  store gets today's `ERROR{INTERNAL}` after the 10 s command timeout
  (`session/session.go:131`, `transport/handlers.go:531`), and the client
  resyncs. That window is not covered by the pause rule.
- The lease probes the event table only. A throttled index table, an engine
  failure or one poisoned game partition can still fail a game while the lease
  passes. That belongs to the load and fault tests (#138, #203).

## 2. The idea in one paragraph

Earlier drafts detected an outage afterwards and patched a credit into every
game's clock, which produced double credits, ordering problems, a write storm
at recovery and a race between a flag and the credit. Here the clock is **a
pure function of two stored facts**: the game's own events, and the **service
record**, a small list of the intervals in which the cluster could not prove it
was serving. No game is patched. The credit is the length of the overlap of two
stored intervals, so the same inputs give the same answer on any node, live or
rebuilt. Every decision takes one snapshot of the service record into the pure
`Decide`. `Decide` refuses a clock decision without a fresh proof, and it ends
any game a long gap has voided before it decides anything else. During a pause
the server holds nothing: a command it cannot decide is answered at once, and
the client holds the player's move.

## 3. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-4 | An outage abort ends with no result and no rating change (section 8). |
| NFR-7 | A flag, a move charge, a takeback accept and a clock start are decided only with a fresh proof (section 6). An outage abort is decided from stored facts (section 8). The residual error is stated in section 1. |
| NFR-2 | A move the server cannot decide is answered `MOVE_REJECTED{SERVER_BUSY}`, which guarantees it was not applied, so the client may send it again (section 7). Nothing is dropped silently. |
| NFR-9 | A live clock is replayable from the game stream and the service record, both durable (section 5). |
| GS-7 I3 | Live and rebuilt sessions read the same two inputs and run the same `Decide` rule, so they agree. |
| #95 | One failed or ambiguous write opens no gap. Only a missed renewal does (section 4). |

## 4. The service record: lease and gaps

New package `service` (new work). It owns the lease writer, the in-memory gap
list and the proof. One `service.Node` per process.

**Scope: one lease per cluster.** Stage 1 of GS-8 has one node, so the lease is
per cluster, and a restart under a different node id still finds it. `node_id`
is stored as information only. Stage 2 may not start before gaps are scoped by
the game's owner and ownership interval (section 12).

**Items.** Both live in the event table, so they share its failure mode, and
both use its fixed keys (`store/store.go:8-15`).

| Item | `stream_id` (S) | `seq` (N) | Attributes |
|---|---|---|---|
| Lease | `lease/{cluster}` | `0` | `incarnation` (N), `nonce` (S), `renewed_ms` (N), `enabled_at_ms` (N, 0 = off), `node_id` (S) |
| Gap | `svc/{cluster}` | `start_ms` | `end_ms` (N), `suspect` (BOOL), `incarnation` (N), `node_id` (S) |

Neither is a game event and neither has a codec payload. Readers of the event
table skip every `stream_id` that does not start with `game/`. No such reader
exists in this tree today, and a test pins the rule for the first one.

**Store additions (new work).** `store.Store` gains three methods, all built on
the existing `Query` and `TransactWriteItems` (`store/store.go:50-53`), so
`DynamoAPI` is unchanged:
- `ReadLease(ctx)`: a consistent `Query` on `stream_id = lease/{cluster}`.
- `ReadGaps(ctx)`: a consistent, paginated `Query` on `svc/{cluster}`.
- `Renew(ctx, r Renewal)`: one `TransactWriteItems` with an `Update` of the
  lease and, when the renewal carries a gap, a `Put` of the gap. An `Update`
  keeps `enabled_at_ms`, which a `Put` would overwrite.

The in-memory store (`game/store_stub.go`) and the in-process DynamoDB fake
(`store/fake_index_test.go`) get the same three methods, and the contract
suite runs against both and against DynamoDB Local in CI.

**Renewal.** Every **P = 500 ms** at a fixed rate, at most one in flight, with
a 500 ms timeout. The defaults must keep `S >= 2P + timeout`, with **S =
1.5 s**. Let `lastAck` be the stamp of the last renewal known to have
committed, and `t2` the wall clock when the next one is sent.
- If `t2 < lastAck` (a backward wall step), nothing is sent. The proof expires
  on its own, and the node waits until the wall clock passes `lastAck`.
- If `t2 - lastAck < S` and the node is `Ready` (see Start), the renewal is the
  lease `Update` alone.
- Otherwise it also carries the gap `(lastAck, t2)`. While the node is not
  `Ready`, the gap always starts at `G0`, the stored `renewed_ms` that the claim
  found, so every renewal extends the one start gap.
- The lease condition is `incarnation = :own AND nonce = :own AND
  renewed_ms < :t2`.
- The gap condition is `attribute_not_exists(seq) OR end_ms < :t2`, so a retry
  with the same `lastAck` **extends** the same gap and never collides with its
  own earlier commit.

**Outcomes.** The lease and the gap commit in one transaction, so the stored
`renewed_ms` can never pass an unrecorded gap. Memory changes only after a
commit is known.
- **Success.** `lastAck = t2`; the gap, if any, is merged into memory; a new
  proof is published (section 6).
- **Failure or ambiguous answer.** The node reads the lease back with
  `ReadLease`. It does not use `CancellationReasons`, because `appendTx` does
  not request the old item (`store/index.go:101-160`).
  - The read fails: nothing changes, and the next period tries again.
  - Foreign `nonce`: a newer process holds the lease. The node is
    **superseded** and stops serving (section 7).
  - Own nonce and `renewed_ms = t2`: the transaction committed, gap included.
    It is adopted as a success.
  - Own nonce and any other value: it did not commit. A late commit of an
    earlier timed-out attempt can still land. It holds a stamp below the next
    `t2`, so the lease condition still passes, and its gap, if any, has the
    same `start_ms = lastAck` and is extended by the next attempt.

**Suspect gaps (clock steps and freezes).** Each committed renewal records the
monotonic clock too. For a gap seen by a running process, if the wall delta
exceeds the monotonic delta by more than S, the gap is **suspect**: a forward
wall step or a VM freeze, which cannot be told apart. A suspect gap is credited
in full (its wall length) and **never aborts**. A real hypervisor freeze longer
than T therefore credits and does not abort, which harms no player. A process
pause (SIGSTOP, GC, cgroup freeze) advances both clocks, so it is a real gap.
Operations must slew clocks, not step them, and the node alarms on every
suspect gap.

**Start.**
1. Wait for the host clock to report synchronised (Linux `adjtimex` without
   `STA_UNSYNC`; skipped on a development host). Otherwise a VM that boots with
   a stale clock and is stepped by NTP later would record a long real gap.
2. `ReadLease`. A failed read means no serving, retried every P.
3. A missing lease is a first start only if `ReadGaps` is empty **and** every
   `live_games` shard reads empty (GS-8 stage 1). Otherwise the node refuses
   to serve and alarms, and an operator resolves it.
4. If the host clock is behind the stored `renewed_ms`, wait until it passes.
5. Claim the lease: `incarnation + 1` and a random `nonce`, conditional on the
   stored incarnation and nonce. The claim carries the gap `(G0, t2)`, the
   node's downtime, where `G0` is the stored `renewed_ms`. A restart gap has no
   monotonic baseline, so it is always real. An ambiguous claim is resolved by
   reading the lease back, as above. A retry reuses the same nonce, so a late
   commit of the first attempt and the retry are the same claim.
6. `ReadGaps` into memory. From now on every new gap is merged on commit.
7. **Adoption is part of the start gap.** The node is not `Ready` until GS-8
   stage 1 adoption has built every live game. Until then:
   - the snapshot has `Ready = false`, so `fresh` is false and no clock decision
     is made (section 6), although the lease is renewed and the node is not
     superseded;
   - every renewal carries `(G0, t2)`, so the start gap grows to the moment the
     node is ready. The players of an adopted game are credited for the time
     before its session existed, and a crash during adoption loses none of it,
     because the lease and the gap commit together;
   - sessions are built without a started clock if they need one (section 6),
     and the outage abort rule (section 8) already applies to them.
   When adoption completes, the next renewal sets `Ready = true`, publishes the
   proof, and then closes `Restored()` (section 7 gives the order). An adoption
   that runs longer than T aborts every live game, like any gap. This supersedes
   the `OwnerClaimed.gap_ms` credit of GS-8 (§6.4, line 257) for stage 1; stage 2
   must scope it by owner (section 12).

**First deploy of this build.** Live games exist and no lease does. The
operator runs a seeding command that writes `lease/{cluster}` with
`incarnation 0` and `renewed_ms = now`. The downtime of that one deploy is not
creditable.

**Incarnation.** The incarnation is the persisted counter above. GS-8 §4.1
describes a random incarnation per process. This spec amends it, and
`owner_incarnation` in `live_games` stores this counter as a decimal string.
GS-8's `OwnerClaimed.gap_ms` (§6.4) is superseded by the service-record gaps.

**Switch.** Recording the lease and gaps is always on. **Applying** them is
behind one cluster-level switch, `enabled_at_ms` in the lease. An operator
command sets it to now (on) or to 0 (off). The command is an `Update` with
`attribute_exists(seq)`, so an absent lease is never created by it.
- Every node reads the lease at start and every 10 s.
- **Off** means exactly today's behaviour: no proof check, no credit, no
  outage abort, no `SERVICE_STATUS`. Turning it off mid-game takes back any
  credit already shown, which is acceptable only as an emergency rollback.
- **On** clips every gap to start no earlier than `enabled_at_ms`, so turning
  the switch on never credits or aborts a game for history.
- It is turned on only after the codec and every decoder know `0x0B`, `0x07`
  and `0x8B` (section 9).

## 5. The clock projection

`game.Gaps` (new) is a sorted list of half-open intervals
`[start, end)` with a `suspect` flag each, already clipped to `enabled_at_ms`.
`Gaps.Overlap(a, b)` is the length of the union of gaps inside `[a, b]`. It
uses binary search, so a long gap list stays off the hot path. With the switch
off, `Gaps` is empty and every function below returns exactly what it returns
today.

The fold and the stored events do not change. `LastChangeTS` stays a real
timestamp, set by `ClockStarted`, `MovePlayed` and `TakebackRewound`
(`game/statemachine.go:390,424,622`), and `ClockAfterMs` is stored per move as
today.

| Function today | Change |
|---|---|
| `State.ElapsedMs(nowMs)` (`game/statemachine.go:185`) | Takes `g Gaps`: `max(0, (nowMs - LastChangeTS) - g.Overlap(LastChangeTS, nowMs))`. Its three callers pass the snapshot's gaps: the move charge (`game/pipeline.go:274`), `firstMoveTimedOut` (`:352`) and `sideToMoveFlagged` (`:398`). |
| `State.ClocksAt(nowMs, tc)` (`game/statemachine.go:196`) | Takes `g Gaps` and passes it to `ElapsedMs`. |
| `NextTickMs(s, opts)` (`game/pipeline.go:361`) | Takes `g Gaps`. Both deadlines (`:371,382`) start at `d = LastChangeTS + limit + 1`. Let `L = LastChangeTS`. Walk the gaps in order of start, clip each to `[max(g1, L), g2)`, skip an empty clip, and for each one with `g1' < d` set `d = d + (g2 - g1')`. A gap that starts exactly
at `d` does not move it: `ElapsedMs` at `d` overlaps that gap by 0 ms and flags
at `d`. One pass. This covers a gap that lies wholly between `L` and `d`, a gap that starts before `L`, and adjacent gaps, because `d` grows as the walk goes on. A gap only pushes a deadline later, so a timer that fired early just finds nothing to do, and the session republishes (`session/session.go:97`). |
| `SnapshotOf(st, now, tc)` (`session/commands.go:45`) | Takes the proof snapshot (section 6). With a fresh proof `effNow = now`; without one `effNow = renewedMs`, so a STATE sent during an unrecorded gap shows the clock frozen at the last proof. `ServerT = effNow`. The ended-game view (`transport/transport.go:425`) passes no gaps; no clock runs on an ended game. |
| `moveTimeMs` (`game/pipeline.go:317`) | Unchanged. The receipt time `r` is clamped as today, and the charge is `ElapsedMs(r, g)`: a gap before `r` is not charged, and the time between `r` and the decision is charged to nobody, as today (#183). |

- **Only the running clock gains.** The opponent's clock was not running.
- **Takeback.** A rewind sets `LastChangeTS` to the rewind time, so older gaps
  fall outside `[LastChangeTS, now]`.
- **Creation time.** `GameCreated` carries no timestamp, and adding one would
  change a frozen event. GS-8 step 2 makes game ids UUIDv7 (today UUIDv4,
  `matchmaking/create.go:163`). The registry reads `CreatedMs` from the id and
  passes it in `session.Config`. A game created before GS-8 step 2 has a
  UUIDv4 id, and its `CreatedMs` is **0**, so the abort rule treats it as
  having lived through every gap. That is safe, because every gap is clipped to
  `enabled_at_ms` and a game that existed before the switch was turned on did
  live through any gap after it. Such games are adopted, never refused. GS-8 step 2 mints UUIDv7 ids from its
  first commit on (section 13 orders it first), so a v4 id is never minted after
  it. Tests
  pass `CreatedMs` explicitly.
- **Fair play.** Think time is the `ServerTS` difference minus `Overlap` over
  the same interval. No consumer exists in this tree yet. The first one must
  do this.
- **Retention.** A node loads every gap at start (section 4). Pruning gaps older
  than the oldest live game's `CreatedMs` is later work.

## 6. The proof, inside `Decide`

**The snapshot.** `service.Node` publishes one immutable value through an
atomic pointer:

```
Snapshot{ RenewedMs, Gaps, LongRealStartMs, Enabled, Ready, Superseded }
```

It is replaced after every committed renewal and every lease reread. The
session reads it with `Config.Service.Snapshot()` (new) and passes it to
`Decide` as a new argument. `exec` already reads `nowMs` again on every retry
(`session/session.go:220`). It now takes a new snapshot first on every retry,
then reads `nowMs`, so a pause between the two makes the proof look older,
never fresher.

**Proof.** `fresh(snap, nowMs)` is `snap.Ready && !snap.Superseded &&
0 <= nowMs - snap.RenewedMs <= S`. A backward wall step gives a negative age
and therefore no proof. A node that is still adopting (section 4, Start) is not
`Ready`, so it has no proof.

**Re-check before the write.** For a decision that was made **under the proof**
(the four commands of step 2, after their clock-free rows), `exec` takes a
fresh snapshot and reads `nowMs` again immediately before `Append` or
`AppendBatch`, and returns `ErrNoProof` if `fresh` no longer holds. It does not
apply to an outage abort or to any command that needs no proof: the abort reads
only stored facts, so a lapse of the proof must not turn it into `SERVER_BUSY`. This narrows
the window in which a pause between `Decide` and the write can let a stale
decision land to the latency of the append itself. It cannot close it, and the
residual is the one section 1 states.

**Order inside `Decide` (new, `game/pipeline.go:185`).** With the switch on:
1. **Outage abort.** If `outageAbortDue(s, createdMs, snap)`, `Decide`
   returns the outage abort (section 8) **for every command except
   `CmdFinishHalfTerminal`**, resign and the draw and takeback commands
   included. This replaces v0.5's queued abort. It does not
   depend on any queue, so no released command can get past it, and a rebuilt
   session runs the same rule.
2. **Proof.** These commands need `fresh`, or `Decide` returns
   `game.ErrNoProof` and nothing else: `CmdMove`, `CmdTickFlag`,
   `CmdAcceptTakeback` and `CmdStartClock`. They are the commands that read a
   running clock or set its baseline. **The check sits at the first clock read
   or baseline write inside each decide function, after its clock-free rows.**
   For `decideMove` (`game/pipeline.go:227-260`) that is after the unknown-game,
   seat, duplicate re-ack, game-over, stale-ply and not-your-turn rows, and just
   before `moveTimeMs` (`:266`). A re-ack of a move that landed, a move on an
   ended game and every other cheap reject are therefore answered with no
   proof and are never `SERVER_BUSY`. `SERVER_BUSY` is returned only for a move
   that would otherwise have been applied, so it still guarantees "not
   applied". `decideTick` on an ended or unstarted game returns its no-op
   without proof.
3. Today's decision, with `snap.Gaps` passed to the clock functions of
   section 5.

`CmdResign`, `CmdAbort` (a client abort before ply 2), the draw commands,
`CmdProposeTakeback` and `CmdDeclineTakeback` read no clock, so they need no
proof. A resign is still never refused for lack of proof.

**A voided game cannot end by resign (owner-visible choice, v0.7).** v0.6
exempted resign from the abort rule and also said a voided game's resign is a
no-op. Both cannot hold: with the exemption, `decideResign`
(`game/pipeline.go:421`) would end a game that a long gap had voided with a
decisive result, and a rated loss would stand for a game the server could not
run. v0.7 drops the exemption. The abort is returned for the resign, and
`handleEnding` already broadcasts GAME_END when the state says `Ended`
(`transport/handlers.go`, section 0), so the resigning player sees an aborted,
unrated game.

**Every command is answered when the rule fires.** `MoveOutcome` gains
`Voided bool` (`game/pipeline.go:51`): the game was ended by the outage rule
and the move was not processed. `Decide` sets it for `CmdMove`, and `handleMove`
adds `case out.Voided: s.broadcastGameEnd(p.GameID)` beside `Flagged`, and the
comment on `MoveOutcome` ("exactly one of Accepted / Rejected / Flagged") adds
`Voided`
(`transport/handlers.go:526-588`). Without it a MOVE gets no reply, because
`handleMove` has no case for an outcome with no flag set. The ticker, the draw
and takeback handlers, resign and abort already reply from the state.

**`ErrNoProof` is an outcome, not a failure.**
- `exec` returns it unchanged. It is not a write error, so it never sets
  `stale` (`session/session.go:240-256`).
- `handleMove` maps it to `MOVE_REJECTED{SERVER_BUSY}` (section 7).
- `handleEnding` maps it to `ERROR{SERVER_BUSY}` for a takeback accept.
- The flag ticker does not count it as an error or log it
  (`transport/handlers.go:335-346`). Its loop changes in five ways:
  1. **No lost wakeup.** At the top of every iteration it takes
     `r := node.Restored()` **before** it reads `NextTick` and runs
     `TickGameOver`. `Restored()` returns the current channel, which is closed
     once on each restoration and then replaced. A restoration that lands after
     the read closes `r`, so the ticker cannot sleep through it.
  2. **`r` is a case of the `select` in every state**, armed or not
     (`transport/handlers.go:285-326`). An idle game whose clock cannot run out
     still wakes, so a game voided by a long gap is ended, and a game whose
     deadline moved is re-armed.
  3. **Bounded wake.** A wake on `r` waits a random delay in a window of
     `min(30 s, max(1 s, N / R))`, with `N` live games and `R` outage-abort
     writes per second (default 200, a tuning value for G-E). The wait is a
     `select` on the delay timer, server shutdown and `sess.Closed()`, so it
     never delays a shutdown or outlives its session. The window is capped
     because pacing is the token bucket's job (below), not the jitter's.
  4. **`ErrNoProof` parks the ticker; it neither spins nor loses a wake.** When
     `TickGameOver` returns `ErrNoProof`, the loop does **not** go back to the
     top. It waits on the `r` it took **before** that failed check, with no
     timer armed, plus a fallback `retryAt = now + iv` (one tick interval,
     `handlers.go:380-386`'s "never spin" rule extended to this path). A
     restoration that landed after `r` was taken has already closed it, so the
     wait returns at once; a wake that is somehow missed costs one interval.
     Today's guard runs only after a nil error, so without this rule a due,
     past deadline would re-arm with `wait = 0` and call `TickGameOver` in a hot
     loop for the whole pause, once per game.
  5. **A wake on `r` falls through.** Today only `t.C` falls through to the
     check, and `TickChanged` does `continue` (`handlers.go:320-324`). A wake on
     `r` runs `EnsureClockStarted` and then the check, as the timer does.
- **`Restored()` is an atomic swap, then close.** `Node` holds the channel in an
  atomic pointer. A restoration stores a new channel and then closes the one it
  replaced, so a caller never receives a closed channel that will not be
  replaced. With a nil `Service` (switch off) `Restored()` returns a nil
  channel, which never fires in a `select`.
- **Bounded outage-abort writes.** One token bucket at `R` (default 200 per
  second) guards the two **sweep** sources of outage aborts: the ticker's wake
  (above) and adoption. Adoption (`NewSession` during GS-8 stage 1 adoption)
  takes a token before it builds a session whenever the snapshot's
  `LongRealStartMs` is non-zero, and the adoption pool has a fixed width
  (default 16). An abort that a player's own command triggers is not limited
  by the bucket: its rate is the players' command rate, and it is the same
  single write the sweep would have made. With `N = 100k` and `R = 200` a full
  sweep takes about 500 s. That is accepted: every voided game already refuses
  everything but the abort, so the delay costs nobody a clock decision, and an
  idle game has no running clock because its clock is unstarted or its mover is
  frozen by the gap. The test measures the write rate.
- `NewSession` runs `CmdFinishHalfTerminal` and then `CmdStartClock`
  (`session/session.go:187-192`). Today any error from either fails the build.
  v0.7 changes it: `errors.Is(err, game.ErrNoProof)` from `CmdStartClock` is not
  an error, and the session is returned with its clock not started.
- **Starting the clock later.** A session whose clock is not started has no
  tick deadline (`NextTickMs` returns `ok = false`, `game/pipeline.go:361`), so
  nothing would ever start it. `Session` gets `EnsureClockStarted(ctx)` (new),
  which runs `CmdStartClock` and is a no-op for a started game. The ticker calls
  it first on every wake from `r`, and `NextTick` is re-read after it.

**Wiring.** `service.Node` reads and writes through a small `service.LeaseStore`
interface (`ReadLease`, `ReadGaps`, `Renew`). `store.Store` and `game.MemStore`
(`game/store_stub.go:19`) implement it. `session.Config` gains `Service` and
`CreatedMs`. A nil `Service` means the switch is off, so every existing session
test is unchanged. The `game` package tests (`decide_test.go`,
`nexttick_test.go`) call `Decide`, `ElapsedMs` and `NextTickMs` directly; their
signatures change, and they are edited to pass empty gaps and a nil snapshot.

## 7. Serving, pausing, and the client's pending move

**The server never holds a command.** `Handle` routes each frame inline
(`transport/dispatch.go:141-150`), so a parked command would block the same
connection's resign and resync frames. A command the server cannot decide is
answered at once:
- `MOVE` with no proof: `MOVE_REJECTED{reason: SERVER_BUSY}` with the current
  ply. `Decide` returns `ErrNoProof` before any write and `handleMove` maps it, so
  the move was **not applied**.
- `TAKEBACK_ACCEPT` with no proof: `ERROR{SERVER_BUSY}`, through
  `mapSessionErr` (`transport/handlers.go:732`).
- `SEEK` with no proof: `ERROR{SERVER_BUSY}`. The check sits next to the
  rated-play gate (`transport/handlers.go:123`), before any pool lookup. **This
  is the only creation path.** `matchmaking.Seek` is called only from
  `handleSeek` (`transport/handlers.go:156`), it creates a game only inside that
  call (`matchmaking/create.go:142,162`), and no background goroutine pairs
  queued seekers. A queued seeker is paired only by a later seeker's `Seek`,
  which is gated. A test pins that no other caller exists. A process pause
  between the gate and the append can still commit a game stamped before the
  pause; section 8 says what that costs.
- `RESIGN`, the draw commands, `TAKEBACK`, `TAKEBACK_DECLINE` and a client
  `ABORT` need no proof and are decided as usual, except that the outage abort
  rule comes first (section 6). `RESYNC` and STATE are served with the frozen
  clock (section 5).

**The pause notice.** `service.Node` exposes `Paused()` and `Restored()`
channels. A watcher checks every P.
- **Paused means "no fresh proof"**, not "the age passed S": `!fresh(snap,
  now)`, so it also covers a backward wall step and a node that is not `Ready`.
  The transport then sends `SERVICE_STATUS{paused: true, since_ms: RenewedMs}`
  to every authenticated connection, once per pause.
- **On restoration, in this order:** (1) the node publishes the new snapshot;
  (2) it closes `Restored()`; (3) the transport sends each seated connection a
  STATE for its game; (4) it sends `SERVICE_STATUS{paused: false}`. A client
  that sees `paused: false` therefore never gets `SERVER_BUSY` for a reason that
  still holds. The fan-out goes through each connection's bounded outbound
  queue (`transport/pump.go:66`, `Write`), so a slow client cannot stall it (a
  full queue closes the connection, so a lost `paused: false` cannot strand a
  client: it reconnects and AUTHENTICATE reports the status), and it is
  paced with the same window as the ticker wake (section 6).
- **A connection that authenticates during a pause** gets
  `SERVICE_STATUS{paused: true}` **before** its STATE (the STATE push of
  AUTHENTICATE, `transport/transport.go`). A client that reconnects after a
  restart, a deploy or a process pause therefore learns the state of the server
  from the first two frames, and `paused: false` follows when service returns.
- During a process pause, restart or deploy the server cannot send. Clients see
  a dropped connection and reconnect. Section 7's client rules cover that case.

**The client holds the move (owner decision 2026-11-27).** This needs a change
to WEB-F01 and the other clients:
A **pending move** is `{ply, uci, position}`, where `position` is the board
position (FEN) it was made on, and it is **unsent** until a `MOVE` frame carrying
it has been written to the socket. There is at most one pending move and at
most one send in flight.
1. On `paused: true`, or while the connection is down: stop the local clock tick
   and show "Server problem, clocks paused". A move the player makes is shown
   on the board as pending and is **not sent**.
2. **Release.** The client keeps the latest STATE and the latest
   `SERVICE_STATUS` of the connection. It evaluates the release rule on **both**
   events: an unsent pending move is sent when the latest STATE has the pending
   move's ply **and** position and the latest status is not `paused: true`. The
   restoration order is STATE, then `paused: false` (section 7), so the move is
   normally sent on `paused: false`; a reconnect during which the server sent no
   `paused: true` releases it on the STATE. If the latest STATE's ply or
   position differs, the pending move is discarded: the game moved on (an outage
   abort, a resign, a takeback).
3. On `MOVE_REJECTED{SERVER_BUSY}` for the move in flight: it was not applied.
   It becomes unsent again, and is sent on `paused: false`, or after 1 s,
   doubling up to 8 s, until a `GAME_END`, a `MOVE_ACCEPTED` for its ply, or a
   STATE that differs arrives.
4. **Any other reply discards the pending move and resyncs.** A
   `MOVE_REJECTED` with any reason other than `SERVER_BUSY`, and any `ERROR`
   (including `ERROR{INTERNAL}`, "move not confirmed", `transport/handlers.go:531`)
   for the move in flight, clears it and sends RESYNC. The STATE that comes
   back is the truth.
5. **Never resent after a send that got no reply.** If the connection drops after
   the frame was written and before any reply, the move may have landed. The
   client discards it and reads the STATE of the reconnect. This is why the
   match is on position as well as ply: after a takeback the same ply recurs,
   and a move that already landed must not be applied a second time.
6. A pending move is lost if the app closes. The player's clock was stopped, so
   the cost is one move to play again.

This is the **one exemption** from PROTOCOL's rule that a client never resends
a `MOVE` (`docs/specs/S2-realtime/PROTOCOL.md:312`). It is safe for three reasons:
`SERVER_BUSY` is returned before any write, and only for a move that would have
been applied (section 6); the client resends only a move it
knows was not applied (rules 3 and 5); and a duplicate of a move that did land
is still re-acked by the duplicate-MOVE rule (`game/pipeline.go:234-251`). No
other rejection or error allows a resend.

**Superseded.** A node that finds a foreign nonce on its lease (section 4)
stops serving: it stops its listener, closes every session
(`transport/transport.go:356`, `Registry.CloseAll`) and every connection, and
exits. A zombie can still decide for up to S under its last proof. The event
store's seq condition keeps every stream consistent in that window, and
section 1 states the residual.

## 8. The outage abort (gap longer than T)

T is **2 minutes**. A **real** gap longer than T voids every game it overlapped.
A suspect gap never aborts.

`outageAbortDue(s, createdMs, snap)` (new, `game` package) is true when all of
these hold:
- `s.Phase` is `Matched` or `Live`,
- `s.HalfTerminal == nil` and `s.LastMoveTerminal() == nil` (a game already
  decided is finished by `CmdFinishHalfTerminal`, never aborted),
- `snap.LongRealStartMs > createdMs`. `LongRealStartMs` is the start of the
  latest **merged real** interval longer than T (0 when there is none), computed
  once when a gap is committed, so the check is O(1) and the gap list is not
  scanned on every `Decide`. A game created before the gap started lived
  through it. A game created at or after the start was created by a commit
  inside the interval, which can happen only when a pause fell between the
  `SEEK` gate and the append (section 7); that game is not voided by that gap.
  A UUIDv4 game has `CreatedMs = 0` and is voided by any such gap.

**Merging for the abort.** Two interval sets are kept. The **credit set** holds
every gap, suspect or not, merged where intervals overlap or abut
(`end == start`); `Overlap` and the clock functions read it. The **abort set**
holds the real gaps only, merged the same way, and a merged interval longer
than T voids games. A suspect gap never joins the abort set, so a one-hour
forward clock step next to a 1 s real gap does not abort anything. The start
gap (section 4, Start) is always written `suspect = false`, and every
renewal that extends it rewrites that. A crash during adoption leaves
`[G0, t_last)` and the next claim writes `[t_last, t2')`, two items that abut:
the merged interval is their sum, so a crash loop inside the start window
still adds up and aborts when the total passes T. Serving time between two
crashes separates the gaps, and they do not add.

**The decision.** `Decide` returns `Aborted{By: SystemActor}` followed by
`GameEnded{ResultVector: Draw, Draw; Termination: ABORT_OUTAGE}` with
`Effect = EffectEnded`. It is written through the GS-8 stage 1 ending path, so
the index deletes and the `user_recent_games` row commit with it in one
transaction, and a player who was offline gets the result at their next
AUTHENTICATE.

**Who can produce it.** No command carries it. Only the rule in `Decide`
returns it. A client `ABORT` is `CmdAbort` with the authenticated seat
(`transport/handlers.go:629`) and always ends `TermAbort`
(`game/pipeline.go:387`).

**The fold.** `knownTermination` (`game/statemachine.go:715`) adds `0x0B`.
`State` gains `prevSystemAbort bool`. `Evolve` sets it on `Aborted{0xFF}` and
clears it on every other event. `applyGameEnded` accepts `ABORT_OUTAGE` only
when it is set and the result vector is all draws. The existing first-move
abort also writes `Aborted{0xFF}` (`game/pipeline.go:332`), followed by
`GameEnded{ABORT}`, which this check does not touch.

**How every voided game ends.**
- When a renewal records a real gap longer than T, `Restored()` fires and every
  live session's ticker wakes, armed or not (section 6), after its jitter and
  through the abort token bucket, and sends `CmdTickFlag`. `Decide` ends the
  game, and the ticker broadcasts `GAME_END` and drops it as for any ending
  (`transport/handlers.go:335-389`). Until then a player's own command for that
  game gets the same abort, so the delay costs nobody a decision.
- An abort write that fails is retried by the ticker's backoff (`:340-346`).
  Until it lands, every other command for that game hits the same rule first,
  so no move, flag or takeback gets through.
- A rebuilt session runs `CmdStartClock` in `NewSession`
  (`session/session.go:190`). The rule fires there, so a voided game is ended
  before its first STATE or MOVE is served, whether or not a proof holds. The
  rule needs only stored facts.

**Unrated.** `ResultVector` is all draws, as for `ABORT`. Every consumer keys on
the termination. A test checks that rating, fair play, replay and the
ended-game view treat `0x0B` as unrated.

**A slow deploy** longer than T aborts every live game. This follows from the
owner's choice of T.

**The join timer** (`transport/handlers.go:442`) is process-local and GS-8 arms
it only for a freshly paired game (GS-8 §4.2). With the switch on, when it fires
it waits for a fresh proof, then extends itself by the gaps' overlap since it
was armed. It aborts only if the extended deadline has also passed and no seat
has joined. No new command is needed.

## 9. Wire, clients, rollout

- **No new game event.** The service record is not a game event, so the frozen
  event set is unchanged and #187 is not a prerequisite. An old node folding a
  stream that ends `0x0B` rejects it (`game/statemachine.go:663`): fail closed.
- **Termination `0x0B ABORT_OUTAGE`.** `protocol/codec/tests/negative.rs:766`
  uses `0x0B` as its unknown termination and moves to `0x0C`.
- **`MOVE_REJECTED` reason `0x07 SERVER_BUSY`** and **`ERROR` code `0x07
  SERVER_BUSY`** (codes `0x01`-`0x06` used, `codec/message_seam.go:127-132`).
- **`0x8B SERVICE_STATUS {paused: bool, since_ms: u64}`**, server to client. It
  is declared **before `Error`** in both the Rust enum and the Go seam
  (`codec/message_seam.go:135-148`), as ADR-17 and ADR-19 did, so `Error`'s
  serde index moves from 10 to 11. The seam test and its corpus map
  (`corpusServerMsgs()`) gain the new code and the coverage guard is bumped.
- **ADR-20** bumps the protocol for these four. The ADR-19 process applies:
  review, golden vectors, decoders in Rust, Go, TypeScript, Swift and Kotlin.
- **Clients** implement section 7's pending move and stop the clock display
  while paused. "Game aborted: server problem, no rating change" for `0x0B`.
- **Rollout.** Ship the server with the switch off (today's behaviour), then
  the codec and every client, then turn the switch on. After a game has ended
  `0x0B` there is no rollback to a build that does not know it.

## 10. Tests (the #203 harness gets an outage phase)

Store and lease (`service`, `store`; contract suite on `MemStore`, the
in-process fake and DynamoDB Local):
- A renewal with a gap commits the lease and the gap together. Fail it at each
  item: the stored `renewed_ms` never passes an unrecorded gap.
- The reply is lost after a commit: the read-back sees the own stamp and adopts
  it, gap included.
- A timed-out attempt commits late: the next renewal still passes, and its gap
  extends the same `start_ms` item.
- A read-back after a backward wall step sees the own nonce and an older
  stamp: not adopted, no gap.
- A foreign nonce stops serving. A failed lease read changes nothing.
- The renewal `Update` keeps `enabled_at_ms`. The enable command on an absent
  lease creates nothing. A claim retry reuses its nonce.
- Every read is consistent, and the fake rejects what DynamoDB rejects.

Start:
- A restart under a different node id finds the lease and records the downtime
  as one real gap.
- A missing lease with gaps or live games refuses to serve. A first start
  records no gap. The seeding command allows the first deploy.
- A clock behind the stored lease waits. An unsynchronised clock waits.

Clock projection (`game`, pure):
- `Overlap` over adjacent, overlapping, duplicate and out-of-order gaps gives
  the same answer. Binary search over 10,000 gaps.
- A store outage of D under T: no flag for the time lost, and the mover's clock
  afterwards equals the clock before. A rebuilt session agrees with the live
  one at the exact gap boundary (I3).
- A move received before, inside and after a gap is charged only its own time.
- `NextTickMs` with a clock expiring 1 ms after a gap start, with several
  adjacent gaps, with a gap wholly between `LastChangeTS` and the deadline, and
  with a gap that starts before `LastChangeTS`, ends in one pass and matches a
  brute-force replay. The first-move deadline moves the same way.
- An older gap does not affect a rewound clock.
- With the switch off, every function returns exactly what it returns today
  (the existing suites run unchanged with a nil `Service`).
- A gap straddling `enabled_at_ms` is clipped. Switching on with an old gap
  over T neither credits nor aborts. Off, on, off.

Proof inside `Decide`:
- A fake clock jumps between the snapshot and `nowMs`: `ErrNoProof`, no flag.
- A seq-conflict retry takes a new snapshot.
- `CmdMove`, `CmdTickFlag`, `CmdAcceptTakeback` and `CmdStartClock` without a
  proof return `ErrNoProof` and write nothing. Resign, draws, propose and
  decline takeback, and a client abort proceed (and are aborted by the rule when
  a long gap has voided the game).
- A fresh proof that expires between `Decide` and the append: `exec`'s re-check
  returns `ErrNoProof` and nothing is written.
- A node that is not `Ready` (adopting) has no proof; the lease is still
  renewed and the start gap grows with every renewal.
- `ErrNoProof` never sets `stale`.
- Every handler outcome replies. Mutation: treating `ErrNoProof` as an empty
  decision leaves a MOVE unanswered, and the test fails.

Abort:
- A real gap over T: every live and every matched game ends `ABORT_OUTAGE`,
  unrated, through the indexed ending write. A game past ply 2 still aborts.
- A half-terminal game and a game whose last move was terminal are finished,
  not aborted.
- **Every command kind gets a reply when the rule fires** (table-driven): MOVE
  (`Voided` and GAME_END), tick, resign, client abort, draw offer, accept and
  decline, takeback propose, accept and decline. Mutation: no `Voided` case in
  `handleMove` leaves a MOVE unanswered, and the test fails.
- Resign on a voided game gets an unrated `ABORT_OUTAGE`, never a decisive
  result.
- The abort write fails once: a MOVE, a tick and a takeback accept for that
  game all get the abort, never their own decision. It lands on retry.
- A rebuild after a crash between the gap and the aborts ends the game in
  `NewSession`, before any STATE.
- A suspect gap over T credits and never aborts. A real pause (SIGSTOP in the
  harness) over T aborts.
- A client frame cannot produce `ABORT_OUTAGE`. The fold rejects
  `GameEnded{ABORT_OUTAGE}` after anything but `Aborted{0xFF}`, including
  `Aborted{0xFF}`, another event, then `GameEnded{ABORT_OUTAGE}`.
- Rating, replay and the ended-game view treat `0x0B` as unrated.

Serving and clients:
- A MOVE without a proof gets `MOVE_REJECTED{SERVER_BUSY}` and nothing is
  appended. Sent again after the proof returns, it is applied once and charged
  nothing for the outage. Sent twice after it landed, it is re-acked.
- A SEEK without a proof is refused and enqueues nothing. A grep-based test
  fails if `matchmaking.Seek` gains a second caller.
- A MOVE that is a duplicate of one that landed, a MOVE on an ended game, a
  stale-ply MOVE and a not-your-turn MOVE without a proof get their normal
  reply, never `SERVER_BUSY`. Only a move that would be applied gets BUSY.
- An outage-abort decision does not return `ErrNoProof` when the proof lapses
  between `Decide` and the append.
- RESIGN on the same connection right after a BUSY is read and applied.
- STATE without a proof shows `server_t` equal to the last proof.
- Pause and restore: every seated connection gets `paused: true`, then a STATE
  and `paused: false`, in that order.
- The ticker without a proof logs nothing, backs off nothing, and wakes on
  `Restored()`. A restoration that lands between the ticker's `NextTick` read and
  its sleep still wakes it (fault injection at that point).
- `ErrNoProof` in the ticker: no hot loop (the call count over a 10 s pause with
  1,000 due games stays near one per interval), and a restoration injected
  between the failed check and the wait still wakes it at once.
- `Restored()` swap: a caller never receives a closed channel that is not
  replaced; with a nil `Service` it returns a channel that never fires. A wake
  on `r` runs `EnsureClockStarted` and the check. Shutdown and `sess.Closed()`
  interrupt the jitter wait.
- An idle game with an unarmed ticker (clock not started, or no deadline) and a
  real gap over T is ended after restoration. A game whose session was built
  without a clock because of `ErrNoProof` starts it through
  `EnsureClockStarted` and runs its first-move timeout.
- A connection that authenticates during a pause gets `paused: true` before its
  STATE, and `paused: false` after the restoration STATE. The new snapshot is
  published before `Restored()` closes and before `paused: false`. A backward
  wall step shows the pause notice.
- The join timer during a pause extends by the gap and does not abort a game
  whose seat joins after restore.
- A superseded node stops serving and closes every session.
- Recovery herd: N live games, a gap over T, every game aborted at no more than
  R writes per second (token bucket), jitter window `max(1 s, N / R)`, and the
  STATE fan-out measured against the outbound queue bound. Adoption of N voided
  games takes a token per game, and the measured abort write rate stays at or
  below R.
- Slow adoption: a node that takes longer than the first-move timeout to adopt
  N games credits each game the time until it is `Ready`, and a crash during
  adoption loses none of it. An adoption longer than T aborts every game.
- Merged abort set: a 90 s real gap followed by an abutting 90 s real gap
  aborts; two 90 s gaps with serving time between them do not. A real 1 s gap
  abutting a one-hour suspect gap does not abort. A restart during adoption
  loses no time and the start gap stays real. `LongRealStartMs` equals a brute
  force over the gap list.
- A game created inside a gap (a pause between the SEEK gate and the append) is
  not voided by that gap, and one created before it is.
- A game with a UUIDv4 id has `CreatedMs = 0`; with the switch on, a gap after
  `enabled_at_ms` over T aborts it, and a gap before it does nothing.
- Web client (amends WEB-F01's contract tests): STATE then `paused: false`
  releases the pending move on the second event, and `paused: false` then a STATE
  also releases it. Any `MOVE_REJECTED` other than BUSY and any `ERROR` for the
  move in flight discards it and sends RESYNC. A pending move is sent on
  `paused: false` at the same ply and position, and discarded at a different ply
  or position. It is resent after a BUSY with backoff, never after a send that
  got no reply, and never after any other error. A takeback to the same ply with
  a lost ack does not apply the move twice. A reconnect during a pause releases
  nothing until `paused: false`; a reconnect with no pause releases an unsent
  pending move.

Codec: golden vectors for `0x0B`, reason `0x07`, error `0x07` and
`SERVICE_STATUS`; parity in every decoder; `negative.rs` updated.

Mutations, each recorded in the PR: overlap ignored; `NextTickMs` shifting only
when the deadline lies inside a gap, or shifting for a gap that starts exactly at
the deadline; `ErrNoProof` re-arming with a zero wait; BUSY returned for a
re-ack; ticker taking `Restored()` after the
check; ticker without `Restored()` in the unarmed case; resign exempt from the
abort rule; flag decided without a
proof; snapshot taken after `nowMs`; lease written without its gap; gap
condition without the extension clause; read-back adopting any own-nonce
stamp; abort rule after the proof check; abort rule skipped for `CmdMove`;
`prevSystemAbort` not cleared.

## 11. Designs rejected, and why

| Design | Why not |
|---|---|
| A `ClockCredited` event per game (v0.1, v0.2) | N writes at recovery, a race with the flag, double credit on rebuild, a watermark with ordering rules. Two review rounds found 12 blockers, all from here. |
| Counting failed writes to detect an outage | It misses pauses and partial throttling and can starve flags. |
| A separate health table | It can be healthy while the event table is down, so players would be flagged unfairly. |
| A Lichess-style fixed bonus | It cannot cover a crash, and it gives one side time the other never gets. |
| A cumulative credit cap that aborts (v0.3) | It breaks I3 at the boundary and answers a problem exact credit does not have. |
| The server holds a move in a gate (v0.4, v0.5) | `Handle` routes inline (`transport/dispatch.go:150`), so the gate needed its own queue, ordering rules, a deadline and a bound, and each review found a hole in them. The player gets the same result when the client holds the move (section 7). |
| Enqueuing the outage abort before the gate opens (v0.5) | The command channel is unbuffered (`session/session.go:169`), so nothing is enqueued before a sender blocks, and a failed abort let play through. A rule in `Decide` has neither problem. |
| Resign exempt from the outage abort (v0.6) | It contradicted "a voided game's resign is a no-op" and would end a game the server could not run with a decisive, rated result (section 6). |
| Publishing proof while adopted games are still being built (v0.6) | Those players' clocks ran with no session and nobody credited them. Adoption is now part of the start gap (section 4). |
| Reading `CancellationReasons` to classify a failed renewal (v0.5) | `appendTx` does not request the old item (`store/index.go:101-160`), and a backward step fails the condition with the node's own nonce. Reading the lease back answers both. |

## 12. Specs that change, and what the owner signs

- **GS-6** §3.5 and Q-GS6f (decided); the ticker waits on `Restored()` in every
  state and calls `EnsureClockStarted`; `NewSession` accepts `ErrNoProof`;
  `MoveOutcome.Voided`.
- **GS-7** §3.5: the clock functions take `Gaps`; `Decide` takes the snapshot;
  the outage abort rule; `prevSystemAbort`.
- **GS-4**: the lease and gap items, `ReadLease`, `ReadGaps`, `Renew`.
- **GS-8**: Q1 answered; §4.1 incarnation is the persisted counter; stage 2
  must scope gaps by the game's owner and ownership interval before it starts;
  its liveness can use the lease.
- **PROTOCOL** §4A and §5: `0x0B`, reason and error `0x07`, `0x8B`, and the
  BUSY exemption from the no-resend rule. **WEB-F01**: the pending move and the
  pause notice.
- **MASTER-PLAN**: ADR-20.

At ratification the owner signs: T = 2 minutes; exact credit with a stated
early start of at most P plus the renewal timeout (1 s) per gap; a voided game
ends unrated even if a player resigns; adoption time is credited as part of the
start gap; the lease in the event table;
ADR-20 with the four wire changes; the client-held move and its one resend
exemption; a suspect gap credits and never aborts.

## 13. Build order

Each step is one PR with its tests. Step 1 needs GS-8 stage 1 step 2 merged.
1. **Store and service record.** `ReadLease`, `ReadGaps`, `Renew` on
   `store.Store`, `MemStore` and the fake; the `service` package's renewal
   loop, read-back, suspect classification, start rules and seeding command;
   `Snapshot`, `Paused()`, `Restored()`. No behaviour change: nothing reads the
   snapshot yet. This is the riskiest part and is built first.
2. **Clock projection and proof.** `Gaps`; the clock functions of section 5;
   `Decide` takes the snapshot; `ErrNoProof` through `exec`, the handlers and
   the ticker; `EnsureClockStarted`; `CreatedMs` from UUIDv7 (0 for v4).
   Switch still off.
3. **Outage abort.** The rule, the fold check, `0x0B` in the Go fold.
4. **Wire and clients.** ADR-20, codec, decoders, `SERVICE_STATUS`, BUSY, the
   web client's pending move.
5. **Switch on**, after step 4 has shipped.

## 14. Review mapping

### v0.5 review (1 blocker, 12 should-fix points, 6 nits)

| Finding | Where answered |
|---|---|
| B1 abort precedence with an unbuffered channel, failed aborts | §6 rule inside `Decide`; §8 how every voided game ends |
| S1 no-proof drops a command | §6 `ErrNoProof` is an error; §10 every handler replies |
| S2 a move is not held in the first S | §1 stated window |
| S3 next action after BUSY | §7 client pending move, the one resend exemption |
| S4 own-nonce failure too coarse | §4 read-back adopts only the exact stamp sent |
| S5 cancellation reasons need old values | §4 read-back instead; §11 |
| S6 key schema | §4 items table (`stream_id`, `seq`) |
| S7 switch semantics | §4 switch: cluster-level, off state, clipping |
| S8 abort uses the index path, half-terminal | §8 decision and rule |
| S9 sticky `AbortedBySystem` | §8 `prevSystemAbort` cleared on every other event |
| S10 §3 said aborts need a proof | §3 NFR-7 row |
| S11 restart clock, NTP after boot | §4 start steps 1, 4 and 5 |
| S12 GS-8 incarnation, join timer | §4 incarnation; §8 join timer; §12 |
| N1 "no bonus" against the early start | §1; §12 owner signs it |
| N2 concrete codes | §9 |
| N3 old clients' clock display | §9 rollout: switch on only after clients ship |
| N4 linear scan | §5 binary search |
| N5 `TransactionConflict` between renewals | §4 at most one in flight; a conflict is a failed renewal, resolved by read-back |
| N6 herd includes the STATE fan-out | §7 bounded queues; §10 recovery herd |

### v0.7 review (0 blockers, 8 should-fix points, 5 nits)

| Finding | Where answered |
|---|---|
| F1 ticker spin or lost wake on `ErrNoProof` | §6 ticker rules 4 and 5; §10 |
| F2 release rule never releases | §7 client rule 2, evaluated on STATE and on `paused: false`; §10 |
| F3 stuck pending move; BUSY for a move that landed | §7 client rule 4; §6 proof sits after the clock-free rows; §10 |
| F4 abort writes bounded only in the ticker | §6 bounded outage-abort writes (ticker and adoption, capped window); §10 |
| F5 merging, merged suspect, O(1) check | §8 merging for the abort, `LongRealStartMs`; §4 start gap is always real; §10 |
| F6 creation paths | §7 SEEK: `handleSeek` is the only path (code-checked); §8 rule; §10 |
| F7 re-check scope | §6 re-check applies to decisions made under proof only; §10 |
| F8 wake plumbing | §6 swap-then-close, nil channel, fall-through, interruptible wait; §10 |
| N1 `<=` in the walk | §5 `<` |
| N2 v4 never minted after step 2 | §5 |
| N3 one wording for `ErrNoProof` | §7 |
| N4 §1 table | §1 |
| N5 `pump.go` citation | §7 |
| `MoveOutcome` comment | §6 (the comment at `game/pipeline.go:49` is updated with `Voided`) |

### v0.6 review (0 blockers, 13 should-fix points, 7 nits)

| Finding | Where answered |
|---|---|
| S1 outage abort leaves a MOVE unanswered | §6 `MoveOutcome.Voided`; §10 every command kind replies |
| S2 resign contradicts the abort rule | §6 resign is not exempt; §11; §12 |
| S3 lost wakeup on `Restored()` | §6 ticker takes the channel before the check; §10 fault injection |
| S4 idle tickers do not wake | §6 `Restored()` in the `select` in every state; §10 |
| S5 recovery herd unbounded | §6 token bucket and window `max(1 s, N / R)`; §7 paced fan-out; §10 |
| S6 no pause signal after restart or reconnect | §7 `SERVICE_STATUS` before STATE on AUTHENTICATE; client rules 1, 2 and 4 |
| S7 pause definition gap | §7 paused means no fresh proof |
| S8 slow adoption not credited | §4 Start step 7, adoption is part of the start gap; §10 |
| S9 `CmdStartClock` has no trigger | §6 `NewSession` accepts `ErrNoProof`, `EnsureClockStarted` |
| S10 UUIDv7 strands in-flight games | §5 `CreatedMs = 0` for a v4 id |
| S11 `NextTickMs` walk wrong | §5 clip and walk rule; §10 brute-force replay |
| S12 pending move matched by ply alone | §7 pending move `{ply, uci, position}`, no resend after a send with no reply |
| S13 publish order | §7 restoration order |
| N early-credit bound | §1 |
| N forward step in a real outage | §1 |
| N proof re-check before append | §6 |
| N enable needs `attribute_exists` | §4 switch |
| N claim retry nonce | §4 Start step 5 |
| N GS-8 `gap_ms` cross-reference | §4 incarnation, Start step 7 |
| N backward-step stall | §1 |
| Citation nits (4th `ElapsedMs` caller, line ranges, join timer, README, test edits, `SERVICE_STATUS` order) | §0, §6, §9 and the S2 README |

The v0.3 and v0.4 review mappings are in v0.5 (commit 248b9ab, §14). Rows
that v0.6 changes: the gate (v0.3 B4, S2; v0.4 N6, S7) is replaced by §7; abort
precedence (v0.4 S8) and the abort write bound (v0.4 S10) by §6 and §8; the
`Created` time (v0.4 N3) comes from UUIDv7 in §5, not from the store.
