# Engine performance — §6.1 / NFR-1 measurements (G-A performance box)

Spec: `docs/specs/S1-engine/ENGINE-CORE.md` §6.1. Benches: `engine/benches/movegen.rs`
(criterion, release build, median of 100 samples). Reproduce: `cargo bench --bench movegen`.

## Results (chunk 3 stage 3a, 2026-10-12)

| Metric | §6.1 target | Measured (median) | Verdict |
|---|---|---|---|
| `legal_moves` — Kiwipete (dense middlegame) | **< 1 ms** (goal: tens of µs) | **~1.94 µs** | ✅ ~500× under target |
| `legal_moves` — startpos | (reference) | ~0.69 µs | — |
| `is_legal` — single-move validation (S2 hot path) | **< 10 µs** | **~1.93 µs** | ✅ ~5× under target |

Measured on the current **simple** generator: pseudo-legal generation + a copy-make
legality filter + ray-walk `is_square_attacked`, with **no magic bitboards and no
pin-aware fast path**.

## Decision: magic bitboards are NOT required for G-A (D5, measure-first)

ENGINE-CORE §1.4.3 / §3.5 are explicit — "correctness before speed; optimize later
**only if** perft timing demands." The measurement shows the simple generator clears
both §6.1 targets with a ~500× margin on `legal_moves` and ~5× on `is_legal`.
Building magic bitboards now would be optimizing already-fast correct code against no
measured need, so stage 3b (magics) is **not undertaken for G-A**. It remains available
as a future optimization if a later workload (deep search in X-6 analysis, not the
rules engine) ever demands it.

## Caveats / honest notes

- **`is_legal` is measured via `uci_to_move`** (decode + search the legal set), the
  exact §4.1 call S2 makes per intent. It is ~1.9 µs because it computes the full
  `legal_moves` set; a dedicated single-move `is_legal` (apply one move + king-safety
  test) would be faster still, but is unnecessary — the current path already beats
  < 10 µs by ~5×.
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
