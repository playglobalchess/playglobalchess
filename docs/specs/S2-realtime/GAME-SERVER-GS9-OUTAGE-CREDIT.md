# Game-server GS-9 — a clock that never charges players for our downtime (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS9 (closes GS-6 Q-GS6f; amends GS-6 §3.5, GS-7 §3.5, PROTOCOL §4A) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.3 (fresh design; supersedes 0.1 and 0.2, section 11) |
| Last updated | 2026-11-27 |
| Fixes (issues) | #207; also supplies the restart-downtime rule GS-8 stage 1 needs (GS-8 Q1) |
| Depends on | GS-4 (event store), GS-6 (ticker, #97, #182), GS-7 (fold, `LastChangeTS`, first-move rule, #183 receipt time) |
| Owner decisions | 2026-11-27: exact credit, T = 2 min, no bonus time |

## 1. What the player gets

| What happens | What the player sees |
|---|---|
| A hiccup under 1.5 s | Nothing. This is network lag (#82). |
| We cannot serve for 1.5 s to 2 min (store outage, pause, restart, deploy) | A "server problem, clocks paused" notice. Nobody flags. When service returns both clocks show exactly what they were when we stopped. A move sent meanwhile is played when service returns. |
| We cannot serve for more than 2 min | The game is aborted with no rating change and a message saying why. |

Lichess gives a fixed +10 to +20 s on a planned restart and nothing for a crash,
and it never aborts. Chess.com aborts live games on a restart. This design
gives back exactly the lost time, in every case, and aborts only when a game
cannot sensibly continue.

## 2. The idea in one paragraph

Earlier drafts tried to detect an outage after it happened and then patch a
credit into every game's clock. That produced double credits, ordering
problems, a write storm at recovery and a race between a flag and the credit.
The root cause is that the clock was a running total that someone had to
correct. Here the clock is **a pure function of two stored facts**: the game's
own events, and the node's **service record**, a small append-only list of the
intervals in which the node could not prove it was serving. A game is never
patched. Nothing needs deduplicating, because the credit is the length of the
overlap of two stored intervals, and the same inputs give the same answer on
any node, live or rebuilt.

## 3. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-4 | An outage abort ends with no result and no rating change (section 8). |
| NFR-7 | A flag or an abort is decided only with proof that no unrecorded gap exists (section 6). |
| NFR-9 | A live clock is replayable from the game stream and the service record, both durable (section 5). |
| GS-7 I3 | Live and rebuilt sessions read the same two inputs, so they agree. |
| #95 | One failed or ambiguous write opens no gap. Only a missed lease renewal does (section 4). |

## 4. The service record: lease and gaps

**Lease.** Each node holds a lease item `lease/{node_id}` in the event table,
reserved key space, same table and write path as game events. Fields:
`{incarnation, renewed_ms}`. The node renews it every **P = 1 s** (timeout 1 s)
with a conditional put that requires its own incarnation. The lease probes the
exact dependency games need: if the event table cannot take a write, the
lease fails, and a lease on any other table would not say so. Cost: one small
write per node per second.

**Incarnation.** A counter, not a time. At start the node reads its lease item
with a consistent read and writes `incarnation + 1` with a condition on the old
value. A zombie older process fails its next renewal, learns it is superseded,
and **must stop serving**, not just stop writing the lease.

**Gap.** Two consecutive successful renewals at `t1` and `t2` with
`t2 - t1 > S = 1.5 s` mean the node was not provably serving in `(t1, t2)`.
This single rule covers a store outage, a process pause (GC, VM freeze), a
restart (the new incarnation compares its first renewal with the old item's
`renewed_ms`), and a slow deploy. There is no failure counting. The credit
starts at `t1`, the last proof of service, so it can be up to one renewal
period early, a bounded advantage for the player on move. It is never late.
Time wrongly missed is below `S`, indistinguishable from lag, and #82 owns it.

**Service record.** Before a node serves again after a gap it appends one
`ServiceGap{start_ms, end_ms, node_id, incarnation}` to the service stream
`svc/{cluster}` (event-table stream, conditional append, retried until it
succeeds). One write per gap, whatever the number of games. The record is
append-only and tiny. Gaps are read into memory at start and on each append.

**First start.** If a consistent read confirms there is no lease item, there is
no gap. If the read fails, the node does not serve.

**Clock source.** Stamps are wall-clock milliseconds, since a freeze stops the
monotonic clock. A wall step forward of 1.5 s or more reads as a gap, which
favours players, and one of T or more aborts games. Operations must slew
clocks, not step them, and the node alarms on a step.

## 5. The clock projection

`Gaps.Overlap(a, b)` is the length of the union of recorded gaps inside
`[a, b]`. The game fold and the stored events do not change: `LastChangeTS` stays
a real timestamp and `ClockAfterMs` is stored per move as today. Only the
functions that read the running clock take a `Gaps` argument:

- **Elapsed.** `elapsed(now) = max(0, (now - LastChangeTS) - Overlap(LastChangeTS, now))`.
  `ClocksAt`, flag decisions and the first-move timeout use it, so one change
  covers the STATE projection, the tick and the move charge.
- **Move charge (#183).** A move received at `r` is charged
  `(r - LastChangeTS) - Overlap(LastChangeTS, r)`, with `r` clamped as today.
  A gap before `r` is not charged. A gap after `r`, while the move waited, is
  charged to nobody: the opponent's clock starts when the move is processed.
- **Next tick (#182).** The deadline is computed with the gaps known now. When
  the timer fires, the node re-checks with the gaps known then and re-arms if
  time was credited meanwhile.
- **Only the running clock gains.** The opponent's clock was not running, so
  its overlap with the gap changes nothing: it is not charged either.
- **Takeback and first moves.** A rewind sets `LastChangeTS` to the rewind time,
  so older gaps fall outside `[LastChangeTS, now]`. The first-move timeout and the
  join abort use the same elapsed function.
- **Ended games** need no gaps: `GameEnded` and the per-move clocks are stored.
  Fair-play think time is `ServerTS` difference minus `Overlap` over the same
  interval.

## 6. The serving proof: one rule for every clock decision

A decision that reads a running clock is valid only if the node holds a
**serving proof** at that moment:

1. its lease was renewed within the last `S` (1.5 s), and
2. every gap up to that renewal is already in the service record, and
3. its incarnation is current.

Without a proof, the node does not decide, it waits. The proof comes back as
soon as the next renewal succeeds and the gap is appended. This one rule
replaces the earlier hold, ordering and corroboration rules:

- **Flag tick, first-move abort, join abort.** The timer fires, checks the
  proof, and if there is none waits for it, then recomputes with the new gaps.
  A pause that stops the process cannot flag anyone, because after the pause
  the lease age is above `S`.
- **MOVE and takeback accept.** They wait in the game's queue, keeping the
  receipt time `r` (stamped at the socket read, #183). With the proof back they
  are decided with the gaps, so the outage is never charged. If the wait passes
  the command timeout the player gets `ERROR{INTERNAL}` as today. The client does
  not resend (ADR-19 (a)), and the credited `STATE` shows the true clock.
- **Resign, draw offer and accept, abort.** They do not read the clock and
  proceed. A resign is never held back by an outage.
- **No starvation.** The proof depends only on the lease and the service stream,
  never on a game's stream, so one poisoned game cannot hold the others. If the
  store is down, no write can succeed anyway; the wait ends with the first
  successful renewal and gap append.
- **No write storm.** Recovery is one lease write and one gap write. Games
  need no write to get their credit.

## 7. Rebuild and restart

- A rebuilt session loads the game stream and the in-memory service record.
  Live and rebuilt agree because they read the same two durable inputs.
- **Restart.** The new incarnation writes the gap `(old renewed_ms, first
  renewal)` before serving. The gap is the node's downtime, whatever the number
  of games or when each is rebuilt. A slow rebuild adds no gap and aborts
  nothing.
- A node never serves while the lease read, the lease write or the gap append is
  failing.
- **Planned deploy.** It is the same gap, with the same exact credit. A drain
  that hands games to another node first is GS-8 stage 2 (D5).

## 8. The abort (gap longer than T)

T is **2 minutes**. The rule is the same for every cause, because the lease
itself proves the absence of service. No corroboration is needed.

- After appending a gap longer than T, the node aborts every live game whose
  `LastChangeTS` is before the gap's end, with the session command
  `CmdAbortOutage`. It appends `Aborted{by: SystemActor}` and
  `GameEnded{termination: ABORT_OUTAGE}` in one write. A client command can
  never produce `SystemActor` or `ABORT_OUTAGE`. The fold requires
  `ABORT_OUTAGE` to come with `Aborted{0xFF}` in the same write.
- **No lost abort.** A crash between the gap append and the aborts is repaired
  at build: a live game whose clock span overlaps a gap longer than T aborts at
  its next decision, from stored facts alone.
- **Too much credit.** A game whose total overlap with gaps exceeds 10 minutes is
  aborted the same way, so flapping cannot give unlimited free time.
- **Unrated.** `ABORT` and `ABORT_OUTAGE` never count as a draw (GS-7 §3.5).
- **A slow deploy.** A deploy that takes longer than T aborts every live game.
  This follows from the owner's choice of T.

## 9. Clients and wire

- **No new game event.** A credit is not an event in the game stream, so the
  frozen event set is unchanged and no old game stream holds anything new.
- **Notice.** While a node has no proof but can still talk to clients, it
  sends a `SERVICE_STATUS{paused: true}` push, then `{paused: false}` followed
  by a `STATE` for each affected game when service returns. Clients stop their
  local clock tick while paused. This message is additive, and the client
  notice can ship after the server rule. Its code is set in ADR-20.
- **Abort.** Both players get `GAME_END` with the new termination `0x0B
  ABORT_OUTAGE` (the u8 table uses 0x01 to 0x0A today). The client shows "Game
  aborted: server problem, no rating change".
- **Fail closed.** A node that does not know `0x0B` refuses to fold the game, as
  today. The new rule needs every node to be gap-aware before any real rated
  game, and stage 1 has one node.
- **Consumers.** The ended-game view, rating, fair play and replay read
  `ABORT_OUTAGE` as unrated. Think-time readers subtract `Overlap`.
- ADR-19 process applies: version bump, review, golden vector for `0x0B` and
  the new message, decoders in Rust, Go, TypeScript, Swift and Kotlin.

## 10. Tests (the #203 harness gets an outage phase)

- **Credit.** A store outage of D under T: no flag for time lost, the mover's
  clock afterwards equals the clock before, a rebuilt session agrees.
- **Pause.** A process pause with no failed write: a tick right after resume
  does not flag. A pause shorter than `S` is not credited (boundary test).
- **Gap race.** Writes recover before the lease: a MOVE and a TAKEBACK_ACCEPT
  in that gap are held and charged nothing for the outage.
- **Queued move.** Received before the gap, inside it, and after it.
- **Poisoned stream.** One game that always fails does not delay another game's
  flag. A throttled event table at 30%: flags fire after proof returns.
- **Restart.** The previous lease gives the right gap; first start gives none; a
  failing lease read means no serving; a zombie fails renewal and stops serving;
  a crash loop; a rebuild after start adds nothing.
- **Idempotence.** The same gap seen twice, overlapping gaps and out-of-order
  appends give the same clock. A retried gap append after a conflict adds one.
- **Abort.** Every live game ends `ABORT_OUTAGE`, unrated; a game past
  `abortBeforePly` still aborts; a client cannot produce it; a crash between
  the gap and the aborts is repaired at build; the cumulative cap aborts.
- **Takeback and first moves.** An older gap does not affect a rewound clock;
  the first-move and join timeouts move with a gap.
- **Resign** during an outage proceeds.
- **Wall step.** A forward and a backward step: forward reads as a gap, backward
  as none.
- **Codec.** Golden vectors for `0x0B` and `SERVICE_STATUS`, parity in every
  decoder, an old node fails closed.
- **Mutations.** Overlap ignored, flag decided without proof, move charged
  without the gap, lease checked after the decision, gap not appended before
  serving.

## 11. Designs rejected, and why

| Design | Why not |
|---|---|
| A `ClockCredited` event per game (v0.1, v0.2) | N writes at recovery, a race with the flag, double credit on rebuild, a watermark with ordering rules, and a stored baseline shift. Two review rounds found 12 blockers, all from here. |
| Detecting an outage by counting failed writes | It misses pauses and partial throttling and can starve flags. |
| A separate health table | It can be healthy while the event table is down, so players would be flagged unfairly. |
| Lichess-style fixed bonus | It cannot cover a crash, and it gives one side time the other never gets. |

## 12. Specs that change when this is accepted

GS-6 §3.5 and Q-GS6f (decided); GS-7 §3.5 (clock projection takes the service
record; the abort exception); GS-4 (lease item, `svc/` stream); GS-8 (Q1
answered; adoption writes a gap for the dead owner; gaps are scoped to the game's
owner at that time; its liveness can use the lease); PROTOCOL §4A and WEB-F01;
MASTER-PLAN (ADR-20).

## 13. Build order

1. Lease, incarnation and the service stream (store and node state, no
   behaviour change).
2. The clock projection with `Gaps`, and the serving proof on clock decisions.
3. `CmdAbortOutage`, the notice, `0x0B` and the clients.
