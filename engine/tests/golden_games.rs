//! §8.5 golden-game replay + regression net (ENGINE-CORE §8.5, chunk 3 stage 3c).
//!
//! A curated set of complete real/known games, each a list of SAN moves,
//! replayed move-by-move through `san_to_move` + `make`, asserting the FEN at
//! **every ply**. Two kinds of assertion per game:
//!
//! 1. **Correctness anchor (external oracle) — the FINAL FEN only.** Each game's
//!    final FEN is a human-verified, externally-known value (not engine-derived),
//!    so the end of the replay is checked against an outside oracle.
//! 2. **Regression lock (NOT an external oracle) — the per-ply FENs.** The full
//!    per-ply sequence is engine-GENERATED and committed: it asserts "the engine
//!    still does what it did when this game was added," catching FUTURE drift. It
//!    does NOT independently verify the intermediate positions — their current
//!    correctness rests on the perft/edge-case/property suites, not on this file.
//!    This is the §8.5 regression net: **every bug found becomes a new golden
//!    case before its fix merges**, and when the engine's behaviour legitimately
//!    changes, the per-ply locks must be **re-derived**, with the final anchor
//!    re-verified against an outside source — they are not self-certifying.
//!
//! The games are deliberately chosen to exercise the special-move paths §8.5
//! names: en passant, promotion, castling both sides, and a threefold-repetition
//! shuffle. SAN move text (not raw PGN with tags/comments) is used — full PGN
//! parsing is F-08's job, not the engine's.
//!
//! To ADD a game: list its SAN moves and its known final FEN; run once to print
//! the per-ply FENs (the `--nocapture` dump helper below), paste them in, and
//! the game becomes a permanent regression lock.

use pgc_engine::{make, san_to_move, Position};

/// Replay a SAN move list from the start position, returning the FEN after each
/// ply (index 0 = after move 1). Panics with context if any SAN is illegal —
/// which itself is a correctness signal (the engine rejected a move a real game
/// legally made).
fn replay(sans: &[&str]) -> Vec<String> {
    let mut pos = Position::startpos();
    let mut fens = Vec::with_capacity(sans.len());
    for (i, san) in sans.iter().enumerate() {
        let m = san_to_move(&pos, san)
            .unwrap_or_else(|e| panic!("ply {} ({san}): {e} in {}", i + 1, pos.to_fen()));
        make(&mut pos, m);
        fens.push(pos.to_fen());
    }
    fens
}

/// Assert the per-ply FEN sequence (the engine-generated REGRESSION LOCK) AND
/// that the final FEN matches `final_anchor` (the human-verified EXTERNAL
/// oracle). Only the final anchor is independently verified; the intermediate
/// plies lock current behaviour, they do not certify it (see the module header).
fn assert_game(name: &str, sans: &[&str], expected_per_ply: &[&str], final_anchor: &str) {
    let got = replay(sans);
    assert_eq!(
        got.len(),
        expected_per_ply.len(),
        "{name}: replayed {} plies, expected {}",
        got.len(),
        expected_per_ply.len()
    );
    for (i, (g, e)) in got.iter().zip(expected_per_ply).enumerate() {
        assert_eq!(g, e, "{name}: FEN mismatch at ply {}", i + 1);
    }
    assert_eq!(
        got.last().map(String::as_str),
        Some(final_anchor),
        "{name}: final FEN does not match the external anchor"
    );
}

// --- Golden games (SAN; per-ply FENs verified via the engine, final FEN is the
// external correctness anchor). Each hits a special-move path §8.5 names. ---

/// Scholar's mate — ends in checkmate (`Qxf7#`). Exercises the mate end-state.
#[test]
fn golden_scholars_mate() {
    let sans = ["e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#"];
    let per_ply = [
        "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1",
        "rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 0 2",
        "rnbqkbnr/pppp1ppp/8/4p3/2B1P3/8/PPPP1PPP/RNBQK1NR b KQkq - 1 2",
        "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/8/PPPP1PPP/RNBQK1NR w KQkq - 2 3",
        "r1bqkbnr/pppp1ppp/2n5/4p2Q/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 3 3",
        "r1bqkb1r/pppp1ppp/2n2n2/4p2Q/2B1P3/8/PPPP1PPP/RNB1K1NR w KQkq - 4 4",
        "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4",
    ];
    assert_game(
        "scholars",
        &sans,
        &per_ply,
        "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4",
    );
    // The anchor position is checkmate.
    let final_pos = Position::from_fen(per_ply[6]).unwrap();
    assert_eq!(
        pgc_engine::status(&final_pos),
        pgc_engine::GameStatus::Checkmate,
        "scholars: final position must be checkmate"
    );
}

/// Both sides castle king-side. Exercises O-O for White and Black.
#[test]
fn golden_castling_both_sides() {
    let sans = [
        "e4", "e5", "Nf3", "Nc6", "Bc4", "Bc5", "O-O", "Nf6", "d3", "O-O",
    ];
    let per_ply = [
        "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1",
        "rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 0 2",
        "rnbqkbnr/pppp1ppp/8/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R b KQkq - 1 2",
        "r1bqkbnr/pppp1ppp/2n5/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R w KQkq - 2 3",
        "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/5N2/PPPP1PPP/RNBQK2R b KQkq - 3 3",
        "r1bqk1nr/pppp1ppp/2n5/2b1p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4",
        "r1bqk1nr/pppp1ppp/2n5/2b1p3/2B1P3/5N2/PPPP1PPP/RNBQ1RK1 b kq - 5 4",
        "r1bqk2r/pppp1ppp/2n2n2/2b1p3/2B1P3/5N2/PPPP1PPP/RNBQ1RK1 w kq - 6 5",
        "r1bqk2r/pppp1ppp/2n2n2/2b1p3/2B1P3/3P1N2/PPP2PPP/RNBQ1RK1 b kq - 0 5",
        "r1bq1rk1/pppp1ppp/2n2n2/2b1p3/2B1P3/3P1N2/PPP2PPP/RNBQ1RK1 w - - 1 6",
    ];
    assert_game(
        "castle",
        &sans,
        &per_ply,
        "r1bq1rk1/pppp1ppp/2n2n2/2b1p3/2B1P3/3P1N2/PPP2PPP/RNBQ1RK1 w - - 1 6",
    );
}

/// En passant: 1.e4 Nf6 2.e5 d5 3.exd6 e.p. Exercises the ep capture + the ep
/// target appearing then being consumed.
#[test]
fn golden_en_passant() {
    let sans = ["e4", "Nf6", "e5", "d5", "exd6"];
    let per_ply = [
        "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1",
        "rnbqkb1r/pppppppp/5n2/8/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 1 2",
        "rnbqkb1r/pppppppp/5n2/4P3/8/8/PPPP1PPP/RNBQKBNR b KQkq - 0 2",
        "rnbqkb1r/ppp1pppp/5n2/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3",
        "rnbqkb1r/ppp1pppp/3P1n2/8/8/8/PPPP1PPP/RNBQKBNR b KQkq - 0 3",
    ];
    // Ply 4 shows the ep target `d6`; ply 5 the captured black d5 pawn is gone
    // and a white pawn stands on d6.
    assert_game(
        "en_passant",
        &sans,
        &per_ply,
        "rnbqkb1r/ppp1pppp/3P1n2/8/8/8/PPPP1PPP/RNBQKBNR b KQkq - 0 3",
    );
}

/// Promotion by capture: White's a-pawn marches and captures the b8 knight with
/// promotion (bxa8=Q), which ALSO clears Black's queen-side castling right (E-6
/// via a promoting capture of the home rook's square neighbour). Exercises
/// promotion + capture + rights loss together.
#[test]
fn golden_promotion_capture() {
    let sans = [
        "a4", "Nf6", "a5", "Ne4", "a6", "Nc6", "axb7", "Nb8", "bxa8=Q",
    ];
    let per_ply = [
        "rnbqkbnr/pppppppp/8/8/P7/8/1PPPPPPP/RNBQKBNR b KQkq - 0 1",
        "rnbqkb1r/pppppppp/5n2/8/P7/8/1PPPPPPP/RNBQKBNR w KQkq - 1 2",
        "rnbqkb1r/pppppppp/5n2/P7/8/8/1PPPPPPP/RNBQKBNR b KQkq - 0 2",
        "rnbqkb1r/pppppppp/8/P7/4n3/8/1PPPPPPP/RNBQKBNR w KQkq - 1 3",
        "rnbqkb1r/pppppppp/P7/8/4n3/8/1PPPPPPP/RNBQKBNR b KQkq - 0 3",
        "r1bqkb1r/pppppppp/P1n5/8/4n3/8/1PPPPPPP/RNBQKBNR w KQkq - 1 4",
        "r1bqkb1r/pPpppppp/2n5/8/4n3/8/1PPPPPPP/RNBQKBNR b KQkq - 0 4",
        "rnbqkb1r/pPpppppp/8/8/4n3/8/1PPPPPPP/RNBQKBNR w KQkq - 1 5",
        "Qnbqkb1r/p1pppppp/8/8/4n3/8/1PPPPPPP/RNBQKBNR b KQk - 0 5",
    ];
    assert_game(
        "promotion_capture",
        &sans,
        &per_ply,
        "Qnbqkb1r/p1pppppp/8/8/4n3/8/1PPPPPPP/RNBQKBNR b KQk - 0 5",
    );
}

/// Threefold by position: both knights out and back twice returns the start
/// position to its 3rd occurrence. Exercises repetition identity + the
/// halfmove clock, and asserts `status_with_history` sees the claimable draw.
#[test]
fn golden_threefold_shuffle() {
    let sans = ["Nf3", "Nf6", "Ng1", "Ng8", "Nf3", "Nf6", "Ng1", "Ng8"];
    let per_ply = [
        "rnbqkbnr/pppppppp/8/8/8/5N2/PPPPPPPP/RNBQKB1R b KQkq - 1 1",
        "rnbqkb1r/pppppppp/5n2/8/8/5N2/PPPPPPPP/RNBQKB1R w KQkq - 2 2",
        "rnbqkb1r/pppppppp/5n2/8/8/8/PPPPPPPP/RNBQKBNR b KQkq - 3 2",
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 4 3",
        "rnbqkbnr/pppppppp/8/8/8/5N2/PPPPPPPP/RNBQKB1R b KQkq - 5 3",
        "rnbqkb1r/pppppppp/5n2/8/8/5N2/PPPPPPPP/RNBQKB1R w KQkq - 6 4",
        "rnbqkb1r/pppppppp/5n2/8/8/8/PPPPPPPP/RNBQKBNR b KQkq - 7 4",
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 8 5",
    ];
    assert_game(
        "threefold",
        &sans,
        &per_ply,
        "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 8 5",
    );

    // Replay accumulating prior-position keys; the final position is the start
    // position on its 3rd occurrence ⇒ claimable threefold (§3.7).
    let mut pos = Position::startpos();
    let mut prior = Vec::new();
    for san in sans {
        prior.push(pos.zobrist());
        let m = san_to_move(&pos, san).unwrap();
        make(&mut pos, m);
    }
    assert_eq!(
        pgc_engine::status_with_history(&pos, &prior),
        pgc_engine::GameStatus::DrawRepetition(pgc_engine::DrawClaim::Claimable),
        "threefold: final position should be a claimable repetition draw"
    );
}
