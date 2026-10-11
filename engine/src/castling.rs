//! Castling-rights bitset — the `{WK, WQ, BK, BQ}` state FEN field 3 carries
//! (ENGINE-CORE §3.3). Part of position identity for repetition (§3.7, E-11).

use crate::types::Color;

/// The four castling rights packed into a `u8` (bits 0..=3).
///
/// Bit layout: `WK=1, WQ=2, BK=4, BQ=8`. The whole value participates in
/// position identity (ENGINE-CORE §3.7 / E-11), so it is `Eq`/`Hash`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Default)]
pub struct CastlingRights(u8);

impl CastlingRights {
    pub const WHITE_KING: u8 = 1 << 0;
    pub const WHITE_QUEEN: u8 = 1 << 1;
    pub const BLACK_KING: u8 = 1 << 2;
    pub const BLACK_QUEEN: u8 = 1 << 3;

    /// No castling rights.
    #[inline]
    #[must_use]
    pub const fn empty() -> Self {
        CastlingRights(0)
    }

    /// All four rights (the start position).
    #[inline]
    #[must_use]
    pub const fn all() -> Self {
        CastlingRights(Self::WHITE_KING | Self::WHITE_QUEEN | Self::BLACK_KING | Self::BLACK_QUEEN)
    }

    #[inline]
    #[must_use]
    pub const fn from_bits(bits: u8) -> Self {
        CastlingRights(bits & 0b1111)
    }

    #[inline]
    #[must_use]
    pub const fn bits(self) -> u8 {
        self.0
    }

    #[inline]
    #[must_use]
    pub const fn has(self, flag: u8) -> bool {
        self.0 & flag != 0
    }

    #[inline]
    pub fn add(&mut self, flag: u8) {
        self.0 |= flag;
    }

    #[inline]
    pub fn remove(&mut self, flag: u8) {
        self.0 &= !flag;
    }

    /// King-side right flag for a color.
    #[inline]
    #[must_use]
    pub const fn king_side(color: Color) -> u8 {
        match color {
            Color::White => Self::WHITE_KING,
            Color::Black => Self::BLACK_KING,
        }
    }

    /// Queen-side right flag for a color.
    #[inline]
    #[must_use]
    pub const fn queen_side(color: Color) -> u8 {
        match color {
            Color::White => Self::WHITE_QUEEN,
            Color::Black => Self::BLACK_QUEEN,
        }
    }

    /// FEN field-3 text: `"KQkq"`, or `"-"` when no rights remain
    /// (ENGINE-CORE §3.3). Order is fixed K, Q, k, q.
    #[must_use]
    pub fn to_fen(self) -> String {
        if self.0 == 0 {
            return "-".to_string();
        }
        let mut s = String::with_capacity(4);
        if self.has(Self::WHITE_KING) {
            s.push('K');
        }
        if self.has(Self::WHITE_QUEEN) {
            s.push('Q');
        }
        if self.has(Self::BLACK_KING) {
            s.push('k');
        }
        if self.has(Self::BLACK_QUEEN) {
            s.push('q');
        }
        s
    }

    /// Parse FEN field 3. `"-"` is empty; otherwise each of `KQkq` sets its
    /// flag. Returns `None` on an unknown character or a duplicate.
    #[must_use]
    pub fn from_fen(s: &str) -> Option<Self> {
        if s == "-" {
            return Some(Self::empty());
        }
        if s.is_empty() {
            return None;
        }
        let mut rights = Self::empty();
        for c in s.chars() {
            let flag = match c {
                'K' => Self::WHITE_KING,
                'Q' => Self::WHITE_QUEEN,
                'k' => Self::BLACK_KING,
                'q' => Self::BLACK_QUEEN,
                _ => return None,
            };
            if rights.has(flag) {
                return None; // duplicate char
            }
            rights.add(flag);
        }
        Some(rights)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn all_roundtrip() {
        let r = CastlingRights::all();
        assert_eq!(r.to_fen(), "KQkq");
        assert_eq!(CastlingRights::from_fen("KQkq"), Some(r));
    }

    #[test]
    fn empty_roundtrip() {
        let r = CastlingRights::empty();
        assert_eq!(r.to_fen(), "-");
        assert_eq!(CastlingRights::from_fen("-"), Some(r));
    }

    #[test]
    fn subset_roundtrip() {
        let mut r = CastlingRights::empty();
        r.add(CastlingRights::WHITE_KING);
        r.add(CastlingRights::BLACK_QUEEN);
        assert_eq!(r.to_fen(), "Kq");
        assert_eq!(CastlingRights::from_fen("Kq"), Some(r));
    }

    #[test]
    fn rejects_bad_fen() {
        assert_eq!(CastlingRights::from_fen(""), None);
        assert_eq!(CastlingRights::from_fen("X"), None);
        assert_eq!(CastlingRights::from_fen("KK"), None); // duplicate
    }

    #[test]
    fn remove_clears_flag() {
        let mut r = CastlingRights::all();
        r.remove(CastlingRights::WHITE_KING);
        assert!(!r.has(CastlingRights::WHITE_KING));
        assert!(r.has(CastlingRights::WHITE_QUEEN));
    }
}
