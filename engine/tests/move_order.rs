//! Pins the `legal_moves` order (ENGINE-CORE §3.8). The order is the
//! generator's emission order, not a sort: piece type (P, N, B, R, Q, K), then
//! from-square ascending (a1 = 0), then each piece's fixed delta/direction
//! table (sliders nearest square first), with promotions Q, R, B, N. Any change
//! to move generation that reorders moves fails here, so the change is
//! deliberate and this golden is updated in the same PR.

use pgc_engine::{legal_moves, move_to_uci, Position};

fn uci_order(fen: &str) -> String {
    let p = Position::from_fen(fen).expect("valid FEN");
    legal_moves(&p)
        .iter()
        .map(|&m| move_to_uci(m))
        .collect::<Vec<_>>()
        .join(" ")
}

#[test]
fn startpos_order_is_pinned() {
    assert_eq!(
        uci_order("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"),
        "a2a3 a2a4 b2b3 b2b4 c2c3 c2c4 d2d3 d2d4 e2e3 e2e4 f2f3 f2f4 g2g3 g2g4 \
         h2h3 h2h4 b1c3 b1a3 g1h3 g1f3"
    );
}

#[test]
fn kiwipete_order_is_pinned() {
    // Covers every piece type, captures, and both castles (emitted last, after
    // the king's steps).
    assert_eq!(
        uci_order("r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1"),
        "a2a3 a2a4 b2b3 g2g3 g2g4 g2h3 d5d6 d5e6 c3d1 c3b1 c3a4 c3b5 e5f7 e5g6 \
         e5g4 e5d3 e5c4 e5c6 e5d7 d2e3 d2f4 d2g5 d2h6 d2c1 e2f1 e2d3 e2c4 e2b5 \
         e2a6 e2d1 a1b1 a1c1 a1d1 h1g1 h1f1 f3g4 f3h5 f3f4 f3f5 f3f6 f3g3 f3h3 \
         f3e3 f3d3 e1f1 e1d1 e1g1 e1c1"
    );
}

#[test]
fn promotion_order_is_pinned() {
    // Black to move: push and capture promotions, each expanded Q, R, B, N.
    assert_eq!(
        uci_order("n1n5/PPPk4/8/8/8/8/4Kppp/5N1N b - - 0 1"),
        "g2g1q g2g1r g2g1b g2g1n g2f1q g2f1r g2f1b g2f1n g2h1q g2h1r g2h1b g2h1n \
         a8c7 a8b6 c8e7 c8d6 c8b6 c8a7 d7e8 d7e7 d7e6 d7d6 d7c6 d7c7"
    );
}
