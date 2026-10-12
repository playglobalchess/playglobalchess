//! Codec mobile-parity ORACLE (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.3, NFR-10,
//! ADR-9). Mirrors `engine/tests/mobile_parity_oracle.rs`.
//!
//! The mobile parity requirement is that the SAME Rust codec, compiled into the
//! iOS (Swift/UniFFI) and Android (Kotlin/UniFFI) client cores, produces
//! **byte-identical** results to the server. This file is the host-side oracle:
//! it pins a representative frame per codec kind (one of each direction + an
//! event, plus the 5-char promotion edge) that the mobile parity targets
//! (`platform/mobile/{Swift,Kotlin}CodecParityTest.*`) reproduce — each target
//! decodes the frame through the generated binding and asserts the decoded
//! value re-encodes to the identical bytes.
//!
//! The FULL 33-vector byte-for-byte freeze lives in the pure crate's
//! `golden_freeze` test (already green); this slice is deliberately small so it
//! runs fast in a mobile unit-test target, and its hex constants are a subset of
//! `protocol/vectors/vectors.json` — change in lockstep (the parity contract).

use pgc_protocol::{
    decode_client, decode_event, decode_server, encode_client, encode_event, encode_server,
};

/// `client/move_promotion_5char` from the committed corpus — the 5-char UCI edge.
pub const CLIENT_MOVE_PROMO_HEX: &str =
    "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871";
/// `server/game_end` from the committed corpus — a full termination enum + i16s.
pub const SERVER_GAME_END_HEX: &str =
    "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817";
/// `event/rating_applied` from the committed corpus — the f32-bearing event.
pub const EVENT_RATING_APPLIED_HEX: &str = "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d";

fn from_hex(h: &str) -> Vec<u8> {
    (0..h.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&h[i..i + 2], 16).expect("hex"))
        .collect()
}

#[test]
fn oracle_client_decode_reencode_is_identity() {
    let bytes = from_hex(CLIENT_MOVE_PROMO_HEX);
    let msg = decode_client(&bytes).expect("decode");
    assert_eq!(encode_client(&msg).expect("encode"), bytes);
}

#[test]
fn oracle_server_decode_reencode_is_identity() {
    let bytes = from_hex(SERVER_GAME_END_HEX);
    let msg = decode_server(&bytes).expect("decode");
    assert_eq!(encode_server(&msg).expect("encode"), bytes);
}

#[test]
fn oracle_event_decode_reencode_is_identity() {
    let bytes = from_hex(EVENT_RATING_APPLIED_HEX);
    let env = decode_event(&bytes).expect("decode");
    assert_eq!(encode_event(&env).expect("encode"), bytes);
}

/// Documents, in code, that the oracle hex constants are a subset of the frozen
/// corpus — a change here is a deliberate, reviewed update to the parity
/// contract (same discipline as the engine oracle).
#[test]
fn parity_oracle_constants_are_stable() {
    assert!(CLIENT_MOVE_PROMO_HEX.starts_with("0103")); // ver=1, type=MOVE
    assert!(SERVER_GAME_END_HEX.starts_with("0185")); // ver=1, type=GAME_END
    assert!(EVENT_RATING_APPLIED_HEX.contains("0c0c")); // seq=12, type=RatingApplied
}
