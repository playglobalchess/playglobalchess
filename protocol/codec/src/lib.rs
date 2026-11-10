//! # pgc-protocol — PlayGlobalChess wire + event codec (ADR-14, X-2/X-13)
//!
//! The **single schema-defined binary codec** mandated by ADR-14: one
//! encoder/decoder for the SPEC-S2-PROTOCOL §4A byte layout, living in the
//! shared Rust core and consumed byte-identically by the Go game-server and all
//! three clients (web/iOS/Android). "One codec, many consumers" — a client or
//! service that hand-rolls bytes is a bug (NFR-5 spirit, mirroring the engine's
//! "one rules engine"). Specs: `docs/specs/S2-realtime/PROTOCOL.md`
//! (SPEC-S2-PROTOCOL, RATIFIED §4A) and `PLAN-CODEC.md` (PLAN-S2-CODEC).
//!
//! ## Scope (this crate)
//! - The §4A value types: id newtypes, the frozen u8 enum tables, the value
//!   structs ([`types`]).
//! - The wire message set split by direction ([`ClientMsg`], [`ServerMsg`]) and
//!   the append-only event set ([`GameEvent`] + [`EventEnvelope`]).
//! - [`encode_client`]/[`decode_client`], [`encode_server`]/[`decode_server`]
//!   over the `ver | type | payload` frame envelope (§4A.1), and
//!   [`encode_event`]/[`decode_event`] over the event-store envelope (§4A.8).
//!
//! This crate is **pure**: no chess logic (the engine owns it), no I/O, no
//! networking (PLAN-CODEC §7). `#![forbid(unsafe_code)]`, like the engine
//! crates.
//!
//! ## Codec = postcard; field ORDER is the contract
//! The codec library is `postcard` (ADR-14 sub-choice, PLAN-CODEC §3 DECIDED):
//! compact varint framing, pure Rust, no external IDL. postcard is
//! **positional** — it identifies a field by its position, not a tag — so the
//! §4A field order IS the wire contract, and an accidental reorder silently
//! corrupts the wire. The guard against that is the committed golden-vector
//! corpus (`protocol/vectors/`): the golden-freeze test replays every committed
//! `(value, bytes)` pair, turning any reorder into a loud byte-mismatch CI
//! failure (PLAN-CODEC §3, SPEC-S2-PROTOCOL §8).
//!
//! The frozen u8 enum tables (§4A.4/5/7/9) are modeled as `u8`-backed newtypes
//! (not serde enums) so each serializes as exactly its frozen byte — see
//! [`types`] for why postcard's variant-index encoding would otherwise break
//! the non-contiguous §4A values.
//!
//! ## No panics on decode (PLAN-CODEC §4.1)
//! Every decode entry point is total: truncated / unknown-type /
//! unsupported-ver / wrong-direction / bad-enum / oversize input all return a
//! typed [`CodecError`], never a panic — a panic across the FFI/wasm/cgo
//! boundary is a client hard crash.

#![forbid(unsafe_code)]
#![warn(missing_debug_implementations)]

pub mod codec;
pub mod error;
pub mod event;
pub mod message;
pub mod types;

// --- flat public API (mirrors the engine's flat re-export discipline) -------

pub use codec::{
    decode_client, decode_client_intermediate, decode_envelope_intermediate, decode_event,
    decode_server, decode_server_intermediate, encode_client, encode_client_intermediate,
    encode_envelope_intermediate, encode_event, encode_server, encode_server_intermediate, VER_V1,
};
pub use error::{CodecError, MAX_FRAME_BYTES};
pub use event::{EventEnvelope, GameEvent};
pub use message::{client_code, server_code, ClientMsg, ServerMsg};
pub use types::{is_known_color, is_known_result};
pub use types::{
    AuthToken, Color, ErrorCode, EventType, GameId, PoolId, Reason, SeatColor, SeatRating,
    SeatResult, StateBody, Status, Termination, TimeControlId, UserId, ABORT_BY_SYSTEM,
    COLOR_BLACK, COLOR_WHITE, NO_SEAT, RESULT_DRAW, RESULT_LOSS, RESULT_WIN,
};
