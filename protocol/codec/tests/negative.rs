//! Negative-decode tests (SPEC-S2-PROTOCOL §5, §8; PLAN-CODEC §4.1).
//!
//! Every malformed input must return the SPECIFIED `CodecError` variant and
//! **never panic** — a panic across the FFI/wasm/cgo boundary is a client hard
//! crash. We assert `is_err()` and the exact variant, never `unwrap`. This is
//! the per-branch decode coverage the engine lesson calls for: round-trip tests
//! only exercise the encoder's own output, so each external-input failure path
//! needs its own test.

use pgc_protocol::{
    decode_client, decode_event, decode_server, encode_client, encode_server, ClientMsg,
    CodecError, GameId, ServerMsg, MAX_FRAME_BYTES,
};

// --- truncated / too-short --------------------------------------------------

#[test]
fn empty_frame_is_too_short() {
    assert_eq!(decode_client(&[]), Err(CodecError::TooShort(0)));
    assert_eq!(decode_server(&[]), Err(CodecError::TooShort(0)));
}

#[test]
fn one_byte_frame_is_too_short() {
    assert_eq!(decode_client(&[0x01]), Err(CodecError::TooShort(1)));
}

#[test]
fn truncated_payload_is_malformed_not_panic() {
    // A valid MOVE frame, then chop its payload mid-string.
    let full = encode_client(&ClientMsg::Move {
        game_id: GameId::from("f47ac10b-58cc-4372-a567-0e02b2c3d479"),
        ply_expected: 17,
        uci: "e2e4".to_string(),
    })
    .unwrap();
    let truncated = &full[..full.len() - 3];
    let res = decode_client(truncated);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload, got {res:?}"
    );
}

#[test]
fn trailing_garbage_after_payload_is_malformed() {
    // A complete PING frame plus one extra byte → must be rejected (no silent
    // accept of trailing bytes).
    let mut frame = encode_client(&ClientMsg::Ping { t: 42 }).unwrap();
    frame.push(0xAB);
    let res = decode_client(&frame);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload for trailing garbage, got {res:?}"
    );
}

// --- unsupported version ----------------------------------------------------

#[test]
fn unsupported_version_is_rejected() {
    // ver=0x02 (not v1), type=PING, a plausible payload.
    let frame = [0x02, 0x09, 0x00];
    assert_eq!(
        decode_client(&frame),
        Err(CodecError::UnsupportedVersion(0x02))
    );
    assert_eq!(
        decode_server(&[0x00, 0x81, 0x00]),
        Err(CodecError::UnsupportedVersion(0x00))
    );
}

// --- unknown type -----------------------------------------------------------

#[test]
fn unknown_client_type_is_rejected() {
    // ver=1, type=0x7F (a client-direction code with no message), empty payload.
    let frame = [0x01, 0x7F];
    assert_eq!(decode_client(&frame), Err(CodecError::UnknownType(0x7F)));
}

#[test]
fn unknown_server_type_is_rejected() {
    // ver=1, type=0xF0 (server-direction, unassigned).
    let frame = [0x01, 0xF0];
    assert_eq!(decode_server(&frame), Err(CodecError::UnknownType(0xF0)));
}

// --- wrong direction --------------------------------------------------------

#[test]
fn server_type_on_client_path_is_wrong_direction() {
    // A real MATCHED (server) frame fed to the client/server decoder.
    let matched = encode_server(&ServerMsg::Pong { t: 1, server_t: 2 }).unwrap();
    let res = decode_client(&matched);
    assert!(
        matches!(res, Err(CodecError::WrongDirection(0x86, "client"))),
        "expected WrongDirection(0x86, client), got {res:?}"
    );
}

#[test]
fn client_type_on_server_path_is_wrong_direction() {
    let ping = encode_client(&ClientMsg::Ping { t: 1 }).unwrap();
    let res = decode_server(&ping);
    assert!(
        matches!(res, Err(CodecError::WrongDirection(0x09, "server"))),
        "expected WrongDirection(0x09, server), got {res:?}"
    );
}

// --- bad enum discriminant --------------------------------------------------

#[test]
fn bad_reason_discriminant_is_rejected() {
    // MOVE_REJECTED with reason=0x7E (not in §4A.4). Build the frame by hand:
    // ver, type, game_id(str36), ply_expected(u32 varint), reason(u8).
    let gid = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
    let mut frame = vec![0x01, 0x83];
    frame.push(gid.len() as u8); // 0x24 varint length
    frame.extend_from_slice(gid.as_bytes());
    frame.push(0x12); // ply_expected = 18
    frame.push(0x7E); // bogus reason
    let res = decode_server(&frame);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload for bad reason, got {res:?}"
    );
}

#[test]
fn bad_event_type_discriminant_is_rejected() {
    // Event envelope with type byte 0x7F (not in §4A.9).
    // stream_id(str), seq(u32), type(u8), schema_ver(u16), ts(u64), body...
    let stream = "game/f47ac10b-58cc-4372-a567-0e02b2c3d479";
    let mut frame = vec![stream.len() as u8];
    frame.extend_from_slice(stream.as_bytes());
    frame.push(0x01); // seq = 1
    frame.push(0x7F); // bogus event type
    frame.push(0x00); // schema_ver = 0
    frame.push(0x00); // ts = 0
    let res = decode_event(&frame);
    assert_eq!(res, Err(CodecError::UnknownType(0x7F)));
}

// --- oversize ---------------------------------------------------------------

#[test]
fn oversize_frame_is_rejected_on_decode() {
    // A frame larger than the §4A.1 4 KB cap → TooLarge, before any parse.
    let big = vec![0x01u8; MAX_FRAME_BYTES + 1];
    assert_eq!(
        decode_client(&big),
        Err(CodecError::TooLarge(MAX_FRAME_BYTES + 1))
    );
}

#[test]
fn oversize_frame_is_rejected_on_encode() {
    // A STATE with a FEN past the cap → encode returns TooLarge, never a frame
    // that would be rejected downstream. (Defensive: the §4A.1 cap is primarily
    // a decode-side protocol check, but encoding an oversize frame is a bug.)
    let huge_fen = "x".repeat(MAX_FRAME_BYTES);
    let msg = ServerMsg::State {
        game_id: GameId::from("f47ac10b-58cc-4372-a567-0e02b2c3d479"),
        state: pgc_protocol::StateBody {
            fen: huge_fen,
            turn: 0,
            clocks: vec![1000, 1000],
        },
    };
    let res = encode_server(&msg);
    assert!(
        matches!(res, Err(CodecError::TooLarge(_))),
        "expected TooLarge on oversize encode, got {res:?}"
    );
}

// --- closed-domain u8 fields (result_vector, color) -------------------------
//
// result_vector (0/1/2, §4A.3) and color (0/1, §4A.8) are frozen closed domains
// carried as raw u8/[u8]. An out-of-domain byte is game-truth garbage and must
// be rejected at the codec boundary (§8), exactly like an unknown enum8
// discriminant — these are the tests for the fields that were previously passed
// through verbatim.

#[test]
fn out_of_domain_result_vector_rejected_on_wire() {
    // GAME_END with result_vector = [7, 0] (7 ∉ {0,1,2}). Build by hand:
    // ver,type, game_id(str36), result_vector([u8] len+bytes), termination,
    // rating_delta([i16] len+bytes).
    let gid = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
    let mut frame = vec![0x01, 0x85];
    frame.push(gid.len() as u8);
    frame.extend_from_slice(gid.as_bytes());
    frame.push(0x02); // result_vector len 2
    frame.push(0x07); // bogus result
    frame.push(0x00); // loss
    frame.push(0x01); // termination = MATE
    frame.push(0x02); // rating_delta len 2
    frame.push(0x00); // 0 (zigzag)
    frame.push(0x00); // 0
    let res = decode_server(&frame);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload for result_vector=[7], got {res:?}"
    );
}

#[test]
fn out_of_domain_result_vector_rejected_in_event() {
    // GameEnded event with result_vector=[3]. Envelope: stream_id, seq, type,
    // schema_ver, ts, body(result_vector:[u8], termination:enum8).
    let stream = "game/f47ac10b-58cc-4372-a567-0e02b2c3d479";
    let mut frame = vec![stream.len() as u8];
    frame.extend_from_slice(stream.as_bytes());
    frame.push(0x0b); // seq = 11
    frame.push(0x0b); // type = GameEnded (0x0B)
    frame.push(0x00); // schema_ver = 0
    frame.push(0x00); // ts = 0
    frame.push(0x01); // result_vector len 1
    frame.push(0x03); // bogus result 3
    frame.push(0x01); // termination = MATE
    let res = decode_event(&frame);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload for event result_vector=[3], got {res:?}"
    );
}

#[test]
fn out_of_domain_color_rejected_in_event() {
    // GameCreated with a seat color = 3 (∉ {0,1}). Body: pool_id:str, rated:bool,
    // seats:[{seat_idx:u8,color:u8}], start_fen:str, time_control:str.
    let stream = "game/f47ac10b-58cc-4372-a567-0e02b2c3d479";
    let mut frame = vec![stream.len() as u8];
    frame.extend_from_slice(stream.as_bytes());
    frame.push(0x01); // seq = 1
    frame.push(0x01); // type = GameCreated (0x01)
    frame.push(0x00); // schema_ver = 0
    frame.push(0x00); // ts = 0
                      // body:
    frame.push(0x01); // pool_id len 1
    frame.push(b'p');
    frame.push(0x01); // rated = true
    frame.push(0x01); // seats len 1
    frame.push(0x00); // seat_idx 0
    frame.push(0x03); // color 3 (bogus)
    frame.push(0x01); // start_fen len 1
    frame.push(b'f');
    frame.push(0x02); // time_control len 2
    frame.extend_from_slice(b"tc");
    let res = decode_event(&frame);
    assert!(
        matches!(res, Err(CodecError::MalformedPayload(_))),
        "expected MalformedPayload for event color=3, got {res:?}"
    );
}

#[test]
fn in_domain_result_and_color_still_accepted() {
    // Guard against over-rejection: every valid result (0/1/2) and color (0/1)
    // must still decode. (Round-trip of the corpus covers the happy path too,
    // but assert the boundary values here explicitly.)
    use pgc_protocol::{encode_event, EventEnvelope, GameEvent, PoolId, SeatColor, TimeControlId};
    for color in [0u8, 1u8] {
        let env = EventEnvelope {
            stream_id: "game/x".into(),
            seq: 1,
            schema_ver: 0,
            ts: 0,
            event: GameEvent::GameCreated {
                pool_id: PoolId::from("p"),
                rated: true,
                seats: vec![SeatColor { seat_idx: 0, color }],
                start_fen: "f".into(),
                time_control: TimeControlId::from("tc"),
            },
        };
        let b = encode_event(&env).unwrap();
        assert!(decode_event(&b).is_ok(), "color={color} must decode");
    }
    for rv in [vec![0u8, 1], vec![2u8, 0], vec![1u8, 2]] {
        let msg = ServerMsg::GameEnd {
            game_id: GameId::from("f47ac10b-58cc-4372-a567-0e02b2c3d479"),
            result_vector: rv.clone(),
            termination: pgc_protocol::Termination::MATE,
            rating_delta: vec![0, 0],
        };
        let b = encode_server(&msg).unwrap();
        assert!(
            decode_server(&b).is_ok(),
            "result_vector={rv:?} must decode"
        );
    }
}

// --- encode-side domain validation (GS-4a / S1) -----------------------------
//
// encode_event must reject the SAME out-of-domain closed-domain values that
// decode_event rejects, so a durable store built on this seam can never persist
// an event frame it cannot read back (NFR-9). Before this fix encode_event was
// permissive while decode_event validated — a write/read asymmetry.

#[test]
fn out_of_domain_termination_rejected_on_encode() {
    use pgc_protocol::{encode_event, EventEnvelope, GameEvent};
    let env = EventEnvelope {
        stream_id: "game/x".into(),
        seq: 1,
        schema_ver: 0,
        ts: 0,
        event: GameEvent::GameEnded {
            result_vector: vec![2, 0],
            termination: pgc_protocol::Termination(0x00), // 0x00 is reserved/unknown
        },
    };
    assert!(
        matches!(encode_event(&env), Err(CodecError::MalformedPayload(_))),
        "encode_event must reject termination 0x00"
    );
}

#[test]
fn out_of_domain_result_rejected_on_encode() {
    use pgc_protocol::{encode_event, EventEnvelope, GameEvent};
    let env = EventEnvelope {
        stream_id: "game/x".into(),
        seq: 1,
        schema_ver: 0,
        ts: 0,
        event: GameEvent::GameEnded {
            result_vector: vec![9, 0], // 9 ∉ {0,1,2}
            termination: pgc_protocol::Termination::MATE,
        },
    };
    assert!(
        matches!(encode_event(&env), Err(CodecError::MalformedPayload(_))),
        "encode_event must reject result 9"
    );
}

#[test]
fn out_of_domain_color_rejected_on_encode() {
    use pgc_protocol::{encode_event, EventEnvelope, GameEvent, PoolId, SeatColor, TimeControlId};
    let env = EventEnvelope {
        stream_id: "game/x".into(),
        seq: 1,
        schema_ver: 0,
        ts: 0,
        event: GameEvent::GameCreated {
            pool_id: PoolId::from("p"),
            rated: true,
            seats: vec![SeatColor {
                seat_idx: 0,
                color: 7,
            }], // 7 ∉ {0,1}
            start_fen: "f".into(),
            time_control: TimeControlId::from("tc"),
        },
    };
    assert!(
        matches!(encode_event(&env), Err(CodecError::MalformedPayload(_))),
        "encode_event must reject seat color 7"
    );
}

// --- GameEvent serde variant-index freeze (GS-4a drift guard) ---------------
//
// The GS-4a cgo boundary intermediate encodes a `GameEvent` by its serde VARIANT
// INDEX (declaration order), and the Go mirror hard-codes those indices
// (vGameCreated=0 .. vRatingApplied=11). The §4A frame encodes by the explicit
// `event_type()` byte instead, so the frame-based golden_freeze test does NOT
// notice a variant REORDER — but a reorder would silently shift the intermediate
// index and corrupt the Go round-trip. This pins each variant's serde index, so
// a reorder/insertion fails HERE, at the source, before it reaches GS-4's store.
#[test]
fn game_event_serde_variant_index_is_frozen() {
    use pgc_protocol::types::{PoolId, SeatColor, SeatRating, Termination, TimeControlId};
    use pgc_protocol::GameEvent;

    // postcard serializes an enum as uvarint(variant_index) ++ body; for indices
    // 0..=11 the uvarint is a single byte == the index.
    fn index_of(ev: &GameEvent) -> u8 {
        postcard::to_allocvec(ev).expect("event serializes")[0]
    }

    assert_eq!(
        index_of(&GameEvent::GameCreated {
            pool_id: PoolId::from("p"),
            rated: true,
            seats: vec![SeatColor {
                seat_idx: 0,
                color: 0
            }],
            start_fen: "f".into(),
            time_control: TimeControlId::from("tc"),
        }),
        0,
        "GameCreated must be serde variant 0"
    );
    assert_eq!(
        index_of(&GameEvent::PlayerJoined {
            seat_idx: 0,
            user_id: "u".into()
        }),
        1
    );
    assert_eq!(
        index_of(&GameEvent::MovePlayed {
            ply: 0,
            seat_idx: 0,
            uci: "e2e4".into(),
            clock_after_ms: 0,
            server_ts: 0
        }),
        2
    );
    assert_eq!(index_of(&GameEvent::DrawOffered { seat_idx: 0 }), 3);
    assert_eq!(index_of(&GameEvent::DrawAccepted { seat_idx: 0 }), 4);
    assert_eq!(index_of(&GameEvent::Resigned { seat_idx: 0 }), 5);
    assert_eq!(index_of(&GameEvent::Aborted { by: 0 }), 6);
    assert_eq!(index_of(&GameEvent::TakebackReq { seat_idx: 0 }), 7);
    assert_eq!(index_of(&GameEvent::TakebackAccepted { seat_idx: 0 }), 8);
    assert_eq!(index_of(&GameEvent::ClockFlagged { seat_idx: 0 }), 9);
    assert_eq!(
        index_of(&GameEvent::GameEnded {
            result_vector: vec![],
            termination: Termination::MATE
        }),
        10
    );
    assert_eq!(
        index_of(&GameEvent::RatingApplied {
            pool_id: PoolId::from("p"),
            per_seat: Vec::<SeatRating>::new()
        }),
        11
    );
}

// --- a frame exactly at the cap is accepted (boundary) ----------------------

#[test]
fn frame_at_cap_boundary_encodes() {
    // Find a FEN length that lands the whole frame at or just under 4096 so the
    // boundary itself is exercised (not just over/under by a lot).
    let gid = GameId::from("f47ac10b-58cc-4372-a567-0e02b2c3d479");
    // Grow a FEN until the encoded frame is within 1 byte of the cap.
    for fen_len in (3900..MAX_FRAME_BYTES).step_by(1) {
        let msg = ServerMsg::State {
            game_id: gid.clone(),
            state: pgc_protocol::StateBody {
                fen: "y".repeat(fen_len),
                turn: 0,
                clocks: vec![1, 1],
            },
        };
        match encode_server(&msg) {
            Ok(bytes) => assert!(bytes.len() <= MAX_FRAME_BYTES),
            Err(CodecError::TooLarge(_)) => {
                // Once we cross the cap, we're done exercising the boundary.
                return;
            }
            Err(e) => panic!("unexpected error near cap: {e}"),
        }
    }
}
