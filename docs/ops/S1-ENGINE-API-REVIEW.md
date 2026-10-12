# §4 Public API review — S1 engine shared contract (G-A box 10)

| Field | Value |
|---|---|
| Reviews | ENGINE-CORE §4 (Interfaces & contracts) as the shared contract S2 (Go, ADR-12) and the client core (ADR-9, UniFFI) bind |
| Prepared | 2026-10-12, by the S1 implementation (engine author) |
| Decision needed from | **S2 stream lead** + **architecture group** (CONVENTIONS §Cross-stream contracts) |
| Status | **APPROVED 2026-10-12** (owner, acting as S2 + architecture authority) |
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

## 4. Sign-off

| Reviewer | Role | Verdict | Date |
|---|---|---|---|
| Ashish (project owner) | S2 stream lead (acting) | ☑ **approved** — all 5 §3 decisions confirmed | 2026-10-12 |
| Ashish (project owner) | Architecture group (acting) | ☑ **approved** | 2026-10-12 |

Both rows approved → **box 10 is checked** in `docs/ops/G-A-SCORECARD.md`, and
this record satisfies the §9 review-record step (box 11). The owner approved in
both roles; when the S2 stream and a separate architecture group are staffed,
re-confirmation against this record is a formality, not a re-review.
