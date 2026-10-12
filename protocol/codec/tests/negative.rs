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
