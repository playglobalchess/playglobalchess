//! §8.1 perft conformance corpus — the G-A backbone (ENGINE-CORE §8.1, chunk 2
//! stage 2b).
//!
//! Node-count verification against the published CPW reference counts for the
//! six canonical positions. Exact match is required; a single-node mismatch is
//! a generator bug, localised with [`pgc_engine::perft_divide`] (§4.4).
//!
//! # Two tiers (ENGINE-CORE §8.8)
//! - **Fast tier** (`perft_corpus_fast_*`): runs on **every commit / PR**. Each
//!   position to a depth whose node count stays in the low millions, so the
//!   whole file finishes in a few seconds. These already exercise every tricky
//!   rule (Kiwipete castling/pins, Position-3 ep/pins) deeply enough to catch
//!   essentially all move-gen defects.
//! - **Slow tier** (`perft_corpus_slow_*`, marked `#[ignore]`): the full §8.1
//!   **target depths** (startpos d6, Kiwipete d5, …). These run **hundreds of
//!   millions of nodes** and are gated to the nightly / pre-G-A run via
//!   `cargo test -- --ignored`. They are NOT run per commit (minutes-long) and
//!   must NOT be run in a memory-constrained interactive session.
//!
//! Perft counts move PATHS, not game outcomes: it never short-circuits on
//! mate/stalemate/draw (§8.1). All reference counts are the Chess Programming
//! Wiki published values.

use pgc_engine::{parse_fen, perft, perft_divide};

const STARTPOS: &str = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const KIWIPETE: &str = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1";
const POS3: &str = "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1";
const POS4: &str = "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1";
const POS5: &str = "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8";
const POS6: &str = "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10";

/// Assert `perft(fen, depth) == expected`; on mismatch, dump `perft_divide` at
/// depth 1 so a failure localises to a root move rather than just a wrong total.
#[track_caller]
fn assert_perft(name: &str, fen: &str, depth: u32, expected: u64) {
    let mut pos = parse_fen(fen).expect("corpus FEN must parse");
    let got = perft(&mut pos, depth);
    if got != expected {
        let mut p2 = parse_fen(fen).unwrap();
        let divide = perft_divide(&mut p2, 1);
        let breakdown: Vec<String> = divide
            .iter()
            .map(|(m, n)| format!("{}={n}", m.to_uci()))
            .collect();
        panic!(
            "{name} perft(d{depth}) = {got}, expected {expected} (off by {}). \
             depth-1 divide: [{}]",
            got as i64 - expected as i64,
            breakdown.join(", ")
        );
    }
}

// --------------------------------------------------------------------------
// Fast tier — runs every commit (§8.8). A few seconds total.
// --------------------------------------------------------------------------

#[test]
fn perft_corpus_fast_startpos() {
    assert_perft("startpos", STARTPOS, 1, 20);
    assert_perft("startpos", STARTPOS, 2, 400);
    assert_perft("startpos", STARTPOS, 3, 8_902);
    assert_perft("startpos", STARTPOS, 4, 197_281);
}

#[test]
fn perft_corpus_fast_kiwipete() {
    assert_perft("kiwipete", KIWIPETE, 1, 48);
    assert_perft("kiwipete", KIWIPETE, 2, 2_039);
    assert_perft("kiwipete", KIWIPETE, 3, 97_862);
}

#[test]
fn perft_corpus_fast_pos3() {
    assert_perft("pos3", POS3, 1, 14);
    assert_perft("pos3", POS3, 2, 191);
    assert_perft("pos3", POS3, 3, 2_812);
    assert_perft("pos3", POS3, 4, 43_238);
    assert_perft("pos3", POS3, 5, 674_624);
}

#[test]
fn perft_corpus_fast_pos4() {
    assert_perft("pos4", POS4, 1, 6);
    assert_perft("pos4", POS4, 2, 264);
    assert_perft("pos4", POS4, 3, 9_467);
    assert_perft("pos4", POS4, 4, 422_333);
}

#[test]
fn perft_corpus_fast_pos5() {
    assert_perft("pos5", POS5, 1, 44);
    assert_perft("pos5", POS5, 2, 1_486);
    assert_perft("pos5", POS5, 3, 62_379);
}

#[test]
fn perft_corpus_fast_pos6() {
    assert_perft("pos6", POS6, 1, 46);
    assert_perft("pos6", POS6, 2, 2_079);
    assert_perft("pos6", POS6, 3, 89_890);
}

// --------------------------------------------------------------------------
// Slow tier — the full §8.1 target depths. #[ignore]d: run only in the
// nightly / pre-G-A tier via `cargo test -- --ignored`. Hundreds of millions
// of nodes; minutes-long; do NOT run in an interactive/memory-tight session.
// --------------------------------------------------------------------------

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_startpos_d6() {
    assert_perft("startpos", STARTPOS, 6, 119_060_324);
}

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_kiwipete_d5() {
    assert_perft("kiwipete", KIWIPETE, 5, 193_690_690);
}

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_pos3_d6() {
    assert_perft("pos3", POS3, 6, 11_030_083);
}

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_pos4_d5() {
    assert_perft("pos4", POS4, 5, 15_833_292);
}

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_pos5_d5() {
    assert_perft("pos5", POS5, 5, 89_941_194);
}

#[test]
#[ignore = "slow tier (§8.8): run in nightly/pre-G-A via `cargo test -- --ignored`"]
fn perft_corpus_slow_pos6_d5() {
    assert_perft("pos6", POS6, 5, 164_075_551);
}

/// Sanity: `perft_divide` at depth 1 sums to `perft` at depth 1 for every
/// corpus position (cheap structural check that the divide harness itself is
/// consistent — §4.4).
#[test]
fn perft_divide_sums_match_corpus() {
    for (name, fen) in [
        ("startpos", STARTPOS),
        ("kiwipete", KIWIPETE),
        ("pos3", POS3),
        ("pos4", POS4),
        ("pos5", POS5),
        ("pos6", POS6),
    ] {
        let mut p = parse_fen(fen).unwrap();
        let d1: u64 = perft(&mut p, 1);
        let mut p2 = parse_fen(fen).unwrap();
        let sum: u64 = perft_divide(&mut p2, 1).iter().map(|(_, n)| n).sum();
        assert_eq!(sum, d1, "{name}: perft_divide sum != perft at depth 1");
        // perft_divide must also restore the position.
        assert_eq!(
            p2,
            parse_fen(fen).unwrap(),
            "{name}: perft_divide did not restore position"
        );
    }
}
