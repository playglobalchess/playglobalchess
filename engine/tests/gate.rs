//! Chunk-1 gate tests (ENGINE-CORE §8.6, §8.1).
//!
//! The two conditions chunk 1 had to satisfy, kept as permanent regression
//! guards: the FEN round-trip property on fixed positions and
//! `perft(startpos, 1) == 20`, plus a shallow perft smoke check and the
//! no-panic fuzz. The §8.6 PROPERTY net (make/unmake inverse, UCI/SAN/FEN
//! round-trips, perft(·,1)==len over random reachable positions) now lives in
//! its single home `tests/properties.rs` (stage 2f).

use pgc_engine::{parse_fen, perft, Position};

/// Gate 2: the opening position has exactly 20 legal moves.
#[test]
fn gate_perft_startpos_depth_1() {
    assert_eq!(perft(&mut Position::startpos(), 1), 20);
}

/// Gate 1 (fixed cases): round-trip a representative set of FENs through
/// parse → emit → parse and assert the `Position` is unchanged.
///
/// We assert on the `Position` (`parse_fen(to_fen(p)) == p`), the exact spec
/// property, rather than on text — because the OPEN-D strict-export rule can
/// legitimately change the ep *text* while leaving the parsed position equal.
#[test]
fn gate_fen_roundtrip_fixed_positions() {
    let fens = [
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", // startpos
        "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1", // Kiwipete
        "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1",                // Position 3
        "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1", // Position 4
        "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8", // Position 5
        "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10", // Position 6
        "rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3", // ep capturable
        "8/8/8/8/8/8/8/8 w - - 0 1",                                // empty board
        "4k3/8/8/8/8/8/8/4K3 b - - 99 150",                         // high clocks, black to move
    ];
    for fen in fens {
        let p = parse_fen(fen).expect("valid test FEN");
        let round = parse_fen(&p.to_fen()).expect("re-parse of emitted FEN");
        assert_eq!(round, p, "round-trip changed the position for: {fen}");
    }
}

/// Shallow perft **smoke check** (NOT the §8.1 corpus, which lives in
/// `tests/perft_corpus.rs`). A cheap confidence probe that the generator is
/// correct beyond depth 1.
#[test]
fn smoke_perft_startpos_shallow() {
    assert_eq!(perft(&mut Position::startpos(), 2), 400);
    assert_eq!(perft(&mut Position::startpos(), 3), 8902);
}

#[test]
fn fen_parser_never_panics_on_garbage() {
    // Acceptance (§5): malformed FEN -> typed error, never a panic.
    let garbage = [
        "",
        " ",
        "x",
        "/////// w - - 0 1",
        "8/8/8/8/8/8/8/8",
        "99/8/8/8/8/8/8/8 w - - 0 1",
        "8/8/8/8/8/8/8/8 w KQkq zz 0 1",
        "8/8/8/8/8/8/8/8 w - - -1 1",
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR",
        "\0\0\0",
        "8/8/8/8/8/8/8/8 w - - 99999999999999999999 1",
    ];
    for g in garbage {
        let _ = parse_fen(g); // must return Result, not panic
    }
}
