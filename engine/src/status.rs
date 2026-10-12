//! Game-end & draw detection (ENGINE-CORE §3.7, §4.1) — chunk 2 stage 2d.
//!
//! `status(p)` reports every end condition derivable from a **single**
//! position: checkmate, stalemate, insufficient material, and the 50-/75-move
//! rules (from `halfmove_clock`). Repetition needs game history, which one
//! `Position` cannot carry, so it is surfaced by the separate
//! [`status_with_history`] — the pure `status(p)` never returns a repetition
//! variant.
//!
//! **The engine only reports availability; it never ends a game.** `status`
//! distinguishes *claimable* draws (threefold, 50-move) from *automatic* ones
//! (checkmate, stalemate, fivefold, 75-move, insufficient material). Acting on a
//! claim is S2's governance policy (§4.1), not the engine's.
//!
//! `status` does NOT drive move generation — perft and the generator never
//! consult it (§8.1). It is a read-only query over a finished position.

use crate::movegen::{is_in_check, legal_moves};
use crate::position::Position;
use crate::types::{Color, PieceType};

/// Whether a draw condition is auto-applied or must be claimed (§4.1).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum DrawClaim {
    /// Automatic — the game is over by rule (fivefold, 75-move, insufficient
    /// material, stalemate).
    Automatic,
    /// Claimable — a player may claim it, but play may also continue (threefold,
    /// 50-move). The engine reports availability; S2 decides.
    Claimable,
}

/// The result of evaluating a position's end conditions (ENGINE-CORE §4.1).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum GameStatus {
    /// The game continues; no end condition is present.
    Ongoing,
    /// Side to move is in check with no legal move — the side to move loses.
    Checkmate,
    /// Side to move is not in check but has no legal move — automatic draw.
    Stalemate,
    /// Insufficient mating material for either side — automatic draw.
    DrawInsufficientMaterial,
    /// 50-move (`Claimable`, ≥100 plies) or 75-move (`Automatic`, ≥150 plies).
    DrawFiftyMove(DrawClaim),
    /// Threefold (`Claimable`) or fivefold (`Automatic`) repetition. Only ever
    /// produced by [`status_with_history`].
    DrawRepetition(DrawClaim),
}

/// Material too sparse for either side to force mate (ENGINE-CORE §3.7 / E-12):
/// K vs K, K+B vs K, K+N vs K, and K+B vs K+B with **both** bishops on the same
/// color complex. **K+N+N vs K is NOT a draw** (mate is not forced but is
/// possible) — this classifier returns `false` for it, by design.
#[must_use]
pub fn is_insufficient_material(pos: &Position) -> bool {
    // Any pawn, rook, or queen on the board ⇒ sufficient (mate is possible).
    for color in [Color::White, Color::Black] {
        for kind in [PieceType::Pawn, PieceType::Rook, PieceType::Queen] {
            if pos.piece_bb(crate::types::Piece::new(color, kind)) != 0 {
                return false;
            }
        }
    }

    // Count minor pieces per color and track bishop square-colors.
    let mut knights = 0u32;
    let mut bishops = 0u32;
    let mut bishop_on_light = false;
    let mut bishop_on_dark = false;
    for color in [Color::White, Color::Black] {
        knights += pos
            .piece_bb(crate::types::Piece::new(color, PieceType::Knight))
            .count_ones();
        let bb = pos.piece_bb(crate::types::Piece::new(color, PieceType::Bishop));
        bishops += bb.count_ones();
        for sq in crate::bitboard::BitIter::new(bb) {
            // Light square iff (file + rank) is odd (a1=dark under LERF).
            if (sq.file() + sq.rank()) % 2 == 1 {
                bishop_on_light = true;
            } else {
                bishop_on_dark = true;
            }
        }
    }

    let minors = knights + bishops;
    match (knights, bishops, minors) {
        // K vs K.
        (0, 0, 0) => true,
        // K+B vs K  or  K+N vs K (exactly one minor anywhere).
        (_, _, 1) => true,
        // Two bishops, zero knights: draw iff all bishops share one color
        // complex (K+B vs K+B same color, or KBB-vs-K same color — still no
        // forced mate). Opposite colors ⇒ NOT insufficient.
        (0, 2, 2) => !(bishop_on_light && bishop_on_dark),
        // Everything else (incl. K+N+N vs K — E-12 — and any ≥2 knights or
        // knight+bishop combos) is NOT auto-insufficient.
        _ => false,
    }
}

/// End-condition status of a single position (ENGINE-CORE §4.1).
///
/// Precedence: terminal no-move states (checkmate / stalemate) first, then the
/// automatic material/75-move draws, then the claimable 50-move draw, else
/// `Ongoing`. Repetition is NOT considered here (needs history) — use
/// [`status_with_history`].
#[must_use]
pub fn status(pos: &Position) -> GameStatus {
    if legal_moves(pos).is_empty() {
        return if is_in_check(pos) {
            GameStatus::Checkmate
        } else {
            GameStatus::Stalemate
        };
    }

    if is_insufficient_material(pos) {
        return GameStatus::DrawInsufficientMaterial;
    }

    // 75-move is automatic and takes precedence over the claimable 50-move.
    if pos.halfmove_clock >= 150 {
        return GameStatus::DrawFiftyMove(DrawClaim::Automatic);
    }
    if pos.halfmove_clock >= 100 {
        return GameStatus::DrawFiftyMove(DrawClaim::Claimable);
    }

    GameStatus::Ongoing
}

/// End-condition status INCLUDING repetition (ENGINE-CORE §3.7, §4.1).
///
/// `history` is the sequence of Zobrist keys ([`Position::zobrist`]) of every
/// position that has occurred in the game **so far, including `pos` itself** —
/// the key sequence S2 accumulates as it folds `MovePlayed` events. Repetition
/// is counted by key occurrence; a terminal no-move state or an automatic draw
/// still takes precedence over repetition (a checkmate that repeats a position
/// is a checkmate).
///
/// - 5+ occurrences ⇒ `DrawRepetition(Automatic)` (fivefold).
/// - 3+ occurrences ⇒ `DrawRepetition(Claimable)` (threefold).
///
/// NOTE: this counts by Zobrist key. A production caller that treats a *claim*
/// as game-ending should re-verify with full position equality (§3.7) to rule
/// out a hash collision; this function reports availability, matching the
/// engine's "detect, don't govern" contract.
#[must_use]
pub fn status_with_history(pos: &Position, history: &[u64]) -> GameStatus {
    // Position-derived conditions win over repetition.
    let base = status(pos);
    if base != GameStatus::Ongoing {
        return base;
    }

    let key = pos.zobrist();
    let occurrences = history.iter().filter(|&&k| k == key).count();
    if occurrences >= 5 {
        return GameStatus::DrawRepetition(DrawClaim::Automatic);
    }
    if occurrences >= 3 {
        return GameStatus::DrawRepetition(DrawClaim::Claimable);
    }

    GameStatus::Ongoing
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pos(fen: &str) -> Position {
        Position::from_fen(fen).expect("valid FEN")
    }

    #[test]
    fn checkmate_back_rank() {
        // Black king mated on the back rank by a rook, own pawns blocking.
        assert_eq!(
            status(&pos("6k1/5ppp/8/8/8/8/8/R5K1 b - - 0 1")),
            GameStatus::Ongoing
        );
        let mate = pos("R5k1/5ppp/8/8/8/8/8/6K1 b - - 0 1");
        assert_eq!(status(&mate), GameStatus::Checkmate);
    }

    #[test]
    fn fools_mate_is_checkmate() {
        // 1. f3 e5 2. g4 Qh4#
        let m = pos("rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 1 3");
        assert_eq!(status(&m), GameStatus::Checkmate);
    }

    #[test]
    fn classic_stalemate() {
        // Black king a8, White Kc7? use the canonical: black to move, no legal
        // move, not in check. "k7/8/1Q6/8/8/8/8/7K b - - 0 1" is mate; use the
        // stalemate: "7k/5Q2/6K1/8/8/8/8/8 b - - 0 1" — Kh8, Qf7, Kg6: no move.
        let s = pos("7k/5Q2/6K1/8/8/8/8/8 b - - 0 1");
        assert_eq!(status(&s), GameStatus::Stalemate);
    }

    #[test]
    fn insufficient_material_classes_e12() {
        assert!(is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/8 w - - 0 1"
        ))); // K v K
        assert!(is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/5B2 w - - 0 1"
        ))); // K+B v K
        assert!(is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/5N2 w - - 0 1"
        ))); // K+N v K
             // K+B v K+B, same color complex (both on dark): draw.
        assert!(is_insufficient_material(&pos(
            "8/8/2b5/4k3/8/8/4K3/5B2 w - - 0 1"
        )));
        // K+B v K+B, opposite colors: NOT a draw.
        assert!(!is_insufficient_material(&pos(
            "8/8/3b4/4k3/8/8/4K3/5B2 w - - 0 1"
        )));
        // E-12: K+N+N v K is NOT auto-draw.
        assert!(!is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/4NN2 w - - 0 1"
        )));
        // A pawn ⇒ sufficient.
        assert!(!is_insufficient_material(&pos(
            "8/8/8/4k3/8/4P3/4K3/8 w - - 0 1"
        )));
    }

    #[test]
    fn insufficient_material_status_is_automatic_draw() {
        assert_eq!(
            status(&pos("8/8/8/4k3/8/8/4K3/8 w - - 0 1")),
            GameStatus::DrawInsufficientMaterial
        );
    }

    #[test]
    fn fifty_and_seventyfive_move() {
        // halfmove 100 ⇒ claimable; 150 ⇒ automatic. Use a position with legal
        // moves and sufficient material (rooks) so only the clock triggers.
        let claim = pos("4k3/8/8/8/8/8/R7/4K2R w - - 100 80");
        assert_eq!(
            status(&claim),
            GameStatus::DrawFiftyMove(DrawClaim::Claimable)
        );
        let auto = pos("4k3/8/8/8/8/8/R7/4K2R w - - 150 120");
        assert_eq!(
            status(&auto),
            GameStatus::DrawFiftyMove(DrawClaim::Automatic)
        );
        // Below 100: ongoing.
        let ongoing = pos("4k3/8/8/8/8/8/R7/4K2R w - - 99 80");
        assert_eq!(status(&ongoing), GameStatus::Ongoing);
    }

    #[test]
    fn repetition_threefold_and_fivefold() {
        let p = Position::startpos();
        let k = p.zobrist();
        // History including the current position: 1 occurrence ⇒ ongoing.
        assert_eq!(status_with_history(&p, &[k]), GameStatus::Ongoing);
        // 3 occurrences ⇒ claimable threefold.
        assert_eq!(
            status_with_history(&p, &[k, k, k]),
            GameStatus::DrawRepetition(DrawClaim::Claimable)
        );
        // 5 occurrences ⇒ automatic fivefold.
        assert_eq!(
            status_with_history(&p, &[k, k, k, k, k]),
            GameStatus::DrawRepetition(DrawClaim::Automatic)
        );
    }

    #[test]
    fn repetition_does_not_override_terminal() {
        // A checkmate position that also "repeats" is still checkmate.
        let mate = pos("R5k1/5ppp/8/8/8/8/8/6K1 b - - 0 1");
        let k = mate.zobrist();
        assert_eq!(
            status_with_history(&mate, &[k, k, k, k, k]),
            GameStatus::Checkmate
        );
    }

    #[test]
    fn status_does_not_mutate_or_depend_on_clocks_for_mate() {
        // Checkmate detection ignores the clock entirely.
        let m = pos("rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 99 3");
        assert_eq!(status(&m), GameStatus::Checkmate);
    }
}
