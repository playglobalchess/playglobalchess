#!/usr/bin/env python3
"""Check engine micro-bench medians against the ENGINE-CORE §6.1 thresholds.

Run AFTER `cargo bench --bench movegen` (which writes criterion estimates under
engine/target/criterion/<name>/new/estimates.json). Reads the median (point
estimate) in nanoseconds for each gated benchmark and fails (exit 1) if it
regresses past its §6.1 target. Nightly-tier only — see .github/workflows/ci.yml.

Thresholds (§6.1):
  legal_moves/kiwipete  < 1 ms      (1_000_000 ns)   — "typical middlegame"
  is_legal/kiwipete_move< 10 µs     (   10_000 ns)   — S2 per-intent hot path

The targets are generous ceilings, not the measured values (~1.9 µs each as of
stage 3a); CI guards against a REGRESSION past the spec ceiling, not against
drift from the current number.
"""
import json
import os
import sys

# benchmark id (criterion dir) -> §6.1 ceiling in nanoseconds
THRESHOLDS_NS = {
    "legal_moves/kiwipete": 1_000_000,
    "is_legal/kiwipete_move": 10_000,
}

CRITERION_ROOT = os.path.join(
    os.path.dirname(__file__), "..", "..", "engine", "target", "criterion"
)


def median_ns(bench_id: str) -> float:
    # criterion flattens '/' in a bench id to '_' for the directory name.
    bench_dir = bench_id.replace("/", "_")
    path = os.path.join(CRITERION_ROOT, bench_dir, "new", "estimates.json")
    if not os.path.isfile(path):
        raise FileNotFoundError(
            f"no criterion estimates at {path} — did `cargo bench --bench movegen` run?"
        )
    with open(path) as f:
        data = json.load(f)
    # criterion 0.5: estimates.json has {"median": {"point_estimate": <ns>, ...}}
    return float(data["median"]["point_estimate"])


def main() -> int:
    failed = False
    for bench_id, ceiling in THRESHOLDS_NS.items():
        try:
            got = median_ns(bench_id)
        except FileNotFoundError as e:
            print(f"::error::{e}")
            failed = True
            continue
        ok = got < ceiling
        mark = "OK " if ok else "FAIL"
        print(f"{mark} {bench_id}: median {got:.0f} ns (ceiling {ceiling} ns)")
        if not ok:
            print(f"::error::{bench_id} regressed past §6.1 ceiling: {got:.0f} ns >= {ceiling} ns")
            failed = True
    if failed:
        return 1
    print("all §6.1 bench thresholds pass")
    return 0


if __name__ == "__main__":
    sys.exit(main())
