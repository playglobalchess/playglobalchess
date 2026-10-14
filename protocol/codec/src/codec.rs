//! The frame codec: the `ver | type | payload` envelope (SPEC-S2-PROTOCOL
//! §4A.1) over postcard-encoded payloads.
//!
//! ## Envelope vs payload — the ADR-14 reading
//!
//! PLAN-CODEC §3 settles this: ADR-14's "the codec owns framing" means the
//! *payload* bytes are whatever postcard emits (varint ints, length-prefixed
//! strings/sequences), while the two **dispatch header bytes** `ver` and `type`
//! are written and read **directly** by this module — they are the frame's
//! fixed envelope (§4A.1 offsets 0 and 1), not a postcard-serialized value.
//! §4.1 says exactly this: "`encode`/`decode` over the `ver | type | payload`
//! envelope, dispatching on the frozen type codes".
//!
//! So the on-wire shape is:
//!
//! ```text
//! byte 0   : ver   (0x01 for v1)
//! byte 1   : type  (§4A.2 frozen code; high bit = direction)
//! byte 2.. : postcard(payload-struct in §4A.3 field order)
//! ```
//!
//! ## No panics (PLAN-CODEC §4.1)
//!
//! Every decode entry point is total: malformed input returns a
//! [`CodecError`], never a panic — a panic across the FFI/wasm/cgo boundary is
//! a client hard crash. Encoders cannot fail on valid in-memory values; the
//! only encode-side error is the defensive §4A.1 oversize check.

use serde::Serialize;

use crate::error::{CodecError, MAX_FRAME_BYTES};
use crate::event::{
    AbortedBody, EventEnvelope, GameCreatedBody, GameEndedBody, GameEvent, MovePlayedBody,
    PlayerJoinedBody, RatingAppliedBody, SeatIdxBody,
};
use crate::message::{
    client_code, server_code, ClientMsg, ErrorPayload, GameEndPayload, MatchedPayload,
    MoveAcceptedPayload, MovePayload, MoveRejectedPayload, PongPayload, SeekPayload, ServerMsg,
    StatePayload,
};
use crate::types::EventType;

/// Protocol major version for v1 frames (§4A.1).
pub const VER_V1: u8 = 0x01;

// --- small helpers ----------------------------------------------------------

/// postcard-serialize a payload struct, mapping any error to a typed
/// `MalformedPayload` (serialization of a valid in-memory value does not fail
/// in practice; this keeps the path total without an `unwrap`).
fn ser<T: Serialize>(value: &T) -> Result<Vec<u8>, CodecError> {
    postcard::to_allocvec(value).map_err(|e| CodecError::MalformedPayload(e.to_string()))
}

/// Deserialize a payload struct from the full remaining frame bytes, requiring
/// that ALL bytes are consumed — trailing garbage after a well-formed payload
/// is a malformed frame, not an accepted message (SPEC-S2-PROTOCOL §8).
fn de_exact<T: for<'de> serde::Deserialize<'de>>(bytes: &[u8]) -> Result<T, CodecError> {
    let (value, rest) = postcard::take_from_bytes::<T>(bytes)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))?;
    if !rest.is_empty() {
        return Err(CodecError::MalformedPayload(format!(
            "{} trailing byte(s) after payload",
            rest.len()
        )));
    }
    Ok(value)
}

/// Validate every entry of a decoded `result_vector` against the frozen §4A.3
/// domain (`0`/`1`/`2`). Rejects an out-of-contract byte with a typed error
/// rather than passing garbage to the server/clients (§8). Shared by the
/// GAME_END wire message and the GameEnded event.
fn check_result_vector(rv: &[u8]) -> Result<(), CodecError> {
    if let Some(bad) = rv
        .iter()
        .copied()
        .find(|&v| !crate::types::is_known_result(v))
    {
        return Err(CodecError::MalformedPayload(format!(
            "result_vector entry 0x{bad:02x} is not a frozen result value (0=loss,1=draw,2=win)"
        )));
    }
    Ok(())
}

/// Assemble a frame from its header bytes and a payload, enforcing the §4A.1
/// 4 KB cap against the ACTUAL encoded size (PLAN-CODEC §3).
fn frame(ver: u8, type_code: u8, payload: Vec<u8>) -> Result<Vec<u8>, CodecError> {
    let total = 2 + payload.len();
    if total > MAX_FRAME_BYTES {
        return Err(CodecError::TooLarge(total));
    }
    let mut out = Vec::with_capacity(total);
    out.push(ver);
    out.push(type_code);
    out.extend_from_slice(&payload);
    Ok(out)
}

/// Split a frame into `(ver, type, payload)`, running the version check and the
/// 4 KB cap. Shared prefix for both direction decoders.
fn split_frame(frame: &[u8]) -> Result<(u8, &[u8]), CodecError> {
    if frame.len() > MAX_FRAME_BYTES {
        return Err(CodecError::TooLarge(frame.len()));
    }
    if frame.len() < 2 {
        return Err(CodecError::TooShort(frame.len()));
    }
    let ver = frame[0];
    if ver != VER_V1 {
        return Err(CodecError::UnsupportedVersion(ver));
    }
    let type_code = frame[1];
    Ok((type_code, &frame[2..]))
}

// --- client → server messages ----------------------------------------------

/// Encode a `C→S` message to a wire frame (§4A.1).
pub fn encode_client(msg: &ClientMsg) -> Result<Vec<u8>, CodecError> {
    let type_code = msg.type_code();
    let payload = match msg {
        ClientMsg::Authenticate { token } => ser(token)?,
        ClientMsg::Seek {
            pool_id,
            time_control,
            rated,
        } => ser(&SeekPayload {
            pool_id: pool_id.clone(),
            time_control: time_control.clone(),
            rated: *rated,
        })?,
        ClientMsg::Move {
            game_id,
            ply_expected,
            uci,
        } => ser(&MovePayload {
            game_id: game_id.clone(),
            ply_expected: *ply_expected,
            uci: uci.clone(),
        })?,
        ClientMsg::OfferDraw { game_id }
        | ClientMsg::Resign { game_id }
        | ClientMsg::Abort { game_id }
        | ClientMsg::Takeback { game_id }
        | ClientMsg::Rematch { game_id } => ser(game_id)?,
        ClientMsg::Ping { t } => ser(t)?,
    };
    frame(VER_V1, type_code, payload)
}

/// Decode a wire frame as a `C→S` message. This is the **server's** decode
/// path: a server-direction type code (high bit set) is a wrong-direction
/// error (§3.1, §4A.2).
pub fn decode_client(frame: &[u8]) -> Result<ClientMsg, CodecError> {
    let (type_code, payload) = split_frame(frame)?;
    // Direction check: C→S codes are 0x01–0x7F (high bit clear).
    if type_code & 0x80 != 0 {
        return Err(CodecError::WrongDirection(type_code, "client"));
    }
    match type_code {
        client_code::AUTHENTICATE => Ok(ClientMsg::Authenticate {
            token: de_exact(payload)?,
        }),
        client_code::SEEK => {
            let p: SeekPayload = de_exact(payload)?;
            Ok(ClientMsg::Seek {
                pool_id: p.pool_id,
                time_control: p.time_control,
                rated: p.rated,
            })
        }
        client_code::MOVE => {
            let p: MovePayload = de_exact(payload)?;
            Ok(ClientMsg::Move {
                game_id: p.game_id,
                ply_expected: p.ply_expected,
                uci: p.uci,
            })
        }
        client_code::OFFER_DRAW => Ok(ClientMsg::OfferDraw {
            game_id: de_exact(payload)?,
        }),
        client_code::RESIGN => Ok(ClientMsg::Resign {
            game_id: de_exact(payload)?,
        }),
        client_code::ABORT => Ok(ClientMsg::Abort {
            game_id: de_exact(payload)?,
        }),
        client_code::TAKEBACK => Ok(ClientMsg::Takeback {
            game_id: de_exact(payload)?,
        }),
        client_code::REMATCH => Ok(ClientMsg::Rematch {
            game_id: de_exact(payload)?,
        }),
        client_code::PING => Ok(ClientMsg::Ping {
            t: de_exact(payload)?,
        }),
        other => Err(CodecError::UnknownType(other)),
    }
}

// --- server → client messages ----------------------------------------------

/// Encode an `S→C` message to a wire frame (§4A.1).
pub fn encode_server(msg: &ServerMsg) -> Result<Vec<u8>, CodecError> {
    let type_code = msg.type_code();
    let payload = match msg {
        ServerMsg::Matched {
            game_id,
            seat_idx,
            opponent,
            start_state,
        } => ser(&MatchedPayload {
            game_id: game_id.clone(),
            seat_idx: *seat_idx,
            opponent: opponent.clone(),
            start_state: start_state.clone(),
        })?,
        ServerMsg::MoveAccepted {
            game_id,
            ply,
            clock_after_ms,
        } => ser(&MoveAcceptedPayload {
            game_id: game_id.clone(),
            ply: *ply,
            clock_after_ms: *clock_after_ms,
        })?,
        ServerMsg::MoveRejected {
            game_id,
            ply_expected,
            reason,
        } => ser(&MoveRejectedPayload {
            game_id: game_id.clone(),
            ply_expected: *ply_expected,
            reason: *reason,
        })?,
        ServerMsg::State { game_id, state } => ser(&StatePayload {
            game_id: game_id.clone(),
            state: state.clone(),
        })?,
        ServerMsg::GameEnd {
            game_id,
            result_vector,
            termination,
            rating_delta,
        } => ser(&GameEndPayload {
            game_id: game_id.clone(),
            result_vector: result_vector.clone(),
            termination: *termination,
            rating_delta: rating_delta.clone(),
        })?,
        ServerMsg::Pong { t, server_t } => ser(&PongPayload {
            t: *t,
            server_t: *server_t,
        })?,
        ServerMsg::Error { code, msg } => ser(&ErrorPayload {
            code: *code,
            msg: msg.clone(),
        })?,
    };
    frame(VER_V1, type_code, payload)
}

/// Decode a wire frame as an `S→C` message. This is the **client's** decode
/// path: a client-direction type code (high bit clear) is a wrong-direction
/// error (§3.1, §4A.2).
pub fn decode_server(frame: &[u8]) -> Result<ServerMsg, CodecError> {
    let (type_code, payload) = split_frame(frame)?;
    // Direction check: S→C codes are 0x80–0xFF (high bit set).
    if type_code & 0x80 == 0 {
        return Err(CodecError::WrongDirection(type_code, "server"));
    }
    match type_code {
        server_code::MATCHED => {
            let p: MatchedPayload = de_exact(payload)?;
            Ok(ServerMsg::Matched {
                game_id: p.game_id,
                seat_idx: p.seat_idx,
                opponent: p.opponent,
                start_state: p.start_state,
            })
        }
        server_code::MOVE_ACCEPTED => {
            let p: MoveAcceptedPayload = de_exact(payload)?;
            Ok(ServerMsg::MoveAccepted {
                game_id: p.game_id,
                ply: p.ply,
                clock_after_ms: p.clock_after_ms,
            })
        }
        server_code::MOVE_REJECTED => {
            let p: MoveRejectedPayload = de_exact(payload)?;
            if !p.reason.is_known() {
                return Err(CodecError::MalformedPayload(format!(
                    "unknown reason discriminant 0x{:02x}",
                    p.reason.value()
                )));
            }
            Ok(ServerMsg::MoveRejected {
                game_id: p.game_id,
                ply_expected: p.ply_expected,
                reason: p.reason,
            })
        }
        server_code::STATE => {
            let p: StatePayload = de_exact(payload)?;
            Ok(ServerMsg::State {
                game_id: p.game_id,
                state: p.state,
            })
        }
        server_code::GAME_END => {
            let p: GameEndPayload = de_exact(payload)?;
            if !p.termination.is_known() {
                return Err(CodecError::MalformedPayload(format!(
                    "unknown termination discriminant 0x{:02x}",
                    p.termination.value()
                )));
            }
            check_result_vector(&p.result_vector)?;
            Ok(ServerMsg::GameEnd {
                game_id: p.game_id,
                result_vector: p.result_vector,
                termination: p.termination,
                rating_delta: p.rating_delta,
            })
        }
        server_code::PONG => {
            let p: PongPayload = de_exact(payload)?;
            Ok(ServerMsg::Pong {
                t: p.t,
                server_t: p.server_t,
            })
        }
        server_code::ERROR => {
            let p: ErrorPayload = de_exact(payload)?;
            if !p.code.is_known() {
                return Err(CodecError::MalformedPayload(format!(
                    "unknown error code discriminant 0x{:02x}",
                    p.code.value()
                )));
            }
            Ok(ServerMsg::Error {
                code: p.code,
                msg: p.msg,
            })
        }
        other => Err(CodecError::UnknownType(other)),
    }
}

// --- event store ------------------------------------------------------------
//
// The persisted event frame (§4A.8) reuses postcard but has a DIFFERENT
// envelope from the wire: `stream_id, seq, type, schema_ver, ts, body`. There
// is no `ver` byte and no direction — events are internal to the store. The
// `type` byte (§4A.9) is written directly here (like the wire `type`), then the
// matching body struct's postcard bytes.

/// Encode a persisted event to its stored byte form (§4A.8).
///
/// Layout: `postcard(stream_id) | postcard(seq:u32) | type:u8 |
/// postcard(schema_ver:u16) | postcard(ts:u64) | postcard(body)`. The `type`
/// byte is the frozen §4A.9 code derived from the event variant.
pub fn encode_event(env: &EventEnvelope) -> Result<Vec<u8>, CodecError> {
    let mut out = Vec::new();
    // Envelope header, in §4A.8 field order. seq/schema_ver/ts go through
    // postcard (varint) like any other payload field; stream_id is a string.
    out.extend_from_slice(&ser(&env.stream_id)?);
    out.extend_from_slice(&ser(&env.seq)?);
    out.push(env.event.event_type().value()); // `type` byte (§4A.9)
    out.extend_from_slice(&ser(&env.schema_ver)?);
    out.extend_from_slice(&ser(&env.ts)?);
    // Body, in §4A.8 field order.
    out.extend_from_slice(&encode_event_body(&env.event)?);
    Ok(out)
}

/// Serialize a full [`EventEnvelope`] as the GS-4a cgo-boundary **intermediate**
/// (postcard of the whole envelope struct). This is NOT the §4A frame — it is a
/// self-contained, in-process byte payload the Go server hands to
/// `pgc_encode_event` so Rust can rebuild the envelope and call [`encode_event`].
/// Symmetric with [`decode_envelope_intermediate`]; both sides (Rust seam + the
/// Go mirror) use the same postcard derive, so field order is the only contract
/// and it is proven by the GS-4a parity test. Lives here (not in `codec-cgo`) so
/// the C-ABI crate stays delegate-only (no serialization logic).
pub fn encode_envelope_intermediate(env: &EventEnvelope) -> Result<Vec<u8>, CodecError> {
    ser(env)
}

/// Deserialize a GS-4a cgo-boundary intermediate back to an [`EventEnvelope`].
/// The inverse of [`encode_envelope_intermediate`]. A malformed intermediate is a
/// `MalformedPayload` error (the seam maps it to its own `BAD_INTERMEDIATE` code,
/// distinct from a bad §4A frame).
pub fn decode_envelope_intermediate(bytes: &[u8]) -> Result<EventEnvelope, CodecError> {
    postcard::from_bytes::<EventEnvelope>(bytes)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))
}

fn encode_event_body(ev: &GameEvent) -> Result<Vec<u8>, CodecError> {
    match ev {
        GameEvent::GameCreated {
            pool_id,
            rated,
            seats,
            start_fen,
            time_control,
        } => {
            // Validate the frozen closed domain on ENCODE too, so encode and
            // decode agree: a frame this produces must be one decode_event can
            // read back (GS-4a / S1 — otherwise the durable store could persist
            // an un-replayable event, defeating NFR-9). Mirrors the seat-color
            // check in decode_event_body.
            if let Some(bad) = seats
                .iter()
                .map(|s| s.color)
                .find(|&c| !crate::types::is_known_color(c))
            {
                return Err(CodecError::MalformedPayload(format!(
                    "seat color 0x{bad:02x} is not a frozen v1 color value (0=white,1=black)"
                )));
            }
            ser(&GameCreatedBody {
                pool_id: pool_id.clone(),
                rated: *rated,
                seats: seats.clone(),
                start_fen: start_fen.clone(),
                time_control: time_control.clone(),
            })
        }
        GameEvent::PlayerJoined { seat_idx, user_id } => ser(&PlayerJoinedBody {
            seat_idx: *seat_idx,
            user_id: user_id.clone(),
        }),
        GameEvent::MovePlayed {
            ply,
            seat_idx,
            uci,
            clock_after_ms,
            server_ts,
        } => ser(&MovePlayedBody {
            ply: *ply,
            seat_idx: *seat_idx,
            uci: uci.clone(),
            clock_after_ms: *clock_after_ms,
            server_ts: *server_ts,
        }),
        GameEvent::DrawOffered { seat_idx }
        | GameEvent::DrawAccepted { seat_idx }
        | GameEvent::Resigned { seat_idx }
        | GameEvent::TakebackReq { seat_idx }
        | GameEvent::TakebackAccepted { seat_idx }
        | GameEvent::ClockFlagged { seat_idx } => ser(&SeatIdxBody {
            seat_idx: *seat_idx,
        }),
        GameEvent::Aborted { by } => ser(&AbortedBody { by: *by }),
        GameEvent::GameEnded {
            result_vector,
            termination,
        } => {
            // Symmetric with decode (GS-4a / S1): reject an out-of-domain
            // termination or result byte on ENCODE so the store never persists a
            // GameEnded frame decode_event would reject (NFR-9).
            if !termination.is_known() {
                return Err(CodecError::MalformedPayload(format!(
                    "termination 0x{:02x} is not a frozen v1 value",
                    termination.value()
                )));
            }
            check_result_vector(result_vector)?;
            ser(&GameEndedBody {
                result_vector: result_vector.clone(),
                termination: *termination,
            })
        }
        GameEvent::RatingApplied { pool_id, per_seat } => ser(&RatingAppliedBody {
            pool_id: pool_id.clone(),
            per_seat: per_seat.clone(),
        }),
    }
}

/// Decode a stored event from its byte form (§4A.8). Total over all inputs:
/// malformed bytes → [`CodecError`], never a panic.
pub fn decode_event(bytes: &[u8]) -> Result<EventEnvelope, CodecError> {
    // Read the envelope header positionally (§4A.8), carrying the cursor.
    let (stream_id, rest) = postcard::take_from_bytes::<String>(bytes)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))?;
    let (seq, rest) = postcard::take_from_bytes::<u32>(rest)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))?;
    let (&type_byte, rest) = rest.split_first().ok_or(CodecError::MalformedPayload(
        "missing event type byte".into(),
    ))?;
    let (schema_ver, rest) = postcard::take_from_bytes::<u16>(rest)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))?;
    let (ts, rest) = postcard::take_from_bytes::<u64>(rest)
        .map_err(|e| CodecError::MalformedPayload(e.to_string()))?;

    let ev_type = EventType(type_byte);
    if !ev_type.is_known() {
        return Err(CodecError::UnknownType(type_byte));
    }
    let event = decode_event_body(type_byte, rest)?;

    Ok(EventEnvelope {
        stream_id,
        seq,
        schema_ver,
        ts,
        event,
    })
}

fn decode_event_body(type_byte: u8, bytes: &[u8]) -> Result<GameEvent, CodecError> {
    // Dispatch on the frozen §4A.9 `type` byte. `EventType` is a u8-backed
    // newtype (its named values are associated consts, not match-able variants),
    // so we compare the raw byte against each frozen value's `.value()`.
    let ev = if type_byte == EventType::GameCreated.value() {
        let b: GameCreatedBody = de_exact(bytes)?;
        if let Some(bad) = b
            .seats
            .iter()
            .map(|s| s.color)
            .find(|&c| !crate::types::is_known_color(c))
        {
            return Err(CodecError::MalformedPayload(format!(
                "seat color 0x{bad:02x} is not a frozen v1 color value (0=white,1=black)"
            )));
        }
        GameEvent::GameCreated {
            pool_id: b.pool_id,
            rated: b.rated,
            seats: b.seats,
            start_fen: b.start_fen,
            time_control: b.time_control,
        }
    } else if type_byte == EventType::PlayerJoined.value() {
        let b: PlayerJoinedBody = de_exact(bytes)?;
        GameEvent::PlayerJoined {
            seat_idx: b.seat_idx,
            user_id: b.user_id,
        }
    } else if type_byte == EventType::MovePlayed.value() {
        let b: MovePlayedBody = de_exact(bytes)?;
        GameEvent::MovePlayed {
            ply: b.ply,
            seat_idx: b.seat_idx,
            uci: b.uci,
            clock_after_ms: b.clock_after_ms,
            server_ts: b.server_ts,
        }
    } else if type_byte == EventType::DrawOffered.value() {
        GameEvent::DrawOffered {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::DrawAccepted.value() {
        GameEvent::DrawAccepted {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::Resigned.value() {
        GameEvent::Resigned {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::TakebackReq.value() {
        GameEvent::TakebackReq {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::TakebackAccepted.value() {
        GameEvent::TakebackAccepted {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::ClockFlagged.value() {
        GameEvent::ClockFlagged {
            seat_idx: de_exact::<SeatIdxBody>(bytes)?.seat_idx,
        }
    } else if type_byte == EventType::Aborted.value() {
        GameEvent::Aborted {
            by: de_exact::<AbortedBody>(bytes)?.by,
        }
    } else if type_byte == EventType::GameEnded.value() {
        let b: GameEndedBody = de_exact(bytes)?;
        if !b.termination.is_known() {
            return Err(CodecError::MalformedPayload(format!(
                "unknown termination discriminant 0x{:02x}",
                b.termination.value()
            )));
        }
        check_result_vector(&b.result_vector)?;
        GameEvent::GameEnded {
            result_vector: b.result_vector,
            termination: b.termination,
        }
    } else if type_byte == EventType::RatingApplied.value() {
        let b: RatingAppliedBody = de_exact(bytes)?;
        GameEvent::RatingApplied {
            pool_id: b.pool_id,
            per_seat: b.per_seat,
        }
    } else {
        // Unreachable in practice: decode_event rejects a non-frozen type byte
        // via is_known() before calling this. Kept total without a panic.
        return Err(CodecError::UnknownType(type_byte));
    };
    Ok(ev)
}
