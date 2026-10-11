//! Perft harness (ENGINE-CORE §4.4) — the move-generation test/benchmark
//! surface.
//!
//! `perft(p, depth)` counts leaf nodes of the legal-move tree to `depth`;
//! `perft_divide` reports the per-root-move subtree counts, the primary tool
//! for localizing a move-gen bug during G-A bring-up (§4.4 — "required, not
//! optional").
//!
//! # Scope note (chunk 1)
//! This is the harness **skeleton**: the counting logic is complete and correct
//! against the current generator, and the gate `perft(startpos, 1) == 20` is
//! asserted here. The full §8.1 reference corpus (Kiwipete / Positions 3–6 at
//! depth 5–6, startpos to depth 6) is wired as a dedicated conformance suite in
//! chunk 2, once the generator is verified at depth ≥ 2. Perft counts move
//! paths, never game outcomes: it does **not** short-circuit on checkmate,
//! stalemate, or any draw (§8.1).

use crate::movegen::{legal_moves, make, unmake};
use crate::moves::Move;
use crate::position::Position;

/// Count leaf nodes of the legal move tree from `pos` to `depth` plies.
///
/// `perft(pos, 0) == 1` (the position itself is the single leaf). Draw/mate
/// detection is intentionally ignored — perft counts reachable move paths only.
///
/// Uses make/unmake (§3.6) on a single mutable position — the zero-allocation
/// hot path — rather than cloning per node. `pos` is restored exactly on
/// return (every `make` is paired with its `unmake`), so the `&mut` is an
/// internal optimization invisible to callers.
#[must_use]
pub fn perft(pos: &mut Position, depth: u32) -> u64 {
    if depth == 0 {
        return 1;
    }
    let moves = legal_moves(pos);
    // Depth 1: the node count is exactly the number of legal moves (the
    // `perft(p,1) == legal_moves(p).len()` cross-check, §8.6).
    if depth == 1 {
        return moves.len() as u64;
    }
    let mut nodes = 0u64;
    for &m in moves.iter() {
        let undo = make(pos, m);
        nodes += perft(pos, depth - 1);
        unmake(pos, m, undo);
    }
    nodes
}

/// Per-root-move perft breakdown (ENGINE-CORE §4.4). For each legal move from
/// `pos`, the number of leaf nodes under it at `depth`. `depth == 0` yields an
/// empty vector (no root moves counted). Used to localize mismatches.
///
/// Restores `pos` exactly on return (make/unmake paired per root move).
#[must_use]
pub fn perft_divide(pos: &mut Position, depth: u32) -> Vec<(Move, u64)> {
    if depth == 0 {
        return Vec::new();
    }
    let mut out = Vec::new();
    for &m in legal_moves(pos).iter() {
        let undo = make(pos, m);
        let count = perft(pos, depth - 1);
        unmake(pos, m, undo);
        out.push((m, count));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn perft_depth_0_is_one() {
        assert_eq!(perft(&mut Position::startpos(), 0), 1);
    }

    /// This chunk's gate: `perft(startpos, 1) == 20`.
    #[test]
    fn perft_startpos_depth_1_is_20() {
        assert_eq!(perft(&mut Position::startpos(), 1), 20);
    }

    #[test]
    fn perft_divide_sums_to_perft() {
        // At depth 1 each root move contributes exactly 1 leaf, so the divide
        // has 20 entries each counting 1, summing to perft(_,1) = 20.
        let mut p = Position::startpos();
        let divide = perft_divide(&mut p, 1);
        assert_eq!(divide.len(), 20);
        let sum: u64 = divide.iter().map(|(_, n)| n).sum();
        assert_eq!(sum, perft(&mut p, 1));
    }

    #[test]
    fn perft_divide_depth_0_is_empty() {
        assert!(perft_divide(&mut Position::startpos(), 0).is_empty());
    }

    /// make/unmake inside perft must leave the root position untouched (§3.6):
    /// a deeper perft run restores `pos` exactly, so a FEN snapshot is stable.
    #[test]
    fn perft_restores_root_position() {
        let mut p = Position::startpos();
        let before = p.to_fen();
        let _ = perft(&mut p, 3);
        assert_eq!(
            p.to_fen(),
            before,
            "perft did not restore the root position"
        );
        assert_eq!(p, Position::startpos());
    }
}
