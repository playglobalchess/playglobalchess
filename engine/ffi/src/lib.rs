//! UniFFI binding surface for the PlayGlobalChess engine (ADR-9: one engine,
//! three consumers). This crate adds **no chess logic** — every function
//! delegates to `pgc_engine`, so the Swift (iOS) and Kotlin (Android) clients
//! bind the exact same rules the server uses. The §8.8 mobile-parity test
//! drives these three functions and asserts byte-identical results against the
//! host oracle (see `engine/tests/mobile_parity_oracle.rs`).
//!
//! The surface is deliberately tiny: the parity gate only needs to reproduce
//! perft counts and FEN sequences. The full engine API (legal_moves, status,
//! SAN, …) is exposed incrementally as the client core (S5) needs it; this is
//! the parity slice, not the whole FFI contract.

#![forbid(unsafe_code)]

use pgc_engine::{make, san_to_move, Position};

uniffi::setup_scaffolding!();

/// The Standard starting position as FEN. Trivial, but it anchors the parity
/// test's starting point identically on every platform.
#[uniffi::export]
pub fn startpos_fen() -> String {
    Position::startpos().to_fen()
}

/// Perft (leaf-node count) from a FEN to `depth`. The parity test asserts this
/// equals the host oracle's count (e.g. startpos d5 = 4,865,609) byte-for-byte
/// on iOS and Android (NFR-10, ADR-9 determinism).
///
/// Returns an error string on an invalid FEN rather than panicking — a panic
/// across the FFI boundary is a hard crash on mobile (§5 no-panic contract).
#[uniffi::export]
pub fn perft_from_fen(fen: String, depth: u32) -> Result<u64, FfiError> {
    let mut pos = Position::from_fen(&fen).map_err(|e| FfiError::Fen(e.to_string()))?;
    Ok(pgc_engine::perft(&mut pos, depth))
}

/// Replay a list of SAN moves from the start position, returning the FEN after
/// each ply. This is the golden-slice primitive: the parity test replays the
/// same SAN on iOS/Android and asserts the per-ply FEN vector matches the host.
#[uniffi::export]
pub fn replay_sans(sans: Vec<String>) -> Result<Vec<String>, FfiError> {
    let mut pos = Position::startpos();
    let mut fens = Vec::with_capacity(sans.len());
    for (i, san) in sans.iter().enumerate() {
        let m =
            san_to_move(&pos, san).map_err(|e| FfiError::Move(format!("ply {}: {e}", i + 1)))?;
        make(&mut pos, m);
        fens.push(pos.to_fen());
    }
    Ok(fens)
}

/// Errors crossing the FFI boundary (never a panic — §5).
#[derive(Debug, thiserror::Error, uniffi::Error)]
pub enum FfiError {
    #[error("invalid FEN: {0}")]
    Fen(String),
    #[error("invalid move: {0}")]
    Move(String),
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn startpos_fen_matches_engine() {
        assert_eq!(startpos_fen(), Position::startpos().to_fen());
    }

    #[test]
    fn perft_from_fen_startpos() {
        let fen = startpos_fen();
        assert_eq!(perft_from_fen(fen.clone(), 1).unwrap(), 20);
        assert_eq!(perft_from_fen(fen, 4).unwrap(), 197_281);
    }

    #[test]
    fn perft_from_bad_fen_is_error_not_panic() {
        assert!(perft_from_fen("not a fen".to_string(), 1).is_err());
    }

    #[test]
    fn replay_sans_scholars() {
        let sans: Vec<String> = ["e4", "e5", "Bc4", "Nc6", "Qh5", "Nf6", "Qxf7#"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let fens = replay_sans(sans).unwrap();
        assert_eq!(fens.len(), 7);
        assert_eq!(
            fens.last().unwrap(),
            "r1bqkb1r/pppp1Qpp/2n2n2/4p3/2B1P3/8/PPPP1PPP/RNB1K1NR b KQkq - 0 4"
        );
    }

    #[test]
    fn replay_bad_san_is_error_not_panic() {
        assert!(replay_sans(vec!["e4".into(), "Zz9".into()]).is_err());
    }
}
