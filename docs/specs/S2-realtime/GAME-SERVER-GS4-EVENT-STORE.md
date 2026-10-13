# Game-server GS-4 — durable event store (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS4 (implementation slice of SPEC-S2-GAME-SERVER §2c / ADR-10) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-13 |
| Implements (master IDs) | X-2, X-14 (game-event store), NFR-2 (no game loss), NFR-7 (persist-before-ack), NFR-9 (fully replayable), ADR-10, ADR-12 |
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
- A concrete `EventStore` implementation (Q-GS4A: proposed Postgres) under
  `services/game-server/internal/store`, with:
  - `Append(ctx, streamID, ev) (seq, err)` — durable, returns only after commit;
    assigns the next **strictly-increasing, gap-free** seq per stream (§4A.8);
    the `(streamID, seq)` pair is unique, so a concurrent/duplicate append at an
    occupied seq is rejected, not silently reordered.
  - `Load(ctx, streamID) ([]Event, error)` — the full ordered slice, seq 0..n;
    unknown stream → empty slice, no error (the interface's contract).
- **Serialization of `game.Event` to/from durable rows** — the stored form of
  each event body (GameCreated, MovePlayed, …). Q-GS4B decides whether this
  reuses the frozen §4A wire codec (via the Go cgo binding) or a store-local
  encoding; proposed: reuse the codec so stored bytes == wire bytes (one
  encoding, NFR-10 spirit; replay/audit produce identical frames).
- **Atomic terminal append** — the thing that lets GS-4 *remove* GS-3's
  half-terminal recovery window: ClockFlagged+GameEnded (and MovePlayed+GameEnded
  on a terminal move) can be committed in a **single transaction**, so a crash
  can never leave a half-terminal stream. GS-3's `finishIfHalfTerminal` stays as
  defense-in-depth but is no longer the correctness guarantee. (Q-GS4C: do we add
  an `AppendBatch([]Event)` to the interface for this, or keep single Append and
  rely on the store's own transaction around the session's two calls? Proposed:
  add `AppendBatch` — see §4/§10.)
- **Schema + migrations** for the store (Q-GS4A).
- **Config + connection lifecycle** (DSN from env/secret, pool, `context.Context`
  on every call — ADR-12).
- Tests: the §8 durability/ordering/concurrency/persist-before-ack suite, run
  against a real store instance in CI (Q-GS4D: how CI gets one — a service
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
| NFR-2 no game loss / §3.5 resume on any node | §3.3 — Load replays the durable stream; a rebuilt session on any node reproduces state. |
| §4A.8 strictly-increasing gap-free seq | §3.2 — unique `(stream_id, seq)`; seq assigned transactionally. |
| NFR-9 fully replayable / audit / anti-cheat feed | §3 — the stream is the immutable record; replay == fold. |
| GS-3 terminal atomicity | §3.4 — transactional multi-event append removes the half-terminal window. |
| NFR-5 (no chess logic) | unchanged — the store moves opaque event bytes; no game logic. |

## 3. Design

### 3.1 Store choice (Q-GS4A — proposed Postgres)

A single `game_events` table (one row per event):

```
game_events(
  stream_id   text    not null,   -- "game/{uuid}" (SPEC-S2-PROTOCOL §4A.8)
  seq         bigint  not null,   -- 0-based, strictly increasing per stream
  event_type  smallint not null,  -- the §4A event type code
  payload     bytea   not null,   -- the serialized event body (Q-GS4B)
  committed_at timestamptz not null default now(),
  primary key (stream_id, seq)
)
```

The `(stream_id, seq)` primary key is the ordering + idempotency guarantee: a
second attempt to write an already-occupied seq fails the insert (unique
violation), which is how a racing/duplicate append is rejected rather than
silently reordered. Reads are `WHERE stream_id = $1 ORDER BY seq`.

*Why Postgres (proposed):* it is already the mandated relational store (ADR-10),
so GS-4 adds **no new operational dependency** for P1/F-01; it gives real
durability + ACID transactions (needed for §3.4 atomic terminal append); and the
`EventStore` interface means a dedicated event-store product can replace it later
(G-E scale) with no change to the session/pipeline. The alternative — standing up
a separate event-store technology now — is more infra for no F-01 benefit.
**Q-GS4A** records this for architecture sign-off.

### 3.2 Append (NFR-7, §4A.8)

`Append(ctx, streamID, ev)`:
1. open a transaction;
2. compute the next seq = `coalesce(max(seq)+1, 0)` for the stream (or pass the
   expected seq and rely on the PK to reject a gap/dup — see Q-GS4E);
3. insert the row;
4. commit — and return the seq **only after** the commit succeeds. A failed
   commit returns a non-nil error and NO seq, so the caller (pipeline) does not
   ack (NFR-7). This is the exact contract the `MemStore` stub modelled
   synchronously; GS-4 makes it a real durable commit.

### 3.3 Load (NFR-2, §3.5)

`Load(ctx, streamID)` reads all rows for the stream ordered by seq, deserializes
each payload back to a `game.Event`, and returns the slice. The live session
folds it (`FoldWithEngine`) to rebuild state on any node — no original-node
affinity. Unknown stream → empty slice, nil error.

### 3.4 Atomic terminal append (removes GS-3's recovery window)

GS-3 appends ClockFlagged then GameEnded (and MovePlayed then GameEnded) as
separate calls, and recovers a half-terminal stream on rebuild because the stub
has no transaction. GS-4 exposes a way to commit the ending events **atomically**
(Q-GS4C: proposed `AppendBatch(ctx, streamID, []Event) ([]seq, error)`), so a
crash cannot interleave. GS-3's `finishIfHalfTerminal` is kept as
defense-in-depth (and still covers a pre-GS-4 stub stream), but is no longer the
primary guarantee once the session uses the batch append.

## 4. Interfaces & contracts

GS-4 implements the existing `game.EventStore` with no change to its two
methods. It MAY **additively** extend the interface with `AppendBatch` for §3.4
(Q-GS4C) — a coordinated change (the stub, the session's two-append ending
paths, and tests update together, like the GS-3 Q-GS3B change). No wire bytes
change; the serialized payload format is an internal store contract (Q-GS4B),
not the §4A wire contract (though proposed to be byte-identical to it).

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Append at an occupied seq (concurrent/dup) | PK unique violation → error, not silent reorder/overwrite; caller does not ack. |
| Commit fails after insert (crash/disconnect mid-tx) | transaction rolls back; no row; Append returns error; move did not happen (NFR-7). |
| Load an unknown stream | empty slice, nil error (interface contract). |
| Corrupt/unknown event_type on Load | error (refuse to project a corrupt stream — mirrors FoldWithEngine's corrupt-stream refusal). |
| Terminal ending appended non-atomically (pre-batch) | GS-3 recovery still closes it on rebuild; §3.4 batch removes the window. |
| DB unavailable at startup | NewStore fails fast with a clear error; the server does not start serving games it cannot persist. |
| Very long stream | O(n) Load; fine at F-01 scale; snapshotting is deferred (out of scope). |

## 6. Non-functional handling

- **6.1 move latency (NFR-1):** the durable append is the dominant term on the
  move path (the move pipeline already identified this). Keep it tight —
  single-row insert on an indexed PK, connection pooling, co-located DB on the
  play path (§6.1). Numbers measured at G-E, not asserted here.
- **6.2 no game loss (NFR-2):** durability is the point; a node loss loses only
  ephemeral state, never committed events.
- **6.3 scale (G-E):** per-game sharding and a dedicated event-store product are
  G-E concerns; the interface keeps them swappable.

## 7. Security & abuse

- Credentials (DSN) from a vault/secret, never in code (conventions).
- Parameterized queries only (no string-built SQL) — input validation default.
- The store holds game-public events (moves/clocks/results); no tokens/PII cross
  into it. Tenant isolation by stream_id; a session only ever reads/writes its
  own stream id.

## 8. Test plan

- **Append/Load round-trip:** append a sequence, Load returns the identical
  ordered slice; payloads deserialize byte-for-byte to the original events.
- **Seq integrity:** seq is 0-based, strictly increasing, gap-free; a second
  append at an occupied seq errors (concurrency: parallel appends to one stream
  produce a consistent gap-free order, exactly one winner per seq).
- **Persist-before-ack (NFR-7):** a forced commit failure returns an error and
  commits NO row (the real-store analogue of `MemStore.FailNextAppend`); the
  pipeline/session do not advance.
- **Durability across restart (NFR-2):** append, drop the store handle,
  reconnect, Load — the stream is intact; a rebuilt session reproduces state.
- **Atomic terminal (§3.4):** an AppendBatch of ClockFlagged+GameEnded either
  commits both or neither (inject a mid-batch failure; assert no half-terminal
  row set); a rebuilt session is ENDED, never resumes LIVE.
- **Unknown/corrupt stream:** unknown → empty; a row with a bad event_type →
  Load errors.
- **The session suite re-run against the real store** (swap `MemStore` for the
  GS-4 store in the GS-3 integration tests) — proves the session is correct
  against real durability, not just the stub.
- CI runs these against a real store instance (Q-GS4D: a Postgres service
  container in the `services` job).

## 9. Acceptance criteria (Definition of Done)

- [ ] `services/game-server/internal/store` implements `game.EventStore`
      (durable, strict gap-free seq, persist-before-ack) against the chosen store.
- [ ] Schema + migrations committed; `NewStore` validates connectivity + schema.
- [ ] §8 suite green against a REAL store instance in CI (not just a mock).
- [ ] The GS-3 session integration tests pass with the real store swapped in for
      `MemStore`.
- [ ] Atomic terminal append (§3.4) lands with its all-or-nothing test; GS-3's
      half-terminal recovery is retained but documented as defense-in-depth.
- [ ] No chess logic in the store (reviewer greps); parameterized queries only.
- [ ] `make test` green (existing codec/engine/session suites not regressed),
      `gofmt`/`vet` clean; CI `services` green on the hosted runner.
- [ ] This chunk makes no lettered gate green — F-01 slice still needs
      matchmaking + WS transport + rating. Stated in the PR.
- [ ] Q-GS4A (store product) signed off by the architecture group; recorded as an
      ADR note under ADR-10 if the owner wants it formal.

## 10. Open questions

- **Q-GS4A — event-store product — PROPOSED (Postgres).** Use Postgres (a
  `game_events` table) as the event store, since it is already the mandated
  relational store (no new dependency), gives ACID for the atomic terminal
  append, and is replaceable behind the interface at G-E. Alternative: stand up a
  dedicated event-store/log product now (more infra, no F-01 benefit). **Proposed:
  Postgres.** Needs architecture-group sign-off (sub-decision of ADR-10).
- **Q-GS4B — stored payload encoding — PROPOSED (reuse the §4A codec).** Store
  each event body using the frozen wire codec (via the Go cgo binding) so stored
  bytes == wire bytes — replay/audit emit identical frames, one encoding (NFR-10
  spirit). Alternative: a store-local encoding (e.g. JSONB for queryability).
  **Proposed: wire codec**; revisit if analytics needs a queryable column (that
  is the columnar warehouse's job, not the truth store's).
- **Q-GS4C — atomic multi-event append — PROPOSED (add AppendBatch).** Add
  `AppendBatch(ctx, streamID, []Event) ([]seq, error)` to `game.EventStore` so
  the session commits ClockFlagged+GameEnded (and MovePlayed+GameEnded)
  atomically, removing GS-3's half-terminal window. Alternative: keep single
  Append and wrap the session's two calls in a store transaction exposed some
  other way (leakier). **Proposed: AppendBatch**, as a coordinated additive
  interface change (stub + session + tests together).
- **Q-GS4D — CI store instance — PROPOSED (service container).** Run the §8
  suite against a Postgres service container in the `services` CI job.
  Alternative: an embedded/in-memory PG shim (less faithful). **Proposed: service
  container** so the durability/concurrency tests exercise real transactions.
- **Q-GS4E — seq assignment — PROPOSED (server-side max+1 in-tx).** Assign seq as
  `max(seq)+1` within the append transaction, relying on the PK to reject a race.
  Alternative: the caller passes the expected seq (optimistic concurrency,
  surfaces conflicts explicitly). **Proposed: server-side in-tx**; the session is
  single-writer per game (GS-3 actor), so contention is only across nodes, which
  the PK handles. Revisit if multi-node writers to one stream become possible.
