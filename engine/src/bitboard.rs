//! Thin bitboard helpers over `u64` (LERF layout — bit `i` = square `i`,
//! ENGINE-CORE §3.2). Kept minimal for this chunk (board representation, FEN,
//! and the perft skeleton); the attack-table / magic-bitboard machinery arrives
//! with full move generation in chunk 2.

use crate::types::Square;

/// Iterate the set squares of a bitboard, least-significant bit first.
///
/// Yields each `Square` whose bit is set. Non-allocating.
#[derive(Debug)]
pub struct BitIter(u64);

impl BitIter {
    #[inline]
    #[must_use]
    pub const fn new(bb: u64) -> Self {
        BitIter(bb)
    }
}

impl Iterator for BitIter {
    type Item = Square;

    #[inline]
    fn next(&mut self) -> Option<Square> {
        if self.0 == 0 {
            return None;
        }
        let idx = self.0.trailing_zeros() as u8;
        // Clear the lowest set bit.
        self.0 &= self.0 - 1;
        // Safe: idx < 64 because self.0 != 0 so trailing_zeros() < 64.
        Square::new(idx)
    }
}

/// Count the set bits (population count).
#[inline]
#[must_use]
pub const fn popcount(bb: u64) -> u32 {
    bb.count_ones()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bit_iter_yields_squares_ascending() {
        let a1 = Square::new(0).unwrap();
        let e4 = Square::from_coord("e4").unwrap();
        let h8 = Square::new(63).unwrap();
        let bb = a1.bb() | e4.bb() | h8.bb();
        let got: Vec<Square> = BitIter::new(bb).collect();
        assert_eq!(got, vec![a1, e4, h8]);
        assert_eq!(popcount(bb), 3);
    }

    #[test]
    fn bit_iter_empty() {
        assert_eq!(BitIter::new(0).count(), 0);
    }
}
