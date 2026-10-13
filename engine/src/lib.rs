//! # pgc-engine — PlayGlobalChess core chess rules engine (X-1)
//!
//! The authoritative, server-side **Standard chess** rules engine. Single
//! source of truth for move legality across the Go server (ADR-12) and the
//! shared client core (ADR-9). Spec: `docs/specs/S1-engine/ENGINE-CORE.md`
//! (SPEC-S1-ENGINE-CORE). Feeds gate **G-A**.
//!
//! ## Build status — chunk 1 of 4 toward G-A
//! This crate currently implements, against the ratified spec:
//! - **Board representation** (§3.2–3.3): bitboards + redundant mailbox, LERF
//!   indexing, the [`Position`] struct carrying all FEN state.
//! - **FEN** parse/emit with the **OPEN-D hybrid** (§3.3): lenient internal ep
//!   target, strict exported FEN.
//! - A **pseudo-legal + legality-filter** move generator (§3.5) — the simple,
//!   correct form; sufficient to make the chunk gate green.
//! - **make/unmake** with an [`Undo`] token (§3.6, chunk 2 stage 2a): the
//!   zero-clone perft / validation hot path, a perfect inverse
//!   (`unmake(make(p,m)) == p`, property-tested). [`movegen::apply_move`] is
//!   the copy-make convenience over it.
//! - The **perft harness** (§4.4): [`perft`] and [`perft_divide`], now driven
//!   by make/unmake on a single mutable position.
//!
//! Gated now: the FEN round-trip property (`parse_fen(to_fen(p)) == p`, §8.6),
//! `perft(startpos, 1) == 20`, and the make/unmake inverse (§8.6).
//!
//! Implemented since: the full §8.1 perft corpus [2b], the E-1..E-14 edge-case
//! corpus (§8.2) [2c], game-end/draw detection with a Zobrist key
//! (§3.7, §4.1, OPEN-B) [2d] — [`status`], [`status_with_history`], [`zobrist`]
//! — and move encoding (§4.2) [2e]: [`move_to_san`], [`san_to_move`],
//! position-aware [`uci_to_move`] / [`move_to_uci`].
//!
//! **Deferred to later chunks (NOT yet verified) — the rest of G-A:** magic
//! bitboards and the §6.1 performance gate; full golden replay (§8.5); mobile
//! parity (§8.8); the differential tier as a standing gate (§8.7). The variant
//! seam (§3.9) is forward-looking. Chunk 2 is complete: the engine is
//! correctness-complete for Standard, but G-A also needs the performance,
//! golden-replay, and mobile-parity work.
//!
//! Design tenets honored: pure, deterministic, no RNG / clock / global state
//! (§1.4.1); stable move order by (from, to, promotion) (§3.8).

#![forbid(unsafe_code)]
#![warn(missing_debug_implementations)]

pub mod bitboard;
pub mod castling;
pub mod fen;
pub mod movegen;
pub mod moves;
pub mod perft;
pub mod position;
pub mod san;
pub mod status;
pub mod types;
pub mod zobrist;

// --- Public API surface (ENGINE-CORE §4) ------------------------------------
// Flat re-exports so consumers (S2 via FFI/gRPC, the client core via UniFFI)
// bind a stable top-level API, as §4 specifies.

pub use castling::CastlingRights;
pub use fen::{parse_fen, to_fen, FenError};
pub use movegen::{
    is_in_check, is_legal, is_square_attacked, legal_moves, make, pseudo_legal_moves, unmake, Undo,
};
pub use moves::{Move, MoveList};
pub use perft::{perft, perft_divide};
pub use position::Position;
pub use san::{move_to_san, move_to_uci, san_to_move, uci_to_move, MoveError};
pub use status::{
    has_mating_material, is_insufficient_material, status, status_with_history, DrawClaim,
    GameStatus,
};
pub use types::{Color, Piece, PieceType, Square};
pub use zobrist::zobrist;
