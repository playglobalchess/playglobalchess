# Game-server GS-5 — matchmaking (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS5 (implementation slice of SPEC-S2-GAME-SERVER §3.4) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-15 |
| Implements (master IDs) | X-2, F-01.3 (matchmaking), ADR-10 (Redis = ephemeral), ADR-12 |
| Depends on | SPEC-S2-GAME-SERVER (RATIFIED §3.4); GS-4 (the durable `game.EventStore` the created stream is written to, PR #47); the codec seam (`ClientMsg::Seek`, `ServerMsg::Matched`, `GameCreated`/`PlayerJoined` events — all frozen in `protocol/`) |
| Gates it feeds | Creates the `GameCreated`+`PlayerJoined` stream the live session (GS-3) folds. A hard prerequisite for the F-01 vertical slice; makes no lettered gate green by itself. |

> **Implementation slice of an already-ratified behavior.** SPEC-S2-GAME-SERVER
> §3.4 already DECIDED the matchmaking *model* (a Redis sorted-set seek pool keyed
> by rating, closest-rating pairing over a widening window, allocate game_id →
> append `GameCreated`+two `PlayerJoined` → emit `MATCHED{start_state}`; Redis is
> ephemeral, never truth). GS-5 implements that model for F-01 Standard 2-seat.
> It cites the frozen wire/event shapes (`ClientMsg::Seek`,
> `ServerMsg::Matched`, the `GameCreated`/`PlayerJoined` §4A.8 events) and never
> redefines them. The two hard fairness questions (§3.4 Q-GS3 — window-widening
> schedule, provisional ratings, anti-sniping) stay **parked pending the rating
> service (X-4)**; GS-5 ships a simple, correct, replaceable pairing policy.

## 1. Purpose & scope

Build the matchmaker that turns `SEEK` into a created game: a seeker enters a
Redis-backed pool, the matchmaker pairs two compatible seekers, allocates a
`game_id`, assigns seats/colors, writes the opening stream to the GS-4 store, and
emits `MATCHED{start_state}` to both. After `MATCHED` the live session (GS-3)
takes over from the durable stream — matchmaking's only durable output is the
appended `GameCreated`+`PlayerJoined`.

**In scope (GS-5):**
- A `matchmaker` package under `services/game-server/internal/matchmaking` with:
  - `Seek(ctx, seeker)` — add a seeker to the pool's waiting set and attempt an
    immediate pair; returns a match if one is available now, else enqueues.
  - `Cancel(ctx, seeker)` — withdraw an unmatched seeker (disconnect / explicit).
  - the pairing + widening-window logic (pure, unit-testable, Redis-free core).
- A **Redis seek-pool store** (ADR-10 ephemeral): a sorted-set per pool keyed by
  rating, with an **atomic claim** so two nodes cannot both claim the same
  waiting seeker; removal is deferred until after the opening stream is appended
  (§3.3/§3.4, Q-MM6).
- **Game creation on a pair:** allocate a `game_id` (UUID), assign seats + colors
  (Q-MM3), build the `GameCreated`+two `PlayerJoined` events, and commit them to
  the GS-4 store via a single `AppendBatch` at seq 0 (atomic opening — Q-MM4),
  then hand the stream id back so the caller emits `MATCHED{start_state}`.
- **Config + Redis client lifecycle** (endpoint/credentials from env/secret,
  `context.Context` on every call — ADR-12), with `New` fail-fast on an
  unreachable Redis.
- Tests: pure pairing/windowing unit tests + a Redis-backed integration tier
  (Q-MM5: a Redis service container in CI, gated like GS-4's DynamoDB Local).

**Out of scope (later chunks — do NOT start):**
- The WS transport that carries `SEEK`/`MATCHED` frames and binds a connection to
  a seat — GS-6. GS-5 exposes `Seek`/`Cancel` as Go calls; GS-6 wires the frames.
- Rating-aware fairness tuning (Q-GS3) — needs X-4; GS-5's window schedule is a
  placeholder the rating service will refine.
- Rematch/rejoin pools, N-seat/variant pools, spectator routing.
- Sharding/routing of pools across nodes (§6.3, Q-GS2) — G-E.

## 2. Requirements traced

| Requirement | Where |
|---|---|
| §3.4 pool-based pairing by rating, widening window | §3.2 — the pairing core. |
| §3.4 Redis seek pool, ephemeral, never truth (ADR-10) | §3.3 — sorted-set + atomic claim; a node loss drops only unmatched seekers. |
| §3.4 on pair: GameCreated + 2 PlayerJoined, MATCHED{start_state} | §3.4 — game creation writes the opening stream to the GS-4 store. |
| NFR-2 (no game loss) | §3.3 — matchmaking state is Redis-only; the created game is durable the instant the opening batch commits (GS-4). |
| NFR-5 (no chess logic) | the matchmaker allocates seats/colors and the start FEN STRING (Standard startpos, carried opaquely); it computes no chess. |
| F-01.3 rated/unrated pools | §3.2 — rated seekers pair only with rated, unrated with unrated. |

## 3. Design

### 3.1 Seeker & pool model

A **seeker** is `{user_id, pool_id, rating, rated, seek_id, enqueued_at}`.
`pool_id` already encodes rating-band × format × time-control (§3.3 of the parent
spec), so a pool is a single time-control + rated/unrated bucket; `rating` orders
seekers *within* it. The waiting set is a Redis **sorted set** per pool, score =
`rating`, member = `seek_id` (with the seeker fields in a companion hash keyed by
`seek_id`, TTL'd so an abandoned seek self-expires).

### 3.2 Pairing core (pure, Redis-free)

The pairing decision is a pure function over the current waiting set so it is
unit-testable without Redis:

- On a new seeker S, find the waiting seeker whose rating is closest to S's within
  the current **window** `W(elapsed)`. The window widens with the oldest waiting
  seeker's age: `W(t) = base + slope·t`, capped at `W_max` (concrete numbers are a
  placeholder — Q-GS3/X-4 will tune them; the schedule is a config value, not a
  frozen constant).
- A seeker never pairs with itself; rated pairs only with rated (the pool already
  separates these, so this is an invariant check, not a filter).
- Deterministic tie-break (closest rating, then oldest `enqueued_at`) so the pure
  core is testable against fixed inputs.

### 3.3 Redis seek-pool store (ephemeral, ADR-10)

- `Enqueue(seeker)` — `ZADD pool:{id} rating seek_id` + `HSET seek:{seek_id} …`
  with a TTL (an abandoned seek expires rather than lingering).
- `TryClaim(newSeeker)` — the **atomic claim**: read candidates near the window,
  pick the pure-core winner, and **atomically mark both seek_ids as claimed** (a
  Lua script / `MULTI` that flips a `claimed` flag — e.g. moves them to a
  per-node `claimed:{node}` set with a short TTL — so two nodes cannot both claim
  the same waiting seeker). It does **not** delete them yet. The loser of a
  concurrent claim retries against the updated set. **Removal is deferred to
  after the opening stream is durably appended** (§3.4 step 5, Q-MM6) — the claim
  reserves the pair; game creation commits it. Redis holds no game truth — a node
  loss drops only unmatched/claimed-but-uncommitted seekers (their claim TTL
  lapses and they re-seek).
  **Claim invariant (#90):** the claim succeeds only if BOTH seekers are still
  waiting (their seek hash exists AND they are still members of the pool) and
  NEITHER is claimed, all checked inside the one Lua script. `Remove` deletes the
  claim key together with the hash and the membership, so a claim-keys-only check
  would let a node holding a stale pool read re-claim a just-matched seeker and
  create a second game.
- `Release(seek_id)` — undo a claim (claim held but creation failed): clear the
  `claimed` flag so the seeker is pairable again (bounded by the claim TTL anyway).
- `Remove(seek_id)` — `ZREM` + `DEL` the hash; called in §3.4 step 5 AFTER the
  opening append, to finalize the match.
- `Cancel(seek_id)` — a seeker withdraws: `ZREM` + `DEL` the hash (same as Remove,
  caller-initiated).

### 3.4 Game creation on a pair (writes the opening stream)

On a successful atomic **claim** of A and B (the pair is reserved in Redis, not
yet removed), in this exact order so a crash never consumes a seeker without a
created game (Q-MM6):
1. allocate `game_id` = a v4 UUID; `stream_id = StreamIDFor(game_id)`.
2. assign seats + colors (Q-MM3): seat 0 / seat 1, colors white/black.
3. build `GameCreated{pool_id, rated, seats:[{0,white},{1,black}], start_fen =
   Standard startpos string, time_control}` + `PlayerJoined{0, A}` +
   `PlayerJoined{1, B}` (seat↔user per the color assignment).
4. **FIRST** commit all three as a **single `AppendBatch` anchored at seq 0**
   (seqs 0,1,2 in one atomic batch) to the GS-4 store (Q-MM4) — an atomic
   opening: a crash can never leave a half-created game (GameCreated with no
   players), mirroring GS-4's atomic-terminal guarantee. The created stream is
   now durable truth.
5. **THEN** `Remove` both seek_ids from Redis (finalize the claim), and return
   `(game_id, start_state)` so the caller (GS-6 transport) emits
   `MATCHED{start_state}` to both seats. If step 4 fails, `Release` the claims
   (the pair becomes pairable again) and surface the error — nothing was created,
   nothing lost. If the crash lands BETWEEN step 4 and step 5, the game exists and
   is reachable; the stale claims lapse via TTL — the safe direction (Q-MM6).
   The `start_state` is the §4A.6 `StateBody` for the opening position (reused per
   Q-P4 — one state format).

The live session (GS-3) is then constructed from the durable stream exactly as it
is for a reconnect — matchmaking has no special handoff beyond the committed
events.

## 4. Interfaces & contracts

- Matchmaker exposes `Seek(ctx, Seeker) (Match, error)` and
  `Cancel(ctx, seekID) error`; `Match = {GameID, StreamID, StartState, Seats}`.
  No wire frames here — GS-6 maps `ClientMsg::Seek` → `Seek` and `Match` →
  `ServerMsg::Matched`.
- Writes the opening stream through the **existing** `game.EventStore`
  (`AppendBatch` at seq 0) — no new store surface; GS-4's typed `ErrSeqConflict`
  cannot arise at seq 0 for a fresh UUID game, but is handled (a UUID collision or
  a double-create is reconciled by abandoning the duplicate, never overwriting).
- No §4A byte change; no new event types (`GameCreated`/`PlayerJoined` are frozen).
- The Redis seek-pool store is behind a `SeekPool` interface, so the pure
  matchmaker is tested with an in-memory fake and the Redis impl is swappable.

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| No compatible waiting seeker | the seeker is enqueued; `Seek` returns "no match yet" (not an error); a later counter-seek pairs it. |
| Two nodes claim the same waiting seeker | the atomic `TryClaim` (Lua/`MULTI`) lets exactly one win; the loser retries pairing against the updated set — no double-match, no lost seeker. |
| Seeker disconnects while waiting | `Cancel` (or the hash TTL) removes it; no stale pairing. |
| Crash BETWEEN the atomic claim and the opening AppendBatch | the claim is held but no game exists; the claim TTL lapses and both seekers re-seek. No game lost (none created). |
| Crash BETWEEN the opening AppendBatch and the Redis Remove | the game IS created (durable truth) and reachable; the stale claims lapse via TTL. The safe direction — claim→append→remove (Q-MM6). |
| Opening AppendBatch fails | `Release` both claims (they become pairable again); surface the error. Nothing created, nothing lost. |
| Opening AppendBatch conflict at seq 0 | only possible on a game_id reuse (UUID collision, ~never) — abandon and re-allocate; never overwrite an existing stream. |
| Redis unavailable | `New` fail-fasts; an in-flight `Seek` returns an error and the client retries — no game is lost (none created yet). |
| Rated seeker vs unrated pool | impossible by pool_id construction; asserted, not filtered. |

## 6. Non-functional handling

- **6.1 pairing latency.** A seek is a bounded ZRANGEBYSCORE around the seeker's
  rating + an atomic claim — O(log n) Redis ops, no scan. Measured at G-E.
- **6.2 no game loss (NFR-2).** Matchmaking is Redis-ephemeral; the created game
  is durable the instant the opening batch commits. A node loss drops only
  unmatched seekers.
- **6.3 scale (G-E).** Pools shard by `pool_id` across the Redis cluster; the
  pairing core is per-pool and lock-free beyond the atomic claim. Concrete
  sharding is Q-GS2 / G-E.

## 7. Security & abuse

- Redis credentials via env/secret, never in code; least-privilege.
- A seeker is bound to an authenticated `user_id` (GS-6 supplies it from the
  authed connection); the matchmaker never trusts a client-supplied rating — it
  reads the rating from X-4/accounts (for F-01 bring-up, a seed rating until X-4
  lands, flagged Q-MM2).
- Seek-flood / rapid re-seek rate-limiting is a GS-6/transport concern (Redis
  rate-limit counters, ADR-10); noted, not implemented here.

## 8. Test plan

- **Pairing core (pure, no Redis):** closest-rating match within the window;
  widening over the oldest seeker's age; no self-pair; rated/unrated invariant;
  deterministic tie-break — table-driven against fixed waiting sets.
- **Atomic claim (Redis):** two concurrent `TryClaim` for the same waiting seeker
  → exactly one match, the other re-queued; asserted against a real Redis
  (Q-MM5 service container), not just the fake.
- **Game creation:** a pair writes exactly `GameCreated`+2×`PlayerJoined` at seqs
  0,1,2 via one `AppendBatch`; folding the stream (GS-3 `FoldWithEngine`) yields a
  LIVE game with the right seats/colors/time-control; `MATCHED.start_state` equals
  the opening `StateBody`.
- **Opening atomicity:** inject a mid-batch failure → neither opening event lands
  (no half-created game).
- **Cancel / TTL:** a cancelled or expired seeker is not paired.
- **Crash-ordering (Q-MM6):** the AppendBatch-before-Redis-remove ordering leaves
  a created game reachable even if the Redis remove is lost.
- `make test` green (existing suites not regressed), `gofmt`/`vet` clean; the
  Redis integration tier runs against a `redis` service container in CI.

## 9. Acceptance criteria (Definition of Done)

- [ ] `internal/matchmaking` with a pure pairing core + a `SeekPool` interface and
      a Redis implementation; `New` fail-fasts on unreachable Redis.
- [ ] A pair allocates a game_id, assigns seats/colors, and commits
      `GameCreated`+2×`PlayerJoined` atomically at seq 0 via the GS-4 store;
      the folded stream is a LIVE game with correct seats.
- [ ] The atomic claim prevents double-matching under concurrent seeks (proven
      against a REAL Redis in CI, not just a fake).
- [ ] Pure pairing/windowing unit tests + the Redis integration tier green;
      `make test` green; CI `services` green with a Redis service container.
- [ ] No chess logic in the matchmaker (reviewer greps); no new §4A shapes.
- [ ] Makes no lettered gate green — F-01 still needs the WS transport (GS-6) +
      rating + anti-cheat. Stated in the PR.
- [ ] Redis as the seek-pool product is recorded (ADR-10 already names Redis for
      ephemeral; GS-5 cites it — a new ADR only if an owner wants it formal).

## 10. Open questions

- **Q-MM1 — Redis client library.** `redis/go-redis/v9` (the de-facto standard,
  maintained, supports Lua + cluster). Alternative: `rueidis` (faster, heavier
  API). **Proposed: go-redis/v9** for F-01; revisit at G-E if latency demands.
- **Q-MM2 — seeker rating source before X-4.** The matchmaker needs a rating to
  order the pool, but the rating service (X-4) isn't built. **Proposed:** a seed
  rating (e.g. 1500) read behind a `RatingSource` interface, so X-4 swaps in later
  with no matchmaker change. Pairing still works (everyone near 1500 early on).
- **Q-MM3 — seat/color assignment.** **Proposed:** seat 0 = white, seat 1 =
  black; the two seekers are assigned by a coin-flip (deterministic in tests via
  an injected RNG) so color is fair. Alternative: alternate by rating — rejected
  (no fairness basis). Revisit for rematch (color-swap) in a later chunk.
- **Q-MM4 — opening-stream atomicity.** **Proposed:** one `AppendBatch` of all
  three opening events at seq 0 (reusing GS-4's `TransactWriteItems`), so a
  half-created game is impossible — symmetric with GS-4's atomic terminal.
  Alternative: three separate appends + recovery — rejected (reintroduces the
  half-state window GS-4 removed).
- **Q-MM5 — Redis in CI.** **Proposed:** a `redis` service container on the
  `services` job (like GS-4's DynamoDB Local), with the atomic-claim integration
  test gated on a `REDIS_ENDPOINT` env (skips locally, runs in CI).
- **Q-MM6 — pair/create ordering under crash.** **Proposed:** AppendBatch the
  opening stream on a reserved game_id FIRST, THEN remove the two seekers from
  Redis — so a crash between leaves a *created, reachable* game (truth) rather
  than two silently-consumed seekers with no game. The lingering Redis seeks
  self-expire via TTL. Flagged for review because it is the one ordering choice
  with a durability consequence.
