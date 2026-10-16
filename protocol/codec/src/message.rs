//! The SPEC-S2-PROTOCOL §4A.2/§4A.3 wire messages, split by direction.
//!
//! `ClientMsg` = the `C→S` set (type codes `0x01–0x7F`), `ServerMsg` = the
//! `S→C` set (`0x80–0xFF`). The high bit of the type code is the direction
//! (§4A.2), which `codec.rs` uses for a cheap wrong-direction check.
//!
//! Each variant's fields are in the exact §4A.3 order. These enums carry only
//! the *payload*; the `ver | type` header is written/read by `codec.rs`, and
//! each variant's postcard-serialized form is the payload bytes after the
//! 2-byte header. The variant → type-code mapping is the frozen §4A.2 table and
//! is pinned here (not left to serde's variant index).

use serde::{Deserialize, Serialize};

use crate::types::{
    AuthToken, ErrorCode, GameId, PoolId, Reason, SeatResult, StateBody, Termination, TimeControlId,
};

/// Client → server messages (§4A.2, codes `0x01–0x7F`).
// `Serialize`/`Deserialize` are derived ONLY for the GS-6a cgo message boundary
// (the postcard-of-ClientMsg intermediate the Go transport decodes). As with
// GameEvent, this encodes by serde VARIANT INDEX and is NEVER the §4A wire form
// — the frozen §4A frame is produced only by `crate::codec::encode_client`
// (selected by the §4A.2 type byte). The golden-vector freeze test proves the
// §4A encoding is unchanged by this derive.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum ClientMsg {
    /// `0x01` — `token:str`.
    Authenticate { token: AuthToken },
    /// `0x02` — `pool_id:str, time_control:str, rated:bool`.
    Seek {
        pool_id: PoolId,
        time_control: TimeControlId,
        rated: bool,
    },
    /// `0x03` — `game_id:str36, ply_expected:u32, uci:str5`.
    Move {
        game_id: GameId,
        ply_expected: u32,
        uci: String,
    },
    /// `0x04` — `game_id:str36`.
    OfferDraw { game_id: GameId },
    /// `0x05` — `game_id:str36`.
    Resign { game_id: GameId },
    /// `0x06` — `game_id:str36`.
    Abort { game_id: GameId },
    /// `0x07` — `game_id:str36`.
    Takeback { game_id: GameId },
    /// `0x08` — `game_id:str36`.
    Rematch { game_id: GameId },
    /// `0x09` — `t:u64` (client monotonic ms).
    Ping { t: u64 },
    /// `0x0A` (§4A v1.1, ADR-17) — `game_id:str36`. Client asks for a fresh STATE
    /// for a game it is seated in (on-demand resync; complements the auth-time
    /// server push).
    Resync { game_id: GameId },
}

/// Server → client messages (§4A.2, codes `0x80–0xFF`).
///
/// `Serialize`/`Deserialize` are for the GS-6a cgo message boundary only (see
/// [`ClientMsg`]): the postcard of a `ServerMsg` is the intermediate the Go
/// transport hands to `pgc_encode_server`. NOT the §4A frame — that is produced
/// by [`crate::codec::encode_server`].
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum ServerMsg {
    /// `0x81` — `game_id:str36, seat_idx:u8, opponent:str, start_state:STATE-body`.
    Matched {
        game_id: GameId,
        seat_idx: u8,
        opponent: String,
        start_state: StateBody,
    },
    /// `0x82` — `game_id:str36, ply:u32, uci:str5, clock_after_ms:u32`.
    /// `uci` is the move just applied (§4A v1.2, ADR-18) — carried so a client
    /// (mover AND opponent, both seats are sent this frame) applies the one
    /// authoritative move to its own board without a local engine. Field order
    /// mirrors the `MovePlayed` event body (Q-V12a).
    MoveAccepted {
        game_id: GameId,
        ply: u32,
        uci: String,
        clock_after_ms: u32,
    },
    /// `0x83` — `game_id:str36, ply_expected:u32, reason:enum8`.
    MoveRejected {
        game_id: GameId,
        ply_expected: u32,
        reason: Reason,
    },
    /// `0x84` — `game_id:str36, fen:str, turn:u8, clocks:[u32; seat_count]`.
    /// The `fen..clocks` tail is a `StateBody` (the §4A.6 shared state format).
    State { game_id: GameId, state: StateBody },
    /// `0x85` — `game_id:str36, result_vector:[u8], termination:enum8,
    /// rating_delta:[i16]`.
    GameEnd {
        game_id: GameId,
        result_vector: Vec<SeatResult>,
        termination: Termination,
        rating_delta: Vec<i16>,
    },
    /// `0x86` — `t:u64, server_t:u64`.
    Pong { t: u64, server_t: u64 },
    /// `0x87` (§4A v1.1, ADR-17) — `game_id:str36, by:u8`. Pushed to the opponent
    /// when a first (non-ending) draw OFFER is recorded, so a pending offer is a
    /// first-class notification rather than a UI inference.
    DrawOffered { game_id: GameId, by: u8 },
    /// `0x8F` — `code:enum8, msg:str`.
    Error { code: ErrorCode, msg: String },
}

// --- frozen type codes (§4A.2) ---------------------------------------------

/// Client → server frozen type codes (§4A.2).
pub mod client_code {
    pub const AUTHENTICATE: u8 = 0x01;
    pub const SEEK: u8 = 0x02;
    pub const MOVE: u8 = 0x03;
    pub const OFFER_DRAW: u8 = 0x04;
    pub const RESIGN: u8 = 0x05;
    pub const ABORT: u8 = 0x06;
    pub const TAKEBACK: u8 = 0x07;
    pub const REMATCH: u8 = 0x08;
    pub const PING: u8 = 0x09;
    /// §4A v1.1 (ADR-17).
    pub const RESYNC: u8 = 0x0A;
}

/// Server → client frozen type codes (§4A.2).
pub mod server_code {
    pub const MATCHED: u8 = 0x81;
    pub const MOVE_ACCEPTED: u8 = 0x82;
    pub const MOVE_REJECTED: u8 = 0x83;
    pub const STATE: u8 = 0x84;
    pub const GAME_END: u8 = 0x85;
    pub const PONG: u8 = 0x86;
    /// §4A v1.1 (ADR-17).
    pub const DRAW_OFFERED: u8 = 0x87;
    pub const ERROR: u8 = 0x8F;
}

impl ClientMsg {
    /// The frozen §4A.2 type code for this message (the `type` header byte).
    pub fn type_code(&self) -> u8 {
        match self {
            ClientMsg::Authenticate { .. } => client_code::AUTHENTICATE,
            ClientMsg::Seek { .. } => client_code::SEEK,
            ClientMsg::Move { .. } => client_code::MOVE,
            ClientMsg::OfferDraw { .. } => client_code::OFFER_DRAW,
            ClientMsg::Resign { .. } => client_code::RESIGN,
            ClientMsg::Abort { .. } => client_code::ABORT,
            ClientMsg::Takeback { .. } => client_code::TAKEBACK,
            ClientMsg::Rematch { .. } => client_code::REMATCH,
            ClientMsg::Ping { .. } => client_code::PING,
            ClientMsg::Resync { .. } => client_code::RESYNC,
        }
    }
}

impl ServerMsg {
    /// The frozen §4A.2 type code for this message (the `type` header byte).
    pub fn type_code(&self) -> u8 {
        match self {
            ServerMsg::Matched { .. } => server_code::MATCHED,
            ServerMsg::MoveAccepted { .. } => server_code::MOVE_ACCEPTED,
            ServerMsg::MoveRejected { .. } => server_code::MOVE_REJECTED,
            ServerMsg::State { .. } => server_code::STATE,
            ServerMsg::GameEnd { .. } => server_code::GAME_END,
            ServerMsg::Pong { .. } => server_code::PONG,
            ServerMsg::DrawOffered { .. } => server_code::DRAW_OFFERED,
            ServerMsg::Error { .. } => server_code::ERROR,
        }
    }
}

// --- payload wire structs ---------------------------------------------------
//
// postcard serializes a struct as its fields in declaration order with no tag.
// We serialize each message's PAYLOAD via a per-variant struct whose field
// order is exactly §4A.3 — rather than serializing the enum directly, because a
// serde enum would prepend a variant-index tag that is not in the §4A wire
// format. `codec.rs` picks the struct by type code and writes `ver | type` +
// the struct's postcard bytes. Field order here IS the wire contract (§3).

/// `SEEK` payload (§4A.3): `pool_id, time_control, rated`.
#[derive(Serialize, Deserialize)]
pub(crate) struct SeekPayload {
    pub pool_id: PoolId,
    pub time_control: TimeControlId,
    pub rated: bool,
}

/// `MOVE` payload (§4A.3): `game_id, ply_expected, uci`.
#[derive(Serialize, Deserialize)]
pub(crate) struct MovePayload {
    pub game_id: GameId,
    pub ply_expected: u32,
    pub uci: String,
}

/// `MATCHED` payload (§4A.3): `game_id, seat_idx, opponent, start_state`.
#[derive(Serialize, Deserialize)]
pub(crate) struct MatchedPayload {
    pub game_id: GameId,
    pub seat_idx: u8,
    pub opponent: String,
    pub start_state: StateBody,
}

/// `MOVE_ACCEPTED` payload (§4A.3, v1.2 ADR-18): `game_id, ply, uci,
/// clock_after_ms`. `uci` added after `ply` (Q-V12a, matching `MovePlayed`).
#[derive(Serialize, Deserialize)]
pub(crate) struct MoveAcceptedPayload {
    pub game_id: GameId,
    pub ply: u32,
    pub uci: String,
    pub clock_after_ms: u32,
}

/// `MOVE_REJECTED` payload (§4A.3): `game_id, ply_expected, reason`.
#[derive(Serialize, Deserialize)]
pub(crate) struct MoveRejectedPayload {
    pub game_id: GameId,
    pub ply_expected: u32,
    pub reason: Reason,
}

/// `STATE` payload (§4A.3): `game_id, fen, turn, clocks`. The `state` field is
/// a nested `StateBody`; postcard has no per-struct framing, so a nested struct
/// serializes as its fields inline and positionally — i.e. the bytes are
/// exactly `game_id, fen, turn, clocks`, and the `fen, turn, clocks` tail is
/// byte-identical to `MATCHED.start_state` (§4A.6). No `serde(flatten)` — that
/// is unsupported by non-self-describing formats like postcard.
#[derive(Serialize, Deserialize)]
pub(crate) struct StatePayload {
    pub game_id: GameId,
    pub state: StateBody,
}

/// `GAME_END` payload (§4A.3): `game_id, result_vector, termination,
/// rating_delta`.
#[derive(Serialize, Deserialize)]
pub(crate) struct GameEndPayload {
    pub game_id: GameId,
    pub result_vector: Vec<SeatResult>,
    pub termination: Termination,
    pub rating_delta: Vec<i16>,
}

/// `PONG` payload (§4A.3): `t, server_t`.
#[derive(Serialize, Deserialize)]
pub(crate) struct PongPayload {
    pub t: u64,
    pub server_t: u64,
}

/// `ERROR` payload (§4A.3): `code, msg`.
#[derive(Serialize, Deserialize)]
pub(crate) struct ErrorPayload {
    pub code: ErrorCode,
    pub msg: String,
}

/// `DRAW_OFFERED` payload (§4A v1.1, ADR-17): `game_id, by`.
#[derive(Serialize, Deserialize)]
pub(crate) struct DrawOfferedPayload {
    pub game_id: GameId,
    pub by: u8,
}
