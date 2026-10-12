//! SAN / UCI round-trip property over reachable positions (ENGINE-CORE §8.6,
//! chunk 2 stage 2e). Replaying random legal moves reaches positions with real
//! captures, promotions, checks, and disambiguation — a far stronger exercise
//! of `move_to_san` / `san_to_move` than any single hand-built FEN.

use pgc_engine::{legal_moves, make, move_to_san, move_to_uci, san_to_move, uci_to_move, Position};
use proptest::prelude::*;

prop_compose! {
    fn reachable(max_plies: usize)(seed in any::<u64>()) -> Position {
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
                break;
            }
            let m = moves.as_slice()[(next() as usize) % moves.len()];
            make(&mut pos, m);
        }
        pos
    }
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(200))]

    /// For every legal move of a reachable position, `san_to_move(move_to_san(m))
    /// == m` and `uci_to_move(move_to_uci(m)) == m` (§8.6, §4.2 round-trip).
    #[test]
    fn san_and_uci_roundtrip(pos in reachable(50)) {
        for m in legal_moves(&pos).iter().copied() {
            let san = move_to_san(&pos, m);
            prop_assert_eq!(san_to_move(&pos, &san), Ok(m), "SAN round-trip failed: {}", san);

            let uci = move_to_uci(m);
            prop_assert_eq!(uci_to_move(&pos, &uci), Ok(m), "UCI round-trip failed: {}", uci);
        }
    }

    /// SAN is never ambiguous for engine-generated text: parsing our own SAN
    /// yields exactly one move (no `Ambiguous` error), i.e. disambiguation is
    /// always sufficient.
    #[test]
    fn generated_san_is_never_ambiguous(pos in reachable(50)) {
        for m in legal_moves(&pos).iter().copied() {
            let san = move_to_san(&pos, m);
            prop_assert!(san_to_move(&pos, &san).is_ok(), "ambiguous/invalid SAN: {}", san);
        }
    }
}
