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
both §6.1 targets with a ~500× margin on `legal_moves` and ~35× on `is_legal`.
Building magic bitboards now would be optimizing already-fast correct code against no
measured need, so stage 3b (magics) is **not undertaken for G-A**. It remains available
as a future optimization if a later workload (deep search in X-6 analysis, not the
rules engine) ever demands it.

### KNOWN, DELIBERATE spec drift (not yet reconciled — owner decision 2026-10-12)

ENGINE-CORE §3.2/§3.3 ("Sliding attacks via magic bitboards (or PEXT…)") and §6.3
("Precomputed attack/magic tables…") still describe magic bitboards in **normative
voice**, because the spec is **RATIFIED** and this decision has deliberately NOT been
written back into it. The owner chose to keep the magics-skip as a `PERF.md` note only
and to **hold** the formal reconciliation (a proposed **ADR-13** was drafted and
parked). So the spec and this decision disagree **on purpose, with the gap recorded
here** rather than silently.

Implications for a future reader of the spec:
- §3.3/§6.3's magic-bitboard language is an *available technique*, not a shipped
  requirement. Conformance is the §8 corpus + the §6.1 numbers, both green **without**
  magics.
- Do **not** read §3.3 as mandating magics, rebuild the rejected work, or file a
  "spec non-compliance" bug on their absence — this note (and the parked ADR-13) is the
  authority until the owner elects to ratify the reconciliation.
- The reconciliation path, when wanted, is: approve ADR-13, append it to
  `docs/MASTER-PLAN.md`, and add a one-line "see ADR-13" pointer in ENGINE-CORE
  §3.3/§6.3. No spec edit is made without that approval.

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

## CI — two tiers, and an honest note on §8.8's "per-commit threshold"

§8.8's fast tier names an "`is_legal`/`legal_moves` micro-bench **threshold**" as a
per-commit gate; the slow tier runs the full statistical bench nightly. The honest
engineering reality: **a wall-clock timing threshold cannot be enforced reliably on a
shared CI runner.** A debug-profile timing test tripped at 234 µs against a 200 µs
ceiling on a contended GitHub runner (real release figure ~1.9 µs) — a false positive,
not a regression. A timed assertion on shared infra flakes by construction, so shipping
one would make green CI meaningless. It was tried and removed.

What runs where instead:

- **Per-commit** (`engine` job, every PR): the hot paths are *exercised* deterministically
  — `properties.rs` runs `legal_moves` and `is_legal` across 256 random reachable
  positions (incl. `prop_is_legal_agrees_with_legal_moves` over all 64×64 pairs), and
  `perft_corpus.rs` drives `legal_moves` to depth 5. A behavioural regression (wrong
  moves, a fallback path) is caught every commit; these also implicitly bound throughput
  by completing within CI's time budget. This is the deterministic per-commit guard.
- **Nightly** (`engine-perf-bench`, schedule-only): `cargo bench` (release) +
  `check_bench.py` against the precise §6.1 ceilings (legal_moves < 1 ms, is_legal
  < 10 µs). This is the **authoritative timing gate** — run in a release build off the
  noisy per-commit path, which is the only place a wall-clock threshold is trustworthy.

So §8.8's *intent* (catch a perf regression; the threshold lives in CI) is met: the
per-commit tier guards behaviour deterministically, the nightly tier guards timing to
spec. The spec's literal "per-commit threshold" wording is reconciled by this note
rather than by a flaky timed test; if the owner wants a true per-commit timed gate it
needs a dedicated self-hosted/pinned runner, out of scope here.
