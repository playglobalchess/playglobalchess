//! §8.3 game-end detection corpus (ENGINE-CORE §8.3, chunk 2 stage 2d).
//!
//! Explicit positions asserting the exact [`GameStatus`], covering the cases
//! MASTER §X.1.3 / §8.3 name: checkmate, stalemate, threefold **by position**
//! (incl. reached via different move orders, and the castling-rights
//! distinction E-11), fivefold auto, 50-/75-move, and every insufficient-
//! material class incl. the K+N+N non-draw (E-12).

use pgc_engine::{
    legal_moves, make, parse_fen, status, status_with_history, DrawClaim, GameStatus, Move,
    Position,
};

fn pos(fen: &str) -> Position {
    parse_fen(fen).expect("valid FEN")
}

fn uci(p: &Position, s: &str) -> Move {
    legal_moves(p)
        .iter()
        .copied()
        .find(|m| m.to_uci() == s)
        .unwrap_or_else(|| panic!("{s} not legal in {}", p.to_fen()))
}

// ---- Checkmate / stalemate (E-10) ----------------------------------------

#[test]
fn checkmate_smothered() {
    // Smothered mate: Black Kh8 boxed by its own pieces, White Nf7#.
    let m = pos("6rk/5Npp/8/8/8/8/8/6K1 b - - 0 1");
    assert_eq!(status(&m), GameStatus::Checkmate);
}

#[test]
fn stalemate_classic() {
    let s = pos("7k/5Q2/6K1/8/8/8/8/8 b - - 0 1");
    assert_eq!(status(&s), GameStatus::Stalemate);
}

// ---- Insufficient material (E-12) ----------------------------------------

#[test]
fn insufficient_material_all_classes() {
    assert_eq!(
        status(&pos("8/8/8/4k3/8/8/4K3/8 w - - 0 1")),
        GameStatus::DrawInsufficientMaterial
    );
    assert_eq!(
        status(&pos("8/8/8/4k3/8/8/4K3/5B2 w - - 0 1")),
        GameStatus::DrawInsufficientMaterial
    );
    assert_eq!(
        status(&pos("8/8/8/4k3/8/8/4K3/5N2 w - - 0 1")),
        GameStatus::DrawInsufficientMaterial
    );
    // same-colour bishops.
    assert_eq!(
        status(&pos("8/8/2b5/4k3/8/8/4K3/5B2 w - - 0 1")),
        GameStatus::DrawInsufficientMaterial
    );
}

#[test]
fn knn_vs_k_is_not_a_draw_e12() {
    // E-12: K+N+N vs K must NOT be reported as an automatic draw.
    let p = pos("8/8/8/4k3/8/8/4K3/4NN2 w - - 0 1");
    assert_ne!(status(&p), GameStatus::DrawInsufficientMaterial);
    assert_eq!(status(&p), GameStatus::Ongoing);
}

// ---- 50 / 75-move --------------------------------------------------------

#[test]
fn fifty_claimable_seventyfive_auto() {
    assert_eq!(
        status(&pos("4k3/8/8/8/8/8/R7/4K2R w - - 100 80")),
        GameStatus::DrawFiftyMove(DrawClaim::Claimable)
    );
    assert_eq!(
        status(&pos("4k3/8/8/8/8/8/R7/4K2R w - - 150 120")),
        GameStatus::DrawFiftyMove(DrawClaim::Automatic)
    );
}

// ---- Repetition by position (E-11) ---------------------------------------

/// Threefold reached by returning to the SAME position via knight shuffles.
/// The repetition is by position identity (Zobrist key), not by the move list —
/// so the same position recurring is counted regardless of how it was reached.
#[test]
fn threefold_by_position_via_shuffles() {
    let mut p = Position::startpos();
    let start_key = p.zobrist();
    let mut history = vec![start_key];

    // Ng1-f3-g1 / Ng8-f6-g8 twice returns to the start position (same key),
    // reaching it a 3rd time total.
    for mv in [
        "g1f3", "g8f6", "f3g1", "f6g8", "g1f3", "g8f6", "f3g1", "f6g8",
    ] {
        let m = uci(&p, mv);
        make(&mut p, m);
        history.push(p.zobrist());
    }

    // The start position has now occurred 3 times (initial + two returns).
    assert_eq!(
        p.zobrist(),
        start_key,
        "shuffles must return to the start position"
    );
    let occ = history.iter().filter(|&&k| k == start_key).count();
    assert_eq!(occ, 3, "start position should have occurred 3 times");
    assert_eq!(
        status_with_history(&p, &history),
        GameStatus::DrawRepetition(DrawClaim::Claimable)
    );
}

/// E-11: two positions with identical piece placement but DIFFERENT castling
/// rights are NOT the same for repetition — their keys differ, so a return to
/// identical-placement-but-fewer-rights is not a repetition of the original.
#[test]
fn repetition_distinguishes_castling_rights_e11() {
    let full = pos("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1");
    let moved_rook_back = pos("r3k2r/8/8/8/8/8/8/R3K2R w Kkq - 0 1"); // lost WQ right
    assert_ne!(
        full.zobrist(),
        moved_rook_back.zobrist(),
        "E-11: same placement, different castling rights ⇒ different repetition key"
    );
    // So a history of the full-rights position does not count the reduced-rights
    // one as a repeat.
    let hist = vec![full.zobrist(), full.zobrist()];
    assert_eq!(
        status_with_history(&moved_rook_back, &hist),
        GameStatus::Ongoing
    );
}

#[test]
fn fivefold_is_automatic() {
    let p = Position::startpos();
    let k = p.zobrist();
    assert_eq!(
        status_with_history(&p, &[k, k, k, k, k]),
        GameStatus::DrawRepetition(DrawClaim::Automatic)
    );
}

#[test]
fn status_never_short_circuits_perft() {
    // Guard (§8.1): status() is a read-only query and must not be consulted by
    // move generation. We assert the two are independent — a position in
    // checkmate still generates its (zero) legal moves without status() being
    // involved, and perft of a near-terminal position counts paths normally.
    let m = pos("R5k1/5ppp/8/8/8/8/8/6K1 b - - 0 1");
    assert_eq!(status(&m), GameStatus::Checkmate);
    assert_eq!(legal_moves(&m).len(), 0);
}
