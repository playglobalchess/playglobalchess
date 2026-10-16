//! UniFFI binding surface for the PlayGlobalChess wire + event codec (ADR-14:
//! one codec, three consumers). This crate adds **no codec logic** — every
//! function delegates to `pgc_protocol`, so the Swift (iOS) and Kotlin
//! (Android) clients run the exact same encode/decode the server uses (NFR-5,
//! NFR-10). The §8 / §4.3 mobile-parity test drives `decode_* ` + `encode_*`
//! and asserts byte-identical re-encode of every committed golden vector
//! (`protocol/vectors/vectors.json`).
//!
//! ## Why mirror types
//!
//! The pure `pgc-protocol` crate is deliberately UniFFI-free (it is linked by
//! the Go server over cgo and compiled to wasm for web, where UniFFI must not
//! intrude). So this crate declares UniFFI-annotated **mirror** types for the
//! §4A message/event set and converts to/from the core types at the boundary.
//! The mirror is a 1:1 shape copy — it introduces no new fields and no logic,
//! only the `#[derive(uniffi::...)]` the FFI layer needs. The frozen byte
//! layout stays owned by `pgc-protocol`; this crate never touches bytes except
//! by calling the core `encode_*`/`decode_*`.
//!
//! ## No panics across the boundary (PLAN-CODEC §4.1, §5)
//!
//! Every exported function returns `Result<_, FfiCodecError>`; a decode failure
//! is a thrown error on the Swift/Kotlin side, never a trap — a panic across
//! FFI is a mobile hard crash.

#![forbid(unsafe_code)]

use pgc_protocol::{
    ClientMsg as CoreClient, CodecError, ErrorCode as CoreErr, EventEnvelope as CoreEnvelope,
    GameEvent as CoreEvent, Reason as CoreReason, ServerMsg as CoreServer, StateBody as CoreState,
    Termination as CoreTerm,
};

uniffi::setup_scaffolding!();

/// Errors crossing the FFI boundary (never a panic — PLAN-CODEC §4.1). Mirrors
/// the `pgc_protocol::CodecError` variants so a client sees the same taxonomy.
#[derive(Debug, thiserror::Error, uniffi::Error)]
pub enum FfiCodecError {
    #[error("frame too short: {len} byte(s)")]
    TooShort { len: u32 },
    #[error("unsupported protocol version: {ver}")]
    UnsupportedVersion { ver: u8 },
    #[error("unknown type code: {code}")]
    UnknownType { code: u8 },
    #[error("wrong-direction type code {code} for the {path} decode path")]
    WrongDirection { code: u8, path: String },
    #[error("frame too large: {len} bytes")]
    TooLarge { len: u32 },
    #[error("malformed payload: {detail}")]
    MalformedPayload { detail: String },
}

impl From<CodecError> for FfiCodecError {
    fn from(e: CodecError) -> Self {
        match e {
            CodecError::TooShort(n) => FfiCodecError::TooShort { len: n as u32 },
            CodecError::UnsupportedVersion(v) => FfiCodecError::UnsupportedVersion { ver: v },
            CodecError::UnknownType(c) => FfiCodecError::UnknownType { code: c },
            CodecError::WrongDirection(c, p) => FfiCodecError::WrongDirection {
                code: c,
                path: p.to_string(),
            },
            CodecError::TooLarge(n) => FfiCodecError::TooLarge { len: n as u32 },
            CodecError::MalformedPayload(d) => FfiCodecError::MalformedPayload { detail: d },
        }
    }
}

// --- frozen enum mirrors (§4A.4/5/7) ----------------------------------------
// Exposed as UniFFI enums for ergonomic Swift/Kotlin; converted to/from the
// core u8-newtype at the boundary. A core value outside the frozen set cannot
// occur here — the core decode already rejected it — but the TryFrom is total
// so the mapping needs no panic.

/// `MOVE_REJECTED.reason` (§4A.4).
#[derive(Debug, Clone, Copy, PartialEq, Eq, uniffi::Enum)]
pub enum Reason {
    Illegal,
    StalePly,
    GameOver,
    NotYourTurn,
    NotYourSeat,
    UnknownGame,
}

impl Reason {
    fn to_core(self) -> CoreReason {
        match self {
            Reason::Illegal => CoreReason::ILLEGAL,
            Reason::StalePly => CoreReason::STALE_PLY,
            Reason::GameOver => CoreReason::GAME_OVER,
            Reason::NotYourTurn => CoreReason::NOT_YOUR_TURN,
            Reason::NotYourSeat => CoreReason::NOT_YOUR_SEAT,
            Reason::UnknownGame => CoreReason::UNKNOWN_GAME,
        }
    }
    fn from_core(r: CoreReason) -> Result<Self, FfiCodecError> {
        Ok(match r {
            CoreReason::ILLEGAL => Reason::Illegal,
            CoreReason::STALE_PLY => Reason::StalePly,
            CoreReason::GAME_OVER => Reason::GameOver,
            CoreReason::NOT_YOUR_TURN => Reason::NotYourTurn,
            CoreReason::NOT_YOUR_SEAT => Reason::NotYourSeat,
            CoreReason::UNKNOWN_GAME => Reason::UnknownGame,
            other => {
                return Err(FfiCodecError::MalformedPayload {
                    detail: format!("unknown reason 0x{:02x}", other.value()),
                })
            }
        })
    }
}

/// `GAME_END`/`GameEnded.termination` (§4A.5).
#[derive(Debug, Clone, Copy, PartialEq, Eq, uniffi::Enum)]
pub enum Termination {
    Mate,
    Resign,
    Flag,
    Abort,
    DrawAgree,
    Stalemate,
    Repetition,
    Fiftymove,
    Insufficient,
    TimeoutVsInsufficient,
}

impl Termination {
    fn to_core(self) -> CoreTerm {
        match self {
            Termination::Mate => CoreTerm::MATE,
            Termination::Resign => CoreTerm::RESIGN,
            Termination::Flag => CoreTerm::FLAG,
            Termination::Abort => CoreTerm::ABORT,
            Termination::DrawAgree => CoreTerm::DRAW_AGREE,
            Termination::Stalemate => CoreTerm::STALEMATE,
            Termination::Repetition => CoreTerm::REPETITION,
            Termination::Fiftymove => CoreTerm::FIFTYMOVE,
            Termination::Insufficient => CoreTerm::INSUFFICIENT,
            Termination::TimeoutVsInsufficient => CoreTerm::TIMEOUT_VS_INSUFFICIENT,
        }
    }
    fn from_core(t: CoreTerm) -> Result<Self, FfiCodecError> {
        Ok(match t {
            CoreTerm::MATE => Termination::Mate,
            CoreTerm::RESIGN => Termination::Resign,
            CoreTerm::FLAG => Termination::Flag,
            CoreTerm::ABORT => Termination::Abort,
            CoreTerm::DRAW_AGREE => Termination::DrawAgree,
            CoreTerm::STALEMATE => Termination::Stalemate,
            CoreTerm::REPETITION => Termination::Repetition,
            CoreTerm::FIFTYMOVE => Termination::Fiftymove,
            CoreTerm::INSUFFICIENT => Termination::Insufficient,
            CoreTerm::TIMEOUT_VS_INSUFFICIENT => Termination::TimeoutVsInsufficient,
            other => {
                return Err(FfiCodecError::MalformedPayload {
                    detail: format!("unknown termination 0x{:02x}", other.value()),
                })
            }
        })
    }
}

/// `ERROR.code` (§4A.7).
#[derive(Debug, Clone, Copy, PartialEq, Eq, uniffi::Enum)]
pub enum ErrorCode {
    UnsupportedVersion,
    ProtocolViolation,
    UnknownGame,
    Unauthenticated,
    RateLimited,
    Internal,
}

impl ErrorCode {
    fn to_core(self) -> CoreErr {
        match self {
            ErrorCode::UnsupportedVersion => CoreErr::UNSUPPORTED_VERSION,
            ErrorCode::ProtocolViolation => CoreErr::PROTOCOL_VIOLATION,
            ErrorCode::UnknownGame => CoreErr::UNKNOWN_GAME,
            ErrorCode::Unauthenticated => CoreErr::UNAUTHENTICATED,
            ErrorCode::RateLimited => CoreErr::RATE_LIMITED,
            ErrorCode::Internal => CoreErr::INTERNAL,
        }
    }
    fn from_core(c: CoreErr) -> Result<Self, FfiCodecError> {
        Ok(match c {
            CoreErr::UNSUPPORTED_VERSION => ErrorCode::UnsupportedVersion,
            CoreErr::PROTOCOL_VIOLATION => ErrorCode::ProtocolViolation,
            CoreErr::UNKNOWN_GAME => ErrorCode::UnknownGame,
            CoreErr::UNAUTHENTICATED => ErrorCode::Unauthenticated,
            CoreErr::RATE_LIMITED => ErrorCode::RateLimited,
            CoreErr::INTERNAL => ErrorCode::Internal,
            other => {
                return Err(FfiCodecError::MalformedPayload {
                    detail: format!("unknown error code 0x{:02x}", other.value()),
                })
            }
        })
    }
}

// --- value record mirrors ---------------------------------------------------

/// `STATE` body / `MATCHED.start_state` (§4A.6): `fen, turn, clocks`.
#[derive(Debug, Clone, PartialEq, Eq, uniffi::Record)]
pub struct StateBody {
    pub fen: String,
    pub turn: u8,
    pub clocks: Vec<u32>,
}

impl StateBody {
    fn to_core(&self) -> CoreState {
        CoreState {
            fen: self.fen.clone(),
            turn: self.turn,
            clocks: self.clocks.clone(),
        }
    }
    fn from_core(s: &CoreState) -> Self {
        StateBody {
            fen: s.fen.clone(),
            turn: s.turn,
            clocks: s.clocks.clone(),
        }
    }
}

/// A seat descriptor in `GameCreated.seats` (§4A.8): `{seat_idx, color}`.
#[derive(Debug, Clone, PartialEq, Eq, uniffi::Record)]
pub struct SeatColor {
    pub seat_idx: u8,
    pub color: u8,
}

/// A per-seat rating change in `RatingApplied.per_seat` (§4A.8).
#[derive(Debug, Clone, PartialEq, uniffi::Record)]
pub struct SeatRating {
    pub seat_idx: u8,
    pub before: i16,
    pub after: i16,
    pub rd: u16,
    pub vol: f32,
}

// --- message mirrors (§4A.3) ------------------------------------------------

/// Client → server messages (§4A.2).
#[derive(Debug, Clone, PartialEq, Eq, uniffi::Enum)]
pub enum ClientMsg {
    Authenticate {
        token: String,
    },
    Seek {
        pool_id: String,
        time_control: String,
        rated: bool,
    },
    Move {
        game_id: String,
        ply_expected: u32,
        uci: String,
    },
    OfferDraw {
        game_id: String,
    },
    Resign {
        game_id: String,
    },
    Abort {
        game_id: String,
    },
    Takeback {
        game_id: String,
    },
    Rematch {
        game_id: String,
    },
    Ping {
        t: u64,
    },
    /// §4A v1.1 (ADR-17).
    Resync {
        game_id: String,
    },
}

/// Server → client messages (§4A.2).
#[derive(Debug, Clone, PartialEq, uniffi::Enum)]
pub enum ServerMsg {
    Matched {
        game_id: String,
        seat_idx: u8,
        opponent: String,
        start_state: StateBody,
    },
    MoveAccepted {
        game_id: String,
        ply: u32,
        uci: String,
        clock_after_ms: u32,
    },
    MoveRejected {
        game_id: String,
        ply_expected: u32,
        reason: Reason,
    },
    State {
        game_id: String,
        state: StateBody,
    },
    GameEnd {
        game_id: String,
        result_vector: Vec<u8>,
        termination: Termination,
        rating_delta: Vec<i16>,
    },
    Pong {
        t: u64,
        server_t: u64,
    },
    /// §4A v1.1 (ADR-17).
    DrawOffered {
        game_id: String,
        by: u8,
    },
    Error {
        code: ErrorCode,
        msg: String,
    },
}

/// Append-only game events (§3.2, §4A.8).
#[derive(Debug, Clone, PartialEq, uniffi::Enum)]
pub enum GameEvent {
    GameCreated {
        pool_id: String,
        rated: bool,
        seats: Vec<SeatColor>,
        start_fen: String,
        time_control: String,
    },
    PlayerJoined {
        seat_idx: u8,
        user_id: String,
    },
    MovePlayed {
        ply: u32,
        seat_idx: u8,
        uci: String,
        clock_after_ms: u32,
        server_ts: u64,
    },
    DrawOffered {
        seat_idx: u8,
    },
    DrawAccepted {
        seat_idx: u8,
    },
    Resigned {
        seat_idx: u8,
    },
    Aborted {
        by: u8,
    },
    TakebackReq {
        seat_idx: u8,
    },
    TakebackAccepted {
        seat_idx: u8,
    },
    ClockFlagged {
        seat_idx: u8,
    },
    GameEnded {
        result_vector: Vec<u8>,
        termination: Termination,
    },
    RatingApplied {
        pool_id: String,
        per_seat: Vec<SeatRating>,
    },
}

/// The persisted event envelope (§4A.8).
#[derive(Debug, Clone, PartialEq, uniffi::Record)]
pub struct EventEnvelope {
    pub stream_id: String,
    pub seq: u32,
    pub schema_ver: u16,
    pub ts: u64,
    pub event: GameEvent,
}

// --- conversions: mirror <-> core (shape-only; no byte logic) ---------------

impl ClientMsg {
    fn to_core(&self) -> CoreClient {
        use pgc_protocol::{GameId, PoolId, TimeControlId};
        match self {
            ClientMsg::Authenticate { token } => CoreClient::Authenticate {
                token: token.as_str().into(),
            },
            ClientMsg::Seek {
                pool_id,
                time_control,
                rated,
            } => CoreClient::Seek {
                pool_id: PoolId::from(pool_id.as_str()),
                time_control: TimeControlId::from(time_control.as_str()),
                rated: *rated,
            },
            ClientMsg::Move {
                game_id,
                ply_expected,
                uci,
            } => CoreClient::Move {
                game_id: GameId::from(game_id.as_str()),
                ply_expected: *ply_expected,
                uci: uci.clone(),
            },
            ClientMsg::OfferDraw { game_id } => CoreClient::OfferDraw {
                game_id: GameId::from(game_id.as_str()),
            },
            ClientMsg::Resign { game_id } => CoreClient::Resign {
                game_id: GameId::from(game_id.as_str()),
            },
            ClientMsg::Abort { game_id } => CoreClient::Abort {
                game_id: GameId::from(game_id.as_str()),
            },
            ClientMsg::Takeback { game_id } => CoreClient::Takeback {
                game_id: GameId::from(game_id.as_str()),
            },
            ClientMsg::Rematch { game_id } => CoreClient::Rematch {
                game_id: GameId::from(game_id.as_str()),
            },
            ClientMsg::Ping { t } => CoreClient::Ping { t: *t },
            ClientMsg::Resync { game_id } => CoreClient::Resync {
                game_id: GameId::from(game_id.as_str()),
            },
        }
    }

    fn from_core(m: &CoreClient) -> Self {
        match m {
            CoreClient::Authenticate { token } => ClientMsg::Authenticate {
                token: token.as_str().to_string(),
            },
            CoreClient::Seek {
                pool_id,
                time_control,
                rated,
            } => ClientMsg::Seek {
                pool_id: pool_id.as_str().to_string(),
                time_control: time_control.as_str().to_string(),
                rated: *rated,
            },
            CoreClient::Move {
                game_id,
                ply_expected,
                uci,
            } => ClientMsg::Move {
                game_id: game_id.as_str().to_string(),
                ply_expected: *ply_expected,
                uci: uci.clone(),
            },
            CoreClient::OfferDraw { game_id } => ClientMsg::OfferDraw {
                game_id: game_id.as_str().to_string(),
            },
            CoreClient::Resign { game_id } => ClientMsg::Resign {
                game_id: game_id.as_str().to_string(),
            },
            CoreClient::Abort { game_id } => ClientMsg::Abort {
                game_id: game_id.as_str().to_string(),
            },
            CoreClient::Takeback { game_id } => ClientMsg::Takeback {
                game_id: game_id.as_str().to_string(),
            },
            CoreClient::Rematch { game_id } => ClientMsg::Rematch {
                game_id: game_id.as_str().to_string(),
            },
            CoreClient::Ping { t } => ClientMsg::Ping { t: *t },
            CoreClient::Resync { game_id } => ClientMsg::Resync {
                game_id: game_id.as_str().to_string(),
            },
        }
    }
}

impl ServerMsg {
    fn to_core(&self) -> Result<CoreServer, FfiCodecError> {
        use pgc_protocol::GameId;
        Ok(match self {
            ServerMsg::Matched {
                game_id,
                seat_idx,
                opponent,
                start_state,
            } => CoreServer::Matched {
                game_id: GameId::from(game_id.as_str()),
                seat_idx: *seat_idx,
                opponent: opponent.clone(),
                start_state: start_state.to_core(),
            },
            ServerMsg::MoveAccepted {
                game_id,
                ply,
                uci,
                clock_after_ms,
            } => CoreServer::MoveAccepted {
                game_id: GameId::from(game_id.as_str()),
                ply: *ply,
                uci: uci.clone(),
                clock_after_ms: *clock_after_ms,
            },
            ServerMsg::MoveRejected {
                game_id,
                ply_expected,
                reason,
            } => CoreServer::MoveRejected {
                game_id: GameId::from(game_id.as_str()),
                ply_expected: *ply_expected,
                reason: reason.to_core(),
            },
            ServerMsg::State { game_id, state } => CoreServer::State {
                game_id: GameId::from(game_id.as_str()),
                state: state.to_core(),
            },
            ServerMsg::GameEnd {
                game_id,
                result_vector,
                termination,
                rating_delta,
            } => CoreServer::GameEnd {
                game_id: GameId::from(game_id.as_str()),
                result_vector: result_vector.clone(),
                termination: termination.to_core(),
                rating_delta: rating_delta.clone(),
            },
            ServerMsg::Pong { t, server_t } => CoreServer::Pong {
                t: *t,
                server_t: *server_t,
            },
            ServerMsg::Error { code, msg } => CoreServer::Error {
                code: code.to_core(),
                msg: msg.clone(),
            },
            ServerMsg::DrawOffered { game_id, by } => CoreServer::DrawOffered {
                game_id: pgc_protocol::GameId::from(game_id.as_str()),
                by: *by,
            },
        })
    }

    fn from_core(m: &CoreServer) -> Result<Self, FfiCodecError> {
        Ok(match m {
            CoreServer::Matched {
                game_id,
                seat_idx,
                opponent,
                start_state,
            } => ServerMsg::Matched {
                game_id: game_id.as_str().to_string(),
                seat_idx: *seat_idx,
                opponent: opponent.clone(),
                start_state: StateBody::from_core(start_state),
            },
            CoreServer::MoveAccepted {
                game_id,
                ply,
                uci,
                clock_after_ms,
            } => ServerMsg::MoveAccepted {
                game_id: game_id.as_str().to_string(),
                ply: *ply,
                uci: uci.clone(),
                clock_after_ms: *clock_after_ms,
            },
            CoreServer::MoveRejected {
                game_id,
                ply_expected,
                reason,
            } => ServerMsg::MoveRejected {
                game_id: game_id.as_str().to_string(),
                ply_expected: *ply_expected,
                reason: Reason::from_core(*reason)?,
            },
            CoreServer::State { game_id, state } => ServerMsg::State {
                game_id: game_id.as_str().to_string(),
                state: StateBody::from_core(state),
            },
            CoreServer::GameEnd {
                game_id,
                result_vector,
                termination,
                rating_delta,
            } => ServerMsg::GameEnd {
                game_id: game_id.as_str().to_string(),
                result_vector: result_vector.clone(),
                termination: Termination::from_core(*termination)?,
                rating_delta: rating_delta.clone(),
            },
            CoreServer::Pong { t, server_t } => ServerMsg::Pong {
                t: *t,
                server_t: *server_t,
            },
            CoreServer::Error { code, msg } => ServerMsg::Error {
                code: ErrorCode::from_core(*code)?,
                msg: msg.clone(),
            },
            CoreServer::DrawOffered { game_id, by } => ServerMsg::DrawOffered {
                game_id: game_id.as_str().to_string(),
                by: *by,
            },
        })
    }
}

impl GameEvent {
    fn to_core(&self) -> Result<CoreEvent, FfiCodecError> {
        use pgc_protocol::{
            PoolId, SeatColor as CoreSeatColor, SeatRating as CoreSeatRating, TimeControlId, UserId,
        };
        Ok(match self {
            GameEvent::GameCreated {
                pool_id,
                rated,
                seats,
                start_fen,
                time_control,
            } => CoreEvent::GameCreated {
                pool_id: PoolId::from(pool_id.as_str()),
                rated: *rated,
                seats: seats
                    .iter()
                    .map(|s| CoreSeatColor {
                        seat_idx: s.seat_idx,
                        color: s.color,
                    })
                    .collect(),
                start_fen: start_fen.clone(),
                time_control: TimeControlId::from(time_control.as_str()),
            },
            GameEvent::PlayerJoined { seat_idx, user_id } => CoreEvent::PlayerJoined {
                seat_idx: *seat_idx,
                user_id: UserId::from(user_id.as_str()),
            },
            GameEvent::MovePlayed {
                ply,
                seat_idx,
                uci,
                clock_after_ms,
                server_ts,
            } => CoreEvent::MovePlayed {
                ply: *ply,
                seat_idx: *seat_idx,
                uci: uci.clone(),
                clock_after_ms: *clock_after_ms,
                server_ts: *server_ts,
            },
            GameEvent::DrawOffered { seat_idx } => CoreEvent::DrawOffered {
                seat_idx: *seat_idx,
            },
            GameEvent::DrawAccepted { seat_idx } => CoreEvent::DrawAccepted {
                seat_idx: *seat_idx,
            },
            GameEvent::Resigned { seat_idx } => CoreEvent::Resigned {
                seat_idx: *seat_idx,
            },
            GameEvent::Aborted { by } => CoreEvent::Aborted { by: *by },
            GameEvent::TakebackReq { seat_idx } => CoreEvent::TakebackReq {
                seat_idx: *seat_idx,
            },
            GameEvent::TakebackAccepted { seat_idx } => CoreEvent::TakebackAccepted {
                seat_idx: *seat_idx,
            },
            GameEvent::ClockFlagged { seat_idx } => CoreEvent::ClockFlagged {
                seat_idx: *seat_idx,
            },
            GameEvent::GameEnded {
                result_vector,
                termination,
            } => CoreEvent::GameEnded {
                result_vector: result_vector.clone(),
                termination: termination.to_core(),
            },
            GameEvent::RatingApplied { pool_id, per_seat } => CoreEvent::RatingApplied {
                pool_id: PoolId::from(pool_id.as_str()),
                per_seat: per_seat
                    .iter()
                    .map(|r| CoreSeatRating {
                        seat_idx: r.seat_idx,
                        before: r.before,
                        after: r.after,
                        rd: r.rd,
                        vol: r.vol,
                    })
                    .collect(),
            },
        })
    }

    fn from_core(ev: &CoreEvent) -> Result<Self, FfiCodecError> {
        Ok(match ev {
            CoreEvent::GameCreated {
                pool_id,
                rated,
                seats,
                start_fen,
                time_control,
            } => GameEvent::GameCreated {
                pool_id: pool_id.as_str().to_string(),
                rated: *rated,
                seats: seats
                    .iter()
                    .map(|s| SeatColor {
                        seat_idx: s.seat_idx,
                        color: s.color,
                    })
                    .collect(),
                start_fen: start_fen.clone(),
                time_control: time_control.as_str().to_string(),
            },
            CoreEvent::PlayerJoined { seat_idx, user_id } => GameEvent::PlayerJoined {
                seat_idx: *seat_idx,
                user_id: user_id.as_str().to_string(),
            },
            CoreEvent::MovePlayed {
                ply,
                seat_idx,
                uci,
                clock_after_ms,
                server_ts,
            } => GameEvent::MovePlayed {
                ply: *ply,
                seat_idx: *seat_idx,
                uci: uci.clone(),
                clock_after_ms: *clock_after_ms,
                server_ts: *server_ts,
            },
            CoreEvent::DrawOffered { seat_idx } => GameEvent::DrawOffered {
                seat_idx: *seat_idx,
            },
            CoreEvent::DrawAccepted { seat_idx } => GameEvent::DrawAccepted {
                seat_idx: *seat_idx,
            },
            CoreEvent::Resigned { seat_idx } => GameEvent::Resigned {
                seat_idx: *seat_idx,
            },
            CoreEvent::Aborted { by } => GameEvent::Aborted { by: *by },
            CoreEvent::TakebackReq { seat_idx } => GameEvent::TakebackReq {
                seat_idx: *seat_idx,
            },
            CoreEvent::TakebackAccepted { seat_idx } => GameEvent::TakebackAccepted {
                seat_idx: *seat_idx,
            },
            CoreEvent::ClockFlagged { seat_idx } => GameEvent::ClockFlagged {
                seat_idx: *seat_idx,
            },
            CoreEvent::GameEnded {
                result_vector,
                termination,
            } => GameEvent::GameEnded {
                result_vector: result_vector.clone(),
                termination: Termination::from_core(*termination)?,
            },
            CoreEvent::RatingApplied { pool_id, per_seat } => GameEvent::RatingApplied {
                pool_id: pool_id.as_str().to_string(),
                per_seat: per_seat
                    .iter()
                    .map(|r| SeatRating {
                        seat_idx: r.seat_idx,
                        before: r.before,
                        after: r.after,
                        rd: r.rd,
                        vol: r.vol,
                    })
                    .collect(),
            },
        })
    }
}

impl EventEnvelope {
    fn to_core(&self) -> Result<CoreEnvelope, FfiCodecError> {
        Ok(CoreEnvelope {
            stream_id: self.stream_id.clone(),
            seq: self.seq,
            schema_ver: self.schema_ver,
            ts: self.ts,
            event: self.event.to_core()?,
        })
    }
    fn from_core(e: &CoreEnvelope) -> Result<Self, FfiCodecError> {
        Ok(EventEnvelope {
            stream_id: e.stream_id.clone(),
            seq: e.seq,
            schema_ver: e.schema_ver,
            ts: e.ts,
            event: GameEvent::from_core(&e.event)?,
        })
    }
}

// --- exported encode/decode surface (delegate-only) -------------------------

/// Encode a client → server message to a wire frame (§4A.1).
#[uniffi::export]
pub fn encode_client(msg: ClientMsg) -> Result<Vec<u8>, FfiCodecError> {
    Ok(pgc_protocol::encode_client(&msg.to_core())?)
}

/// Decode a wire frame as a client → server message (server-side decode path).
#[uniffi::export]
pub fn decode_client(frame: Vec<u8>) -> Result<ClientMsg, FfiCodecError> {
    let core = pgc_protocol::decode_client(&frame)?;
    Ok(ClientMsg::from_core(&core))
}

/// Encode a server → client message to a wire frame (§4A.1).
#[uniffi::export]
pub fn encode_server(msg: ServerMsg) -> Result<Vec<u8>, FfiCodecError> {
    Ok(pgc_protocol::encode_server(&msg.to_core()?)?)
}

/// Decode a wire frame as a server → client message (client-side decode path).
#[uniffi::export]
pub fn decode_server(frame: Vec<u8>) -> Result<ServerMsg, FfiCodecError> {
    let core = pgc_protocol::decode_server(&frame)?;
    ServerMsg::from_core(&core)
}

/// Encode a persisted event to its stored byte form (§4A.8).
#[uniffi::export]
pub fn encode_event(env: EventEnvelope) -> Result<Vec<u8>, FfiCodecError> {
    Ok(pgc_protocol::encode_event(&env.to_core()?)?)
}

/// Decode a stored event from its byte form (§4A.8).
#[uniffi::export]
pub fn decode_event(bytes: Vec<u8>) -> Result<EventEnvelope, FfiCodecError> {
    let core = pgc_protocol::decode_event(&bytes)?;
    EventEnvelope::from_core(&core)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn client_round_trip_through_ffi() {
        let m = ClientMsg::Move {
            game_id: "f47ac10b-58cc-4372-a567-0e02b2c3d479".into(),
            ply_expected: 17,
            uci: "e2e4".into(),
        };
        let bytes = encode_client(m.clone()).unwrap();
        assert_eq!(decode_client(bytes).unwrap(), m);
    }

    #[test]
    fn server_round_trip_through_ffi() {
        let m = ServerMsg::GameEnd {
            game_id: "f47ac10b-58cc-4372-a567-0e02b2c3d479".into(),
            result_vector: vec![2, 0],
            termination: Termination::Mate,
            rating_delta: vec![12, -12],
        };
        let bytes = encode_server(m.clone()).unwrap();
        assert_eq!(decode_server(bytes).unwrap(), m);
    }

    #[test]
    fn event_round_trip_through_ffi() {
        let e = EventEnvelope {
            stream_id: "game/f47ac10b-58cc-4372-a567-0e02b2c3d479".into(),
            seq: 3,
            schema_ver: 0,
            ts: 1_728_000_000_000,
            event: GameEvent::MovePlayed {
                ply: 1,
                seat_idx: 0,
                uci: "e2e4".into(),
                clock_after_ms: 299_000,
                server_ts: 1_728_000_000_000,
            },
        };
        let bytes = encode_event(e.clone()).unwrap();
        assert_eq!(decode_event(bytes).unwrap(), e);
    }

    #[test]
    fn bad_frame_is_error_not_panic() {
        assert!(decode_client(vec![]).is_err());
        assert!(decode_server(vec![0x01, 0x09]).is_err()); // wrong direction
        assert!(decode_event(vec![0xff]).is_err());
    }

    #[test]
    fn ffi_bytes_match_core_bytes() {
        // The FFI surface must produce byte-identical output to the core codec —
        // it only maps types, never touches bytes. (The parity guarantee.)
        let core = pgc_protocol::ClientMsg::Ping { t: 42 };
        let core_bytes = pgc_protocol::encode_client(&core).unwrap();
        let ffi_bytes = encode_client(ClientMsg::Ping { t: 42 }).unwrap();
        assert_eq!(core_bytes, ffi_bytes);
    }
}
