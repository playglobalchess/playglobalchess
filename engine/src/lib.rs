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
//! - The **perft harness skeleton** (§4.4): [`perft`] and [`perft_divide`].
//!
//! Gated now: the FEN round-trip property (`parse_fen(to_fen(p)) == p`, §8.6)
//! and `perft(startpos, 1) == 20`.
//!
//! **Deferred to later chunks (NOT yet verified):** the full §8.1 perft corpus
//! (Kiwipete / Positions 3–6, deep perft), the E-1..E-14 edge-case corpus
//! (§8.2), game-end/draw detection (§3.7), SAN (§4.2), make/unmake with `Undo`
//! (§3.6), Zobrist/repetition (§3.7, OPEN-B), magic bitboards, benchmarks
//! (§6.1), and mobile parity (§8.8). The variant seam (§3.9) is forward-looking.
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
pub mod types;

// --- Public API surface (ENGINE-CORE §4) ------------------------------------
// Flat re-exports so consumers (S2 via FFI/gRPC, the client core via UniFFI)
// bind a stable top-level API, as §4 specifies. SAN, make/unmake-with-Undo,
// status(), and uci_to_move/move_to_uci-on-Position arrive in later chunks.

pub use castling::CastlingRights;
pub use fen::{parse_fen, to_fen, FenError};
pub use movegen::{is_in_check, is_square_attacked, legal_moves, pseudo_legal_moves};
pub use moves::{Move, MoveList};
pub use perft::{perft, perft_divide};
pub use position::Position;
pub use types::{Color, Piece, PieceType, Square};
