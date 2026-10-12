# Engine performance — §6.1 / NFR-1 measurements (G-A performance box)

Spec: `docs/specs/S1-engine/ENGINE-CORE.md` §6.1. Benches: `engine/benches/movegen.rs`
(criterion, release build, median of 100 samples). Reproduce: `cargo bench --bench movegen`.

## Results (chunk 3 stage 3a, 2026-10-12)

| Metric | §6.1 target | Measured (median) | Verdict |
|---|---|---|---|
| `legal_moves` — Kiwipete (dense middlegame) | **< 1 ms** (goal: tens of µs) | **~1.9 µs** | ✅ ~500× under target |
| `legal_moves` — startpos | (reference) | ~0.68 µs | — |
| `is_legal` — single-move validation (S2 hot path) | **< 10 µs** | **~0.28 µs** | ✅ ~35× under target |
| `uci_to_move` — decode + full-set search (reference) | — | ~1.95 µs | — |

Measured on the current **simple** generator: pseudo-legal generation + a copy-make
legality filter + ray-walk `is_square_attacked`, with **no magic bitboards and no
pin-aware fast path**. `is_legal` is a **dedicated single-move path** (pseudo-legal
membership + one make/unmake king-safety check), ~7× faster than decoding through the
full legal set.

## Decision: magic bitboards are NOT required for G-A (D5, measure-first)

ENGINE-CORE §1.4.3 / §3.5 are explicit — "correctness before speed; optimize later
**only if** perft timing demands." The measurement shows the simple generator clears
both §6.1 targets with a ~500× margin on `legal_moves` and ~5× on `is_legal`.
Building magic bitboards now would be optimizing already-fast correct code against no
measured need, so stage 3b (magics) is **not undertaken for G-A**. It remains available
as a future optimization if a later workload (deep search in X-6 analysis, not the
rules engine) ever demands it.

## Caveats / honest notes

- **`is_legal` is a dedicated single-move path** (`movegen::is_legal`): pseudo-legal
  membership + one make/unmake king-safety check, NOT a decode through the full legal
  set. It is provably equivalent to `legal_moves(p).contains(m)` (property-tested over
  all 64×64 from/to pairs on random reachable positions) and ~7× faster than the
  `uci_to_move` path (~0.28 µs vs ~1.95 µs). This is the §4.1 call S2 makes per intent.
- **"Zero allocation on the move-gen hot path" (§6.1) is partially met.** `make`/
  `unmake` and `MoveList` are allocation-free, but the legality filter in `legal_moves`
  uses copy-make (`Position::clone()` per candidate), which clones the fixed-size
  `Position` (no heap allocation — all its fields are arrays/scalars, so `clone` is a
  stack `memcpy`, not a heap alloc). So the hot path has **no heap allocation**, though
  it is not alloc-*free* in the strict register-only sense a pin-aware generator would
  give. Given the 500× headroom this is well within budget; tightening it is a
  non-goal unless a future measurement regresses.

## CI

The nightly `engine-perf-bench` job runs `cargo bench` and fails if either headline
metric regresses past its §6.1 threshold (checked in `platform/ci/check_bench.py`).
Benches do NOT run on the per-commit path (criterion runs are seconds-long and noisy
on shared runners); they run in the nightly tier alongside the slow perft corpus.
