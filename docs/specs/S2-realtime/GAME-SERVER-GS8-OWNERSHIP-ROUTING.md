# Game-server GS-8 — game ownership, routing and rebuild across nodes (design spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS8 (closes Q-GS2; amends GS-3 §3.5/§6, GS-6 §3.2/§3.4/§5/§6.3, GS-7 §3.5) |
| Owner | S2 Real-time |
| Status | DRAFT |
| Version | 0.1 |
| Last updated | 2026-11-27 |
| Fixes (issues) | #173, #98 (together); #203 is their acceptance test |
| Depends on | GS-3 (session), GS-4 (event store, seq-conditional append), GS-5 (Redis pool), GS-6 (transport), GS-7 (fold) |
| Gates it feeds | G-E (scale proven). XII.2 launch bar: chaos test with zero in-progress-game loss. |

> **Why this exists.** Today a live game exists only in the process that paired
> it. A second healthy node already breaks play (#173), and a restart or node
> loss orphans every in-flight game (#98): clients get nothing, no clock is
> enforced, and the stream is frozen. MASTER-PLAN Part XIII already decides the
> outcome ("a game is sharded to one node ... node death, the game rehydrates on
> another node from the stream; clients resync via STATE"). It does not decide
> the mechanism. This spec does, and closes Q-GS2.

**Review status.** v0.1 is a proposal. Section 9 lists the decisions the owner
must make before any code. Nothing here is built yet.

## 1. Scope

**In scope**
- Who owns a live game, and how a second node learns it is not the owner.
- How a user's connection on one node reaches a game owned by another.
- Rebuilding a game on another node (after restart, drain or node loss),
  including its flag timer, join timer and half-terminal repair.
- Finding a user's live and just-ended games after a restart (AUTHENTICATE).
- Finding games whose owner died while no client is connected (clocks must
  still run out).
- Cross-node MATCHED delivery and durable missed endings.

**Out of scope**
- The load balancer, TLS and the choice of cloud products for Redis and the
  inter-node transport (ADR-16 scoped DynamoDB to the game-event store only; this
  spec does not widen it, see D2).
- Clock credit or abort after a store outage (#207).
- Matchmaking pool sharding (GS-5 keeps its own Redis claim protocol).
- Spectators and broadcast fan-out (X-10).

## 2. Requirements traced

| Req | How this spec meets it |
|---|---|
| NFR-2 zero in-progress-game loss on single-node failure | The stream is the truth. A surviving node rebuilds from it and claims the game (§4.3). Acknowledged events are never lost because they were persisted before the ack (NFR-7). |
| NFR-3 linear horizontal scale | Per-move cost is O(1) lookups (§4.2). No central routing service on the hot path. Membership traffic is per node, not per game. |
| NFR-1 move latency | Edge and owner on the same node keep today's path. A remote owner adds one in-region hop, budgeted at p99 under 5 ms (§5.3). |
| Part XIII, ADR-10 | Redis holds only ephemeral hints (membership, presence, routing cache). It is never the source of truth and is never the fence. |
| XII.2 chaos gate | §8 maps each failure to a test in the #203 harness. |

## 3. Model

- **Owner** of a game: the one node running that game's `Session` actor, flag
  timer and join timer. Exactly one owner is live at a time.
- **Edge** of a user: the node holding that user's WebSocket. Any node can be an
  edge. When the edge is also the owner, today's in-process path is unchanged.
- **Safety and liveness are separate.** Safety (no divergent history, no lost
  acknowledged event, no stale owner writing after takeover) comes only from the
  store: the seq-conditional append (GS-4) and the claim event (§4.1). Liveness
  (who should take over, when) comes from membership in Redis and timing. A wrong
  liveness view can cost a retry or a brief double attempt. It cannot corrupt a
  stream, because the store, not Redis, is the fence.

## 4. Ownership

### 4.1 The claim event

A new additive event, `OwnerClaimed{node_id, epoch}`, is appended by a node that
becomes owner. `epoch` is one more than the last claim in the stream. The fold
records `OwnerNode` and `OwnerEpoch` and changes nothing else (no clock effect,
no phase effect).

- The pairing node writes the first claim (epoch 1) inside the opening
  `AppendBatch` (GS-5 §3.2), so a new game costs no extra write.
- A node taking over appends `OwnerClaimed{self, epoch+1}` at the head it just
  folded. If another claim landed first the append conflicts, the node reloads,
  sees a newer claim, and defers to it.
- Because every owner appends only at the head it folded, a claim commits a
  fence: any append by an older owner at an earlier expected seq now conflicts.

**Invariant I9 (stale owner).** After a claim with epoch `e` commits at seq `k`,
no event written by an owner with epoch below `e` commits at a seq above `k`.
Events committed before `k` are legitimate and were acknowledged before takeover.

A deposed owner learns it on its next conflicting append or tick reload (the
reload shows a claim with a higher epoch). It then stops: its flag timer exits,
its session is dropped, and commands from its local connections are forwarded to
the new owner (§5). A command in flight at the moment of takeover is not
acknowledged; the client resends it and it is idempotent by ply (GS-6 §3.3).

This needs one new event type in the codec (Rust) and the Go seam, and it relies
on #187 (the fold rejects unknown event types, which breaks rolling deploys). See
D1 and §10.

### 4.2 Who owns, and who should take over

- **Current owner** is the node in the last claim. It is a fact in the stream,
  not a hash of the membership, so a node joining or leaving never moves a live
  game between healthy nodes.
- **Membership.** Each node holds `node/{id}` in Redis with a 6 s TTL, refreshed
  every 2 s, carrying its peer address. One key per node, not per game.
- **Takeover** happens only when the current owner is not in membership (it died,
  or it drained and released). The successor is the live node with the highest
  rendezvous hash of `(game_id, node_id)`, so in a stable view exactly one node
  tries. In an unstable view several may try; the claim append picks one.
- **Self-fencing.** A node that cannot refresh its membership key for 4 s stops
  serving: it closes its game sessions and its client connections. The 2 s gap
  between its stop and key expiry is the safety margin for timing; the claim
  fence (§4.1) is the guarantee if timing is wrong.
- **Drain (deploy).** A draining node asks the successor, per game, to claim
  (an explicit hand-off) and then stops serving that game. Clients resync (STATE)
  on the new node. This keeps deploys from looking like node loss.

### 4.3 Rebuild entry point

One function, `startGame(gameID, role)`, is the only way a session comes into
being. It reads the stream, takes the time control from `GameCreated`, folds,
repairs a half-terminal stream (`CmdFinishHalfTerminal`), appends the claim when
taking over, and then **always** arms the flag timer and the join timer. Today
only `deliverMatched` arms them, which is why a rebuilt game has no clock driver
(#98, #173, finding from #212). Pairing, resync, a forwarded command and orphan
adoption all call `startGame`.

## 5. Routing between nodes (#173)

### 5.1 Attach, not presence

The owner never looks a user up. An edge that holds a seated connection sends the
owner `Attach{game, seat, user, conn}` over the node-to-node stream, and
`Detach` when the connection ends. The owner keeps the attached edges for its
game in memory and pushes every server frame (MOVE_ACCEPTED to both seats,
GAME_END, STATE, DRAW_OFFERED and the rest) to the edge of each seat. When the
owner changes, each edge re-attaches to the new owner, because the edge is the
one that holds the connection and knows which games it is seated in. If an
edge's stream to the owner breaks, the owner treats those seats as disconnected,
which is what the clock-aware forfeit path already does (GS-6 §3.4).

This means no per-user presence key in Redis and no per-connection Redis
traffic at 1M connections.

### 5.2 Finding the owner

An edge resolves `game -> owner` from its in-memory cache, then from the Redis
hint `game/{id}/owner` (written by the owner at claim, TTL 1 h), then from the
stream's last claim. The hint is only a cache: a node that receives a command
for a game it does not own replies `NOT_OWNER{hint}` and the edge re-resolves. An
owner that is not in membership triggers takeover (§4.2) by the successor.

### 5.3 The move path and latency

`MOVE` on an edge: stamp `recv` when the socket read returns (#183), look up the
seat locally (O(1), #174), then either call the local session or send the frame
to the owner with `age_ms = now - recv`. The owner computes
`recv_owner = now_owner - age_ms`, so no node's wall clock is compared with
another's. The owner persists, then pushes MOVE_ACCEPTED to both seats' edges.

A remote owner adds one in-region round trip on the move path (edge to owner for
the command, owner to edge for the ack). The budget is p99 under 5 ms for the
hop, so NFR-1's p99 150 ms still holds with the store append dominating. This is
measured in the G-E load test (#138), not assumed.

### 5.4 Ordering and delivery

One ordered stream per (edge, owner) pair carries all frames for all games
between the two nodes. Frames for one connection stay in order. The owner's actor
serialises commands per game, as today. A frame lost to a broken stream is
recovered by the existing client RESYNC, never by replay.

### 5.5 Cross-node MATCHED

The node that wins the pairing creates the game (opening batch including the
first claim, §4.1), becomes its owner, and sends MATCHED to each seat through
that seat's edge. The waiting seeker's edge is recorded in the seek record in the
Redis pool (`edge_node`, `conn`), so the winner reads it from the claimed seek
and needs no presence lookup. If the delivery fails (that edge is gone) nothing
is lost: the user finds the game through the durable index on reconnect (§6).

### 5.6 Connection-local state that must move out of memory

| Today (process-local) | After |
|---|---|
| `userGames` (resync index) | durable user index (§6) |
| `missedEnds`, `missedOrder` (#169, #172) | durable user index (§6) |
| `gameUsers`, per-game `conns` | edge keeps `conns` for its own users; owner keeps the attached-edge list |
| `joined`, `endBroadcast` latches | owner only (they belong to the owner's session) |
| per-connection lookup budget (#170) | stays per connection on the edge |

## 6. Finding a user's games after a restart (#98, #173)

A restart empties every in-memory index, and Redis is not durable (ADR-10), so
the lookup "which games is this user in" needs a durable home.

**Index items (hints, not truth).** Two small mutable item kinds in the
game-event table's account, written in the same `TransactWriteItems` as the
opening `AppendBatch`:

- `user_games`: partition `user/{uid}`, sort `game_id`. Attributes: `seat`,
  `state` (`live`, `ended`), `ended_at`, and a DynamoDB TTL.
- `live_games`: partition `live/{shard}` (shard = hash(game_id) mod 256), sort
  `game_id`. Attributes: `owner_node` (updated at each claim) and `started_at`.

The owner updates them after `GameEnded` commits: `user_games.state = ended`
for both users, and a delete of the `live_games` item. A crash between the
commit and the update leaves a stale hint. That is harmless: every consumer
re-reads the game's stream before acting, so a hint can cost an extra read but
cannot change a result. The stream stays the only truth.

**AUTHENTICATE.** The edge queries `user/{uid}` (newest first, limit 8). For each
candidate it resolves the owner (§5.2) and attaches, or triggers `startGame` when
the owner is not live. A live game gets its STATE pushed. An ended game within
the missed-ending TTL gets its ended STATE once; the edge then deletes the
`user_games` item (replacing `missedEnds`, which survives neither a restart nor a
second node). A reconnect storm after a node loss is one Query per user, with
client backoff and server-side jitter.

**Scope.** This writes outside the event stream, so it touches ADR-16's scope
("DynamoDB, game-event store only"). See D2.

## 7. Games whose owner died with nobody connected

If both players are gone, no client triggers a rebuild, and the clock must still
run out (GS-7 §3.5). Two mechanisms, both ending in `startGame(adopt)`:

- **Fast path (Redis, ephemeral).** Each owner keeps the set
  `node/{id}/games` (add at claim, remove at end). When a node's membership key
  expires, each live node takes the games in that set for which it is the
  rendezvous successor. Adoption is spread with jitter and a per-node
  concurrency cap so a node loss does not become a thundering herd.
- **Slow path (durable audit).** Redis can be lost too. Each shard of
  `live_games` is audited every 60 s (jittered) by the node with the highest
  rendezvous hash for that shard. The audit Queries the shard and adopts any game
  whose `owner_node` is not in membership and for which the auditor is the
  successor. A game is then never stranded for longer than one audit period plus
  rebuild time, whatever happened to Redis.

Targets, measured in the G-E chaos test and not assumed: a game whose owner dies
is serving again within 10 s of membership expiry at p99 for the fast path, and
within 90 s for the slow path.

## 8. Failure modes and how #203 covers them

| Failure | Behaviour | Test (invariant harness, #203) |
|---|---|---|
| Owner crashes between two writes of one command | Another node rebuilds from the stream; the half-terminal repair finishes the ending; the resent command is idempotent (I7) | kill at write k, rebuild, resume the same seeded command stream; I1 to I8 hold |
| Owner crashes after persist, before ack | The client resends; the rebuilt session re-acks from the stream, with no duplicate event (I6, I7) | kill after write, before ack |
| Owner partitioned from Redis but alive | It self-fences after 4 s. If it does not, its next append conflicts with the new claim (I9) | two sessions on one store, old one keeps writing |
| Two nodes both believe they are the successor | One claim wins by the seq condition; the other defers | two simultaneous `startGame(adopt)` |
| Redis lost | Membership is rebuilt by the next heartbeats (2 s); the owner hint is a cache; adoption uses the durable audit | multi-node variant, Redis wiped mid-run |
| Store outage | Unchanged: GS-6 §3.5 retry, #207 for clock credit | existing #97 tests |
| Edge dies | The owner sees the stream drop and treats the seats as disconnected; the client reconnects to any node and resyncs | multi-node variant, kill an edge |
| Owner dies, nobody connected, clock runs out | The successor adopts, repairs, arms the ticker, and the flag ends the game | multi-node variant: kill a node, let the clock run out, expect GAME_END (finding from #212) |
| Stale owner after takeover | I9: it cannot commit; it drops the game | the I9 assertion, no longer pending |

The harness gains a per-event author record `(node, epoch, seq)` so I9 is a
direct assertion over the committed log, not an inference.

## 9. Decisions for the owner (nothing is built before these are answered)

**D1. Fencing by a claim event (recommended) or by timing and CAS alone.**
Recommended: the `OwnerClaimed` event (§4.1). It makes takeover a point in the
log, so I9 is checkable and a stale owner is guaranteed to learn it is stale.
Alternative: no new event; rely on self-fencing plus seq conflicts. Safety
(no divergent history) still holds, but a takeover is not a point in the log, a
quiet game can keep two owners trying, and I9 cannot be asserted. Cost of the
recommendation: one additive event type in the codec, and #187 first.

**D2. Where the durable user and live-game indexes live (§6).** Recommended:
mutable hint items in DynamoDB written in the opening transaction. Alternatives:
(a) Redis only: not durable, so a Redis loss strands players and games, which
fails the zero-loss gate; (b) a per-user event stream: grows forever and needs
compaction. This is the one place the design touches ADR-16's scope; if you want
it recorded, it is an ADR-16 amendment limited to "game index hints", and it
ratifies nothing else about the platform.

**D3. Orphan adoption with a fast Redis path and a slow durable audit (§7).**
Recommended. Alternative: durable audit only (simpler, but a dead node's games
wait up to 90 s for their clocks). Or Redis only (simplest, not safe against a
Redis loss).

**D4. Node-to-node transport.** Recommended: direct gRPC bidirectional streams,
one per (edge, owner) pair, no broker on the move path (ADR-12 already names
gRPC). Alternative: a pub/sub bus (Redis or NATS): simpler fan-out, but a shared
hop and a shared failure domain on the hot path. This is a product choice for
the transport only.

**D5. What counts as a drain.** Recommended: explicit hand-off per game on
deploy (§4.2), so deploys are not node losses. Alternative: treat a deploy as a
node loss and rely on adoption: simpler, but every deploy interrupts every game
for the adoption time.

## 10. Plan, in order, one PR each

Nothing here is built until D1 to D5 are answered.

1. **#187:** the fold stops rejecting unknown event types (needed before any new
   event type ships).
2. **Single-node restart (closes most of #98):** the claim event and `startGame`
   as the only session constructor; the durable indexes (§6); AUTHENTICATE
   recovery; the ticker and join timer always armed; half-terminal repair on
   rebuild. #203 crash phase on one node: kill at random write points, rebuild,
   I1 to I8.
3. **Fencing and takeover:** membership, successor selection, self-fencing,
   deposed-owner handling. #203 I9 enabled.
4. **Routing (closes #173):** the peer streams, attach/detach, `NOT_OWNER`,
   cross-node MATCHED, durable missed endings. #203 multi-node variant.
5. **Orphan adoption and drain:** §7 fast and slow paths, drain hand-off. The
   chaos test of the XII.2 gate.

#173 and #98 are done only when step 5's harness phase passes (their stated
acceptance).

## 11. Specs that change when this is accepted

GS-3 §3.5 and §6 (one owner is a node-level fact, not only a goroutine rule);
GS-6 §3.2, §3.4, §5 ("reconnect to a different node: works"), §6.3; GS-7 §3.5
(failover abort is no longer loose: the successor adopts and arms the timer);
GAME-SERVER Q-GS2 (closed by this spec); PROTOCOL §4A event table (the new event
type, additive).

## 12. Not decided here

The load balancer and TLS termination; the cloud product behind Redis; the
number of shards beyond the 256 used as a sizing example; clock credit after a
store outage (#207).
