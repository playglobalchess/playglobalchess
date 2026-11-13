# Game-server GS-3 — live in-memory game session (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS3 (implementation slice of SPEC-S2-GAME-SERVER §3.5/§3.6/§6.3) |
| Owner | S2 Real-time |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-10-13 |
| Implements (master IDs) | X-2, F-01, F-01.5 (reconnection), NFR-2 (no game loss), NFR-5, NFR-7, ADR-10, ADR-12 |
| Depends on | SPEC-S2-GAME-SERVER (RATIFIED); GS-1 (lifecycle/clock/pipeline, PR #34); GS-2 (engine-cgo `CgoOracle`, PR #38); SPEC-S1-ENGINE-CORE (G-A) |
| Gates it feeds | None directly; the load-bearing step toward the F-01 vertical slice. Does **not** make any gate green by itself. |

> **Implementation slice, not a new ratified contract.** It introduces no new
> wire bytes and no new ADR. It fills the one thing GS-1/GS-2 left unowned: the
> GS-1 move pipeline is a pure function that *expects* a caller to supply the
> authoritative `CurFEN` (the PR #34 hot-path fix) and to drive it — nothing does
> that yet. GS-3 is that owner. It also **closes Q-GS5** (repetition), the gap
> pinned by `TestRepetitionIsNotDetectedYet`. On approval it is implemented; it
> amends no RATIFIED spec. Like GS-2, this slice can instead land as a §3.5/§3.6
> implementation subsection of SPEC-S2-GAME-SERVER if the owner prefers.

## 1. Purpose & scope

Build the **live in-memory game session** — the single in-process owner of one
running game. It holds the authoritative live projection (position, clocks,
folded Zobrist history), drives the GS-1 move pipeline against the GS-2 real
engine, applies the resulting events to the stub store, and serves the §3.5
reconnection resync. This is the component the WS transport (a later chunk)
attaches a connection to; here it is exercised directly (no network).

**In scope (GS-3):**
- A `session` package: a `Session` type owning one game's live state —
  `CurFEN`, per-seat `clock.Clock`, the folded Zobrist key history, phase/ply/
  turn — rebuilt on construction by folding the durable stream
  (`FoldWithEngine`, GS-2's resync path).
- **Driving the move pipeline:** `Session.SubmitMove(uci, seat, …)` supplies the
  owned `CurFEN` to `game.Pipeline.SubmitMove` (GS-1), applies the accepted
  `MovePlayed` to its live projection, advances `CurFEN` from the verdict's
  `NewFEN`, and **appends the resulting Zobrist key to the folded history** —
  **closing Q-GS5** so threefold/fivefold repetition is detected on the live
  path. Flips the GS-2 pin test (see §8).
- **Clock advancement + flag-on-timeout:** the session owns a monotonic clock
  **source** (injected `func() time.Time` for determinism in tests) and charges
  per-seat elapsed time via `clock.Clock.Apply` (GS-1). A seat whose clock
  reaches 0 flags → `ClockFlagged` → `GameEnded{FLAG | TIMEOUT_VS_INSUFFICIENT}`
  (the material split via GS-2's `HasSufficientMaterial`). The clock keeps
  running while a seat is disconnected (§3.5).
- **Reconnection resync (§3.5, pure core):** `Session.StateSnapshot()` returns
  the `STATE{fen, turn, clocks}` projection a reconnecting client needs, folded
  from the stream — node-independent (any node can serve it, §6.3).
- **Takeback (F-01.4, unrated):** drive GS-1's `RefoldTakeback` to roll the live
  projection back to ply-n and resync `CurFEN` + history.
- **Concurrency model (§6.3):** one game is owned by exactly one goroutine at a
  time; the `Session` is NOT a shared mutable object touched by many goroutines.
  GS-3 defines the ownership discipline (a single-writer actor loop or an
  explicit mutex-guarded value — Q-GS3A) and proves it `-race` clean.

**Out of scope (later chunks — do NOT start):**
- The real event store / durability product (ADR-10) — still the GS-1 `MemStore`
  stub. (GS-4.)
- The WS transport / connection lifecycle / `AUTHENTICATE` / frame decode-encode
  on the wire — GS-3 exposes the session API the transport will call; it opens
  no socket.
- Matchmaking (§3.4) and session *creation from a seek* — GS-3 constructs a
  session from an existing game stream; who creates the stream is matchmaking.
- Sharding/routing across nodes (§6.3, G-E) and the §6.1 latency numbers.
- Rating (X-4) — the session emits `GameEnded`; `RatingApplied` is X-4.

## 2. Requirements traced

| Requirement | Where |
|---|---|
| §3.6 move pipeline needs a `CurFEN` supplier + outcome applier | §3.1/§3.2 — the session owns `CurFEN`, calls `Pipeline.SubmitMove`, applies the result. |
| **Q-GS5** repetition on the live path | §3.2 — the session accumulates the Zobrist history and threads it into the oracle; flips the GS-2 pin test. |
| §3.5 reconnection resync, node-independent, no game loss (NFR-2) | §3.3 — `StateSnapshot` folds the durable stream; any node can rebuild. |
| §3.3 clock + flag-on-timeout incl. disconnect | §3.4 — pipeline charges per-move; the session's `TickFlagCheck` handles idle timeout; flag → `GameEnded`. |
| **F-01.4** takeback (unrated) | §3.2 (Takeback) — drives GS-1 `RefoldTakeback` to roll the projection + `priorKeys` + clocks back to ply-n; rated rejects. |
| NFR-7 persist-before-ack | unchanged — the pipeline the session drives already enforces it; the session acks only on the pipeline's accepted outcome. |
| §6.3 goroutine-per-game ownership | §3.5 — single-writer discipline, `-race` proven. |
| NFR-5 no chess logic in Go | the session threads opaque FEN/UCI/zobrist; all legality/terminal/material via `CgoOracle`. |

## 3. Design

### 3.1 The Session value

```
Session {
  gameID    string
  store     game.EventStore        // GS-1 stub now; real store (GS-4) later
  oracle    game.LegalityOracle    // GS-2 CgoOracle
  opts      game.FoldOptions       // time-control expansion
  now       func() time.Time       // injected clock source (monotonic; test-fakeable)

  // live projection (rebuilt by FoldWithEngine on construction, advanced per move)
  curFEN     string
  priorKeys  []uint64              // folded Zobrist history — the Q-GS5 fix
  clocks     clock.Clock
  phase/ply/turn   (from game.State)
  lastMoveAt time.Time             // for elapsed charging
}
```

`NewSession(ctx, gameID, store, oracle, opts, now)` loads the stream and folds
it with `FoldWithEngine` (GS-2) to seed `curFEN` + the terminal/clock projection,
and accumulates `priorKeys` by replaying the stored moves' resulting keys,
seeded with the start position's key (`pgc_engine_zobrist`, #100; GS-7 §3.2
now owns the derivation in `State.PriorKeys`). This
is the one O(ply) rebuild (resume cost, §3.5) — NOT the per-move path.

### 3.2 Move handling (drives GS-1 §3.6, closes Q-GS5)

`SubmitMove(ctx, MoveIntent) -> MoveOutcome` — the session builds the GS-1
`game.MoveIntent` and calls `game.Pipeline.SubmitMove`; it does **not**
re-implement any pipeline step. Clock charging and flag detection already live
INSIDE the pipeline (`st.Clock.Apply` + `MoveOutcome.Flagged` via
`clock.ErrFlagged`, GS-1 `pipeline.go`), so the session must NOT charge the clock
a second time — that would double-deduct and flag early. The session's job is to
supply the inputs the pipeline needs and apply the outcome:
1. compute `elapsedMs = now() - lastMoveAt` (the session owns the only wall
   clock) and build `MoveIntent{GameID, SeatIdx: seat, PlyExpected, UCI,
   CurFEN: <owned>, ElapsedMs: elapsedMs, PriorKeys: <owned history>}`. The
   session passes elapsed time as DATA; the pipeline is the single place it is
   applied to the clock.
2. call `game.Pipeline.SubmitMove(ctx, intent)`. The pipeline authorizes,
   charges the clock once, validates via the engine (now with `PriorKeys` so
   repetition is seen), and persists (NFR-7) — exactly GS-1, unchanged.
3. branch on the returned `MoveOutcome`:
   - `Accepted`: advance `curFEN = outcome.NewFEN`, append `outcome.NewZobrist`
     to `priorKeys`, set `clocks` from the outcome's `ClockAfterMs`/ply/turn, set
     `lastMoveAt = now()`.
   - `Flagged`: drive the flag path (§3.4) — the pipeline already detected it;
     the session appends `ClockFlagged`→`GameEnded`.
   - `Rejected`: no projection change; return the reject for the transport.
4. return the outcome for the transport to encode/emit.

The accumulated `priorKeys` carried on each `MoveIntent` is what lets the
engine's `status_with_history` see repetitions — closing Q-GS5.

> **Q-GS5 wiring detail.** GS-2's `CgoOracle.Legality` currently passes empty
> `prior_keys` and its `LegalityVerdict` carries no Zobrist key. GS-3 needs both
> ends: (a) the verdict must surface the resulting key (`NewZobrist`, additive),
> and (b) the oracle call must accept the prior history (`priorKeys` param, a
> breaking signature change). This is a **coordinated** change across the GS-1
> `LegalityOracle`/`LegalityVerdict`, both implementers, both callers, and
> `MoveIntent` — see Q-GS3B for the exact edit list and why it is done here, not
> in GS-2. The engine-cgo C-ABI needs NO change (it already has both ends).

### 3.3 Reconnection resync (§3.5)

`StateSnapshot()` returns `{fen, turn, clocks, ply, phase}` from the live
projection (which equals a fresh `FoldWithEngine` of the stream — asserted in
§8). A reconnecting client's resync is served by constructing a `Session` from
the durable stream on *any* node, so no original-node affinity is required
(NFR-2, §6.3). GS-3 provides the snapshot; the WS `STATE` frame is the transport
chunk.

### 3.4 Clocks, disconnect, flag-on-timeout (§3.3/§3.5)

The session owns the only wall-clock source (injected `now`), so clock behavior
is deterministic under test. There are two distinct charge paths, and only one
of them touches the clock per move:
- **On a move:** elapsed time is applied INSIDE the pipeline (§3.2 — the session
  only measures `elapsedMs` and hands it over as `MoveIntent.ElapsedMs`; it does
  not call `clock.Apply` itself).
- **On an idle timeout (no move):** `TickFlagCheck(ctx)`, which the
  transport/scheduler calls on a timer (Q-GS3C), measures `now() - lastMoveAt`
  against the side-to-move's remaining time; if it has reached 0 the session
  produces `ClockFlagged` → `GameEnded`. This is the only place the SESSION
  evaluates the clock, and it fires no move — so there is no double-charge with
  the pipeline's per-move `Apply`.
FLAG vs TIMEOUT_VS_INSUFFICIENT is decided by `game.FlagTermination` (GS-1) over
`CgoOracle.HasSufficientMaterial` (GS-2). The
clock keeps running while a seat is disconnected — disconnection is a transport
state the session does not even see; only the clock matters (§3.5).

### 3.5 Concurrency — one owner per game (§6.3)

A `Session` is single-writer: all mutating calls happen on one goroutine. GS-3
picks the discipline (Q-GS3A) and proves it with `-race`. No package-level
mutable state; a `Session` is an explicit value/handle, not a global registry
(the registry/sharding is §6.3 G-E work).

## 4. Interfaces & contracts

GS-3 adds the `session` package API (`NewSession`, `SubmitMove`,
`StateSnapshot`, `TickFlagCheck`, `Takeback`) and makes **one coordinated change**
to the GS-1 move contract for Q-GS5: a `NewZobrist uint64` field on
`LegalityVerdict` (additive — no caller breaks) PLUS a `priorKeys []uint64`
parameter on the `LegalityOracle.Legality` method (a **breaking** signature
change — see Q-GS3B for the exact lockstep edit of both implementers, both
callers, their tests, and the new `MoveIntent.PriorKeys` field). It changes no
wire bytes and no engine behavior; the engine-cgo C-ABI already accepts
`prior_keys` and returns `out_zobrist`, so this is a Go-side plumbing change only
(the C seam is unchanged and `CgoOracle` simply stops hardcoding `nil, 0`).
Changing the GS-1 interface is reviewed by S2; the implementing PR MUST enumerate
every call site (Q-GS3B) so it is scoped as an interface change, not a struct
tweak.

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Repetition reached on the live path | now DETECTED — `priorKeys` is threaded, `status_with_history` returns `DrawRepetition`; auto-draw on fivefold, availability on threefold (claim policy = Go). Flips the GS-2 pin test. |
| Flag while disconnected | clock keeps running; `TickFlagCheck` fires `ClockFlagged`→`GameEnded{FLAG|TIMEOUT_VS_INSUFFICIENT}`. |
| Reconnect mid-game | `StateSnapshot` == a fresh fold of the stream (asserted); zero game loss. |
| Node failover | a new `Session` built from the durable stream on another node reproduces the exact projection (NFR-2). |
| Persist fails mid-move | the GS-1 pipeline returns the error without acking; the session does NOT advance `curFEN`/clocks/history — the move did not happen. |
| Takeback (unrated) | `RefoldTakeback` rolls projection + `priorKeys` + clocks back to ply-n; rated games reject (F-01.4). |
| Concurrent mutators | prevented by the single-owner discipline (§3.5); `-race` proves it. |

## 6. Non-functional handling

- **6.1 move latency:** per-move cost is one engine round-trip (GS-1/GS-2
  property preserved — the session advances `curFEN` incrementally, no re-fold).
  The O(ply) fold is construction/resync only. Numbers measured at G-E.
- **6.2 no game loss (NFR-2):** the session is a projection of the durable
  stream; losing it loses nothing — rebuild from the store.

## 7. Security & abuse

- Authorization stays in the GS-1 pipeline (seat binding, ply guard) — the
  session supplies the authenticated seat the transport bound; it never trusts a
  client-supplied actor (T-1/T-2).
- No new attack surface: no network here. The clock source is server-owned, so a
  client cannot forge elapsed time.

## 8. Test plan

- **Full-game replay (real engine):** drive a complete Standard game through
  `Session.SubmitMove` move by move (match→moves→mate/resign/draw/flag), asserting
  each accepted outcome, the advancing `curFEN`, and the final terminal — against
  the GS-2 `CgoOracle` + GS-1 stub store. No network.
- **Repetition auto-draw (closes Q-GS5):** play a move sequence that repeats a
  position to the fivefold threshold and assert the session reports
  `DrawRepetition(Automatic)` — proving the threaded history works. This test is
  the positive counterpart to GS-2's `TestRepetitionIsNotDetectedYet`, which is
  **updated/retired** in this chunk (its pin has served its purpose).
- **Reconnection resync:** `StateSnapshot()` equals `FoldWithEngine` of the same
  stream at every ply (node-independent rebuild).
- **Clock + flag:** with a fake `now`, charge time across moves; drive a seat to
  0 and assert `ClockFlagged`→`GameEnded`, incl. the TIMEOUT_VS_INSUFFICIENT
  branch (lone-king opponent).
- **Takeback:** unrated re-fold to ply-n restores projection + clocks + history;
  rated rejects.
- **Persist-before-ack at the session level:** a stub-store append failure leaves
  the session projection unchanged (no half-applied move).
- **Concurrency:** `-race` on the chosen ownership discipline.
- `make test` green (codec + engine parity NOT regressed); `gofmt`/`vet` clean;
  `golangci-lint` if available.

## 9. Acceptance criteria (Definition of Done)

- [ ] `services/game-server/internal/session` with `Session` owning CurFEN +
      clocks + folded Zobrist history, built via `FoldWithEngine`.
- [ ] `SubmitMove` drives the GS-1 pipeline with the owned `CurFEN`, advances the
      projection, and threads the accumulated `priorKeys` into the oracle —
      **Q-GS5 closed**; the GS-2 pin test is retired/replaced by the positive
      repetition-auto-draw test.
- [ ] The coordinated `LegalityOracle`/`LegalityVerdict`/`MoveIntent` change
      (prior-keys param in, `NewZobrist` field out) landed in ONE commit touching
      every call site named in Q-GS3B — `CgoOracle` (no longer hardcoding
      `nil,0`), the GS-1 test double, `Pipeline.SubmitMove`, `FoldWithEngine`,
      and their tests — scoped as an interface change, not a struct tweak.
- [ ] `StateSnapshot` == a fresh fold of the stream (resync correctness).
- [ ] Clock-aware flag-on-timeout incl. the material split; deterministic under a
      fake clock source.
- [ ] Takeback (unrated) re-folds projection + history + clocks; rated rejects.
- [ ] Single-owner concurrency discipline, `-race` clean. No chess logic in Go.
- [ ] `make test` green on the hosted runner (`services`); `gofmt`/`vet` clean.
- [ ] This chunk makes **no** gate green — stated in the PR. (F-01 slice still
      needs the WS transport + real store.)

## 10. Open questions

- **Q-GS3A — session concurrency discipline — PROPOSED (actor loop).** One
  goroutine owns a game and serves commands over a channel (actor), vs an
  explicit `sync.Mutex`-guarded `Session` value. **Proposed: a single-writer
  actor loop** — it maps cleanly onto the §6.3 goroutine-per-game model, keeps
  the hot path lock-free, and makes "one owner" a structural guarantee rather
  than a discipline reviewers must re-check. The mutex form is simpler for GS-3's
  no-network tests but pushes the ownership question to the transport chunk.
  Flag if you want the mutex form for now and the actor loop at the transport.
- **Q-GS3B — where the repetition plumbing lands — PROPOSED (a: one coordinated
  interface change here).** Closing Q-GS5 needs the `LegalityOracle` to accept
  the prior Zobrist history and the `LegalityVerdict` to carry the resulting
  key. **Honest scoping — this is NOT fully "additive":** the `LegalityVerdict`
  struct gains a `NewZobrist uint64` field (that part IS additive, no caller
  breaks), but adding `priorKeys []uint64` to the `Legality` METHOD signature is
  a **breaking interface change in Go** — it forces a single coordinated edit, in
  one commit, of EVERY implementer and caller:
  - implementers: `engine.CgoOracle.Legality` (stop hardcoding `nil, 0`, pass the
    keys through) and the GS-1 test double in `internal/game`;
  - callers: `Pipeline.SubmitMove`'s step-3 call (`pipeline.go:184`) and
    `FoldWithEngine`'s replay call (`fold_engine.go:39`), plus their tests;
  - the pipeline must obtain the keys to pass: `MoveIntent` gains a
    `PriorKeys []uint64` field the session populates (and `FoldWithEngine`
    accumulates keys as it replays, so its own replay calls stay correct).
  Options: **(a)** make that one coordinated change now, in GS-3 (proposed —
  GS-3 is the first component with a real history to pass; the engine-cgo C-ABI
  already accepts `prior_keys` and returns `out_zobrist`, so no Rust/C change);
  **(b)** add a parallel `LegalityWithHistory` method, leaving the GS-1 signature
  untouched (no lockstep edit, but two legality code paths and a bool-y "which
  one do I call"). **Proposed: (a)** — one legality path is worth the single
  coordinated edit, and the §9 DoD + the implementing PR must list every call
  site above so the change is scoped as an interface change, not a struct tweak.
- **Q-GS3C — the flag-check trigger.** GS-3 exposes `TickFlagCheck` for a
  caller (transport/scheduler) to drive on a timer; GS-3 does NOT own a real
  timer goroutine (that is transport/scheduling, and a per-game ticker is a §6.3
  scale concern). Proposed: expose the pure check, let the transport chunk own
  the cadence. Flag if the session should own its own timer.
