//! Move representation and UCI encode/decode (ENGINE-CORE §4.2).
//!
//! A `Move` is a from/to square pair plus an optional promotion piece. Castling
//! is encoded king-to-destination (`e1g1`), matching UCI; en passant is a plain
//! pawn move whose legality the generator enforces. SAN (§4.2) is deferred to a
//! later chunk — this chunk needs only UCI, which the perft harness and the
//! round-trip property test use.

use crate::types::{PieceType, Square};
use core::fmt;

/// A pseudo-legal or legal move: `from -> to`, with optional promotion.
///
/// The move does not record *what kind* of move it is (capture, castle, ep) —
/// that is derived from the `Position` it is applied to. This keeps `Move` a
/// small, `Copy` wire value (the UCI form the server stores, §4.2).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Move {
    pub from: Square,
    pub to: Square,
    /// Promotion target (Knight/Bishop/Rook/Queen) when a pawn reaches the last
    /// rank; `None` otherwise. Promotion to pawn or king is never constructed.
    pub promotion: Option<PieceType>,
}

impl Move {
    #[inline]
    #[must_use]
    pub const fn new(from: Square, to: Square) -> Self {
        Self {
            from,
            to,
            promotion: None,
        }
    }

    #[inline]
    #[must_use]
    pub const fn with_promotion(from: Square, to: Square, promotion: PieceType) -> Self {
        Self {
            from,
            to,
            promotion: Some(promotion),
        }
    }

    /// UCI long-algebraic form: `"e2e4"`, `"e7e8q"`, `"e1g1"` (castling).
    #[must_use]
    pub fn to_uci(self) -> String {
        let mut s = String::with_capacity(5);
        s.push_str(&self.from.to_string());
        s.push_str(&self.to.to_string());
        if let Some(p) = self.promotion {
            s.push(p.to_char_lower());
        }
        s
    }

    /// Parse a UCI move string **structurally** (squares + optional promotion
    /// letter). This does NOT check legality or that a promotion is appropriate
    /// — that is the move generator / `is_legal`'s job (chunk 2). Promotion to
    /// pawn/king is rejected here as malformed.
    #[must_use]
    pub fn from_uci(s: &str) -> Option<Self> {
        if s.len() != 4 && s.len() != 5 {
            return None;
        }
        let from = Square::from_coord(&s[0..2])?;
        let to = Square::from_coord(&s[2..4])?;
        let promotion = if s.len() == 5 {
            let c = s.as_bytes()[4] as char;
            let pt = PieceType::from_char(c)?;
            match pt {
                PieceType::Pawn | PieceType::King => return None,
                _ => Some(pt),
            }
        } else {
            None
        };
        Some(Move {
            from,
            to,
            promotion,
        })
    }
}

impl fmt::Display for Move {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_uci())
    }
}

/// A fixed-capacity, non-allocating move list.
///
/// Chess positions have at most 218 legal moves (ENGINE-CORE E-14), so a
/// 256-slot inline buffer never overflows and keeps move-gen off the heap
/// (NFR-1, "zero allocation on the hot path", §6.1).
#[derive(Debug, Clone)]
pub struct MoveList {
    moves: [Move; Self::CAP],
    len: usize,
}

impl MoveList {
    const CAP: usize = 256;

    #[inline]
    #[must_use]
    pub fn new() -> Self {
        // A placeholder move (a1a1) fills unused slots; `len` bounds all access.
        let placeholder = Move::new(
            Square::new(0).expect("0 is a valid square"),
            Square::new(0).expect("0 is a valid square"),
        );
        MoveList {
            moves: [placeholder; Self::CAP],
            len: 0,
        }
    }

    #[inline]
    pub fn push(&mut self, m: Move) {
        debug_assert!(self.len < Self::CAP, "move list overflow (>256 moves)");
        self.moves[self.len] = m;
        self.len += 1;
    }

    #[inline]
    #[must_use]
    pub fn len(&self) -> usize {
        self.len
    }

    #[inline]
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    #[inline]
    #[must_use]
    pub fn as_slice(&self) -> &[Move] {
        &self.moves[..self.len]
    }

    /// Iterate the moves in insertion order.
    pub fn iter(&self) -> impl Iterator<Item = &Move> {
        self.as_slice().iter()
    }
}

impl Default for MoveList {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn uci_roundtrip_plain() {
        for uci in ["e2e4", "g1f3", "a7a5", "e1g1", "h2h1"] {
            let m = Move::from_uci(uci).unwrap();
            assert_eq!(m.to_uci(), uci);
        }
    }

    #[test]
    fn uci_roundtrip_promotion() {
        for uci in ["e7e8q", "a2a1n", "b7b8r", "c2c1b"] {
            let m = Move::from_uci(uci).unwrap();
            assert_eq!(m.to_uci(), uci);
            assert!(m.promotion.is_some());
        }
    }

    #[test]
    fn uci_rejects_malformed() {
        assert_eq!(Move::from_uci(""), None);
        assert_eq!(Move::from_uci("e2"), None);
        assert_eq!(Move::from_uci("e2e9"), None);
        assert_eq!(Move::from_uci("z2e4"), None);
        assert_eq!(Move::from_uci("e7e8k"), None); // promotion to king
        assert_eq!(Move::from_uci("e7e8p"), None); // promotion to pawn
        assert_eq!(Move::from_uci("e2e4e4"), None);
    }

    #[test]
    fn movelist_push_and_slice() {
        let mut ml = MoveList::new();
        assert!(ml.is_empty());
        ml.push(Move::from_uci("e2e4").unwrap());
        ml.push(Move::from_uci("d2d4").unwrap());
        assert_eq!(ml.len(), 2);
        assert_eq!(ml.as_slice()[0].to_uci(), "e2e4");
        assert_eq!(ml.iter().count(), 2);
    }
}
