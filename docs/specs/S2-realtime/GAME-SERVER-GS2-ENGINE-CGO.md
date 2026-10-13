# Game-server GS-2 — engine-cgo legality seam (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS2 (implementation slice of SPEC-S2-GAME-SERVER §3.6/§4) |
| Owner | S2 Real-time |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-10-13 |
| Implements (master IDs) | X-2, X-1, F-01, NFR-5 (engine is the only legality authority), ADR-11, ADR-12, **ADR-15** (uniform Go↔Rust cgo) |
| Depends on | SPEC-S1-ENGINE-CORE (**G-A GREEN**), SPEC-S2-GAME-SERVER (RATIFIED) §3.6/§4/§5/§8, PLAN-S2-CODEC v1.1 §4.5 (the proven `codec-cgo` pattern), GS-1 (merged PR #34 — the `LegalityOracle` interface this fills) |
| Gates it feeds | None directly; unblocks the F-01 vertical slice and G-E. Does **not** make any gate green by itself. |

> **This is an implementation slice, not a new ratified contract.** It introduces
> **no** new wire bytes and **no** new architecture decision. The decision was
> already taken: SPEC-S2-GAME-SERVER **Q-GS4 is DECIDED via ADR-15** — "`pgc-engine
> → engine-cgo` reuses `codec-cgo`'s pattern … built later when the game-server
> wires move validation". GS-2 is that build. This slice exists only to pin the
> engine-cgo **C-ABI surface** and its **test plan** before coding, per the
> spec-first/approval-gated workflow, mirroring how SPEC-S2-GAME-SERVER §4.1 pins
> the codec-cgo surface. On approval it is implemented; it does not amend any
> RATIFIED spec. If the owner prefers, this can instead land as a new §4.5
> subsection appended to SPEC-S2-GAME-SERVER — same content, same review.

## 1. Purpose & scope

Replace GS-1's stubbed `LegalityOracle` test-double (`services/game-server/internal/game`) with the **real S1 engine**, bound into the Go game-server over an in-process cgo C-ABI (ADR-15), so the move pipeline validates moves against the single source of truth (NFR-5) instead of a canned stub.

**In scope (GS-2):**
- A new sibling crate `engine/engine-cgo` (`pgc-engine-cgo`): `staticlib` + `rlib`, `#[no_mangle] extern "C"`, hand-written C header, **delegate-only** over `pgc-engine` (no chess logic added) — the exact pattern `protocol/codec-cgo` already proves.
- A Go package `services/game-server/internal/engine` that links the staticlib via cgo and implements the GS-1 `game.LegalityOracle` interface against it.
- The cgo **safety** proof (fuzz + leak) and the engine **conformance** proof (the Go oracle's answers match the engine's own corpus) per §8.

**Out of scope (later chunks — do NOT start):**
- The real event store / durability product choice (ADR-10) — still the GS-1 in-memory stub.
- Matchmaking (§3.4), reconnection transport (§3.5), the WS connection lifecycle, auth/session binding.
- The F-01 end-to-end vertical slice wired to a web client.
- Sharding/routing/load (§6.3, G-E) and the §6.1 latency numbers (measured at G-E, not asserted here).
- Any engine *behavior* change — `pgc-engine` is G-A GREEN and frozen for this slice; GS-2 only exposes it.

## 2. Requirements traced

| Requirement | Where handled |
|---|---|
| NFR-5 — one rules engine, no chess logic in Go | §3: Go calls the engine over cgo for every legality/terminal/material question; a reviewer greps `internal/engine` + `internal/game` and finds none. |
| ADR-15 — uniform cgo pattern | §3.1/§4: `engine-cgo` is the second instance of the `<crate>-cgo` sibling pattern, identical buffer-ownership + build + fuzz/leak discipline to `codec-cgo`. |
| SPEC-S2-GAME-SERVER §3.6 step 3 — validate legality via the engine | §4: `pgc_engine_apply` is the legality authority call the pipeline already routes through `LegalityOracle.Legality`. |
| §3.3 FLAG vs TIMEOUT_VS_INSUFFICIENT | §4: `pgc_engine_sufficient_material` fills `LegalityOracle.HasSufficientMaterial`. |
| §5 cgo boundary rows (null/!0-status out-buffer, Rust error → typed error) | §4/§5: status-code ABI; Go never dereferences a non-OK out-buffer. |
| NFR-7 persist-before-ack | unchanged — GS-1 already encodes the ordering; GS-2 does not touch the store. |

## 3. Design

### 3.1 Topology — one uniform cgo seam (ADR-15)

```
pgc-engine (#![forbid(unsafe_code)], G-A GREEN)
    └── engine/engine-cgo  (pgc-engine-cgo: the ONE unsafe seam — extern "C", delegate-only)
            │  staticlib (.a) + hand-written engine_cgo.h
            ▼  cgo (in-process, no network hop on the move path)
services/game-server/internal/engine  (CgoOracle: implements game.LegalityOracle)
            ▲
services/game-server/internal/game    (pipeline.go — already calls LegalityOracle, GS-1)
```

Identical to the codec seam already in the tree, so the mechanism is de-risked:
`pgc-engine` stays `#![forbid(unsafe_code)]`; the single hand-written `unsafe`
`extern "C"` surface is quarantined in `engine-cgo` with per-fn safety comments,
`#![deny(unsafe_op_in_unsafe_fn)]`, and a `catch_unwind` at **every** boundary
function (a Rust panic unwinding across the cgo boundary is UB — §5).

### 3.2 What the Go server actually needs from the engine

The GS-1 `LegalityOracle` interface fixes the required answers:

```go
Legality(ctx, fen, uci string) (LegalityVerdict{Legal, NewFEN, Terminal}, error)
HasSufficientMaterial(ctx, fen string, seatIdx uint8) (bool, error)
```

Every one maps to an existing **pure** `pgc-engine` function (no engine change):
- legality + resulting position: `uci_to_move` (position-aware parse) → `is_legal` / `make` → `to_fen`.
- terminal detection after the move: `status_with_history(pos, prior_keys)` →
  mapped to the §4A.5 `Termination` the pipeline puts on `GameEnded`. The engine
  **reports availability, never governs** (status.rs); the mapping of a
  *Claimable* vs *Automatic* draw to a server action is S2 policy, surfaced to Go
  as structured fields, decided in Go (not in the C seam).
- insufficient material (for the flag branch): `is_insufficient_material`.

Repetition needs history (`status_with_history` takes `prior_keys`), so the
seam must accept the Zobrist-key history the server already folds. GS-2's job is
to decide **how that history crosses the C-ABI** — see Q-GS2E-1.

### 3.3 No new chess logic in Go

Go holds no FEN parsing, no move legality, no material or repetition reasoning.
It threads opaque `fen`/`uci` strings and an opaque `prior_keys []uint64` to the
seam and consumes typed scalar answers. This is the NFR-5 line the §9 grep check
enforces.

## 4. Interfaces & contracts — the engine-cgo C-ABI

Byte/scalar-oriented, mirroring Q-GS1's proven decision for the codec (smallest
unsafe surface the fuzz+leak test can cover exhaustively). Strings cross as
`(ptr,len)` UTF-8; outputs Rust allocates and the caller copies then frees via a
single `pgc_engine_free`. Status-code return (`int32_t`), never a C struct with
nested ownership.

```c
/* engine_cgo.h — C-ABI for the PlayGlobalChess S1 engine (ADR-15, NFR-5).
 * Hand-written; the Go contract test linking it is the sync proof. */

#define PGC_ENG_OK                 0
#define PGC_ENG_ERR_FEN            1   /* invalid FEN */
#define PGC_ENG_ERR_MOVE           2   /* unparseable / illegal-form UCI */
#define PGC_ENG_ERR_ILLEGAL        3   /* parsed but not legal in this position */
#define PGC_ENG_ERR_NULL_ARG      98
#define PGC_ENG_ERR_PANIC         99   /* a panic caught at the boundary (never UB) */

/* Apply one UCI move to a FEN.
 *
 * Out-parameter write-conditions (make the hand-written seam unambiguous — a
 * reader of this header alone must know exactly when each out-pointer is
 * written, so no caller does an uninitialized read and the seam never writes on
 * error):
 *   On PGC_ENG_OK (and ONLY then) all five out-params are written:
 *     *out_fen / *out_fen_len = the position AFTER the move (copy, then free);
 *     *out_status = a small enum: 0 ongoing, 1 checkmate, 2 stalemate,
 *                   3 insufficient, 4 fifty-move, 5 repetition, ...;
 *     *out_claim  = 0 none / 1 claimable / 2 automatic (for the draw rows);
 *     *out_zobrist = the Zobrist key of the resulting position, so the server
 *                    appends it to its folded history WITHOUT re-hashing in Go
 *                    (NFR-5: Go computes no chess state). It is the key of the
 *                    position in *out_fen.
 *   On ANY non-OK return (PGC_ENG_ERR_FEN / _MOVE / _ILLEGAL / _NULL_ARG /
 *   _PANIC) NONE of the five out-params is written — not *out_zobrist, not the
 *   scalars, not the buffer. The caller must treat them as untouched and read
 *   none of them. In particular PGC_ENG_ERR_ILLEGAL writes nothing and the
 *   pipeline emits MOVE_REJECTED{ILLEGAL}.
 * The implementation writes the out-params only on the success path, after all
 * fallible work has succeeded, so a mid-computation error cannot leave a
 * partially-written set.
 *
 * prior_keys (ptr,len) is the Zobrist history the server folded, EXCLUDING the
 * resulting position (status_with_history's convention). prior_keys may be NULL
 * iff prior_keys_len == 0. */
int32_t pgc_engine_apply(const uint8_t *fen, size_t fen_len,
                         const uint8_t *uci, size_t uci_len,
                         const uint64_t *prior_keys, size_t prior_keys_len,
                         uint8_t **out_fen, size_t *out_fen_len,
                         int32_t *out_status, int32_t *out_claim,
                         uint64_t *out_zobrist);

/* Insufficient-material query for the flag branch (§3.3). seat_idx picks the
 * side whose material is tested (the opponent of the flagged seat). */
int32_t pgc_engine_sufficient_material(const uint8_t *fen, size_t fen_len,
                                       uint8_t seat_idx, int32_t *out_sufficient);

/* Free a buffer handed out by a success (same ptr+len). NULL = no-op. */
void pgc_engine_free(uint8_t *ptr, size_t len);

/* Engine semantic version, so the Go side can assert it links the expected build. */
uint32_t pgc_engine_abi_version(void);
```

- **`out_status` is a closed enum (frozen on this ABI), not an open `...`:** it
  maps 1:1 from the engine's `GameStatus` (status.rs). Full set — `0` ongoing,
  `1` checkmate, `2` stalemate, `3` insufficient-material, `4` fifty/seventyfive-
  move, `5` repetition. The claim class (`out_claim`: `0` none / `1` claimable /
  `2` automatic) disambiguates the two-tier draw rows (`4` and `5`) exactly as
  `DrawClaim` does. A value outside this set is a seam bug, not a wire-extensible
  field — Go validates it (a closed domain on a trusted internal ABI, consistent
  with the codec's frozen-enum validation discipline). The engine *reports* these;
  Go maps `(status, claim)` to the §4A.5 `Termination` and owns the claim policy
  (Q-GS2E-2) — the C seam performs no governance.
- **Buffer ownership (one rule):** Rust allocates `*out_fen`; Go copies into Go
  memory and calls `pgc_engine_free(out_fen, out_fen_len)` exactly once; Go never
  frees a Rust pointer any other way and never hands Rust a Go pointer Rust
  retains (the cgo pointer rule). `*out_fen` is allocated ONLY on `PGC_ENG_OK`
  (see the per-param write-conditions above), so Go calls `pgc_engine_free` only
  after an OK return and never on an error path. The scalar out-params
  (`out_status`, `out_claim`, `out_zobrist`) are plain stack values the caller
  owns; they are written only on OK and never need freeing.
- **Build wiring:** `services/game-server`'s Makefile builds the staticlib
  (`cargo build -p pgc-engine-cgo --release`, dropping `.a` + header into a known
  gitignored path) and cgo-links it, mirroring the codec step already in the
  Makefile. Per deploy target the matching Rust `--target` is parameterized.
- **Change control:** this C-ABI is an access path to the frozen `pgc-engine`;
  it changes no engine behavior and no wire bytes. It rides ADR-15's mechanism,
  so the architecture group reviews the surface (as it did `codec-cgo`), but no
  new ADR and no spec re-ratification is required.

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Invalid/garbage FEN into the seam | `PGC_ENG_ERR_FEN`; Go surfaces `ERROR{INTERNAL}` (a stored FEN should never be invalid — this is a corrupt-state guard, like `FoldWithEngine`'s corrupt-stream refusal). |
| Unparseable UCI | `PGC_ENG_ERR_MOVE` → the pipeline treats as `MOVE_REJECTED{ILLEGAL}` (a malformed move is not legal). |
| Legal-form but illegal move | `PGC_ENG_ERR_ILLEGAL`, no out buffer → `MOVE_REJECTED{ILLEGAL}`, no event (§3.6 step 3). |
| Rust panic anywhere in a boundary fn | `catch_unwind` → `PGC_ENG_ERR_PANIC`; **never** unwinds across cgo (UB). Before collapsing to the opaque code, the seam MUST log the caught panic payload Rust-side (message + the boundary fn name; the panic location via a panic hook) to the engine's stderr/log, so the bug signal is not thrown away with the return value — the ABI carries only `int32_t`, so the diagnostic lives in the Rust-side log, not the return channel. The Go caller, on receiving `PGC_ENG_ERR_PANIC`, logs its own input context (game_id, ply, uci, and a FEN hash — never a token) and surfaces `ERROR{INTERNAL}`; the pair (Rust panic log + Go input log, correlated by time/game_id) is what lets an on-call reproduce it. Fuzzed in §8 (never UB), and §8 adds a test asserting a deliberately-panicking boundary fn both returns 99 AND emits the Rust-side log line. |
| Null / !0-length-null argument | `PGC_ENG_ERR_NULL_ARG`; Go never dereferences a non-OK out-buffer (§5 cgo row of SPEC-S2-GAME-SERVER). |
| Oversize / truncated buffer | typed error, never a crash — the §8 fuzz sweep asserts this across ~100k malformed calls. |
| Determinism | same (fen, uci, prior_keys) ⇒ identical answer every call (engine is pure + deterministic, G-A); the Go conformance test asserts it. |

## 6. Non-functional handling

- **6.1 latency:** the engine legality call is sub-ms (G-A §6.1) and cgo overhead
  is tens of ns (negligible vs the move budget — PLAN-S2-CODEC §4.5). The move
  path already costs exactly one engine round-trip per move (GS-1 PR #34 fix); the
  real oracle keeps that property. Numbers are measured at G-E, not asserted here.
- **6.2 no new state:** the seam is stateless (pure function calls); it adds no
  node-memory game truth, so §6.2's resume property is unaffected.

## 7. Security & abuse

- **T-1/T-2 move/clock forgery** — strengthened: the server now validates every
  move against the real engine, not a stub, closing the gap that a stub oracle
  could be wrong. The wire still gives clients no truth field.
- **cgo memory safety** — the single unsafe seam is fuzzed + leak-checked (§8),
  identical discipline to `codec-cgo`; Rust owns all allocations.
- **No token/PII crosses the seam** — only FEN/UCI/zobrist, all game-public.

## 8. Test plan

- **Engine conformance (the point of GS-2)** — a Go test drives `CgoOracle`
  through a corpus of positions and asserts its answers (legal?, newFEN,
  terminal status/claim, sufficient-material) match the engine's own ground
  truth. The corpus reuses the engine's edge cases (E-1..E-14), the game-end
  cases, and a golden-game replay — the Go leg of "no chess logic in Go, answers
  come only from the engine". Replays through `CgoOracle` must equal replays
  through `pgc-engine` directly (a Rust test produces the expected vector).
- **cgo safety (fuzz)** — ~100k malformed/random/oversize/null calls to every
  boundary fn: never a crash, always a typed status, never UB from a panic.
- **panic diagnostics** — a boundary fn made to panic deliberately (a test-only
  hook) must (a) return `PGC_ENG_ERR_PANIC` (99), (b) NOT unwind across the seam,
  and (c) emit the Rust-side panic log line (message + boundary-fn name) — the
  §5 panic row's requirement, asserted so the bug signal is provably not lost.
- **out-param write-conditions** — a test asserts that on each error return
  (`_FEN`/`_MOVE`/`_ILLEGAL`/`_NULL_ARG`/`_PANIC`) the five `pgc_engine_apply`
  out-params are left untouched (pre-poisoned sentinels unchanged), proving the
  seam never writes on error and the §4 write-conditions hold.
- **cgo leak** — a counting-allocator or asan test asserting every successful
  `pgc_engine_apply` output is freed exactly once by `pgc_engine_free` (balances).
- **ABI-version lock** — the Go side asserts `pgc_engine_abi_version()` equals the
  build it expects (catches a stale linked `.a`).
- **Pipeline integration (unit, no network)** — swap the GS-1 fake oracle for
  `CgoOracle` in the existing `internal/game` pipeline tests (a real-engine
  variant) and assert the same accept/reject/terminal outcomes the stub drove,
  now against real chess truth (Scholar's mate ⇒ `MATE`, an illegal move ⇒
  `ILLEGAL`, a stalemate ⇒ `STALEMATE`).
- **Determinism** — same inputs ⇒ identical outputs across repeated calls.
- Full `make test` must stay green (the cgo **codec** parity must not regress);
  `gofmt`/`go vet` clean; engine `cargo test`/clippy/fmt clean on the new crate.

## 9. Acceptance criteria (Definition of Done)

- [ ] `engine/engine-cgo` (`pgc-engine-cgo`) crate exists — `staticlib`+`rlib`,
      `#[no_mangle] extern "C"`, hand-written `engine_cgo.h`, delegate-only,
      `catch_unwind` at every boundary fn, per-fn safety comments, clippy/fmt clean.
- [ ] `pgc-engine` is **unchanged** (G-A stays GREEN; a reviewer confirms no diff
      under `engine/src/`).
- [ ] `services/game-server/internal/engine` links it and implements
      `game.LegalityOracle`; the GS-1 interface is satisfied with no signature change.
- [ ] Engine-conformance Go test green (CgoOracle == engine ground truth over the
      edge/game-end/golden corpus).
- [ ] cgo fuzz (no crash / typed errors) + leak (balanced free) tests green.
- [ ] Panic-diagnostics test green: a deliberate boundary panic returns `99`, does
      not unwind, and emits the Rust-side panic log line (§5 / §8).
- [ ] Out-param write-condition test green: every error return leaves all five
      `pgc_engine_apply` out-params untouched (§4 / §8).
- [ ] No chess logic in Go — reviewer greps `internal/engine` + `internal/game`.
- [ ] `make test` green end-to-end (codec parity NOT regressed), `gofmt`/`vet` clean,
      golangci-lint clean if installed (else noted).
- [ ] CI `services` job green on the hosted runner (builds the engine staticlib on
      Linux and runs `go test ./...`). A new `engine-cgo` build step is wired into
      the existing path-filtered `services`/`engine` jobs as needed.
- [ ] This slice does **not** make any gate green — stated in the PR.

## 10. Open questions

- **Q-GS2E-1 — Zobrist history across the C-ABI — PROPOSED (a).** The server folds
  the game's Zobrist history for repetition (`status_with_history` takes
  `prior_keys`). Options: **(a)** pass `prior_keys` as a `(ptr,len)` of `uint64`
  on each `pgc_engine_apply` (stateless seam, server owns history — proposed; it
  keeps the seam pure and matches the engine's "prior keys exclude current"
  convention, and the server already holds the folded keys); **(b)** a stateful
  per-game handle in the seam that accumulates keys (smaller per-call payload, but
  introduces C-side state + lifecycle to leak — rejected: it re-opens exactly the
  ownership-surface the byte-oriented Q-GS1 decision closed). **Proposed reading:
  (a).** Flag if the architecture group wants (b) for a measured move-rate reason.
- **Q-GS2E-2 — terminal status → §4A.5 Termination mapping location — PROPOSED (Go).**
  The engine returns `GameStatus{Ongoing|Checkmate|Stalemate|Draw*}` + a claim
  class; the server maps that to the §4A.5 `Termination` enum and decides whether
  a *Claimable* draw ends the game (policy). Proposed: the C seam returns the
  structured `(status, claim)` scalars and **Go owns the mapping + the claim
  policy** (it is governance, not chess — status.rs is explicit the engine
  "detects, doesn't govern"). The alternative (map inside the seam) would bury S2
  policy in the Rust C layer. **Proposed reading: map in Go.**
- **Q-GS2E-3 — one PR or two?** `engine-cgo` crate (+ its Rust fuzz/leak tests) is
  arguably separable from the Go `internal/engine` consumer. Proposed: **one PR**
  (the crate is meaningless without a consumer proving the link, and the
  conformance test needs both ends), mirroring how codec 4.5 shipped crate +
  Go-consumer together. Flag if you want them split for review size.
