//! Move generation — pseudo-legal generation then a legality filter
//! (ENGINE-CORE §3.5).
//!
//! # Scope (chunk 2 stage 2a toward G-A)
//! This module implements the **two-stage design of §3.5** end to end — all six
//! piece types, castling, en passant, and promotion — plus **make/unmake with
//! an [`Undo`] token** (§3.6), a perfect inverse (`unmake(make(p,m)) == p`,
//! property-tested). It is deliberately the **simple, correct** form §3.5
//! prescribes ("the clearest correct design; optimize later only if perft
//! timing demands"): pseudo-legal + make/unmake + `is_square_attacked`, no
//! magic bitboards and no pin-aware fast path. The legality filter still uses
//! copy-make ([`apply_move`] = `make` with the `Undo` discarded, over a clone),
//! which is correct because it tests a king-safety predicate, not a hot loop.
//!
//! What is **still deferred** (NOT claimed verified here):
//! - the full §8.1 perft corpus (Kiwipete / Positions 3–6 at depth 5–6) and
//!   perft parity at depth ≥ 2 [stage 2b];
//! - the exhaustive E-1..E-14 edge-case assertions (§8.2), incl. the horizontal
//!   ep-pin (E-2) and the castling-through-check matrix (E-4/E-5) [stage 2c];
//! - game-end/draw detection + Zobrist (§3.7) [stage 2d], SAN (§4.2) [stage 2e];
//! - magic bitboards and the §6.1 benchmarks [later chunk].
//!
//! Gated now: `perft(startpos, 1) == 20`, the FEN round-trip, and the
//! make/unmake inverse. Deeper correctness is proven when the corpus lands.

use crate::bitboard::BitIter;
use crate::castling::CastlingRights;
use crate::moves::{Move, MoveList};
use crate::position::Position;
use crate::types::{Color, Piece, PieceType, Square};

// Knight and king offsets as (file_delta, rank_delta).
const KNIGHT_DELTAS: [(i8, i8); 8] = [
    (1, 2),
    (2, 1),
    (2, -1),
    (1, -2),
    (-1, -2),
    (-2, -1),
    (-2, 1),
    (-1, 2),
];
const KING_DELTAS: [(i8, i8); 8] = [
    (0, 1),
    (1, 1),
    (1, 0),
    (1, -1),
    (0, -1),
    (-1, -1),
    (-1, 0),
    (-1, 1),
];
const BISHOP_DIRS: [(i8, i8); 4] = [(1, 1), (1, -1), (-1, 1), (-1, -1)];
const ROOK_DIRS: [(i8, i8); 4] = [(0, 1), (0, -1), (1, 0), (-1, 0)];

#[inline]
fn offset(sq: Square, df: i8, dr: i8) -> Option<Square> {
    let f = sq.file() as i8 + df;
    let r = sq.rank() as i8 + dr;
    if (0..8).contains(&f) && (0..8).contains(&r) {
        Square::from_file_rank(f as u8, r as u8)
    } else {
        None
    }
}

/// Is `sq` attacked by any piece of color `by`?  (ENGINE-CORE §3.5 legality
/// primitive.) Pseudo-legal attack test — ignores pins/own-king safety, which
/// is exactly its job inside the legality filter.
#[must_use]
pub fn is_square_attacked(pos: &Position, sq: Square, by: Color) -> bool {
    // Pawns: a pawn of color `by` attacks diagonally forward. If an enemy pawn
    // sits on a square that would attack `sq`, `sq` is attacked. White pawns
    // attack upward, so a White attacker is one rank *below* `sq`.
    let pawn_rank = match by {
        Color::White => sq.rank().checked_sub(1),
        Color::Black => sq.rank().checked_add(1),
    };
    if let Some(pr) = pawn_rank {
        let by_pawn = Piece::new(by, PieceType::Pawn);
        for df in [-1i8, 1i8] {
            let f = sq.file() as i8 + df;
            if (0..8).contains(&f) {
                if let Some(from) = Square::from_file_rank(f as u8, pr) {
                    if pos.piece_at(from) == Some(by_pawn) {
                        return true;
                    }
                }
            }
        }
    }

    // Knights.
    let by_knight = Piece::new(by, PieceType::Knight);
    for (df, dr) in KNIGHT_DELTAS {
        if let Some(from) = offset(sq, df, dr) {
            if pos.piece_at(from) == Some(by_knight) {
                return true;
            }
        }
    }

    // King.
    let by_king = Piece::new(by, PieceType::King);
    for (df, dr) in KING_DELTAS {
        if let Some(from) = offset(sq, df, dr) {
            if pos.piece_at(from) == Some(by_king) {
                return true;
            }
        }
    }

    // Sliding: bishops/queens on diagonals, rooks/queens on orthogonals.
    let by_bishop = Piece::new(by, PieceType::Bishop);
    let by_rook = Piece::new(by, PieceType::Rook);
    let by_queen = Piece::new(by, PieceType::Queen);
    if slider_attacks(pos, sq, &BISHOP_DIRS, by_bishop, by_queen) {
        return true;
    }
    if slider_attacks(pos, sq, &ROOK_DIRS, by_rook, by_queen) {
        return true;
    }
    false
}

/// Ray-walk from `sq` along each direction; the first blocker, if it is one of
/// the two attacker piece types, attacks `sq`.
fn slider_attacks(
    pos: &Position,
    sq: Square,
    dirs: &[(i8, i8); 4],
    straight: Piece,
    queen: Piece,
) -> bool {
    for &(df, dr) in dirs {
        let mut cur = sq;
        while let Some(next) = offset(cur, df, dr) {
            match pos.piece_at(next) {
                None => cur = next,
                Some(p) => {
                    if p == straight || p == queen {
                        return true;
                    }
                    break; // any other piece blocks the ray
                }
            }
        }
    }
    false
}

/// All **pseudo-legal** moves for the side to move (ENGINE-CORE §3.5 stage 1):
/// ignores whether the move leaves the mover's king in check.
#[must_use]
pub fn pseudo_legal_moves(pos: &Position) -> MoveList {
    let mut list = MoveList::new();
    let us = pos.side_to_move;
    let own = pos.color_bb(us);

    for &kind in &PieceType::ALL {
        let bb = pos.piece_bb(Piece::new(us, kind));
        for from in BitIter::new(bb) {
            match kind {
                PieceType::Pawn => gen_pawn(pos, from, us, &mut list),
                PieceType::Knight => gen_steps(from, &KNIGHT_DELTAS, own, &mut list),
                PieceType::King => {
                    gen_steps(from, &KING_DELTAS, own, &mut list);
                    gen_castling(pos, us, &mut list);
                }
                PieceType::Bishop => gen_slides(pos, from, &BISHOP_DIRS, own, &mut list),
                PieceType::Rook => gen_slides(pos, from, &ROOK_DIRS, own, &mut list),
                PieceType::Queen => {
                    gen_slides(pos, from, &BISHOP_DIRS, own, &mut list);
                    gen_slides(pos, from, &ROOK_DIRS, own, &mut list);
                }
            }
        }
    }
    list
}

/// Non-sliding step moves (knight, king) to empty or enemy-occupied squares.
fn gen_steps(from: Square, deltas: &[(i8, i8); 8], own: u64, list: &mut MoveList) {
    for &(df, dr) in deltas {
        if let Some(to) = offset(from, df, dr) {
            if own & to.bb() == 0 {
                list.push(Move::new(from, to));
            }
        }
    }
}

/// Sliding moves along `dirs` until blocked; capture the first enemy piece.
fn gen_slides(pos: &Position, from: Square, dirs: &[(i8, i8); 4], own: u64, list: &mut MoveList) {
    for &(df, dr) in dirs {
        let mut cur = from;
        while let Some(to) = offset(cur, df, dr) {
            if own & to.bb() != 0 {
                break; // own piece blocks
            }
            list.push(Move::new(from, to));
            if pos.all_occ() & to.bb() != 0 {
                break; // captured an enemy piece; ray stops
            }
            cur = to;
        }
    }
}

/// Pawn pushes, double-pushes, diagonal captures, en passant, promotions.
fn gen_pawn(pos: &Position, from: Square, us: Color, list: &mut MoveList) {
    let (forward, start_rank, promo_rank): (i8, u8, u8) = match us {
        Color::White => (1, 1, 7),
        Color::Black => (-1, 6, 0),
    };
    let all = pos.all_occ();
    let enemy = pos.color_bb(us.flip());

    // Single push.
    if let Some(one) = offset(from, 0, forward) {
        if all & one.bb() == 0 {
            push_pawn_move(from, one, promo_rank, list);
            // Double push from the start rank, both squares empty.
            if from.rank() == start_rank {
                if let Some(two) = offset(from, 0, 2 * forward) {
                    if all & two.bb() == 0 {
                        list.push(Move::new(from, two));
                    }
                }
            }
        }
    }

    // Diagonal captures (incl. en passant target).
    for df in [-1i8, 1i8] {
        if let Some(to) = offset(from, df, forward) {
            let is_capture = enemy & to.bb() != 0;
            let is_ep = pos.ep_target == Some(to);
            if is_capture || is_ep {
                push_pawn_move(from, to, promo_rank, list);
            }
        }
    }
}

/// Emit a pawn move to `to`, expanding to four promotion moves if `to` is on
/// the promotion rank (ENGINE-CORE E-7: underpromotion is first-class).
fn push_pawn_move(from: Square, to: Square, promo_rank: u8, list: &mut MoveList) {
    if to.rank() == promo_rank {
        for pt in [
            PieceType::Queen,
            PieceType::Rook,
            PieceType::Bishop,
            PieceType::Knight,
        ] {
            list.push(Move::with_promotion(from, to, pt));
        }
    } else {
        list.push(Move::new(from, to));
    }
}

/// Castling candidates (ENGINE-CORE §3.5): rights present, squares between
/// empty, king not currently in check and not passing through / landing on an
/// attacked square. The full E-4/E-5 corpus is chunk 2; the rule is enforced
/// here so startpos (no castling available) and basic cases are correct.
fn gen_castling(pos: &Position, us: Color, list: &mut MoveList) {
    let (rank, enemy) = (if us == Color::White { 0u8 } else { 7u8 }, us.flip());
    let king_from = Square::from_file_rank(4, rank).expect("e-file king square");
    // King must be on its home square and not in check.
    if pos.piece_at(king_from) != Some(Piece::new(us, PieceType::King)) {
        return;
    }
    if is_square_attacked(pos, king_from, enemy) {
        return;
    }
    let all = pos.all_occ();

    // King side: squares f,g empty; king path e,f,g not attacked.
    if pos.castling_rights.has(CastlingRights::king_side(us)) {
        let f = Square::from_file_rank(5, rank).unwrap();
        let g = Square::from_file_rank(6, rank).unwrap();
        if all & f.bb() == 0
            && all & g.bb() == 0
            && !is_square_attacked(pos, f, enemy)
            && !is_square_attacked(pos, g, enemy)
        {
            list.push(Move::new(king_from, g));
        }
    }
    // Queen side: squares b,c,d empty; king path e,d,c not attacked (b may be).
    if pos.castling_rights.has(CastlingRights::queen_side(us)) {
        let b = Square::from_file_rank(1, rank).unwrap();
        let c = Square::from_file_rank(2, rank).unwrap();
        let d = Square::from_file_rank(3, rank).unwrap();
        if all & b.bb() == 0
            && all & c.bb() == 0
            && all & d.bb() == 0
            && !is_square_attacked(pos, d, enemy)
            && !is_square_attacked(pos, c, enemy)
        {
            list.push(Move::new(king_from, c));
        }
    }
}

/// All **legal** moves (ENGINE-CORE §3.5 stage 2): a pseudo-legal move is legal
/// iff, after making it, the mover's king is not attacked.
///
/// This chunk uses copy-make (clone + apply) for the filter rather than
/// make/unmake — correct and simple; the make/unmake hot path (§3.6) and its
/// `unmake(make(p,m))==p` property are chunk 2.
#[must_use]
pub fn legal_moves(pos: &Position) -> MoveList {
    let us = pos.side_to_move;
    let mut legal = MoveList::new();
    for &m in pseudo_legal_moves(pos).iter() {
        let mut next = pos.clone();
        apply_move(&mut next, m);
        if let Some(king_sq) = next.king_square(us) {
            if !is_square_attacked(&next, king_sq, us.flip()) {
                legal.push(m);
            }
        }
    }
    legal
}

/// Apply a move to a position in place (copy-make convenience). A thin wrapper
/// over [`make`] that discards the [`Undo`] token — for immutable-style callers
/// (clients, the legality filter) that clone first and never roll back
/// (ENGINE-CORE §3.6 / OPEN-C "copy-make for clients").
///
/// Precondition: `m` is pseudo-legal for `pos`.
pub fn apply_move(pos: &mut Position, m: Move) {
    let _ = make(pos, m);
}

/// The information [`unmake`] needs to restore a position to exactly its state
/// before [`make`] (ENGINE-CORE §3.6). Everything `make` destroys is captured
/// here so `unmake(make(p,m)) == p` holds bit-for-bit.
#[derive(Debug, Clone, Copy)]
pub struct Undo {
    /// Piece captured by this move (normal or en passant), with the square it
    /// stood on — restored on unmake. `None` for a non-capture.
    captured: Option<(Square, Piece)>,
    /// The moving piece *as it was before the move* (pre-promotion type), so a
    /// promotion is undone by putting the pawn back, not the promoted piece.
    moved: Piece,
    /// Position scalars as they were before `make`.
    prev_castling: CastlingRights,
    prev_ep: Option<Square>,
    prev_halfmove: u16,
    prev_fullmove: u32,
}

/// Make a move in place and return the [`Undo`] token to reverse it
/// (ENGINE-CORE §3.6 — the perft / S2-validation hot path). Handles captures,
/// en passant, castling rook movement, promotion, and every FEN-carried state
/// update (castling-right loss, ep target, clocks, side-to-move, fullmove).
///
/// Precondition: `m` is pseudo-legal for `pos`. `make` + `unmake` are a perfect
/// inverse — asserted by the `unmake(make(p,m)) == p` property test (§8.6).
pub fn make(pos: &mut Position, m: Move) -> Undo {
    let us = pos.side_to_move;
    let them = us.flip();
    let mover = pos.piece_at(m.from).expect("move from an occupied square");
    let is_pawn = mover.kind == PieceType::Pawn;

    let undo = Undo {
        captured: None, // filled below if a capture occurs
        moved: mover,
        prev_castling: pos.castling_rights,
        prev_ep: pos.ep_target,
        prev_halfmove: pos.halfmove_clock,
        prev_fullmove: pos.fullmove_number,
    };
    let prev_ep = undo.prev_ep;

    // En-passant capture: the captured pawn is behind the destination square.
    let mut captured_sq_piece: Option<(Square, Piece)> = None;
    let mut captured = pos.piece_at(m.to);
    if is_pawn && Some(m.to) == prev_ep && captured.is_none() {
        let cap_rank = m.from.rank(); // captured pawn shares the mover's rank
        let cap_sq = Square::from_file_rank(m.to.file(), cap_rank).expect("ep captured square");
        if let Some(p) = pos.take_piece(cap_sq) {
            captured = Some(p);
            captured_sq_piece = Some((cap_sq, p));
        }
    }

    // Remove any piece on the destination (normal capture).
    if captured.is_some() && captured_sq_piece.is_none() {
        if let Some(p) = pos.take_piece(m.to) {
            captured_sq_piece = Some((m.to, p));
        }
    }

    // Move the piece (promotion swaps the piece type on arrival).
    pos.take_piece(m.from);
    let arriving = match m.promotion {
        Some(pt) => Piece::new(us, pt),
        None => mover,
    };
    pos.put_piece(m.to, arriving);

    // Castling: move the rook too (king moved two files).
    if mover.kind == PieceType::King {
        let delta = m.to.file() as i8 - m.from.file() as i8;
        if delta == 2 {
            // king side: rook h->f
            let rank = m.from.rank();
            let rook = pos.take_piece(Square::from_file_rank(7, rank).unwrap());
            if let Some(r) = rook {
                pos.put_piece(Square::from_file_rank(5, rank).unwrap(), r);
            }
        } else if delta == -2 {
            // queen side: rook a->d
            let rank = m.from.rank();
            let rook = pos.take_piece(Square::from_file_rank(0, rank).unwrap());
            if let Some(r) = rook {
                pos.put_piece(Square::from_file_rank(3, rank).unwrap(), r);
            }
        }
    }

    // Update castling rights: king move clears both; rook move/capture clears
    // the matching side (E-6: capture of a home rook also clears it).
    update_castling_rights(pos, m, mover, captured);

    // En-passant target: set leniently after a double push (OPEN-D, §3.3).
    pos.ep_target = if is_pawn && (m.to.rank() as i8 - m.from.rank() as i8).abs() == 2 {
        let mid_rank = (m.from.rank() + m.to.rank()) / 2;
        Square::from_file_rank(m.from.file(), mid_rank)
    } else {
        None
    };

    // Halfmove clock: reset on pawn move or capture, else increment.
    if is_pawn || captured.is_some() {
        pos.halfmove_clock = 0;
    } else {
        pos.halfmove_clock = pos.halfmove_clock.saturating_add(1);
    }

    // Fullmove number increments after Black's move.
    if us == Color::Black {
        pos.fullmove_number += 1;
    }
    pos.side_to_move = them;

    debug_assert!({
        pos.debug_check();
        true
    });

    Undo {
        captured: captured_sq_piece,
        ..undo
    }
}

/// Reverse a [`make`] using its [`Undo`] token, restoring the position exactly
/// (ENGINE-CORE §3.6). Precondition: `undo` was produced by `make(pos, m)` and
/// `pos` has not changed since — the perfect inverse the §8.6 property asserts.
pub fn unmake(pos: &mut Position, m: Move, undo: Undo) {
    // The side that *made* the move is the opposite of the current side to move
    // (make flipped it). Restore turn first so helpers read the right color.
    let us = pos.side_to_move.flip();

    // Lift the arrived piece off the destination (promoted piece or the mover).
    pos.take_piece(m.to);
    // Put the original moving piece back on its from-square (undoes promotion:
    // `undo.moved` is the pre-promotion pawn).
    pos.put_piece(m.from, undo.moved);

    // Undo castling rook movement (king moved two files).
    if undo.moved.kind == PieceType::King {
        let delta = m.to.file() as i8 - m.from.file() as i8;
        let rank = m.from.rank();
        if delta == 2 {
            // king side: rook f->h
            if let Some(r) = pos.take_piece(Square::from_file_rank(5, rank).unwrap()) {
                pos.put_piece(Square::from_file_rank(7, rank).unwrap(), r);
            }
        } else if delta == -2 {
            // queen side: rook d->a
            if let Some(r) = pos.take_piece(Square::from_file_rank(3, rank).unwrap()) {
                pos.put_piece(Square::from_file_rank(0, rank).unwrap(), r);
            }
        }
    }

    // Restore any captured piece on the square it stood on (ep restores it on
    // the pawn's square, not the destination — hence the stored square).
    if let Some((sq, piece)) = undo.captured {
        pos.put_piece(sq, piece);
    }

    // Restore scalars and turn.
    pos.castling_rights = undo.prev_castling;
    pos.ep_target = undo.prev_ep;
    pos.halfmove_clock = undo.prev_halfmove;
    pos.fullmove_number = undo.prev_fullmove;
    pos.side_to_move = us;

    debug_assert!({
        pos.debug_check();
        true
    });
}

/// Clear castling rights invalidated by this move (ENGINE-CORE E-6).
fn update_castling_rights(pos: &mut Position, m: Move, mover: Piece, captured: Option<Piece>) {
    // Home squares of the four rooks.
    let wk_rook = Square::from_file_rank(7, 0).unwrap();
    let wq_rook = Square::from_file_rank(0, 0).unwrap();
    let bk_rook = Square::from_file_rank(7, 7).unwrap();
    let bq_rook = Square::from_file_rank(0, 7).unwrap();

    // King move clears both of that color's rights.
    if mover.kind == PieceType::King {
        pos.castling_rights
            .remove(CastlingRights::king_side(mover.color));
        pos.castling_rights
            .remove(CastlingRights::queen_side(mover.color));
    }
    // Rook leaving its home square clears that right.
    if mover.kind == PieceType::Rook {
        if m.from == wk_rook {
            pos.castling_rights.remove(CastlingRights::WHITE_KING);
        } else if m.from == wq_rook {
            pos.castling_rights.remove(CastlingRights::WHITE_QUEEN);
        } else if m.from == bk_rook {
            pos.castling_rights.remove(CastlingRights::BLACK_KING);
        } else if m.from == bq_rook {
            pos.castling_rights.remove(CastlingRights::BLACK_QUEEN);
        }
    }
    // Capturing a rook on its home square clears that right (E-6).
    if captured.map(|c| c.kind) == Some(PieceType::Rook) {
        if m.to == wk_rook {
            pos.castling_rights.remove(CastlingRights::WHITE_KING);
        } else if m.to == wq_rook {
            pos.castling_rights.remove(CastlingRights::WHITE_QUEEN);
        } else if m.to == bk_rook {
            pos.castling_rights.remove(CastlingRights::BLACK_KING);
        } else if m.to == bq_rook {
            pos.castling_rights.remove(CastlingRights::BLACK_QUEEN);
        }
    }
}

/// Is the side to move in check? (ENGINE-CORE §4.1 `is_in_check`.)
#[must_use]
pub fn is_in_check(pos: &Position) -> bool {
    match pos.king_square(pos.side_to_move) {
        Some(k) => is_square_attacked(pos, k, pos.side_to_move.flip()),
        None => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn startpos_has_20_legal_moves() {
        let p = Position::startpos();
        assert_eq!(legal_moves(&p).len(), 20);
    }

    #[test]
    fn startpos_not_in_check() {
        assert!(!is_in_check(&Position::startpos()));
    }

    #[test]
    fn startpos_pseudo_equals_legal() {
        // No pseudo-legal opening move leaves the king in check, so the sets match.
        let p = Position::startpos();
        assert_eq!(pseudo_legal_moves(&p).len(), legal_moves(&p).len());
    }

    #[test]
    fn each_opening_move_is_a_pawn_or_knight_move() {
        let p = Position::startpos();
        for m in legal_moves(&p).iter() {
            let pc = p.piece_at(m.from).unwrap();
            assert!(matches!(pc.kind, PieceType::Pawn | PieceType::Knight));
        }
    }

    /// make/unmake round-trips every legal move of a hand-built position back to
    /// the exact starting state (§3.6). Used to pin special-move paths the
    /// random reachable-position generator may not reliably reach.
    fn assert_make_unmake_restores(fen: &str) {
        let base = Position::from_fen(fen).expect("valid test FEN");
        for &m in legal_moves(&base).iter() {
            let mut p = base.clone();
            let undo = make(&mut p, m);
            unmake(&mut p, m, undo);
            assert_eq!(
                p,
                base,
                "make/unmake not inverse for {} in {fen}",
                m.to_uci()
            );
        }
    }

    #[test]
    fn make_unmake_inverse_en_passant() {
        // White pawn e5, Black just played d7-d5 → ep target d6, exd6 ep available.
        assert_make_unmake_restores("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3");
    }

    #[test]
    fn make_unmake_inverse_promotion_and_capture() {
        // White pawn b7 can push b8=Q/R/B/N or capture axb8/cxb8 with promotion.
        assert_make_unmake_restores("r1b1k2r/pPp5/8/8/8/8/8/4K3 w kq - 0 1");
    }

    #[test]
    fn make_unmake_inverse_castling_both_sides() {
        // White may castle both sides; exercises the rook-movement undo path.
        assert_make_unmake_restores("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1");
    }

    #[test]
    fn make_unmake_inverse_rook_capture_loses_right() {
        // Rook capture of a home rook clears a castling right (E-6); unmake must
        // restore it. White rook a7 can play Rxa8, capturing Black's a8 rook and
        // clearing Black's queen-side right — verified to be a real capture.
        let fen = "r3k3/R7/8/8/8/8/8/4K3 w q - 0 1";
        let base = Position::from_fen(fen).unwrap();
        let a8 = Square::from_coord("a8").unwrap();
        let has_rook_capture = legal_moves(&base)
            .iter()
            .any(|m| m.to == a8 && base.piece_at(a8).is_some());
        assert!(
            has_rook_capture,
            "test FEN must offer the Rxa8 rook capture"
        );
        assert_make_unmake_restores(fen);
    }
}
