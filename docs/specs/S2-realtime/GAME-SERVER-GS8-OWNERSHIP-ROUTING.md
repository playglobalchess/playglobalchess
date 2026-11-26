# Game-server GS-8 — game recovery and ownership across nodes (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS8 (closes Q-GS2; amends GS-3 §3.5/§6, GS-6 §3.2/§3.4/§5/§6.3, GS-7 §3.5) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.3 (two stages; v0.1 and v0.2 each had one independent review) |
| Last updated | 2026-11-27 |
| Fixes (issues) | #98 (stage 1 restart half, stage 2 failover half), #173 (stage 2); #203 is their acceptance test |
| Depends on | GS-3 (session), GS-4 (event store, seq-conditional append), GS-5 (Redis pool), GS-6 (transport), GS-7 (fold) |
| Gates it feeds | G-E (scale proven). XII.2 launch bar: chaos test with zero in-progress-game loss. |

> **Why this exists.** Today a live game exists only in the process that paired
> it. A restart orphans every in-flight game (#98): clients get nothing, no
> clock is enforced, and the stream is frozen. A second healthy node already
> breaks play (#173). MASTER-PLAN Part XIII decides the outcome ("a game is
> sharded to one node ... node death, the game rehydrates on another node from
> the stream; clients resync via STATE"). It does not decide the mechanism. This
> spec does, and closes Q-GS2.

**Two stages.** Stage 1 makes a game survive a restart of the one serving node.
It adds no new event, no claim and no peer traffic. Stage 2 adds more than one
serving node: ownership, fencing, routing and takeover. Stage 1 is designed so
stage 2 adds to it and rewrites none of it. Only stage 1 needs owner decisions
now (§8).

**Review status.** v0.1 had one context-free design review (verdict "needs
another draft", five blockers, answered in §9). v0.2 had a second (verdict
"needs a short v0.3", three blockers and eight should-fix points, answered in
§12). Review is capped at two rounds; the owner decides D1 and Q1 before any
stage 1 code.

## 1. Scope

**Stage 1 (restart recovery, #98 restart half)**
- One constructor for every session, with the flag timer always armed.
- A durable index of live games, written in the same transaction as the
  game's first and last events.
- Adopting every live game at process start, and finding a user's live games on
  AUTHENTICATE.

**Stage 2 (more than one node, #173 and #98 failover half)**
- Who owns a game, how a stale owner is stopped, who takes over.
- Routing a connection on one node to a game owned by another.
- Draining a node for a deploy.

**Out of scope**
- The load balancer, TLS and the cloud product behind Redis and the peer link.
- Clock credit or abort after a store outage (#207). Stage 1 and stage 2 both
  depend on its decision for how downtime is charged (Q1).
- Matchmaking pool sharding (GS-5), spectators (X-10).

## 2. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-2 zero in-progress-game loss | The stream is the truth, and every event is persisted before it is acknowledged (NFR-7). A rebuilt session folds the stream (stage 1: same node after restart; stage 2: any node). |
| NFR-3 linear horizontal scale | Per-move cost stays O(1) lookups (#174). Stage 2 adds no central router on the move path. |
| NFR-1 move latency | Stage 1 changes nothing on the move path. Stage 2 adds one in-region hop for a remote seat, budgeted in §6.5 and measured in G-E, not assumed. |
| Part XIII, ADR-10 | Redis holds only ephemeral hints. It is never the source of truth and never the fence. |
| ADR-19 (a) | A client never retransmits MOVE. Recovery never depends on a resend (§4.5). |
| XII.2 chaos gate | §4.6 and §6.7 map each failure to a #203 test. |

## 3. How Lichess handles it, and what we do differently

Lichess runs one round process that holds every live game in memory. Its
WebSocket servers only relay frames (lila-ws: `lila <-> redis <-> lila-ws <->
client`). Moves reach MongoDB in batches, so a crash can lose recent moves; a
planned stop saves every game and gives the player to move +20 s and the other
+10 s, and a WebSocket restart gives both players +20 s. Lichess does not refund
rating after a server-caused disconnect. It has no ownership transfer or
fencing, because it has one owner.

We keep two things from that: relays attach to the game's owner (stage 2 §6.5)
and a restart compensates the clock (Q1). We differ on durability: NFR-2 and
NFR-7 require each move to be saved before it is confirmed, and a game must
survive the loss of its node. That is why this spec has an index and, in stage
2, fencing. Chess.com is closed source; its published restart policy aborts
live games, and nothing public describes a crash.

## 4. Stage 1 — restart recovery on one node

### 4.1 Facts this relies on

- One process serves at a time. A deploy stops the old process serving before
  the new one starts adopting (drain, then start). The seq-conditional append
  (GS-4) prevents a forked history if two overlap, but two processes would each
  run timers and send frames, so overlap is an operational error to prevent.
- Every writer takes `NextSeq` from a folded, contiguous state. The Dynamo
  store checks only `attribute_not_exists(seq)` and MemStore rejects gaps, so a
  test must run the same contiguity cases against both stores.
- Each process has a stable `node_id` and a random `incarnation` generated at
  start. Both are written into `live_games` at the opening and used in the
  harness's author record `(node, incarnation, seq)`. Stage 2 relies on them to
  tell a restarted node from its zombie predecessor (§6.1).

### 4.2 One constructor

`startGame(gameID, role)` is the only way a session comes into being. `role` is
`pair`, `resync` or `adopt`. It reads the stream, takes the time control from
`GameCreated`, folds, repairs a half-terminal stream (`CmdFinishHalfTerminal`)
and **always** arms the deadline timer (#182), which is what a rebuilt game
lacks today (#98, finding from #212).

The join timer is armed only for `pair`. Its "joined" signal is process-local,
so re-arming it on a rebuild would abort a live game whose players are briefly
disconnected. A rebuilt game below two plies is bounded by the first-move
timeout (GS-7 §3.5), which is measured from the stored `ClockStarted`.

### 4.3 The live-game index

Three new DynamoDB tables, separate from `game_events` (whose sort key is the
number `seq`). Game ids become time-ordered UUIDv7 (today `uuid.NewString()` is
v4), so `game_id` alone sorts newest first and every key is derivable without
`started_at`, which the session cannot recover (the store stamps `committed_at`
at write time and `Load` drops it). Anything that parses a game id as v4 is
checked when this is built.

- `user_live_games`: partition `user_id`, sort `game_id`. Attribute: `seat`.
- `live_games`: partition `shard` (hash of `game_id` mod a configured count),
  sort `game_id`. Attributes: `owner_node` (the pairing node id, written at the
  opening, so stage 2's sweep also finds games that never failed over) and
  `owner_incarnation`.
- `user_recent_games`: partition `user_id`, sort `game_id`, attribute `ended_at`
  and a DynamoDB TTL at the retention of today's `missedEnds`.

`State` gains `Players []string` (the user ids from `PlayerJoined`, which the
fold drops today), so the session can name the keys at the ending. This is a
GS-7 amendment (§10).

**Index operations.** `AppendBatch` and `Append` take a list of index
operations (`Put`, `Delete`, `Update`; stage 2's claim needs `Update` of
`owner_node`). The rule is: any write that contains `GameEnded` carries the
ending operations, including the lone `GameEnded` that `decideFinishHalfTerminal`
emits through `Append`. All run in one `TransactWriteItems`.

- Opening (exactly 3 events, then `ClockStarted` is a separate write by
  `NewSession`; if the node dies between them, adoption stamps `ClockStarted`
  at adoption time, which is harmless): 3 events, 2 `user_live_games` puts and
  1 `live_games` put.
- Ending: the events, 2 `user_live_games` deletes, 1 `live_games` delete and
  2 `user_recent_games` puts. About 8 items, well under the 100-item and 4 MB
  limits.

`store.New` checks that all four tables exist (it checks `game_events` only
today), so a missing table fails at start, not on the first game. Index reads
use `ConsistentRead`. MemStore implements the same operations. Transactional
writes cost twice the write units; the real cost at launch load is measured in
G-E, and the shard count is chosen with headroom because it is hard to change.

**Recently ended.** A user who closes the tab mid-game, loses on time and
reopens it holds no game id (the web client keeps `liveGameId` in memory only).
Today `missedEnds` pushes the ended STATE on AUTHENTICATE. Stage 1 keeps that
behaviour: AUTHENTICATE reads `user_recent_games`, pushes each ended STATE from
the stream (`resyncEnded`), and deletes the entry, as today. This restores
`missedEnds` durably instead of removing it.

Stale entries. Every consumer folds the stream before acting. Adoption deletes
the entries of any game whose stream is ended, and AUTHENTICATE does the same
for the user's own rows. A periodic reconciliation of the whole index is not
part of stage 1.

**Scope.** This writes outside the event stream, so it touches ADR-16
("DynamoDB, game-event store only"). See D1.

### 4.4 Adoption at start and on AUTHENTICATE

- **At start.** The node queries every `live_games` shard and calls
  `startGame(adopt)` for each game, with bounded concurrency (configured,
  starting at 32) and a startup time budget, because folding goes through the
  engine once per ply. Deadline-first order is not available without folding, so
  the order is by id (oldest first); a fold that finds a due clock ends the game
  at its first tick. The node accepts connections immediately.
- **Transport state for an adopted game.** `startGame` lives in `Server`, not
  `Registry`, because it arms the ticker (`armFlagTicker` calls
  `broadcastGameEnd` and `dropGame`). It first reads the stream for the time
  control, then builds the session. It fills the per-game user map and
  `userGames` from the index entry's `seat`, so AUTHENTICATE binds the seat and
  MOVE does not answer NOT_YOUR_SEAT.
- **On AUTHENTICATE.** The edge queries `user_live_games` (newest first, limit
  8) and `user_recent_games`, and calls `startGame(resync)` for each live game
  not yet adopted. The registry shares one build per game (#174). A reconnect
  storm costs two Queries per user.
- **A failed lookup is defined.** If either Query fails, AUTHENTICATE fails
  with a retryable `ERROR{INTERNAL}`; the connection is not marked
  authenticated, so a SEEK cannot pair the user into a second game while the
  first is live.
- **An ambiguous opening.** If `createGame` times out, the matchmaker re-reads
  the stream as `committedAnyway` does and treats a committed opening as paired,
  so the index never shows a game the client was never told about.
- A game found ended is not started; its live entries are deleted.

### 4.5 Clients and the frozen rules

ADR-19 (a): a client never retransmits MOVE. A MOVE in flight when the node dies
gets no reply, and the closed connection is the signal. On reconnect the
client receives STATE from AUTHENTICATE or RESYNC and reconciles from it
(WEB-F01). If the move landed, STATE shows it; if not, the player moves again.
Recovery never depends on a resend.

**Clocks across a restart (Q1).** Nothing changes from today: a game's clocks
run from stored timestamps, so restart downtime is charged to the player on
move, and adoption flags any game whose mover ran out during the downtime. A
rebuilt game below two plies can abort on its first-move timeout. This is the
#207 problem at restart scale, and Lichess gives +20 s on a restart. So stage 1
ships only while rated play is disabled (`SetRatedEnabled`, off by default since
#179) until #207's credit lands, and only if the owner accepts this (Q1).

### 4.6 Failure modes and how #203 covers them (stage 1)

| Failure | Behaviour | Test (invariant harness, #203) |
|---|---|---|
| Process killed between two writes of one command | Restart adopts the game; the half-terminal repair finishes the ending; I1 to I8 hold | kill at write k, rebuild, resume the same seeded command stream |
| Killed after persist, before ack | No retransmit. The reconnect STATE shows the move; the rebuilt session adds no duplicate event | kill after write, before ack |
| Closed tab, lost on time, reopened | AUTHENTICATE pushes the ended STATE from `user_recent_games`, once | end a game with no client, reconnect |
| Ending written through the lone-`GameEnded` repair path | The index deletes and recent-game puts are written too | half-terminal fault store |
| AUTHENTICATE lookup fails | Retryable error, no SEEK allowed | fault store on the index |
| Killed with both players disconnected | Adoption arms the deadline timer; the clock runs out and the game ends | kill, adopt, advance time, expect GAME_END |
| Killed at ply 0 or 1 | No join timer on adopt; the first-move timeout bounds it | kill, adopt, advance past the timeout, expect abort |
| Index entry for an ended game | Adoption deletes it | seed a stale entry, start |
| Index write fails with the opening batch | The transaction fails as a whole, so no game exists | fault store |
| Store down at start | Adoption retries with backoff; connections are still served (#97 behaviour) | outage at start |

The harness gains a crash phase on one node (kill at random write points), and
an author record `(node, epoch, seq)` per event so stage 2's I9 is a direct
assertion over the committed log.

## 5. Stage 1 acceptance

Stage 1 is done when the §4.6 tests pass, the transactional index passes the
contiguity cases against MemStore and DynamoDB Local, and a restart of the game
server in the e2e environment leaves every live game playable. It closes the
restart half of #98 only; #98 and #173 close when stage 2's #203 phase passes.

## 6. Stage 2 — more than one node (designed now, decided later)

This stage is not built until stage 1 ships and the owner answers D2 to D5.
It is written here so stage 1 does not close any door.

### 6.1 Ownership and the claim event

- **Owner** of a game: the one node running its session and timers. **Edge** of
  a user: the node holding the user's WebSocket. Any node can be an edge.
- **Safety and liveness are separate.** Safety (no divergent history, no write
  by a stale owner) comes only from the store. Liveness (who takes over, when)
  comes from membership and timing, and a wrong liveness view costs a retry, not
  a corrupt stream.
- **The claim is written at every adoption, never at the opening.** A game has
  no claim until some process adopts it, whether that is another node or a new
  incarnation of the same node after a restart (a zombie old incarnation cannot
  otherwise be told apart). No claim means epoch 0, owned by the pairing
  incarnation, so games that are never adopted stay on the stage 1 schema.
  `OwnerClaimed{node_id, epoch, server_ts, gap_ms}` is appended at the head the
  taker folded. If another claim landed first, the append conflicts and the
  taker defers.
- **Rollout.** The claim is safety-relevant, so a node must never skip it as an
  unknown event. #187 lands first with a fail-closed rule for safety-relevant
  types, no node writes a claim until every node reads it, and rollback below
  that point is a one-way door. It needs a new event code, a PROTOCOL §4A row
  under ADR-19, golden vectors and the TS, Swift and Kotlin decoders. Other
  stream readers (rating, archive, replay) get the same tolerance.

### 6.2 The ownership guard (invariant I9)

**I9.** After a claim with epoch `e` commits at seq `k`, no event written by an
owner with an epoch below `e` commits above `k`.

The seq condition alone does not give I9, because the session's write path
retries across a conflict: it reloads, re-decides the same command, and appends
at the new head (`exec`, `session.go`). A deposed owner would then append above
the claim. The guard: before every append, and after every reload, compare the
folded `OwnerEpoch` and `OwnerNode` with this session's own. If they differ,
return `ErrNotOwner`, close the session, and never re-decide. It covers
`committedAnyway`, the stale and `needReload` reloads, the flag timer and
session construction.

A per-stream owner item with a conditional check on every move would also fence,
but it costs a second conditional item in every move's write and serialises on
one hot item. The claim gives the same fence through the seq condition for free.

### 6.3 Liveness, partitions and Redis loss

- Each node holds `node/{id}` in Redis (6 s TTL, refreshed every 2 s) from a
  dedicated goroutine that adoption load cannot starve. It is refreshed only
  while the node's own store writes recently succeeded, so a store-only
  partition makes the node leave membership and be adopted.
- **A Redis outage does not fence anything.** Safety comes from the store, so
  games keep playing. Liveness is weaker: a node that dies during a Redis outage
  has its games frozen until membership returns. A whole-fleet store outage
  expires every membership at once, so takeover also waits for the store, and
  an idle node proves store health with a canary write. A node self-fences only on evidence: a
  conflict showing a newer claim, or loss of the store.
- **No claim storms.** An observer may take a game only if its own refreshes
  succeeded for at least one TTL and the owner has been absent for at least one
  TTL. A game has a minimum dwell between claims (from the claim's `server_ts`).
  When Redis is wiped, every node sees only itself, so the observer rule holds
  takeover until heartbeats converge.
- **Peers-only partition.** The owner is alive but unreachable from an edge.
  The edge closes the connection so the client reconnects elsewhere, and the
  seat is treated as disconnected by the owner.

### 6.4 Takeover and adoption

- **Detection** polls membership every 2 s. Keyspace events are not used.
- **Who adopts.** The successor is the live node with the highest rendezvous
  hash of `(game_id, node_id)`. `live_games` has a GSI on `owner_node`, which
  stage 1 writes at the opening and each claim updates; the sweep tolerates its
  eventual consistency by repeating until the departed node has no entries. On a
  membership change each node sweeps the
  GSI for the departed node, every 5 to 10 s while any remain. This replaces
  v0.1's Redis set and the 60 s audit: one durable source, no cleanup of a
  50k-member set. The auditor of each shard has a standby (the second hash).
- **The index hint is verified.** A game is skipped only if its owner node is
  in membership **and** that node confirms it runs the game, so a hint that
  names a live node that failed to start the game (or a lost hint update) does
  not strand it.
- **Clocks.** Failover time is not silently charged to the player on move: the
  claim carries `gap_ms`, designed together with #207's credit event, so the
  rule is one rule. Elapsed time uses each node's wall clock, so GS-7 §3.5's
  100 ms node-skew bound applies and is tested.
- **Deploy drain (D5).** The owner stops accepting commands (`NOT_OWNER`),
  flushes, the successor claims at the quiesced head, and the draining node
  leaves membership so it is never "alive but not running". If the successor
  dies mid hand-off, ordinary takeover applies after membership expiry.

### 6.5 Routing between nodes (#173)

- **Attach, not presence.** An edge sends the owner `Attach{game, seat, user}`
  and `Detach`; the owner pushes frames to each seat's edge. No per-user key in
  Redis. When the owner changes, each edge re-attaches, and the new owner pushes
  STATE to both seats on attach. This is what lets a client with an unacked MOVE
  recover with no resend (ADR-19 (a)).
- **The edge never retries a command.** On a stream break or an owner change it
  answers every pending command with `ERROR{INTERNAL}`.
- **Finding the owner.** In-memory cache, then the Redis hint `game/{id}/owner`
  (TTL longer than the longest game), then the stream's last claim; a node that
  is not the owner replies `NOT_OWNER{hint}`.
- **Move path.** The edge stamps receipt (#183) and sends `age_ms`; the owner
  computes receipt time from its own clock minus `age_ms` minus an estimated
  edge to owner transit, so remote seats are not charged the hop (NFR-1's 50 ms
  clock drift). One in-region hop is budgeted at p99 under 5 ms and measured in
  G-E.
- **Peer streams.** (N-1)/N of seats are remote at N nodes, so this path is the
  normal one. Streams are mutually authenticated, keepalive-checked, and carry a
  bounded queue per stream (as `pump.go`), so a slow peer never blocks a session
  actor.
- **Cross-node MATCHED.** The seek record gains `edge_node` and `conn`
  (`Seeker` and `Enqueue` change). The edge installs its route before it
  forwards MATCHED, so an early MOVE does not hit `NOT_OWNER`. A waiting seeker
  polls the index while its seek is pending, so a pairing node that dies after
  the opening batch does not leave the seeker waiting forever.

### 6.6 Q-GS2 alternatives considered

Consistent hashing by `game_id` (MASTER-PLAN IX.2 "sharded by game_id") moves
live games whenever membership changes. Claim-based ownership moves a game only
when its owner is gone, so a node joining never disturbs a live game. That is
why this spec uses claims.

### 6.7 Failure modes and tests (stage 2)

| Failure | Behaviour | Test (#203) |
|---|---|---|
| Stale owner after takeover | I9: `ErrNotOwner`, the session closes | two sessions on one store, the old one keeps writing, including across a conflict retry |
| Two nodes both try | One claim wins by seq; the other defers | two simultaneous adopts |
| Redis lost or wiped | Play continues; takeover waits for stable membership | Redis wiped mid-run |
| Store-only or peers-only partition | See §6.3 | partition injection (new harness piece) |
| Owner dies, nobody connected | The successor adopts, arms the timer, the flag ends the game (finding from #212) | kill a node, let the clock run out, expect GAME_END |
| Edge dies | The owner treats the seats as disconnected; the client reconnects anywhere | kill an edge |
| Adoption slower than a first-move timeout | The claim's `gap_ms` credit, per #207's rule | slow adoption test |

The harness needs fake membership and partition injection first (plan step 3).

## 7. Plan, in order, one PR each, each with an independent review

**Stage 1**
1. **Store API.** `Append` and `AppendBatch` take index operations; three
   index tables; UUIDv7 game ids; `State.Players`; table checks in `store.New`;
   MemStore support; the contiguity tests against both stores.
2. **Recovery.** `startGame` (in `Server`) as the only constructor, the
   always-armed deadline timer, adoption at start, AUTHENTICATE lookup with
   `user_recent_games` replacing the in-memory `missedEnds`, half-terminal
   repair. The #203 single-node crash phase. Uses `relates to` for #98.

**Stage 2 (after stage 1 ships and D2 to D5 are answered)**
3. **#187, the claim event and the harness pieces.** Fail-closed unknown-event
   rule, the codec event, fake membership and partition injection.
4. **Ownership guard, membership and takeover.** I9 enabled.
5. **Routing (#173).** Peer streams, attach, `NOT_OWNER`, cross-node MATCHED.
6. **Adoption sweep, drain and the chaos test** of the XII.2 gate.

#98 and #173 close only when step 6's harness phase passes.

## 8. Decisions for the owner

**Needed before stage 1 code**
- **D1. Index tables and ADR-16.** Recommended: three small DynamoDB tables
  (live games per user, live games per shard, recently ended per user), written
  in the opening and ending transactions, recorded as an
  ADR-16 amendment limited to "live-game index hints" that ratifies nothing else
  about the platform. Alternatives: Redis only (not durable, so a Redis loss
  strands players and games, failing the zero-loss gate); a scan of
  `game_events` (cost grows with history).
- **Q1. Restart downtime on the clock.** Recommended: accept today's rule
  (downtime charged to the player on move) for stage 1, shipping only while
  rated play is off, and design the credit with #207 before stage 2. Alternative: land #207's credit first, so a restart
  never costs a player a game. Stage 1 ships earlier under the recommendation.

**Needed before stage 2 code (not now)**
- **D2.** Claim at every adoption, never at the opening (recommended), or in
  every opening batch (every game then needs the claim event on a rollback, a
  one-way door at launch).
- **D3.** Redis loss never stops play (recommended) or self-fence after a
  refresh gap.
- **D4.** Peer link: direct gRPC streams between nodes (recommended) or a broker.
  ADR-12 names gRPC for the engine boundary only, and ADR-15 rejects a network
  hop on the move path, so this needs its own recorded decision limited to
  remote seats.
- **D5.** Deploy as an explicit hand-off (recommended) or as a node loss.

## 9. Review of v0.1: what changed

| v0.1 finding | Answer in v0.2 |
|---|---|
| B1 I9 false because `exec` retries across the claim | §6.2 guard before every append and reload |
| B2 Redis loss fences the fleet; claim storms | §6.3 no fencing on Redis loss, hysteresis, dwell, isolated heartbeat |
| B3 join timer aborts live games; slow adoption exceeds first-move timeout | §4.2 join timer only for `pair`; §6.4 `gap_ms` credit |
| B4 index key schema, API and stranding holes | §4.3 separate tables, time-ordered key, one transaction, live only; §6.4 verified hint |
| B5 failover story contradicts ADR-19 | §4.5, §6.5 no resend, STATE on attach |
| Rolling deploy and rollback | §6.1 takeover-only claim, fail-closed rule, one-way door |
| Clocks, partitions, drain, peer streams, plan order | §6.3 to §6.5, §7 |

## 10. Specs that change when this is accepted

GS-3 §3.5 and §6 (the ownership guard, stage 2); GS-7 `State.Players` and
UUIDv7 game ids (stage 1); GS-6 §3.2, §3.4, §5, §6.3
(AUTHENTICATE lookup in stage 1; reconnect to another node in stage 2); GS-7
§3.5 (the rebuilt game's timers); GAME-SERVER Q-GS2 (closed by this spec);
PROTOCOL §4A (the claim event, stage 2); ADR-16 (D1).

## 11. Not decided here

The load balancer and TLS termination; the cloud product behind Redis; the
final shard count and adoption concurrency (configured and measured in G-E);
clock credit after a store outage (#207).

## 12. Review of v0.2: what changed in v0.3

| v0.2 finding | Answer in v0.3 |
|---|---|
| The ending transaction could not name its index keys | §4.3 UUIDv7 game ids, `game_id` as the sort key, `State.Players` |
| Removing `missedEnds` regressed result delivery | §4.3 `user_recent_games`, same retention and consume-once behaviour as today |
| The `owner_node` GSI missed games that never failed over | §4.3 `owner_node` written at the opening; §6.4 eventual consistency |
| Other ending write paths, table checks, consistent reads | §4.3 rule for any write containing `GameEnded`, `store.New` checks, `ConsistentRead` |
| Transport state of adopted games, where `startGame` lives | §4.4 |
| A failed AUTHENTICATE lookup, an ambiguous opening | §4.4 |
| Restart downtime flags games on every deploy | §4.5 stage 1 ships only with rated play off, until #207 |
| Zombie old incarnation versus D2 | §4.1 incarnation id; §6.1 claim at every adoption |
| Stage 2 liveness gaps | §6.3 |
| Adoption order | §4.4 budget and bounded concurrency; deadline-first needs a fold, so not specified |

The periodic reconciliation of stale index rows and the shard count are left to
G-E (§11).
