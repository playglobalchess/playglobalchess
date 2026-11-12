//! §8.6 property-based test net — the fast per-commit invariants (ENGINE-CORE
//! §8.6, chunk 2 stage 2f). This is the **single home** for the spec's property
//! layer; it supersedes the copies that grew up in `gate.rs` and the old
//! `san_roundtrip.rs` during chunks 1–2e.
//!
//! §8.6 names five properties, each asserted below over the SAME shared
//! `reachable_position` generator (random legal play from the start position —
//! every generated position is reachable and self-consistent, a stronger
//! generator than random piece scatter):
//!
//! 1. `unmake(make(p,m)) == p` — make/unmake is a perfect inverse (§3.6).
//! 2. `uci_to_move(move_to_uci(m)) == m` — UCI round-trip (§4.2).
//! 3. `san_to_move(move_to_san(m)) == m` — SAN round-trip (§4.2).
//! 4. `parse_fen(to_fen(p)) == p` — FEN round-trip (§3.3).
//! 5. `perft(p,1) == legal_moves(p).len()` — the cheap perft cross-check that
//!    catches most generation bugs before the slow §8.1 corpus runs.
//!
//! (The sixth §8.6 bullet — pseudo-legal set == pin-aware set — is N/A: there is
//! only one generator, no separate pin-aware fast path to cross-check. It
//! becomes live if/when a pin-aware generator is added as an optimization.)

use pgc_engine::{
    is_legal, legal_moves, make, move_to_san, move_to_uci, parse_fen, perft, san_to_move,
    uci_to_move, unmake, Move, Position, Square,
};
use proptest::prelude::*;

prop_compose! {
    /// A position reached by replaying up to `max_plies` random legal moves
    /// from the start. Deterministic xorshift PRNG from the proptest seed so a
    /// failure shrinks and replays reproducibly.
    fn reachable_position(max_plies: usize)(seed in any::<u64>()) -> Position {
        let mut state = seed | 1;
        let mut next = || {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            state
        };
        let plies = (next() as usize) % (max_plies + 1);
        let mut pos = Position::startpos();
        for _ in 0..plies {
            let moves = legal_moves(&pos);
            if moves.is_empty() {
                break; // checkmate or stalemate reached
            }
            let m = moves.as_slice()[(next() as usize) % moves.len()];
            make(&mut pos, m);
        }
        pos
    }
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(256))]

    /// §8.6(1) — make/unmake inverse. For every legal move of a reachable
    /// position, `unmake(make(p,m))` restores `p` bit-for-bit (§3.6).
    #[test]
    fn prop_make_unmake_is_inverse(pos in reachable_position(40)) {
        let before = pos.clone();
        let mut p = pos;
        for &m in legal_moves(&before).iter() {
            let undo = make(&mut p, m);
            unmake(&mut p, m, undo);
            prop_assert_eq!(&p, &before, "make/unmake not inverse for {}", m.to_uci());
        }
    }

    /// §8.6(2,3) — UCI and SAN round-trips. For every legal move,
    /// `uci_to_move(move_to_uci(m)) == m` and `san_to_move(move_to_san(m)) == m`
    /// (§4.2).
    #[test]
    fn prop_uci_and_san_roundtrip(pos in reachable_position(50)) {
        for m in legal_moves(&pos).iter().copied() {
            let uci = move_to_uci(m);
            prop_assert_eq!(uci_to_move(&pos, &uci), Ok(m), "UCI round-trip: {}", uci);

            let san = move_to_san(&pos, m);
            prop_assert_eq!(san_to_move(&pos, &san), Ok(m), "SAN round-trip: {}", san);
        }
    }

    /// §8.6(4) — FEN round-trip. `parse_fen(to_fen(p)) == p` (§3.3). (EP text may
    /// change under the OPEN-D strict-export rule, but the parsed `Position` is
    /// equal — equality keys ep on availability.)
    #[test]
    fn prop_fen_roundtrip(pos in reachable_position(40)) {
        let text = pos.to_fen();
        let reparsed = parse_fen(&text).expect("emitted FEN must re-parse");
        prop_assert_eq!(reparsed, pos);
    }

    /// §8.6(5) — the cheap perft cross-check: `perft(p,1) == legal_moves(p).len()`
    /// over a large random-position sample.
    #[test]
    fn prop_perft_1_equals_legal_move_count(pos in reachable_position(30)) {
        let n = legal_moves(&pos).len() as u64;
        let mut p = pos;
        prop_assert_eq!(perft(&mut p, 1), n);
    }

    /// `is_legal(p, m)` must agree with `legal_moves(p).contains(m)` for ALL
    /// moves (§4.1): true for exactly the generated set, false for everything
    /// else. We check every legal move (must be is_legal) and a sweep of all
    /// 64×64 from/to pairs (is_legal iff in the legal set) — so a from an empty
    /// square, an enemy piece, or a non-generated destination is all rejected.
    #[test]
    fn prop_is_legal_agrees_with_legal_moves(pos in reachable_position(40)) {
        let legal = legal_moves(&pos);
        let in_set = |m: Move| legal.iter().any(|&l| l == m);

        for &m in legal.iter() {
            prop_assert!(is_legal(&pos, m), "legal move {} rejected by is_legal", m.to_uci());
        }
        // Sweep a sample of arbitrary from/to pairs; is_legal must equal set
        // membership. (Promotion moves carry promotion=None here, matching how
        // non-promoting generated moves compare; a promotion in the legal set
        // with promotion=None is never produced, so equality still holds.)
        for from in 0..64u8 {
            for to in 0..64u8 {
                let m = Move::new(Square::new(from).unwrap(), Square::new(to).unwrap());
                prop_assert_eq!(
                    is_legal(&pos, m),
                    in_set(m),
                    "is_legal disagrees with legal set for {}",
                    m.to_uci()
                );
            }
        }
    }
}

/// Does `pos` have a legal en-passant capture? From the legal move list itself:
/// a pawn move onto the ep target that changes file (onto an empty square).
fn has_legal_ep_capture(pos: &Position) -> bool {
    let Some(ep) = pos.ep_target else {
        return false;
    };
    legal_moves(pos).iter().any(|m| {
        m.to == ep
            && m.from.file() != m.to.file()
            && pos.piece_at(m.from).map(|p| p.kind) == Some(pgc_engine::PieceType::Pawn)
    })
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(64))]

    /// #119 — strict ep availability (exported FEN, Zobrist key, equality)
    /// agrees with move generation at EVERY ply of a random game: the ep square
    /// counts exactly when a legal ep capture exists.
    #[test]
    fn prop_ep_availability_matches_legal_moves(seed in any::<u64>()) {
        let mut state = seed | 1;
        let mut next = || {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            state
        };
        let mut pos = Position::startpos();
        for _ in 0..160 {
            prop_assert_eq!(
                pos.ep_capture_available(),
                has_legal_ep_capture(&pos),
                "ep availability disagrees with legal_moves at {}", pos.to_fen()
            );
            let moves = legal_moves(&pos);
            if moves.is_empty() {
                break;
            }
            let m = moves.as_slice()[(next() as usize) % moves.len()];
            make(&mut pos, m);
        }
    }
}
