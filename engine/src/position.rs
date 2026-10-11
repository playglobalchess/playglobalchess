//! The `Position` — the complete, self-contained game state (ENGINE-CORE §3.3):
//! the fold target of the event log (ADR-10). Carries the redundant
//! bitboards + mailbox representation (§3.2) and every field FEN encodes.
//!
//! # OPEN-D en-passant hybrid (ENGINE-CORE §3.3, E-1 — owner-approved)
//! The engine stores the ep target **leniently**: set after *any* double pawn
//! push, so internal state and `perft` match the published §8.1 counts. The
//! **exported** FEN ([`Position::to_fen`]) is **strict**: it emits the ep
//! square only when a pawn of the side to move could legally capture to it,
//! matching Lichess (`scalachess`). Internal `ep_target` is unchanged; only the
//! *text* differs. Legality of an ep capture is enforced at move-gen time
//! regardless (E-2), so this split never changes which moves are legal.
//!
//! NOTE: "could legally capture" currently uses the *pseudo-legal* adjacency
//! test (an enemy — here, side-to-move — pawn sits on a square that attacks the
//! ep target). The full pin-aware refinement (the horizontal-pin case, E-2)
//! lands with move generation in chunk 2; it can only *remove* an ep square
//! from the exported FEN, never add one, and does not affect internal state,
//! perft, or the round-trip property (which compares `Position`s, not text).
//! This limitation is asserted by a test and tracked for chunk 2.

use crate::bitboard::BitIter;
use crate::castling::CastlingRights;
use crate::fen::FenError;
use crate::types::{Color, Piece, PieceType, Square};

/// Complete game state (everything FEN carries — ENGINE-CORE §3.3).
#[derive(Debug, Clone)]
pub struct Position {
    /// 12 piece bitboards, indexed by [`Piece::bb_index`]
    /// (`white_pawns=0 … black_king=11`).
    bitboards: [u64; 12],
    /// Occupancy by color: `[white, black]`. Derived aggregate kept in sync.
    color_occ: [u64; 2],
    /// Redundant piece-on-square array (§3.2) for O(1) square lookup. Must
    /// never disagree with `bitboards` — asserted by [`Position::debug_check`].
    mailbox: [Option<Piece>; 64],

    pub side_to_move: Color,
    pub castling_rights: CastlingRights,
    /// Lenient ep target: the square a pawn may capture *to*, set after any
    /// double push (OPEN-D internal convention). `None` otherwise.
    pub ep_target: Option<Square>,
    /// Plies since the last capture or pawn move (50-/75-move rule).
    pub halfmove_clock: u16,
    /// Starts at 1, increments after Black's move.
    pub fullmove_number: u32,
}

impl Position {
    /// An empty board, White to move, no rights/ep, clocks at 0/1.
    #[must_use]
    pub fn empty() -> Self {
        Position {
            bitboards: [0; 12],
            color_occ: [0; 2],
            mailbox: [None; 64],
            side_to_move: Color::White,
            castling_rights: CastlingRights::empty(),
            ep_target: None,
            halfmove_clock: 0,
            fullmove_number: 1,
        }
    }

    /// The Standard starting position.
    #[must_use]
    pub fn startpos() -> Self {
        // Unwrap is safe: the start FEN is a compile-time constant and valid.
        Self::from_fen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1")
            .expect("start FEN is valid")
    }

    // --- piece placement primitives (keep bitboards + mailbox + occ in sync) ---

    /// Place `piece` on `sq`. Precondition (debug-asserted): `sq` is empty.
    pub fn put_piece(&mut self, sq: Square, piece: Piece) {
        debug_assert!(
            self.mailbox[sq.index() as usize].is_none(),
            "put_piece onto occupied square {sq}"
        );
        let mask = sq.bb();
        self.bitboards[piece.bb_index()] |= mask;
        self.color_occ[piece.color.index()] |= mask;
        self.mailbox[sq.index() as usize] = Some(piece);
    }

    /// Remove and return whatever occupies `sq` (`None` if empty).
    pub fn take_piece(&mut self, sq: Square) -> Option<Piece> {
        let piece = self.mailbox[sq.index() as usize]?;
        let mask = !sq.bb();
        self.bitboards[piece.bb_index()] &= mask;
        self.color_occ[piece.color.index()] &= mask;
        self.mailbox[sq.index() as usize] = None;
        Some(piece)
    }

    // --- accessors ---

    /// What is on `sq` (O(1) via the mailbox).
    #[inline]
    #[must_use]
    pub fn piece_at(&self, sq: Square) -> Option<Piece> {
        self.mailbox[sq.index() as usize]
    }

    /// Bitboard for one colored piece type.
    #[inline]
    #[must_use]
    pub fn piece_bb(&self, piece: Piece) -> u64 {
        self.bitboards[piece.bb_index()]
    }

    /// Occupancy for one color.
    #[inline]
    #[must_use]
    pub fn color_bb(&self, color: Color) -> u64 {
        self.color_occ[color.index()]
    }

    /// All occupied squares (both colors).
    #[inline]
    #[must_use]
    pub fn all_occ(&self) -> u64 {
        self.color_occ[0] | self.color_occ[1]
    }

    /// The square of `color`'s king, if present.
    #[must_use]
    pub fn king_square(&self, color: Color) -> Option<Square> {
        let bb = self.piece_bb(Piece::new(color, PieceType::King));
        BitIter::new(bb).next()
    }

    // --- strict exported-ep helper (OPEN-D) ---

    /// The **effective** (strict / exported) en-passant square: the lenient
    /// `ep_target` iff a capture to it is actually available, else `None`
    /// (ENGINE-CORE §3.3 OPEN-D). This is both what `to_fen` emits and the ep
    /// component of position identity for repetition (§3.7 / E-11) — the spec
    /// states repetition keys on EP *availability*, "exactly what the strict /
    /// exported form would show." Position equality (`PartialEq`) therefore
    /// compares this, not the raw lenient `ep_target`, which is what makes the
    /// round-trip property `parse_fen(to_fen(p)) == p` hold.
    #[inline]
    #[must_use]
    pub fn effective_ep(&self) -> Option<Square> {
        if self.ep_capture_available() {
            self.ep_target
        } else {
            None
        }
    }

    /// Whether the side to move has a pawn pseudo-legally able to capture to the
    /// current (lenient) `ep_target`. Drives the *strict* exported FEN (§3.3).
    ///
    /// Pseudo-legal only (adjacency); the pin-aware refinement is chunk 2 (see
    /// the module doc). Returns `false` when there is no ep target.
    #[must_use]
    pub fn ep_capture_available(&self) -> bool {
        let Some(ep) = self.ep_target else {
            return false;
        };
        let mover = self.side_to_move;
        // A pawn capturing *to* `ep` sits one rank behind `ep` (from the mover's
        // perspective) on an adjacent file. White captures upward, so its pawn
        // is on rank-1 below ep; Black's is on rank+1 above ep.
        let ep_file = ep.file();
        let ep_rank = ep.rank();
        let pawn_rank = match mover {
            Color::White => ep_rank.checked_sub(1),
            Color::Black => ep_rank.checked_add(1),
        };
        let Some(pawn_rank) = pawn_rank else {
            return false;
        };
        let mover_pawn = Piece::new(mover, PieceType::Pawn);
        for df in [-1i8, 1i8] {
            let f = ep_file as i8 + df;
            if !(0..8).contains(&f) {
                continue;
            }
            if let Some(sq) = Square::from_file_rank(f as u8, pawn_rank) {
                if self.piece_at(sq) == Some(mover_pawn) {
                    return true;
                }
            }
        }
        false
    }

    // --- FEN (delegates to the fen module, kept here for the inherent API) ---

    /// Parse a FEN string into a `Position` (ENGINE-CORE §4.1, §3.3).
    pub fn from_fen(s: &str) -> Result<Self, FenError> {
        crate::fen::parse_fen(s)
    }

    /// Emit the FEN for this position — **strict ep** per the OPEN-D hybrid
    /// (ENGINE-CORE §3.3, §4.1).
    #[must_use]
    pub fn to_fen(&self) -> String {
        crate::fen::to_fen(self)
    }

    // --- debug invariant (§3.2: bitboards and mailbox must never disagree) ---

    /// Debug-build consistency check: every mailbox entry agrees with the
    /// bitboards and the derived occupancy aggregates (ENGINE-CORE §3.2). Panics
    /// on divergence. No-op semantics in release (callers gate on `debug_assert`).
    pub fn debug_check(&self) {
        let mut recomputed_color = [0u64; 2];
        let mut recomputed_pieces = [0u64; 12];
        for i in 0..64u8 {
            let sq = Square::new(i).expect("i < 64");
            if let Some(p) = self.mailbox[i as usize] {
                recomputed_color[p.color.index()] |= sq.bb();
                recomputed_pieces[p.bb_index()] |= sq.bb();
            }
        }
        assert_eq!(
            recomputed_pieces, self.bitboards,
            "bitboard/mailbox divergence (pieces)"
        );
        assert_eq!(
            recomputed_color, self.color_occ,
            "bitboard/mailbox divergence (color occupancy)"
        );
    }
}

/// Position equality is **full state equality** over exactly the identity
/// fields (ENGINE-CORE §3.3, §3.7): piece placement, side to move, castling
/// rights, en-passant *availability*, and the clocks. The redundant occupancy
/// aggregate is a pure function of `bitboards`, so it need not be compared.
/// This is the `==` the FEN round-trip property test asserts
/// (`parse_fen(to_fen(p)) == p`, §8.6).
///
/// EP is compared via [`Position::effective_ep`] — the strict / exported form,
/// i.e. the square only when a capture to it is available — **not** the raw
/// lenient `ep_target`. The spec (§3.3, §3.7) keys repetition identity on EP
/// availability for exactly this reason: `to_fen` emits the strict square and
/// `parse_fen` stores it, so comparing the lenient field would spuriously break
/// the round-trip whenever a lenient ep square has no legal capturer.
impl PartialEq for Position {
    fn eq(&self, other: &Self) -> bool {
        self.bitboards == other.bitboards
            && self.side_to_move == other.side_to_move
            && self.castling_rights == other.castling_rights
            && self.effective_ep() == other.effective_ep()
            && self.halfmove_clock == other.halfmove_clock
            && self.fullmove_number == other.fullmove_number
    }
}

impl Eq for Position {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn startpos_has_32_pieces_and_consistent_state() {
        let p = Position::startpos();
        p.debug_check();
        assert_eq!(p.all_occ().count_ones(), 32);
        assert_eq!(p.side_to_move, Color::White);
        assert_eq!(p.castling_rights, CastlingRights::all());
        assert_eq!(p.ep_target, None);
        assert_eq!(p.halfmove_clock, 0);
        assert_eq!(p.fullmove_number, 1);
    }

    #[test]
    fn kings_located() {
        let p = Position::startpos();
        assert_eq!(p.king_square(Color::White), Square::from_coord("e1"));
        assert_eq!(p.king_square(Color::Black), Square::from_coord("e8"));
    }

    #[test]
    fn put_take_roundtrip_keeps_invariant() {
        let mut p = Position::empty();
        let e4 = Square::from_coord("e4").unwrap();
        let wp = Piece::new(Color::White, PieceType::Pawn);
        p.put_piece(e4, wp);
        p.debug_check();
        assert_eq!(p.piece_at(e4), Some(wp));
        assert_eq!(p.take_piece(e4), Some(wp));
        assert_eq!(p.piece_at(e4), None);
        p.debug_check();
    }

    #[test]
    fn ep_available_true_when_adjacent_enemy_pawn() {
        // After 1. e4, Black to move: no ep (White just pushed, Black's turn,
        // ep target would be from a Black double push). Build the classic case:
        // White pawn e5, Black just played d7-d5 → ep target d6, White to move,
        // White pawn on e5 is adjacent → ep available.
        let p = Position::from_fen("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3")
            .unwrap();
        assert_eq!(p.ep_target, Square::from_coord("d6"));
        assert!(p.ep_capture_available());
    }

    #[test]
    fn ep_available_false_when_no_capturing_pawn() {
        // Lenient internal ep set, but no White pawn adjacent to capture it.
        let p = Position::from_fen("rnbqkbnr/ppp1pppp/8/3p4/8/8/PPPPPPPP/RNBQKBNR w KQkq d6 0 3")
            .unwrap();
        assert_eq!(p.ep_target, Square::from_coord("d6"));
        assert!(!p.ep_capture_available());
    }
}
