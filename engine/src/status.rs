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

/// Insufficient mating material — a *dead position* no sequence of legal moves
/// can checkmate from, so it is an automatic draw (ENGINE-CORE §3.7 / E-12,
/// FIDE Art. 5.2.2 reading).
///
/// With no pawns, rooks, or queens on the board, it is a draw iff:
/// - there is **at most one** minor piece in total (K vs K, K+B vs K, K+N vs
///   K); **or**
/// - **every** minor on the board is a **bishop** (no knights) and **all** of
///   those bishops stand on a **single square colour** — then no mate exists no
///   matter how many there are or who owns them (K+B vs K+B same colour, KBB vs
///   K same colour, any same-colour bishop collection).
///
/// Anything else can (not necessarily must, but *can*) force or allow mate and
/// is therefore **not** auto-insufficient — explicitly including **K+N+N vs K**
/// (E-12: mate is not forced but is possible), any position with a knight plus
/// another minor, and bishops present on both square colours.
#[must_use]
pub fn is_insufficient_material(pos: &Position) -> bool {
    use crate::types::Piece;

    // Any pawn, rook, or queen ⇒ mate is possible ⇒ sufficient.
    for color in [Color::White, Color::Black] {
        for kind in [PieceType::Pawn, PieceType::Rook, PieceType::Queen] {
            if pos.piece_bb(Piece::new(color, kind)) != 0 {
                return false;
            }
        }
    }

    // Count minors (both colours pooled — material is dead or not regardless of
    // which side holds it) and track the square colours bishops occupy.
    let mut knights = 0u32;
    let mut bishops = 0u32;
    let mut bishop_on_light = false;
    let mut bishop_on_dark = false;
    for color in [Color::White, Color::Black] {
        knights += pos
            .piece_bb(Piece::new(color, PieceType::Knight))
            .count_ones();
        let bb = pos.piece_bb(Piece::new(color, PieceType::Bishop));
        bishops += bb.count_ones();
        for sq in crate::bitboard::BitIter::new(bb) {
            // Light square iff (file + rank) is odd under LERF (a1=0,0 ⇒ dark).
            if (sq.file() + sq.rank()) % 2 == 1 {
                bishop_on_light = true;
            } else {
                bishop_on_dark = true;
            }
        }
    }

    // At most one minor in total — the lone-minor and bare-kings draws.
    if knights + bishops <= 1 {
        return true;
    }

    // Two or more minors: a draw ONLY if they are all bishops (no knights) on a
    // single square colour. A knight present, or bishops on both colours, means
    // mate is possible ⇒ not auto-insufficient (K+N+N is caught here).
    knights == 0 && !(bishop_on_light && bishop_on_dark)
}

/// Whether `color` has enough material to deliver checkmate by **some** series
/// of legal moves (ENGINE-CORE §4.1, incremental surface per §4; the per-side
/// companion to [`is_insufficient_material`]).
///
/// This answers the single question the **timeout-vs-insufficient-material**
/// rule needs (FIDE Art. 6.9, surfaced to the game-server at SPEC-S2-GAME-SERVER
/// §3.3): when a player flags, the game is a draw instead of a loss iff the
/// OPPONENT "cannot checkmate the player's king by any possible series of legal
/// moves" — i.e. the opponent (the side that still has time) lacks mating
/// material. S2 asks `has_mating_material(pos, opponent_of_flagged)`; a `false`
/// answer makes it `TIMEOUT_VS_INSUFFICIENT`, a `true` answer makes it `FLAG`.
///
/// It is a per-SIDE capacity test, distinct from [`is_insufficient_material`]'s
/// symmetric *dead-position* test (which is true only when NEITHER side can
/// mate). Example: in K+Q vs K+N, the knight side has no mating material
/// (`false`) while the queen side does (`true`), yet the position is not a dead
/// position for either call of `is_insufficient_material` (it returns `false` —
/// a queen is present). The two functions answer different questions.
///
/// Definition (the settled FIDE-6.9 reading): `color` has mating material unless
/// it holds ONLY one of — a lone king, K+one knight, or K+one bishop. Concretely
/// `color` CAN mate iff it has any pawn, rook, or queen, OR two-or-more minors
/// (two knights, bishop+knight, two bishops — all can force or allow mate
/// against a cooperating king, which "any series of legal moves" permits). A
/// lone king or a single minor cannot. This mirrors how Lichess/chess.com rule
/// timeouts and is the capacity needed for the §3.3 split; it is NOT the
/// dead-position rule and must not be "unified" with it.
///
/// The engine only reports this capacity; whether a flag ends the game as FLAG
/// or TIMEOUT_VS_INSUFFICIENT is S2 governance (detect-don't-govern, §3.7).
#[must_use]
pub fn has_mating_material(pos: &Position, color: Color) -> bool {
    use crate::types::Piece;

    // Any pawn, rook, or queen ⇒ this side can mate.
    for kind in [PieceType::Pawn, PieceType::Rook, PieceType::Queen] {
        if pos.piece_bb(Piece::new(color, kind)) != 0 {
            return true;
        }
    }

    // Otherwise capacity hinges on this side's minor count. Two or more minors
    // (NN, BN, BB — same or opposite colour) can force or allow mate against a
    // cooperating king; a lone king or a single minor cannot.
    let minors = pos
        .piece_bb(Piece::new(color, PieceType::Knight))
        .count_ones()
        + pos
            .piece_bb(Piece::new(color, PieceType::Bishop))
            .count_ones();
    minors >= 2
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
/// `prior_keys` is the sequence of Zobrist keys ([`Position::zobrist`]) of the
/// positions that occurred **before** `pos` in the game — i.e. every earlier
/// node, NOT including `pos` itself. The current visit to `pos` is always
/// counted here, so the total occurrence count is
/// `(prior_keys matching pos) + 1`. Taking *prior* keys makes the convention
/// unambiguous: there is no "did I include the current position?" question for
/// the caller to get wrong (an earlier inclusive-of-`pos` design silently
/// shifted every threshold by one). S2 passes the keys of the positions it has
/// already folded, then asks about the position it just reached.
///
/// A terminal no-move state or an automatic draw still takes precedence over
/// repetition (a checkmate that repeats a position is a checkmate).
///
/// - total 5+ ⇒ `DrawRepetition(Automatic)` (fivefold).
/// - total 3+ ⇒ `DrawRepetition(Claimable)` (threefold).
///
/// Counts by Zobrist key. A caller treating a *claim* as game-ending should
/// re-verify with full position equality (§3.7) to rule out a hash collision;
/// this reports availability, matching the engine's "detect, don't govern"
/// contract.
#[must_use]
pub fn status_with_history(pos: &Position, prior_keys: &[u64]) -> GameStatus {
    // Position-derived conditions win over repetition.
    let base = status(pos);
    if base != GameStatus::Ongoing {
        return base;
    }

    let key = pos.zobrist();
    // +1 for the current visit to `pos`, which is never in `prior_keys`.
    let occurrences = prior_keys.iter().filter(|&&k| k == key).count() + 1;
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
        // prior_keys EXCLUDES the current position; the current visit is +1.
        // No priors ⇒ 1st occurrence ⇒ ongoing.
        assert_eq!(status_with_history(&p, &[]), GameStatus::Ongoing);
        // 2 prior occurrences + current = 3 ⇒ claimable threefold.
        assert_eq!(
            status_with_history(&p, &[k, k]),
            GameStatus::DrawRepetition(DrawClaim::Claimable)
        );
        // 4 prior + current = 5 ⇒ automatic fivefold.
        assert_eq!(
            status_with_history(&p, &[k, k, k, k]),
            GameStatus::DrawRepetition(DrawClaim::Automatic)
        );
        // 1 prior + current = 2 ⇒ still ongoing (not yet threefold).
        assert_eq!(status_with_history(&p, &[k]), GameStatus::Ongoing);
    }

    #[test]
    fn repetition_does_not_override_terminal() {
        // A checkmate position that also "repeats" is still checkmate.
        let mate = pos("R5k1/5ppp/8/8/8/8/8/6K1 b - - 0 1");
        let k = mate.zobrist();
        assert_eq!(
            status_with_history(&mate, &[k, k, k, k]),
            GameStatus::Checkmate
        );
    }

    #[test]
    fn kbb_same_colour_vs_k_is_insufficient() {
        // Two same-coloured bishops (c1 + e1, both dark) vs a lone king: no mate
        // possible ⇒ insufficient (more general than literal "K+B vs K+B").
        assert!(is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/2B1B3 w - - 0 1"
        )));
    }

    #[test]
    fn opposite_colour_bishops_vs_k_is_sufficient() {
        // Two bishops on opposite colours (c1 dark + f1 light) CAN mate ⇒ not draw.
        assert!(!is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/2B2B2 w - - 0 1"
        )));
    }

    #[test]
    fn knight_plus_bishop_vs_k_is_sufficient() {
        // KBN vs K is a forced mate ⇒ never insufficient.
        assert!(!is_insufficient_material(&pos(
            "8/8/8/4k3/8/8/4K3/4BN2 w - - 0 1"
        )));
    }

    #[test]
    fn status_does_not_mutate_or_depend_on_clocks_for_mate() {
        // Checkmate detection ignores the clock entirely.
        let m = pos("rnb1kbnr/pppp1ppp/8/4p3/6Pq/5P2/PPPPP2P/RNBQKBNR w KQkq - 99 3");
        assert_eq!(status(&m), GameStatus::Checkmate);
    }

    // --- has_mating_material: the per-side FIDE-6.9 timeout capacity test ------

    #[test]
    fn mating_material_lone_king_and_single_minor_cannot() {
        // Lone king: no mating material.
        let p = pos("4k3/8/8/8/8/8/8/4K3 w - - 0 1");
        assert!(!has_mating_material(&p, Color::White));
        assert!(!has_mating_material(&p, Color::Black));
        // K + single knight: cannot mate.
        let kn = pos("4k3/8/8/8/8/8/8/4KN2 w - - 0 1");
        assert!(!has_mating_material(&kn, Color::White));
        // K + single bishop: cannot mate.
        let kb = pos("4k3/8/8/8/8/8/8/4KB2 w - - 0 1");
        assert!(!has_mating_material(&kb, Color::White));
    }

    #[test]
    fn mating_material_two_minors_can() {
        // K+N+N: two knights CAN mate a cooperating king (6.9 "any series").
        let knn = pos("4k3/8/8/8/8/8/8/4KNN1 w - - 0 1");
        assert!(has_mating_material(&knn, Color::White));
        // K+B+N: can mate.
        let kbn = pos("4k3/8/8/8/8/8/8/4KBN1 w - - 0 1");
        assert!(has_mating_material(&kbn, Color::White));
        // K+B+B (same or opposite colour — the per-side test does not care): can.
        let kbb = pos("4k3/8/8/8/8/8/8/2B1KB2 w - - 0 1");
        assert!(has_mating_material(&kbb, Color::White));
    }

    #[test]
    fn mating_material_major_or_pawn_can() {
        // A queen, a rook, or a pawn each means this side can mate.
        assert!(has_mating_material(
            &pos("4k3/8/8/8/8/8/8/3QK3 w - - 0 1"),
            Color::White
        ));
        assert!(has_mating_material(
            &pos("4k3/8/8/8/8/8/8/3RK3 w - - 0 1"),
            Color::White
        ));
        assert!(has_mating_material(
            &pos("4k3/8/8/8/8/4P3/8/4K3 w - - 0 1"),
            Color::White
        ));
    }

    #[test]
    fn mating_material_is_per_side_not_symmetric() {
        // K+Q vs K+N: the queen side CAN mate; the knight side CANNOT. This is
        // the case is_insufficient_material cannot answer (it is not a dead
        // position — a queen is on the board — so it returns false for both,
        // telling S2 nothing about the per-side timeout ruling).
        let p = pos("4k1n1/8/8/8/8/8/8/3QK3 w - - 0 1");
        assert!(has_mating_material(&p, Color::White)); // queen side
        assert!(!has_mating_material(&p, Color::Black)); // lone-ish: K + one knight
        assert!(!is_insufficient_material(&p)); // NOT a dead position (queen present)
    }
}
