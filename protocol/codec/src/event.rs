//! The SPEC-S2-PROTOCOL §3.2 / §4A.8 / §4A.9 event-store schema: the canonical
//! append-only `GameEvent` set, their body layouts (field order per §4A.8), and
//! the persisted envelope.
//!
//! Events are persisted via the **same codec** as the wire (§4A.8: "one encoder
//! for wire + store"). The envelope wraps a body:
//!
//! ```text
//! { stream_id:str, seq:u32, type:enum8, schema_ver:u16, ts:u64, body:... }
//! ```
//!
//! As with the wire messages, we serialize the envelope header fields + the
//! body's per-variant struct positionally, picking the body struct by the
//! frozen §4A.9 `type` byte — never serde's enum variant index.

use serde::{Deserialize, Serialize};

use crate::types::{
    EventType, PoolId, SeatColor, SeatRating, SeatResult, Termination, TimeControlId, UserId,
};

/// The canonical v1 game-event set (§3.2, §4A.8). Exactly MASTER §VIII.3 — no
/// additions. Carries only the event *body*; `seq`/`ts`/`schema_ver`/`stream_id`
/// live on the [`EventEnvelope`].
///
/// `Serialize`/`Deserialize` are derived ONLY for the in-process GS-4a cgo
/// boundary (the postcard-of-envelope intermediate the Go server hands to the
/// `pgc_encode_event` seam). That derive encodes a `GameEvent` by serde's
/// VARIANT INDEX — which is correct for the symmetric intermediate (Rust/Go both
/// use the same derive) but is NEVER the §4A wire form: the frozen §4A bytes are
/// produced only by [`crate::codec::encode_event`], which selects the body by the
/// §4A.9 `type` byte. The two paths are independent; the golden-vector freeze
/// test proves the §4A encoding is unchanged by this derive.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum GameEvent {
    /// `pool_id, rated, seats:[{seat_idx,color}], start_fen, time_control`.
    GameCreated {
        pool_id: PoolId,
        rated: bool,
        seats: Vec<SeatColor>,
        start_fen: String,
        time_control: TimeControlId,
    },
    /// `seat_idx, user_id`.
    PlayerJoined { seat_idx: u8, user_id: UserId },
    /// `ply, seat_idx, uci, clock_after_ms, server_ts`.
    MovePlayed {
        ply: u32,
        seat_idx: u8,
        uci: String,
        clock_after_ms: u32,
        server_ts: u64,
    },
    /// `seat_idx`.
    DrawOffered { seat_idx: u8 },
    /// `seat_idx`.
    DrawAccepted { seat_idx: u8 },
    /// `seat_idx`.
    Resigned { seat_idx: u8 },
    /// `by:u8` (`0xFF` = system; [`crate::types::ABORT_BY_SYSTEM`]).
    Aborted { by: u8 },
    /// `seat_idx`.
    TakebackReq { seat_idx: u8 },
    /// `seat_idx`.
    TakebackAccepted { seat_idx: u8 },
    /// `seat_idx`.
    ClockFlagged { seat_idx: u8 },
    /// `result_vector:[u8], termination:enum8`.
    GameEnded {
        result_vector: Vec<SeatResult>,
        termination: Termination,
    },
    /// `pool_id, per_seat:[{seat_idx, before, after, rd, vol}]`.
    RatingApplied {
        pool_id: PoolId,
        per_seat: Vec<SeatRating>,
    },
}

impl GameEvent {
    /// The frozen §4A.9 `type` byte for this event (the envelope `type` field).
    pub fn event_type(&self) -> EventType {
        match self {
            GameEvent::GameCreated { .. } => EventType::GameCreated,
            GameEvent::PlayerJoined { .. } => EventType::PlayerJoined,
            GameEvent::MovePlayed { .. } => EventType::MovePlayed,
            GameEvent::DrawOffered { .. } => EventType::DrawOffered,
            GameEvent::DrawAccepted { .. } => EventType::DrawAccepted,
            GameEvent::Resigned { .. } => EventType::Resigned,
            GameEvent::Aborted { .. } => EventType::Aborted,
            GameEvent::TakebackReq { .. } => EventType::TakebackReq,
            GameEvent::TakebackAccepted { .. } => EventType::TakebackAccepted,
            GameEvent::ClockFlagged { .. } => EventType::ClockFlagged,
            GameEvent::GameEnded { .. } => EventType::GameEnded,
            GameEvent::RatingApplied { .. } => EventType::RatingApplied,
        }
    }
}

/// A persisted event: the §3.2 / §4A.8 envelope around a [`GameEvent`] body.
///
/// Encoding (positional, §4A.8): `stream_id, seq, type, schema_ver, ts, body`.
/// `type` is derived from `event` on encode and validated against it on decode;
/// it is not stored twice in this struct (the body IS the typed `GameEvent`).
///
/// `Serialize`/`Deserialize` are for the GS-4a cgo boundary only (see
/// [`GameEvent`]): the postcard bytes of an `EventEnvelope` are the intermediate
/// the Go server hands across to `pgc_encode_event`. This derive is NOT the §4A
/// frame — the frame is produced by [`crate::codec::encode_event`].
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct EventEnvelope {
    /// `"game/{uuid}"` (§4A.8). The stream this event belongs to.
    pub stream_id: String,
    /// 0-based position in the stream; strictly increasing, no gaps (§3.2).
    pub seq: u32,
    /// Per-event schema version for forward evolution (§3.2, §5).
    pub schema_ver: u16,
    /// Server-assigned persist time (§3.2).
    pub ts: u64,
    /// The event body.
    pub event: GameEvent,
}

// --- per-event body wire structs (field order per §4A.8) --------------------
//
// Serialized positionally by postcard; field order IS the contract (§3). The
// envelope header (`stream_id, seq, type, schema_ver, ts`) is written by
// `codec.rs`, then the matching body struct's postcard bytes.

/// `GameCreated` body (§4A.8).
#[derive(Serialize, Deserialize)]
pub(crate) struct GameCreatedBody {
    pub pool_id: PoolId,
    pub rated: bool,
    pub seats: Vec<SeatColor>,
    pub start_fen: String,
    pub time_control: TimeControlId,
}

/// `PlayerJoined` body (§4A.8).
#[derive(Serialize, Deserialize)]
pub(crate) struct PlayerJoinedBody {
    pub seat_idx: u8,
    pub user_id: UserId,
}

/// `MovePlayed` body (§4A.8).
#[derive(Serialize, Deserialize)]
pub(crate) struct MovePlayedBody {
    pub ply: u32,
    pub seat_idx: u8,
    pub uci: String,
    pub clock_after_ms: u32,
    pub server_ts: u64,
}

/// A single-`seat_idx` body, shared by DrawOffered / DrawAccepted / Resigned /
/// TakebackReq / TakebackAccepted / ClockFlagged (§4A.8 — all `seat_idx:u8`).
#[derive(Serialize, Deserialize)]
pub(crate) struct SeatIdxBody {
    pub seat_idx: u8,
}

/// `Aborted` body (§4A.8): `by:u8` (`0xFF` = system).
#[derive(Serialize, Deserialize)]
pub(crate) struct AbortedBody {
    pub by: u8,
}

/// `GameEnded` body (§4A.8): `result_vector:[u8], termination:enum8`.
#[derive(Serialize, Deserialize)]
pub(crate) struct GameEndedBody {
    pub result_vector: Vec<SeatResult>,
    pub termination: Termination,
}

/// `RatingApplied` body (§4A.8): `pool_id, per_seat:[{...}]`.
#[derive(Serialize, Deserialize)]
pub(crate) struct RatingAppliedBody {
    pub pool_id: PoolId,
    pub per_seat: Vec<SeatRating>,
}
