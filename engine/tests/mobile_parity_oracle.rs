//! §8.8 mobile-parity ORACLE (ENGINE-CORE §8.8, NFR-10, ADR-9, chunk 3 stage 3d).
//!
//! The mobile parity requirement is that the SAME Rust engine, compiled into the
//! iOS (Swift/UniFFI) and Android (Kotlin/UniFFI) client cores, produces
//! **byte-identical** results to the server. This file is the **host-side
//! oracle**: it pins the exact values the mobile test targets must reproduce.
//! The platform tests (under `platform/mobile/`) call the same `pgc-engine-ffi`
//! functions and assert against these constants; because it is one deterministic
//! Rust engine behind all three (§1.4.1), parity is correctness-by-construction
//! and this test is what makes a divergence detectable.
//!
//! The slice is deliberately small (§8.8: "perft startpos d5 + a golden slice")
//! so it runs fast in a mobile unit-test target.

use pgc_engine::{make, perft, san_to_move, Position};

/// The §8.8 mobile-parity perft anchor: startpos depth 5.
const STARTPOS_PERFT_D5: u64 = 4_865_609;

/// The golden slice replayed on each platform (Scholar's mate — short, hits a
/// capture, a check, and mate). Its final FEN is the parity anchor.
const GOLDEN_SLICE_SANS: [&str; 7] = ["e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#"];
const GOLDEN_SLICE_FINAL_FEN: &str =
    "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4";

#[test]
fn oracle_startpos_perft_d5() {
    assert_eq!(perft(&mut Position::startpos(), 5), STARTPOS_PERFT_D5);
}

#[test]
fn oracle_golden_slice_final_fen() {
    let mut pos = Position::startpos();
    for san in GOLDEN_SLICE_SANS {
        let m = san_to_move(&pos, san).expect("golden-slice SAN legal");
        make(&mut pos, m);
    }
    assert_eq!(pos.to_fen(), GOLDEN_SLICE_FINAL_FEN);
}

/// Documents, in code, the exact constants the mobile targets assert against,
/// so a change here is a deliberate, reviewed update to the parity contract.
#[test]
fn parity_contract_constants_are_stable() {
    assert_eq!(STARTPOS_PERFT_D5, 4_865_609);
    assert_eq!(GOLDEN_SLICE_SANS.len(), 7);
    assert!(GOLDEN_SLICE_FINAL_FEN.ends_with(" b KQkq - 0 4"));
}
