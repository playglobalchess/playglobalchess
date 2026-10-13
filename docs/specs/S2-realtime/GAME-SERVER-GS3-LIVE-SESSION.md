# Game-server GS-3 — live in-memory game session (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS3 (implementation slice of SPEC-S2-GAME-SERVER §3.5/§3.6/§6.3) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
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
| §3.3 clock + flag-on-timeout incl. disconnect | §3.4 — session-owned clock source + `Apply`; flag → `GameEnded`. |
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
and accumulates `priorKeys` by replaying the stored moves' resulting keys. This
is the one O(ply) rebuild (resume cost, §3.5) — NOT the per-move path.

### 3.2 Move handling (drives GS-1 §3.6, closes Q-GS5)

`SubmitMove(ctx, seat, plyExpected, uci) -> Outcome`:
1. charge the mover's elapsed time (`now() - lastMoveAt`) via `clock.Apply`; a
   flag here → the flag path (§3.4), no move.
2. call `game.Pipeline.SubmitMove` with the session's **owned `curFEN`** and the
   charged clock — the pipeline authorizes, validates via the engine, and
   persists (NFR-7) exactly as GS-1 does.
3. on an accepted outcome: advance `curFEN = outcome.NewFEN`, append the move's
   resulting Zobrist key to `priorKeys`, update clocks/ply/turn, set
   `lastMoveAt = now()`. **The accumulated `priorKeys` is what the next move's
   legality call passes to the engine so `status_with_history` can see
   repetitions** — closing Q-GS5.
4. return the outcome for the transport to encode/emit.

> **Q-GS5 wiring detail.** GS-2's `CgoOracle.Legality` currently passes empty
> `prior_keys` and its `LegalityVerdict` carries no Zobrist key. GS-3 needs both
> ends: (a) the verdict must surface the resulting key, and (b) the oracle call
> must accept the prior history. This is a small, additive extension of the GS-1
> `LegalityOracle`/`LegalityVerdict` contract (a `priorKeys []uint64` input and a
> `NewZobrist uint64` output) — see Q-GS3B for the exact shape and why it is done
> here, not in GS-2.

### 3.3 Reconnection resync (§3.5)

`StateSnapshot()` returns `{fen, turn, clocks, ply, phase}` from the live
projection (which equals a fresh `FoldWithEngine` of the stream — asserted in
§8). A reconnecting client's resync is served by constructing a `Session` from
the durable stream on *any* node, so no original-node affinity is required
(NFR-2, §6.3). GS-3 provides the snapshot; the WS `STATE` frame is the transport
chunk.

### 3.4 Clocks, disconnect, flag-on-timeout (§3.3/§3.5)

The session owns the only wall-clock source (injected `now`), so clock behavior
is deterministic under test. Elapsed time is charged on each move and on an
explicit `TickFlagCheck(ctx)` the transport/scheduler calls (e.g. on a timer):
if the side to move's remaining time has reached 0, the session produces
`ClockFlagged` → `GameEnded`, with FLAG vs TIMEOUT_VS_INSUFFICIENT decided by
`game.FlagTermination` (GS-1) over `CgoOracle.HasSufficientMaterial` (GS-2). The
clock keeps running while a seat is disconnected — disconnection is a transport
state the session does not even see; only the clock matters (§3.5).

### 3.5 Concurrency — one owner per game (§6.3)

A `Session` is single-writer: all mutating calls happen on one goroutine. GS-3
picks the discipline (Q-GS3A) and proves it with `-race`. No package-level
mutable state; a `Session` is an explicit value/handle, not a global registry
(the registry/sharding is §6.3 G-E work).

## 4. Interfaces & contracts

GS-3 adds the `session` package API (`NewSession`, `SubmitMove`,
`StateSnapshot`, `TickFlagCheck`, `Takeback`) and makes **one small additive
extension** to the GS-1 `game.LegalityOracle` / `LegalityVerdict` for Q-GS5
(prior-keys in, resulting-zobrist out — Q-GS3B). It changes no wire bytes and no
engine behavior; the engine-cgo C-ABI already returns `out_zobrist` and accepts
`prior_keys`, so this is a Go-side plumbing change only (the C seam is unchanged
and `CgoOracle` simply stops hardcoding `nil, 0`). Changing the GS-1 interface
is reviewed by S2; it is backward-additive (the pipeline passes the session's
keys through).

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
- [ ] The additive `LegalityOracle`/`LegalityVerdict` extension (prior-keys in,
      resulting-zobrist out) landed with `CgoOracle` no longer hardcoding `nil,0`.
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
- **Q-GS3B — where the repetition plumbing lands — PROPOSED (here, additive).**
  Closing Q-GS5 needs the `LegalityOracle` to accept `priorKeys` and the
  `LegalityVerdict` to carry the resulting Zobrist key. Options: **(a)** extend
  the GS-1 interface additively now, in GS-3 (proposed — GS-3 is the first
  component that actually has a history to pass, and the engine-cgo C-ABI already
  has both ends, so it is pure Go plumbing); **(b)** a parallel oracle method
  `LegalityWithHistory` leaving the GS-1 signature untouched (more surface, two
  code paths). **Proposed: (a).** The change is backward-additive and keeps one
  legality path.
- **Q-GS3C — the flag-check trigger.** GS-3 exposes `TickFlagCheck` for a
  caller (transport/scheduler) to drive on a timer; GS-3 does NOT own a real
  timer goroutine (that is transport/scheduling, and a per-game ticker is a §6.3
  scale concern). Proposed: expose the pure check, let the transport chunk own
  the cadence. Flag if the session should own its own timer.
