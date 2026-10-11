//! Chunk-1 gate tests (ENGINE-CORE §8.6, §8.1).
//!
//! The two conditions this chunk must satisfy before the PR is green:
//! 1. the FEN round-trip property `parse_fen(to_fen(p)) == p`;
//! 2. `perft(startpos, 1) == 20`.
//!
//! The deep §8.1 perft corpus and the §8.2 edge-case library land in chunk 2.

use pgc_engine::{parse_fen, perft, Position};
use proptest::prelude::*;

/// Gate 2: the opening position has exactly 20 legal moves.
#[test]
fn gate_perft_startpos_depth_1() {
    assert_eq!(perft(&Position::startpos(), 1), 20);
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

// --- Property-based FEN round-trip (ENGINE-CORE §8.6) -----------------------
// Generate random *legal-ish* positions by replaying random legal moves from
// the start position, then assert the round-trip property on each. Replaying
// legal moves keeps every generated position reachable and self-consistent,
// which is a stronger generator than random piece scatter (which could place
// two kings in check, etc.).

use pgc_engine::{legal_moves, movegen::apply_move};

prop_compose! {
    fn random_reachable_position(max_plies: usize)(seed in any::<u64>()) -> Position {
        // Deterministic PRNG from the seed (xorshift) so failures shrink/replay.
        let mut state = seed | 1;
        let mut next = || {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            state
        };
        let plies = (next() as usize) % (max_plies + 1);
        let mut pos = Position::startpos();
        for _ in 0..plies {
            let moves = legal_moves(&pos);
            if moves.is_empty() {
                break; // checkmate or stalemate reached
            }
            let idx = (next() as usize) % moves.len();
            let m = moves.as_slice()[idx];
            apply_move(&mut pos, m);
        }
        pos
    }
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(256))]

    #[test]
    fn prop_fen_roundtrip(pos in random_reachable_position(40)) {
        let text = pos.to_fen();
        let reparsed = parse_fen(&text).expect("emitted FEN must re-parse");
        prop_assert_eq!(reparsed, pos);
    }

    #[test]
    fn prop_perft_1_equals_legal_move_count(pos in random_reachable_position(30)) {
        // §8.6 cheap cross-check: perft(p,1) == legal_moves(p).len().
        prop_assert_eq!(perft(&pos, 1), legal_moves(&pos).len() as u64);
    }
}

/// Shallow perft **smoke check** (NOT the chunk gate; NOT the §8.1 corpus).
///
/// The only *gated* perft condition this chunk is `perft(startpos, 1) == 20`
/// (see `gate_perft_startpos_depth_1`). These two extra shallow startpos counts
/// are a cheap confidence probe that the generator is correct beyond depth 1 —
/// kept deliberately small. The full §8.1 reference corpus (Kiwipete and
/// Positions 3–6 to depth 5–6, startpos to depth 6, with `perft_divide`
/// localisation) is wired as the dedicated G-A conformance suite in chunk 2.
#[test]
fn smoke_perft_startpos_shallow() {
    assert_eq!(perft(&Position::startpos(), 2), 400);
    assert_eq!(perft(&Position::startpos(), 3), 8902);
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
