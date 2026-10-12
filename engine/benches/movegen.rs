//! §6.1 performance benches (ENGINE-CORE §6.1, NFR-1, chunk 3 stage 3a).
//!
//! Measures the two numbers the G-A performance box names:
//! - `legal_moves` on a typical middlegame position — target **< 1 ms**
//!   (goal: tens of µs);
//! - single-move validation (the S2 per-intent hot path) — target **< 10 µs**.
//!
//! Run `cargo bench` (release). criterion reports the median; the nightly CI
//! `engine-perf-bench` job (added in this stage) runs these and the thresholds
//! are checked there, off the per-commit path.
//!
//! Reference position: **Kiwipete** — the canonical dense middlegame (pins,
//! castling both sides, many captures), the hardest realistic branching the
//! generator faces, so it is a conservative stand-in for "typical middlegame".

use criterion::{criterion_group, criterion_main, Criterion};
use pgc_engine::{legal_moves, parse_fen, uci_to_move, Position};
use std::hint::black_box;

const KIWIPETE: &str = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1";

fn bench_legal_moves(c: &mut Criterion) {
    let startpos = Position::startpos();
    let kiwipete = parse_fen(KIWIPETE).expect("kiwipete FEN");

    c.bench_function("legal_moves/startpos", |b| {
        b.iter(|| legal_moves(black_box(&startpos)))
    });
    c.bench_function("legal_moves/kiwipete", |b| {
        b.iter(|| legal_moves(black_box(&kiwipete)))
    });
}

fn bench_is_legal(c: &mut Criterion) {
    // Single-move validation = decode UCI + confirm it is in the legal set,
    // the exact §4.1 call S2 makes per move intent.
    let kiwipete = parse_fen(KIWIPETE).expect("kiwipete FEN");
    // A known-legal Kiwipete move.
    c.bench_function("is_legal/kiwipete_move", |b| {
        b.iter(|| uci_to_move(black_box(&kiwipete), black_box("e2a6")))
    });
}

criterion_group!(benches, bench_legal_moves, bench_is_legal);
criterion_main!(benches);
