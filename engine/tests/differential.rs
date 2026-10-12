//! §8.7 differential testing (ENGINE-CORE §8.7, chunk 3, D3) — DEV-ONLY.
//!
//! Cross-checks our `perft` against the independent reference crate `shakmaty`
//! on the §8.1 corpus positions. This is a DEVELOPMENT gate that finds OUR bugs
//! fast against a second implementation; it is **not** a substitute for the
//! published-reference counts (§8.1) and `shakmaty` is **never** a runtime
//! dependency (OPEN-A: own implementation) — the whole file compiles only under
//! `--features difftest`.
//!
//! Run: `cargo test --features difftest --test differential`
//! (nightly tier; see ENGINE-CORE §8.8).
//!
//! When the feature is off this file is an empty module, so the normal/CI fast
//! build never pulls shakmaty.

#![cfg(feature = "difftest")]

use pgc_engine::{parse_fen, perft as our_perft};
use shakmaty::{fen::Fen, CastlingMode, Chess};

const STARTPOS: &str = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const KIWIPETE: &str = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1";
const POS3: &str = "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1";
const POS4: &str = "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1";
const POS5: &str = "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8";
const POS6: &str = "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10";

/// Reference perft via shakmaty.
fn ref_perft(fen: &str, depth: u32) -> u64 {
    let setup: Fen = fen.parse().expect("shakmaty: parse FEN");
    let pos: Chess = setup
        .into_position(CastlingMode::Standard)
        .expect("shakmaty: legal position");
    shakmaty::perft(&pos, depth)
}

/// Our perft on the same FEN.
fn mine(fen: &str, depth: u32) -> u64 {
    let mut p = parse_fen(fen).expect("our: parse FEN");
    our_perft(&mut p, depth)
}

/// Assert the two engines agree on every corpus position to a shallow depth.
/// Agreement with an independent implementation is strong evidence against a
/// generator bug that a self-consistent perft count might mask.
#[test]
fn differential_perft_matches_shakmaty() {
    let cases: &[(&str, &str, u32)] = &[
        ("startpos", STARTPOS, 4),
        ("kiwipete", KIWIPETE, 4),
        ("pos3", POS3, 5),
        ("pos4", POS4, 4),
        ("pos5", POS5, 4),
        ("pos6", POS6, 4),
    ];
    for &(name, fen, depth) in cases {
        let (a, b) = (mine(fen, depth), ref_perft(fen, depth));
        assert_eq!(a, b, "{name} d{depth}: ours={a} shakmaty={b}");
    }
}
