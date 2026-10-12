//! Web codec parity test (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.4, NFR-10, ADR-9).
//!
//! Runs the SAME frozen golden frames the mobile parity targets use
//! (`protocol/codec-ffi/tests/codec_parity_oracle.rs`, a subset of
//! `protocol/vectors/vectors.json`) through the compiled wasm codec and asserts
//! each decode→re-encode is byte-identical — the NFR-10 proof on the web
//! runtime. Executed by `wasm-pack test --node` (a real wasm VM), so it exercises
//! the actually-shipped artifact, not a host build.

#![cfg(target_arch = "wasm32")]

use pgc_protocol_wasm::{reencode_client, reencode_event, reencode_server};
use wasm_bindgen_test::*;

// Committed golden frames (hex) — mirror codec_parity_oracle.rs; change in
// lockstep (the parity contract).
const CLIENT_MOVE_PROMO_HEX: &str =
    "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871";
const SERVER_GAME_END_HEX: &str =
    "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817";
const EVENT_RATING_APPLIED_HEX: &str = "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d";

fn from_hex(h: &str) -> Vec<u8> {
    (0..h.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&h[i..i + 2], 16).expect("hex"))
        .collect()
}

#[wasm_bindgen_test]
fn client_decode_reencode_identity() {
    let b = from_hex(CLIENT_MOVE_PROMO_HEX);
    assert_eq!(reencode_client(&b).expect("reencode"), b);
}

#[wasm_bindgen_test]
fn server_decode_reencode_identity() {
    let b = from_hex(SERVER_GAME_END_HEX);
    assert_eq!(reencode_server(&b).expect("reencode"), b);
}

#[wasm_bindgen_test]
fn event_decode_reencode_identity() {
    let b = from_hex(EVENT_RATING_APPLIED_HEX);
    assert_eq!(reencode_event(&b).expect("reencode"), b);
}

#[wasm_bindgen_test]
fn malformed_frame_is_err_not_trap() {
    // A decode failure must be a thrown JS error (Err), never a wasm trap/abort.
    assert!(reencode_client(&[]).is_err());
    assert!(reencode_server(&[0x01, 0x09]).is_err()); // wrong direction
    assert!(reencode_event(&[0xff]).is_err());
}
