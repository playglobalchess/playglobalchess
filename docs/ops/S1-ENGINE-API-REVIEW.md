# §4 Public API review — S1 engine shared contract (G-A box 10)

| Field | Value |
|---|---|
| Reviews | ENGINE-CORE §4 (Interfaces & contracts) as the shared contract S2 (Go, ADR-12) and the client core (ADR-9, UniFFI) bind |
| Prepared | 2026-10-12, by the S1 implementation (engine author) |
| Decision needed from | **S2 stream lead** + **architecture group** (CONVENTIONS §Cross-stream contracts) |
| Status | **PREPARED — awaiting S2 + architecture-group ratification.** This is the review *record and findings*, not the sign-off itself: the two parties above must confirm it before G-A box 10 is checked. |

> Honesty note: this document is prepared by the engine author to make the
> review efficient — it lays out the surface, checks it against §4, and flags
> the decisions reviewers must make. It is **not** a substitute for S2 + the
> architecture group actually reviewing and approving; their names/date go in
> the Sign-off table at the bottom when they do.

## 1. The surface under review

### Rust public API (`engine/src/lib.rs`, what S2 binds via FFI/gRPC)
```
Types:      Color, Piece, PieceType, Square, Move, MoveList, Position,
            CastlingRights, Undo, GameStatus, DrawClaim, FenError, MoveError
FEN:        parse_fen, to_fen
Move gen:   legal_moves, pseudo_legal_moves, is_square_attacked, is_in_check,
            is_legal, make, unmake
Encoding:   move_to_uci, uci_to_move, move_to_san, san_to_move
Status:     status, status_with_history, is_insufficient_material
Hashing:    zobrist
Perft:      perft, perft_divide
```

### UniFFI surface (`engine/ffi/src/lib.rs`, what the client cores bind)
```
startpos_fen() -> String
perft_from_fen(fen, depth) -> Result<u64, FfiError>
replay_sans(sans) -> Result<Vec<String>, FfiError>
FfiError { Fen, Move }
```
Deliberately the **parity slice only** — the full client API grows here as S5 needs it (not frozen by G-A).

## 2. Conformance to §4 (findings)

| §4 requirement | Finding |
|---|---|
| §4.1 `parse_fen/to_fen/legal_moves/is_legal/make/unmake/status/is_in_check` | ✅ all present; signatures match the spec's shape. `is_legal` added as a dedicated fast path (§6.1). |
| §4.1 `status()` claimable vs automatic; reports availability, never ends a game | ✅ `GameStatus` + `DrawClaim{Automatic,Claimable}`; engine never auto-ends. **Note:** repetition needs history → surfaced via `status_with_history(p, &[key])` (owner-approved D2), since the pure `status(p)` signature can't see history. **Reviewers: confirm this split is the contract S2 wants.** |
| §4.2 UCI `uci_to_move/move_to_uci` (position-aware) | ✅ `uci_to_move` validates legality; `move_to_uci` pure. |
| §4.2 SAN `move_to_san/san_to_move` with disambiguation, x, +, #, O-O, =Q | ✅ all; round-trip property-tested. |
| §4.2 round-trip inverses | ✅ `properties.rs`. |
| §4.3 engine is the pure fold step; no store/network | ✅ no I/O, no globals, deterministic. |
| §4.4 `perft/perft_divide` | ✅ present; `perft` takes `&mut Position` (restores it) as a zero-clone optimization. |

## 3. Decisions reviewers must confirm (the actual review questions)

1. **`status_with_history(p, &[u64])` as the repetition API** — the engine returns prior-position Zobrist keys' owner (S2) the responsibility to accumulate the key history. Confirm S2's event-fold can supply this cheaply, and that a *claim* re-verifies full equality (collision guard) on S2's side.
2. **`perft(&mut Position)`** — the `&mut` is an internal optimization (restored on return). Confirm this is acceptable at the FFI/gRPC boundary, or whether S2 wants a `&Position` wrapper.
3. **`Undo` is `Copy`, fields private** — confirm the make/unmake token shape suits S2's server hot path (vs. a copy-make `apply`).
4. **FFI surface is a minimal slice**, not the full §4 API. Confirm this is the intended G-A scope (full client API is S5-driven, later).
5. **Magic bitboards absent** (D5, measured ~500× under target) — confirm the architecture group accepts the simple generator for G-A (the spec's §3.3/§6.3 magic language is descriptive, see `PERF.md`; ADR-13 drafted but held).

## 4. Sign-off (to be completed by the reviewers)

| Reviewer | Role | Verdict | Date |
|---|---|---|---|
| _(fill in)_ | S2 stream lead | ☐ approved / ☐ changes requested | |
| _(fill in)_ | Architecture group | ☐ approved / ☐ changes requested | |

When both rows are approved, check **box 10** in `docs/ops/G-A-SCORECARD.md` and
record it against ENGINE-CORE §9 (box 11).
