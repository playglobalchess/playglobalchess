//! Core value types: colors, piece types, pieces, and squares.
//!
//! Square indexing is **LERF** (little-endian rank-file): `0 = a1 … 63 = h8`
//! (ENGINE-CORE §3.2). This is fixed platform-wide so SAN/UCI/FEN and the
//! future magic-bitboard tables all agree.

use core::fmt;

/// Side to move / piece owner (ENGINE-CORE §3.4 — the engine is color-centric;
/// seat mapping is S2/S3's concern).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Color {
    White,
    Black,
}

impl Color {
    /// The opposing color.
    #[inline]
    #[must_use]
    pub const fn flip(self) -> Self {
        match self {
            Color::White => Color::Black,
            Color::Black => Color::White,
        }
    }

    /// Dense index 0=White, 1=Black for array-keyed tables.
    #[inline]
    #[must_use]
    pub const fn index(self) -> usize {
        self as usize
    }
}

/// The six Standard piece types.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum PieceType {
    Pawn,
    Knight,
    Bishop,
    Rook,
    Queen,
    King,
}

impl PieceType {
    /// Dense index 0..=5, used to key the 12 piece bitboards.
    #[inline]
    #[must_use]
    pub const fn index(self) -> usize {
        self as usize
    }

    /// Lowercase FEN/SAN letter for this piece type (color-agnostic).
    #[inline]
    #[must_use]
    pub const fn to_char_lower(self) -> char {
        match self {
            PieceType::Pawn => 'p',
            PieceType::Knight => 'n',
            PieceType::Bishop => 'b',
            PieceType::Rook => 'r',
            PieceType::Queen => 'q',
            PieceType::King => 'k',
        }
    }

    /// Parse a piece type from its (case-insensitive) FEN/SAN letter.
    #[inline]
    #[must_use]
    pub const fn from_char(c: char) -> Option<Self> {
        match c.to_ascii_lowercase() {
            'p' => Some(PieceType::Pawn),
            'n' => Some(PieceType::Knight),
            'b' => Some(PieceType::Bishop),
            'r' => Some(PieceType::Rook),
            'q' => Some(PieceType::Queen),
            'k' => Some(PieceType::King),
            _ => None,
        }
    }

    /// All piece types, in bitboard-index order.
    pub const ALL: [PieceType; 6] = [
        PieceType::Pawn,
        PieceType::Knight,
        PieceType::Bishop,
        PieceType::Rook,
        PieceType::Queen,
        PieceType::King,
    ];
}

/// A colored piece.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Piece {
    pub color: Color,
    pub kind: PieceType,
}

impl Piece {
    #[inline]
    #[must_use]
    pub const fn new(color: Color, kind: PieceType) -> Self {
        Self { color, kind }
    }

    /// FEN letter: uppercase for White, lowercase for Black.
    #[inline]
    #[must_use]
    pub const fn to_fen_char(self) -> char {
        let lower = self.kind.to_char_lower();
        match self.color {
            Color::White => lower.to_ascii_uppercase(),
            Color::Black => lower,
        }
    }

    /// Parse a FEN piece letter (`'P'..'K'` white, `'p'..'k'` black).
    #[inline]
    #[must_use]
    pub const fn from_fen_char(c: char) -> Option<Self> {
        let kind = match PieceType::from_char(c) {
            Some(k) => k,
            None => return None,
        };
        let color = if c.is_ascii_uppercase() {
            Color::White
        } else {
            Color::Black
        };
        Some(Piece::new(color, kind))
    }

    /// Dense index 0..=11 into the piece-bitboard array: color-major
    /// (`white_pawns=0 … black_king=11`), matching ENGINE-CORE §3.2.
    #[inline]
    #[must_use]
    pub const fn bb_index(self) -> usize {
        self.color.index() * 6 + self.kind.index()
    }
}

/// A board square, `0 = a1 … 63 = h8` (LERF, ENGINE-CORE §3.2).
///
/// Invariant: the wrapped index is always in `0..64`. The only constructors
/// enforce this, so every `Square` is valid by construction.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Square(u8);

impl Square {
    /// Construct from a raw 0..63 index. Returns `None` if out of range.
    #[inline]
    #[must_use]
    pub const fn new(index: u8) -> Option<Self> {
        if index < 64 {
            Some(Square(index))
        } else {
            None
        }
    }

    /// Construct from file (0=a..7=h) and rank (0=rank1..7=rank8) under LERF:
    /// `index = rank * 8 + file`.
    #[inline]
    #[must_use]
    pub const fn from_file_rank(file: u8, rank: u8) -> Option<Self> {
        if file < 8 && rank < 8 {
            Some(Square(rank * 8 + file))
        } else {
            None
        }
    }

    /// Raw 0..63 index.
    #[inline]
    #[must_use]
    pub const fn index(self) -> u8 {
        self.0
    }

    /// File 0=a..7=h.
    #[inline]
    #[must_use]
    pub const fn file(self) -> u8 {
        self.0 % 8
    }

    /// Rank 0=rank1..7=rank8.
    #[inline]
    #[must_use]
    pub const fn rank(self) -> u8 {
        self.0 / 8
    }

    /// The single-bit `u64` mask for this square (LERF bit = square index).
    #[inline]
    #[must_use]
    pub const fn bb(self) -> u64 {
        1u64 << self.0
    }

    /// Parse algebraic coordinate like `"e4"` (file letter + rank digit).
    #[must_use]
    pub fn from_coord(s: &str) -> Option<Self> {
        let bytes = s.as_bytes();
        if bytes.len() != 2 {
            return None;
        }
        let file = bytes[0];
        let rank = bytes[1];
        if !(b'a'..=b'h').contains(&file) || !(b'1'..=b'8').contains(&rank) {
            return None;
        }
        Square::from_file_rank(file - b'a', rank - b'1')
    }
}

impl fmt::Display for Square {
    /// Algebraic coordinate, e.g. `e4`.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let file = (b'a' + self.file()) as char;
        let rank = (b'1' + self.rank()) as char;
        write!(f, "{file}{rank}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lerf_corners() {
        // a1 = 0, h1 = 7, a8 = 56, h8 = 63 (LERF).
        assert_eq!(Square::from_file_rank(0, 0).unwrap().index(), 0);
        assert_eq!(Square::from_file_rank(7, 0).unwrap().index(), 7);
        assert_eq!(Square::from_file_rank(0, 7).unwrap().index(), 56);
        assert_eq!(Square::from_file_rank(7, 7).unwrap().index(), 63);
    }

    #[test]
    fn square_roundtrip_coord() {
        for i in 0..64u8 {
            let sq = Square::new(i).unwrap();
            let s = sq.to_string();
            assert_eq!(Square::from_coord(&s).unwrap(), sq);
        }
    }

    #[test]
    fn out_of_range_rejected() {
        assert_eq!(Square::new(64), None);
        assert_eq!(Square::from_file_rank(8, 0), None);
        assert_eq!(Square::from_coord("i1"), None);
        assert_eq!(Square::from_coord("a9"), None);
        assert_eq!(Square::from_coord("e"), None);
    }

    #[test]
    fn piece_fen_char_roundtrip() {
        for &color in &[Color::White, Color::Black] {
            for &kind in &PieceType::ALL {
                let p = Piece::new(color, kind);
                assert_eq!(Piece::from_fen_char(p.to_fen_char()), Some(p));
            }
        }
    }

    #[test]
    fn bb_index_is_dense_0_to_11() {
        let mut seen = [false; 12];
        for &color in &[Color::White, Color::Black] {
            for &kind in &PieceType::ALL {
                let idx = Piece::new(color, kind).bb_index();
                assert!(idx < 12);
                assert!(!seen[idx], "duplicate bb_index {idx}");
                seen[idx] = true;
            }
        }
        assert!(seen.iter().all(|&b| b));
    }
}
