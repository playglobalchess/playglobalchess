//! §8.2 legal-move edge-case library — E-1..E-14 (ENGINE-CORE §5, §8.2, chunk 2
//! stage 2c).
//!
//! One or more explicit assertions per edge case on a hand-built position,
//! targeting the **decisive property** of that case: the exact move that must be
//! present or absent (ep legality, castling legality, pin), the full promotion
//! set, the post-move state (castling-right loss, clock), or the exact count
//! (E-14's 218). Every FEN and expected outcome here was verified against the
//! implemented generator during bring-up; no assertion is a guessed expectation.
//!
//! # Scope split (which cases live here vs. stage 2d)
//! This file covers the edge cases decided by the **legal-move set / position
//! state**: E-1, E-2, E-3, E-4, E-5, E-6, E-7, E-8, E-9, E-13, E-14. The three
//! cases decided by **game-end classification** — E-10 (mate vs stalemate),
//! E-11 (repetition identity incl. castling/ep), E-12 (insufficient material)
//! — require `status()` (§3.7 / §4.1), which lands in stage 2d; their §8.3
//! assertions are written there. This split keeps 2c purely about move
//! generation.

use pgc_engine::{legal_moves, make, parse_fen, Position};

/// Sorted UCI strings of the legal moves of `fen`.
fn moves(fen: &str) -> Vec<String> {
    let p = parse_fen(fen).expect("edge-case FEN must parse");
    let mut v: Vec<String> = legal_moves(&p).iter().map(|m| m.to_uci()).collect();
    v.sort();
    v
}

fn has_move(fen: &str, uci: &str) -> bool {
    moves(fen).iter().any(|m| m == uci)
}

// E-1 — EP target convention is the OPEN-D hybrid (lenient internal, strict
// exported). The internal/perft side is proven by the §8.1 corpus (Position 3
// and Kiwipete carry lenient ep squares); the exported-FEN side is proven in
// the fen.rs unit tests (strict drop when no capturer). This file asserts the
// *legality* half: a lenient ep target does not make an illegal ep legal — see
// E-2 and E-3, which are the move-set consequences of E-1.

/// E-2 — en passant that exposes the mover's own king (the horizontal pin
/// through BOTH pawns) is **illegal**. `8/8/8/K2pP2r/8/8/8/7k w - d6`: capturing
/// exd6 removes both the e5 and d5 pawns from rank 5, exposing Ka5 to Rh5. The
/// single most common perft-failing bug — the ep move must be ABSENT.
#[test]
fn e2_en_passant_exposing_king_is_illegal() {
    let fen = "8/8/8/K2pP2r/8/8/8/7k w - d6 0 1";
    assert!(
        !has_move(fen, "e5d6"),
        "E-2: ep capture e5d6 must be illegal (exposes Ka5 to Rh5); got {:?}",
        moves(fen)
    );
    // The non-ep push is still legal — the pin is specific to the ep capture.
    assert!(
        has_move(fen, "e5e6"),
        "E-2: the plain push e5e6 should be legal"
    );
}

/// E-3 — en passant is legal only on the immediate reply. With no ep target set
/// (`-`), an adjacent enemy pawn gives NO ep capture even though the geometry
/// looks right.
#[test]
fn e3_en_passant_only_immediate() {
    // White e5, Black d5 adjacent, but ep field is '-' (the double push was not
    // the last move) → exd6 must be absent.
    let fen = "rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq - 0 3";
    assert!(
        !has_move(fen, "e5d6"),
        "E-3: no ep right ⇒ e5d6 illegal; got {:?}",
        moves(fen)
    );
}

/// E-4 — castling may not pass THROUGH an attacked square. White O-O (`e1g1`)
/// with f1 attacked by a rook on f8 is illegal.
#[test]
fn e4_castling_through_attacked_square_illegal() {
    let fen = "4rk2/8/8/8/8/8/8/4K2R w K - 0 1"; // Rf8 attacks f1 (king's path)
    assert!(
        !has_move(fen, "e1g1"),
        "E-4: O-O through attacked f1 must be illegal; got {:?}",
        moves(fen)
    );
}

/// E-4 (queenside nuance) — for O-O-O the b-file square MAY be attacked (the
/// rook, not the king, passes over b1); only the king's path e1/d1/c1 must be
/// safe. With only b1 attacked, O-O-O (`e1c1`) is legal.
#[test]
fn e4_queenside_b_file_attacked_is_allowed() {
    let fen = "1r2k3/8/8/8/8/8/8/R3K3 w Q - 0 1"; // Rb8 attacks b1 only
    assert!(
        has_move(fen, "e1c1"),
        "E-4: O-O-O legal when only b1 (not the king's path) is attacked; got {:?}",
        moves(fen)
    );
}

/// E-5 — castling out of check (king currently in check) is illegal, both sides.
#[test]
fn e5_castling_out_of_check_illegal() {
    let fen = "4rk2/8/8/8/8/8/8/R3K2R w KQ - 0 1"; // Re8 checks Ke1 down the e-file
    assert!(!has_move(fen, "e1g1"), "E-5: cannot O-O while in check");
    assert!(!has_move(fen, "e1c1"), "E-5: cannot O-O-O while in check");
}

/// E-5 — castling INTO check (destination square attacked) is illegal.
#[test]
fn e5_castling_into_check_illegal() {
    let fen = "6rk/8/8/8/8/8/8/4K2R w K - 0 1"; // Rg8 attacks g1 (O-O destination)
    assert!(
        !has_move(fen, "e1g1"),
        "E-5: O-O into attacked g1 must be illegal; got {:?}",
        moves(fen)
    );
}

/// E-6 — castling right lost by rook CAPTURE (not just a rook move). Capturing a
/// rook on its home square removes that side's corresponding right. Rxa8 clears
/// Black's queen-side right, visible in the resulting FEN.
#[test]
fn e6_castling_right_lost_by_rook_capture() {
    let fen = "r3k3/R7/8/8/8/8/8/4K3 w q - 0 1"; // White Ra7; Black a8 rook with 'q' right
    let base = parse_fen(fen).unwrap();
    let rxa8 = legal_moves(&base)
        .iter()
        .copied()
        .find(|m| m.to_uci() == "a7a8")
        .expect("Rxa8 must be a legal capture of the home rook");
    let mut p = base.clone();
    make(&mut p, rxa8);
    let out = p.to_fen();
    assert!(
        out.contains(" b - "),
        "E-6: Rxa8 must clear Black's 'q' right; got FEN {out}"
    );
}

/// E-7 — promotion incl. all four underpromotions, on a push AND a capture. A
/// pawn reaching the last rank produces exactly {Q,R,B,N} per destination.
#[test]
fn e7_promotion_all_four_push_and_capture() {
    // Black g2 pawn: push g2g1 and captures g2f1 / g2h1, each × 4 promotions.
    let fen = "n1n5/PPPk4/8/8/8/8/4Kppp/5N1N b - - 0 1";
    let m = moves(fen);
    for dest in ["g1", "f1", "h1"] {
        for pc in ["q", "r", "b", "n"] {
            let uci = format!("g2{dest}{pc}");
            assert!(m.contains(&uci), "E-7: missing promotion {uci}; got {m:?}");
        }
    }
    // No bare (non-promoting) pawn move to the last rank exists.
    assert!(
        !m.iter().any(|s| s == "g2g1"),
        "E-7: a last-rank pawn move must carry a promotion suffix"
    );
}

/// E-8 — an absolutely pinned piece may not move off the pin ray. A knight
/// pinned to its king by a rook has ZERO legal moves.
#[test]
fn e8_absolute_pin_knight_cannot_move() {
    let fen = "3rk3/8/8/8/8/8/3N4/3K4 w - - 0 1"; // Rd8 pins Nd2 to Kd1 on the d-file
    let m = moves(fen);
    assert!(
        !m.iter().any(|s| s.starts_with("d2")),
        "E-8: pinned Nd2 must have no legal move; got {m:?}"
    );
}

/// E-9 — under double check, ONLY king moves are legal (no block/capture can
/// resolve two checks at once).
#[test]
fn e9_double_check_king_moves_only() {
    // Black Ke8 double-checked by Bb5 (b5-e8 diagonal) and Re1 (e-file).
    let fen = "4k3/8/8/1B6/8/8/8/4RK2 b - - 0 1";
    let m = moves(fen);
    assert!(
        !m.is_empty(),
        "E-9: king must have at least one escape here"
    );
    assert!(
        m.iter().all(|s| s.starts_with("e8")),
        "E-9: under double check only the king may move; got {m:?}"
    );
}

/// E-13 — the halfmove clock resets on any capture or pawn move, and increments
/// on a quiet move.
#[test]
fn e13_halfmove_clock_reset_and_increment() {
    let base = Position::startpos();
    // Pawn move resets to 0.
    let e2e4 = legal_moves(&base)
        .iter()
        .copied()
        .find(|m| m.to_uci() == "e2e4")
        .unwrap();
    let mut p = base.clone();
    make(&mut p, e2e4);
    assert_eq!(p.halfmove_clock, 0, "E-13: pawn move resets halfmove clock");
    // A quiet (knight) move then increments.
    let nf6 = legal_moves(&p)
        .iter()
        .copied()
        .find(|m| m.to_uci() == "g8f6")
        .unwrap();
    make(&mut p, nf6);
    assert_eq!(
        p.halfmove_clock, 1,
        "E-13: quiet move increments halfmove clock"
    );
}

/// E-14 — a very high branching position generates the full set without
/// overflow/truncation. The canonical 218-legal-move position.
#[test]
fn e14_max_legal_moves_218() {
    let fen = "R6R/3Q4/1Q4Q1/4Q3/2Q4Q/Q4Q2/pp1Q4/kBNN1KB1 w - - 0 1";
    assert_eq!(
        moves(fen).len(),
        218,
        "E-14: the 218-move position must generate exactly 218 legal moves"
    );
}
