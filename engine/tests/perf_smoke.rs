//! Per-commit performance SMOKE guard (ENGINE-CORE §6.1 / §8.8).
//!
//! §8.8's fast tier lists an "`is_legal`/`legal_moves` micro-bench threshold"
//! as a **per-commit** gate. The precise statistical measurement is criterion
//! (`benches/movegen.rs`, nightly `engine-perf-bench` job) — too slow/noisy to
//! run per commit. This test is the per-commit half: an ordinary `#[test]` that
//! times the two hot calls with a GENEROUS order-of-magnitude ceiling, so it
//! catches a catastrophic regression (an accidental O(n²) blow-up, a stray
//! allocation storm) every commit without flaking on shared-runner noise.
//!
//! The ceilings here are DELIBERATELY loose — ~100× the real figures (which are
//! ~1.9 µs `legal_moves`, ~0.28 µs `is_legal` in release; this runs in the
//! debug test profile, so it is slower and noisier). A green here does NOT mean
//! the §6.1 targets are met to spec — that is the nightly criterion job's job.
//! This only asserts the engine has not catastrophically regressed.

use pgc_engine::{is_legal, legal_moves, parse_fen, uci_to_move};
use std::time::Instant;

const KIWIPETE: &str = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1";

/// Loose per-commit ceiling: the median call must be under this in the DEBUG
/// test profile. Real release figures are ~1000× smaller; this only trips on a
/// gross regression. (debug build + CI jitter → keep it generous.)
const LEGAL_MOVES_CEIL_US: u128 = 200; // ~100× the ~1.9 µs release figure, debug-padded
const IS_LEGAL_CEIL_US: u128 = 200;

fn median_call_micros<F: FnMut()>(iters: u32, mut f: F) -> u128 {
    // Warm up, then take the best-of-a-few median to damp scheduler noise.
    for _ in 0..iters {
        f();
    }
    let mut samples = Vec::new();
    for _ in 0..5 {
        let t = Instant::now();
        for _ in 0..iters {
            f();
        }
        samples.push(t.elapsed().as_micros() / iters as u128);
    }
    samples.sort_unstable();
    samples[samples.len() / 2]
}

#[test]
fn perf_smoke_legal_moves_not_catastrophic() {
    let kiwipete = parse_fen(KIWIPETE).expect("kiwipete FEN");
    let per_call = median_call_micros(2000, || {
        let _ = legal_moves(&kiwipete);
    });
    assert!(
        per_call < LEGAL_MOVES_CEIL_US,
        "legal_moves perf-smoke regressed: {per_call} µs/call >= {LEGAL_MOVES_CEIL_US} µs \
         ceiling (debug). Real release figure is ~1.9 µs — see engine/PERF.md and the \
         nightly engine-perf-bench job."
    );
}

#[test]
fn perf_smoke_is_legal_not_catastrophic() {
    let kiwipete = parse_fen(KIWIPETE).expect("kiwipete FEN");
    let m = uci_to_move(&kiwipete, "e2a6").expect("e2a6 legal in kiwipete");
    let per_call = median_call_micros(2000, || {
        let _ = is_legal(&kiwipete, m);
    });
    assert!(
        per_call < IS_LEGAL_CEIL_US,
        "is_legal perf-smoke regressed: {per_call} µs/call >= {IS_LEGAL_CEIL_US} µs \
         ceiling (debug). Real release figure is ~0.28 µs — see engine/PERF.md."
    );
}
