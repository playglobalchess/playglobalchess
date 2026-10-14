# Game-server GS-4 — durable event store (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS4 (implementation slice of SPEC-S2-GAME-SERVER §2c / ADR-10) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.2 |
| Last updated | 2026-10-14 |
| Implements (master IDs) | X-2, X-14 (game-event store), NFR-2 (no game loss), NFR-7 (persist-before-ack), NFR-9 (fully replayable), ADR-10, ADR-12, ADR-16 (event-store product = DynamoDB) |
| Depends on | SPEC-S2-GAME-SERVER (RATIFIED); GS-1 (the `game.EventStore` interface + `MemStore` stub, PR #34); GS-3 (the live session that consumes it, PR #42); ADR-10 (DECIDED: game truth = append-only event store) |
| Gates it feeds | Contributes to NFR-2/NFR-7 being real (not stubbed); a hard prerequisite for the F-01 vertical slice. Does **not** make a lettered gate green by itself. |

> **Implementation slice — but it carries one real ADR-level sub-decision.**
> ADR-10 DECIDED that game truth is an append-only event store, but it did NOT
> pin the concrete *technology* (it names Postgres for relational and Redis for
> ephemeral, but leaves the event store's product open). GS-4 must pick one.
> That choice is surfaced as **Q-GS4A** below with a proposed reading; it rides
> ADR-10's "event store" decision and needs architecture-group sign-off, but is
> a sub-decision, not a new top-level ADR unless the owner wants it recorded as
> one. Everything else here is implementing the already-ratified `game.EventStore`
> contract. No wire bytes change.

## 1. Purpose & scope

Replace GS-1's in-memory `MemStore` stub with a **durable, append-only event
store** implementing the ratified `game.EventStore` interface, so game truth
survives process/node restart (NFR-2), moves are persisted before they are acked
(NFR-7), and every game is fully replayable (NFR-9). This is the store the live
session (GS-3) already appends to and folds from — GS-4 swaps the implementation
behind the interface; the session, pipeline, and engine seam are unchanged.

**In scope (GS-4):**
- A concrete `EventStore` implementation (Q-GS4A — DECIDED: DynamoDB, ADR-16) under
  `services/game-server/internal/store`, with:
  - `Append(ctx, streamID, ev) (seq, err)` — durable, returns only after commit;
    assigns the next **strictly-increasing, gap-free** seq per stream (§4A.8);
    the `(streamID, seq)` pair is unique, so a concurrent/duplicate append at an
    occupied seq is rejected, not silently reordered.
  - `Load(ctx, streamID) ([]Event, error)` — the full ordered slice, seq 0..n;
    unknown stream → empty slice, no error (the interface's contract).
- **Serialization of `game.Event` to/from durable items** — the stored form of
  each event body (GameCreated, MovePlayed, …). Q-GS4B decides whether this
  reuses the frozen §4A wire codec (via the Go cgo binding) or a store-local
  encoding; proposed: reuse the codec, **tagged with a per-item `encoding_version`**
  so stored bytes == wire bytes for that version and the stream stays replayable
  across any future codec evolution (NFR-9 — the version, not the byte-identity,
  is the guarantee; see §3.1).
- **Atomic terminal append** — the thing that lets GS-4 *remove* GS-3's
  half-terminal recovery window: ClockFlagged+GameEnded (and MovePlayed+GameEnded
  on a terminal move) are committed in a **single `TransactWriteItems`**, so a
  crash can never leave a half-terminal stream. GS-3's `finishIfHalfTerminal`
  stays as defense-in-depth but is no longer the correctness guarantee. (Q-GS4C:
  expose this as `AppendBatch([]Event)` on the interface — see §4/§10.)
- **Table definition + provisioning** for the store (Q-GS4A — DynamoDB,
  on-demand capacity; table created via IaC, not an in-app migration).
- **Config + client lifecycle** (AWS region + credentials from env/secret/role,
  a shared `dynamodb.Client`, `context.Context` on every call — ADR-12).
- Tests: the §8 durability/ordering/concurrency/persist-before-ack suite, run
  against a real store instance in CI (Q-GS4D: DynamoDB Local as a service
  container).

**Out of scope (later chunks — do NOT start):**
- Matchmaking (§3.4), WS transport (§3.6 wire), rating (X-4) — GS-5/6/X-4.
- The analytics columnar warehouse (ADR-10) — fed from the event stream, never
  on the play path; a separate X-14 chunk.
- Multi-region replication / sharding of the store (§6.3, G-E).
- Snapshotting / compaction of long streams (a perf optimization; the fold is
  O(ply) and fine at F-01 scale — revisit at G-E).

## 2. Requirements traced

| Requirement | Where |
|---|---|
| ADR-10 game truth = append-only event store | §3 — the whole slice. |
| NFR-7 persist-before-ack | §3.2 — Append returns only after the durable commit; the pipeline already waits on it. |
| NFR-2 no game loss / §3.5 resume on any node | §3.3 — Load replays the durable stream; §3.2 — a valid move that loses a cross-node seq race is re-sequenced by the session, never dropped. |
| §4A.8 strictly-increasing gap-free seq | §3.2 — `(stream_id, seq)` key + conditional put; conflicts typed + reconciled. |
| NFR-9 fully replayable / audit / anti-cheat feed | §3.1/§3.3 — the stream is the immutable record; each item self-describes its `encoding_version` so replay == fold across any future codec evolution. |
| GS-3 terminal atomicity | §3.4 — `TransactWriteItems` multi-event append removes the half-terminal window. |
| NFR-5 (no chess logic) | unchanged — the store moves opaque event bytes; no game logic. |

## 3. Design

### 3.1 Store choice (Q-GS4A — DECIDED: DynamoDB, ADR-16)

A single DynamoDB table `game_events`, one **item** per event:

```
game_events  (ADR-16)
  stream_id        S   (partition key)  -- "game/{uuid}" (SPEC-S2-PROTOCOL §4A.8)
  seq              N   (sort key)       -- 0-based, strictly increasing per stream
  event_type       N                    -- the §4A event type code
  encoding_version N                    -- the codec version `payload` is encoded with (Q-GS4B)
  payload          B                    -- the serialized event body, raw bytes (Q-GS4B)
  committed_at     S                    -- RFC-3339 commit timestamp
```

Capacity mode: **on-demand** (pay-per-request) for F-01 — no idle cost, scales
with traffic (ADR-16); revisit provisioned/reserved capacity at G-E.

The `(stream_id, seq)` key is the ordering + idempotency guarantee: a write uses
a **conditional put** (`attribute_not_exists(seq)` within the partition), so a
second attempt at an already-occupied seq fails with a condition-check error —
how a racing/duplicate append is rejected rather than silently overwritten.
Reads are a single `Query` on the partition, `ScanIndexForward=true` (seq order),
paginated — the full ordered stream for replay/resync.

**Why `encoding_version` exists (NFR-9).** The store is the *immutable,
forever-replayable* record (NFR-9) — it outlives every version of the wire
codec. Storing payloads with the §4A codec (Q-GS4B) makes stored bytes identical
to wire bytes *for a given codec version*, but "byte-identical to the wire" is
**not** the durability guarantee — the per-item `encoding_version` is. Without
it, an item written with codec v1 and one written with a future codec v2 are
indistinguishable bytes, and Load has no way to pick the right decoder, so
replay/audit of historical games silently misdecodes or fails — the exact
guarantee the store exists to provide. With it, every item is self-describing:
Load dispatches on `encoding_version`, so a v1 item stays replayable forever even
after a v2 codec ships. The `event_type` code is pinned to the encoding version
(a type-code's meaning is only defined within a codec version), so the single
version attribute covers both. GS-4 writes the current version (`1`) on every
append; a codec evolution adds a new version value and a decode branch, and never
rewrites or reinterprets existing items.

*Why DynamoDB (ADR-16):* the access pattern — per-stream append keyed
`(stream_id, seq)`, ordered full-stream reads, native conditional-put for
seq-rejection, `TransactWriteItems` for the atomic terminal append — is a
textbook key-value/log fit, with the lowest ops burden of the scale-ready
options and near-zero cost at F-01 traffic (on-demand). It stays behind the
`game.EventStore` interface, so it is replaceable at G-E with no session/pipeline
change. The decision is **scoped to the event store only** — it does not ratify
AWS as the platform for accounts/cache/media/warehouse (ADR-16 §scope).

### 3.2 Append (NFR-7, §4A.8)

`Append(ctx, streamID, ev)`:
1. determine the next seq for the stream (Q-GS4E — the session passes the
   expected next seq, derived from the head it already holds);
2. `PutItem` the event item with a **condition expression**
   `attribute_not_exists(seq)` scoped to the partition, writing
   `encoding_version` = the current codec version (`1`) alongside the §4A-encoded
   `payload` — the conditional put is the durable commit;
3. return the seq **only after** the put succeeds. A failed/aborted put returns a
   non-nil error and NO seq, so the caller (pipeline) does not ack (NFR-7). This
   is the exact contract the `MemStore` stub modelled synchronously; GS-4 makes it
   a real durable, conditional commit.

**Seq-conflict is a typed, recoverable error (NFR-2).** A failed condition check
(the `(stream_id, seq)` item already exists) is returned as a **distinguishable**
`store.ErrSeqConflict`, never a generic error. This matters because the store
**cannot** tell a genuine duplicate from a valid move that lost a cross-node seq
race — the two are identical bytes at the store layer (Q-GS4E). Treating the
rejection as a plain failure would silently drop a legitimate move (an NFR-2
violation). So the store does the narrow thing it can do correctly — reject the
occupied seq and *say which error it is* — and **recovery lives in the caller
that owns game truth, the GS-3 session actor**, not in the store (the store has
no game context to reconcile with). On `ErrSeqConflict` the session reloads the
stream head (`Load`) and reconciles:
- if its event is **already present** at that seq (idempotent re-ack — the same
  reload-and-reconcile shape GS-3's existing re-ack path uses), it is a true
  duplicate → treat as the already-committed event, do not re-append;
- if its event is **genuinely new** (it lost the race to a *different* event),
  re-fold on the new head, re-validate the move against the engine, and append at
  the next free seq — so a valid loser is **re-sequenced, never dropped**.

The session is single-writer per game (the GS-3 actor serializes its own stream),
so this reconciliation is the *across-node* case Q-GS4E identifies; in the common
single-node case the conflict does not arise. Keeping recovery in the session
reuses its fold/re-ack machinery and keeps the store a dumb, correct append log.

### 3.3 Load (NFR-2, §3.5)

`Load(ctx, streamID)` `Query`s all items for the stream in seq order
(`ScanIndexForward=true`, paginated), deserializes each payload back to a
`game.Event` **by dispatching on the item's `encoding_version`** (so a stream
mixing items written across a codec evolution decodes correctly, each with the
decoder matching its own version), and returns the slice. An item whose
`encoding_version` is unknown to this build is a hard error (the server refuses to
project a stream it cannot faithfully replay — same posture as a corrupt
`event_type`). The live session folds the slice (`FoldWithEngine`) to rebuild
state on any node — no original-node affinity. Unknown stream → empty slice, nil
error.

### 3.4 Atomic terminal append (removes GS-3's recovery window)

GS-3 appends ClockFlagged then GameEnded (and MovePlayed then GameEnded) as
separate calls, and recovers a half-terminal stream on rebuild because the stub
has no transaction. GS-4 commits the ending events **atomically** via DynamoDB
`TransactWriteItems` (each with its own `attribute_not_exists(seq)` condition, so
the whole batch is all-or-nothing and still seq-conflict-safe), exposed as
`AppendBatch` (Q-GS4C) — a crash cannot interleave a half-terminal stream. GS-3's
`finishIfHalfTerminal` is kept as defense-in-depth (and still covers a pre-GS-4
stub stream), but is no longer the primary guarantee once the session uses the
batch append.

## 4. Interfaces & contracts

GS-4 implements the existing `game.EventStore` with no change to its two
methods' signatures. It MAY **additively** extend the interface with
`AppendBatch` for §3.4 (Q-GS4C) — a coordinated change (the stub, the session's
two-append ending paths, and tests update together, like the GS-3 Q-GS3B
change). Both `Append` and `AppendBatch` surface a seq collision as the typed
`store.ErrSeqConflict` (§3.2), so the session can branch on it. No wire bytes
change; the serialized payload format is an internal store contract (Q-GS4B),
not the §4A wire contract — it is byte-identical to the wire *for a given codec
version*, and the item's `encoding_version` (§3.1), not that identity, is what
keeps the stream replayable across codec evolution (NFR-9).

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Append at an occupied seq (true duplicate) | conditional-put fails → typed `ErrSeqConflict`; the session reloads the head, finds its event already present, treats it as the committed event (idempotent), does not re-append. |
| Append at an occupied seq (valid move lost a cross-node race) | same `ErrSeqConflict`; the session reloads the head, re-folds, re-validates, and re-appends at the next free seq — the move is re-sequenced, **never dropped** (NFR-2). |
| Put fails / aborts (crash/disconnect mid-write) | the conditional `PutItem` is atomic — it either lands the whole item or nothing; on failure Append returns an error and NO item exists; the move did not happen (NFR-7). |
| Load an unknown stream | empty slice, nil error (interface contract). |
| Corrupt/unknown event_type on Load | error (refuse to project a corrupt stream — mirrors FoldWithEngine's corrupt-stream refusal). |
| Unknown `encoding_version` on Load | error (refuse to project a stream this build cannot faithfully decode — NFR-9 fidelity over best-effort). |
| Terminal ending appended non-atomically (pre-batch) | GS-3 recovery still closes it on rebuild; §3.4 batch removes the window. |
| DB unavailable at startup | NewStore fails fast with a clear error; the server does not start serving games it cannot persist. |
| Very long stream | O(n) Load; fine at F-01 scale; snapshotting is deferred (out of scope). |

## 6. Non-functional handling

- **6.1 move latency (NFR-1):** the durable append is the dominant term on the
  move path (the move pipeline already identified this). Keep it tight — a single
  conditional `PutItem` on the primary key, a reused client, same-region table on
  the play path. Numbers measured at G-E, not asserted here.
- **6.2 no game loss (NFR-2):** durability is the point; a node loss loses only
  ephemeral state, never committed events (DynamoDB replicates writes across AZs
  before acking).
- **6.3 scale (G-E):** DynamoDB scales horizontally by partition key out of the
  box; provisioned/reserved capacity tuning and (if ever needed) a different
  event-store product are G-E concerns; the `EventStore` interface keeps them
  swappable.

## 7. Security & abuse

- AWS credentials via instance/role or a vault-sourced key, never in code
  (conventions); least-privilege IAM scoped to this one table.
- No SQL surface (DynamoDB API with typed expression values — no string-built
  queries); event bytes are validated on decode (§3.3), input-validation default.
- The store holds game-public events (moves/clocks/results); no tokens/PII cross
  into it. Tenant isolation by `stream_id`; a session only ever reads/writes its
  own stream id.

## 8. Test plan

- **Append/Load round-trip:** append a sequence, Load returns the identical
  ordered slice; payloads deserialize byte-for-byte to the original events.
- **Seq integrity:** seq is 0-based, strictly increasing, gap-free; a second
  append at an occupied seq returns the typed `ErrSeqConflict` (concurrency:
  parallel appends to one stream produce a consistent gap-free order, exactly one
  winner per seq).
- **Seq-conflict recovery (NFR-2):** two tests, one per case the store cannot
  distinguish — (a) the session re-appends its *own* already-committed event →
  reconciled as idempotent, no duplicate item; (b) a *different*, valid event
  lost the seq → the session reloads, re-folds, re-validates, and the event
  lands at the next free seq (asserted present in the stream, nothing dropped).
- **Encoding-version replay (NFR-9):** a stream containing items tagged with the
  current version Loads and folds correctly; an item carrying an unknown
  `encoding_version` makes Load error (not silently misdecode); the test fixes
  the current codec version so a future bump is a deliberate, visible change.
- **Persist-before-ack (NFR-7):** a forced write failure returns an error and
  commits NO item (the real-store analogue of `MemStore.FailNextAppend`); the
  pipeline/session do not advance.
- **Durability across restart (NFR-2):** append, drop the store handle,
  reconnect, Load — the stream is intact; a rebuilt session reproduces state.
- **Atomic terminal (§3.4):** an AppendBatch of ClockFlagged+GameEnded either
  commits both or neither (inject a mid-batch failure; assert no half-terminal
  item set); a rebuilt session is ENDED, never resumes LIVE.
- **Unknown/corrupt stream:** unknown → empty; an item with a bad event_type →
  Load errors.
- **The session suite re-run against the real store** (swap `MemStore` for the
  GS-4 store in the GS-3 integration tests) — proves the session is correct
  against real durability, not just the stub.
- CI runs these against a real store instance (Q-GS4D: a DynamoDB Local service
  container in the `services` job).

## 9. Acceptance criteria (Definition of Done)

- [ ] `services/game-server/internal/store` implements `game.EventStore`
      (durable, strict gap-free seq, persist-before-ack) against the chosen store.
- [ ] Table definition + provisioning committed as IaC; `NewStore` validates
      connectivity + that the `game_events` table exists; items carry
      `encoding_version` and Load dispatches decoding on it.
- [ ] A seq collision returns the typed `store.ErrSeqConflict`; the GS-3 session
      reconciles it (idempotent re-ack vs re-sequence a valid loser) with the §8
      recovery tests green — a valid move that loses a cross-node race is never
      dropped (NFR-2).
- [ ] §8 suite green against a REAL store instance in CI (not just a mock).
- [ ] The GS-3 session integration tests pass with the real store swapped in for
      `MemStore`.
- [ ] Atomic terminal append (§3.4) lands with its all-or-nothing test; GS-3's
      half-terminal recovery is retained but documented as defense-in-depth.
- [ ] No chess logic in the store (reviewer greps); no string-built queries
      (typed DynamoDB expression values only).
- [ ] `make test` green (existing codec/engine/session suites not regressed),
      `gofmt`/`vet` clean; CI `services` green on the hosted runner.
- [ ] This chunk makes no lettered gate green — F-01 slice still needs
      matchmaking + WS transport + rating. Stated in the PR.
- [ ] Q-GS4A (store product = DynamoDB) is recorded in ADR-16; the GS-4 build
      cites it.

## 10. Open questions

- **Q-GS4A — event-store product — DECIDED: DynamoDB (ADR-16).** Use DynamoDB (a
  `game_events` table keyed `stream_id`/`seq`, on-demand capacity) as the event
  store: the access pattern is a textbook key-value/log fit, conditional-put and
  `TransactWriteItems` give the seq-rejection and atomic-terminal semantics
  natively, ops burden is lowest of the scale-ready options, and cost at F-01 is
  near-zero. Greenfield cloud, so no migration cost. Stays behind the
  `EventStore` interface (replaceable at G-E). **Decision scoped to the event
  store only — does NOT ratify AWS as the platform for other roles** (ADR-16
  §scope). Rejected: Postgres (relational engine, not a log — scale/ops cliff as
  the event store); Kafka/EventStoreDB (feed, or 24/7 cluster — premature infra);
  Cassandra/Scylla (heavier ops than serverless). **DECIDED by the owner,
  recorded as ADR-16 under ADR-10.**
- **Q-GS4B — stored payload encoding — PROPOSED (reuse the §4A codec, versioned
  per item).** Store each event body using the frozen wire codec (via the Go cgo
  binding), tagged with a per-item `encoding_version` (§3.1). For a given version,
  stored bytes == wire bytes — replay/audit emit identical frames, one encoding
  (NFR-10 spirit). The version attribute, **not** that byte-identity, is the
  durability guarantee: the store outlives any single wire-codec version (NFR-9),
  so Load must dispatch on the item's version to stay replayable across a future
  codec change — otherwise old items become ambiguous and misdecode. GS-4 writes
  version `1`; a codec evolution adds a value + a decode branch and never
  rewrites existing items. Alternative: a store-local encoding — rejected;
  analytics queryability is the columnar warehouse's job (ADR-10), not the truth
  store's. **Proposed: versioned §4A codec.**
- **Q-GS4C — atomic multi-event append — PROPOSED (add AppendBatch).** Add
  `AppendBatch(ctx, streamID, []Event) ([]seq, error)` to `game.EventStore` so
  the session commits ClockFlagged+GameEnded (and MovePlayed+GameEnded)
  atomically via `TransactWriteItems`, removing GS-3's half-terminal window.
  Alternative: keep single Append and leak the store's transaction some other way
  (leakier). **Proposed: AppendBatch**, as a coordinated additive interface
  change (stub + session + tests together).
- **Q-GS4D — CI store instance — PROPOSED (DynamoDB Local container).** Run the §8
  suite against a **DynamoDB Local** service container in the `services` CI job
  (it supports conditional puts and `TransactWriteItems`, so the concurrency and
  atomic-terminal tests are faithful). Alternative: a hand-rolled in-memory fake
  (less faithful). **Proposed: DynamoDB Local** so the durability/concurrency
  tests exercise real conditional-write semantics.
- **Q-GS4E — seq assignment & conflict recovery — PROPOSED (caller-expected seq
  via conditional put; typed error; session reconciles).** The session passes the
  expected next seq (it already holds the head), and the store writes it with a
  conditional `PutItem` (`attribute_not_exists(seq)`), so the key rejects a race
  — no server-side `max(seq)` scan needed. The session is single-writer per game
  (GS-3 actor), so a conflict is the *across-node* case, not an in-process one.
  Critically, a rejection is **not** terminal: the store returns a typed
  `store.ErrSeqConflict` (it cannot itself tell a true duplicate from a valid
  move that lost the race — identical bytes), and the **GS-3 session actor owns
  recovery** (§3.2) — reload the head, then idempotent re-ack if its event is
  already present, or re-fold/re-validate/re-append at the next seq if it is a
  genuinely new loser, so a valid move is never dropped (NFR-2). Alternative:
  server-side next-seq derivation — an extra read on the hot path for no benefit
  under single-writer. **Proposed: caller-expected seq + conditional put +
  `ErrSeqConflict` + session-owned reconcile.** Revisit if multi-node writers to
  one stream become routine (the recovery contract already covers it).
