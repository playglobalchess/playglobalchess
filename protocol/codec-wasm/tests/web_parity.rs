//! Web codec parity test (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.4, NFR-10, ADR-9).
//!
//! Runs the SAME frozen golden frames the mobile parity targets use
//! (`protocol/codec-ffi/tests/codec_parity_oracle.rs`, a subset of
//! `protocol/vectors/vectors.json`) through the compiled wasm codec and asserts
//! each decode→re-encode is byte-identical — the NFR-10 proof on the web
//! runtime. Executed by `wasm-pack test --node` (a real wasm VM), so it exercises
//! the actually-shipped artifact, not a host build.

#![cfg(target_arch = "wasm32")]

use pgc_protocol_wasm::{
    decode_client, decode_server, encode_client, encode_server, reencode_client, reencode_event,
    reencode_server,
};
use wasm_bindgen::JsValue;
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

// === S4a typed message seam parity (SPEC-S4-WEB-F01 §3.0, §6) ===============
//
// The seam is correct iff, for every §4A message in the SHARED golden corpus,
// decode(frame) -> typed JS object -> encode(object) reproduces the IDENTICAL
// bytes. That proves the typed TS-facing seam emits exactly the same wire bytes
// as the Rust/Go seams freeze (NFR-10) — the §4A encoding stays Rust-owned and
// the JS object round-trips losslessly. Covers ALL client + server messages,
// including the §4A v1.1 RESYNC (0x0A) and DRAW_OFFERED (0x87) additions.

/// Full golden client-frame corpus (hex), mirroring `protocol/vectors/vectors.json`
/// kind=client. Change in lockstep with that corpus (the parity contract).
const GOLDEN_CLIENT: &[&str] = &[
    "01011e65794a68624763694f694a49557a49314e69392e6865616465722e736967", // authenticate
    "01020e626c69747a2d7374616e6461726409626c69747a2d352b3301",           // seek
    "01032466343761633130622d353863632d343337322d613536372d306530326232633364343739110465326534", // move
    "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871", // move_promotion_5char
    "01042466343761633130622d353863632d343337322d613536372d306530326232633364343739", // offer_draw
    "01052466343761633130622d353863632d343337322d613536372d306530326232633364343739", // resign
    "01062466343761633130622d353863632d343337322d613536372d306530326232633364343739", // abort
    "01072466343761633130622d353863632d343337322d613536372d306530326232633364343739", // takeback
    "01082466343761633130622d353863632d343337322d613536372d306530326232633364343739", // rematch
    "0109fb80cea6a532",                                                               // ping
    "010a2466343761633130622d353863632d343337322d613536372d306530326232633364343739", // resync (§4A v1.1)
];

/// Full golden server-frame corpus (hex), mirroring kind=server.
const GOLDEN_SERVER: &[&str] = &[
    "01812466343761633130622d353863632d343337322d613536372d30653032623263336434373900106772616e646d61737465725f6a616e6538726e62716b626e722f70707070707070702f382f382f382f382f50505050505050502f524e42514b424e522077204b516b71202d203020310002e0a712e0a712", // matched
    "01822466343761633130622d353863632d343337322d613536372d306530326232633364343739129c9412", // move_accepted
    "01832466343761633130622d353863632d343337322d613536372d3065303262326333643437391202", // move_rejected
    "01842466343761633130622d353863632d343337322d613536372d30653032623263336434373943726e62716b626e722f70703170707070702f382f3270352f3450332f354e322f50505050315050502f524e42514b4231522062204b516b7120653320313030203139390002e807d00f", // state_max_fen
    "01842466343761633130622d353863632d343337322d613536372d3065303262326333643437393b726e62716b626e722f70707070707070702f382f382f3450332f382f50505050315050502f524e42514b424e522062204b516b71206533203020310102e0a712e0a712", // state
    "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817", // game_end
    "0186fb80cea6a532c883cea6a532",                                       // pong
    "01872466343761633130622d353863632d343337322d613536372d30653032623263336434373901", // draw_offered (§4A v1.1)
    "018f0509736c6f7720646f776e",                                         // error
];

#[wasm_bindgen_test]
fn client_seam_round_trips_every_golden_frame() {
    for (i, hex) in GOLDEN_CLIENT.iter().enumerate() {
        let frame = from_hex(hex);
        // §4A frame -> typed JS ClientMsg -> §4A frame must be byte-identical.
        let msg = decode_client(&frame).unwrap_or_else(|_| panic!("decode_client #{i}"));
        let reframed = encode_client(msg).unwrap_or_else(|_| panic!("encode_client #{i}"));
        assert_eq!(reframed, frame, "client seam round-trip #{i} ({hex})");
    }
}

#[wasm_bindgen_test]
fn server_seam_round_trips_every_golden_frame() {
    for (i, hex) in GOLDEN_SERVER.iter().enumerate() {
        let frame = from_hex(hex);
        // §4A frame -> typed JS ServerMsg -> §4A frame must be byte-identical.
        let msg = decode_server(&frame).unwrap_or_else(|_| panic!("decode_server #{i}"));
        let reframed = encode_server(msg).unwrap_or_else(|_| panic!("encode_server #{i}"));
        assert_eq!(reframed, frame, "server seam round-trip #{i} ({hex})");
    }
}

#[wasm_bindgen_test]
fn seam_rejects_wrong_direction_and_garbage() {
    // decode_client fed a SERVER frame (high type bit set) → Err, not a trap.
    assert!(decode_client(&from_hex(GOLDEN_SERVER[0])).is_err());
    // decode_server fed a CLIENT frame → Err.
    assert!(decode_server(&from_hex(GOLDEN_CLIENT[0])).is_err());
    // Empty / garbage frames → Err.
    assert!(decode_client(&[]).is_err());
    assert!(decode_server(&[0xff]).is_err());
}

#[wasm_bindgen_test]
fn encode_seam_rejects_malformed_js_object() {
    // A JS object that is not a valid ClientMsg/ServerMsg shape → thrown error,
    // never a trap. `null` / undefined deserialize-fail cleanly.
    assert!(encode_client(JsValue::NULL).is_err());
    assert!(encode_server(JsValue::NULL).is_err());
}

// Self-bias guard: the round-trip tests above only feed objects that `decode_*`
// itself produced, so they cannot prove a HAND-BUILT object (the shape the TS
// client will actually construct) deserializes correctly — the serde enum
// tag/field shape could differ from what a developer writes. These tests build
// the JS object the TS client builds (via JSON.parse) and assert it encodes to
// the exact golden bytes. Also pins the externally-tagged `{Variant:{fields}}`
// shape + bare-string transparent newtypes the TS types must mirror.

/// Build a JS object from a JSON literal, exactly as a TS caller would hand the
/// seam a typed object.
fn js_obj(json: &str) -> JsValue {
    js_sys::JSON::parse(json).expect("valid JSON literal")
}

#[wasm_bindgen_test]
fn hand_built_client_object_encodes_to_golden_bytes() {
    // The §4A client/seek golden frame (vectors.json): Seek{pool_id, time_control, rated}.
    // GameId/PoolId/TimeControlId are #[serde(transparent)] → bare strings.
    let seek =
        js_obj(r#"{"Seek":{"pool_id":"blitz-standard","time_control":"blitz-5+3","rated":true}}"#);
    let bytes = encode_client(seek).expect("encode hand-built Seek");
    let golden = from_hex("01020e626c69747a2d7374616e6461726409626c69747a2d352b3301");
    assert_eq!(
        bytes, golden,
        "hand-built Seek must encode to the golden frame"
    );

    // The §4A v1.1 RESYNC (0x0A) — the additive client message a TS client builds.
    let resync = js_obj(r#"{"Resync":{"game_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479"}}"#);
    let bytes = encode_client(resync).expect("encode hand-built Resync");
    let golden =
        from_hex("010a2466343761633130622d353863632d343337322d613536372d306530326232633364343739");
    assert_eq!(
        bytes, golden,
        "hand-built Resync must encode to the golden frame"
    );
}

#[wasm_bindgen_test]
fn hand_built_server_object_encodes_to_golden_bytes() {
    // The §4A v1.1 DRAW_OFFERED (0x87) — a server message a test harness builds.
    let drawn =
        js_obj(r#"{"DrawOffered":{"game_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","by":1}}"#);
    let bytes = encode_server(drawn).expect("encode hand-built DrawOffered");
    let golden = from_hex(
        "01872466343761633130622d353863632d343337322d613536372d30653032623263336434373901",
    );
    assert_eq!(
        bytes, golden,
        "hand-built DrawOffered must encode to the golden frame"
    );
}

// S2 (context-free review): MOVE is the highest-frequency client frame and the
// only client payload mixing a u32 (`ply_expected`) with a bare String (`uci`)
// beside a #[serde(transparent)] GameId — the shape most likely to be mistyped
// in the TS client. Prove the hand-built object encodes to the golden MOVE frame.
#[wasm_bindgen_test]
fn hand_built_move_encodes_to_golden_bytes() {
    let mv = js_obj(
        r#"{"Move":{"game_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","ply_expected":17,"uci":"e2e4"}}"#,
    );
    let bytes = encode_client(mv).expect("encode hand-built Move");
    let golden = from_hex(
        "01032466343761633130622d353863632d343337322d613536372d306530326232633364343739110465326534",
    );
    assert_eq!(
        bytes, golden,
        "hand-built Move must encode to the golden frame"
    );
}

// S3 (context-free review): GAME_END is the nested/collection-heavy server
// payload — Vec<SeatResult> (`result_vector`, bare [u8]), a transparent-u8
// enum8 (`termination` = bare number, NOT "MATE"/{"MATE":…}), and Vec<i16>
// (`rating_delta`). Pin the exact TS shape a harness must build. The golden
// game_end is result_vector=[2,0], termination=0x0A, rating_delta=[12,-12].
#[wasm_bindgen_test]
fn hand_built_game_end_encodes_to_golden_bytes() {
    let ge = js_obj(
        r#"{"GameEnd":{"game_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","result_vector":[2,0],"termination":10,"rating_delta":[12,-12]}}"#,
    );
    let bytes = encode_server(ge).expect("encode hand-built GameEnd");
    let golden = from_hex(
        "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817",
    );
    assert_eq!(
        bytes, golden,
        "hand-built GameEnd must encode to the golden frame"
    );
}

// B1 (context-free review): the u64 wire fields (Ping.t / Pong.t / server_t)
// must round-trip LOSSLESSLY above Number.MAX_SAFE_INTEGER (2^53-1). The golden
// corpus only carries ms-clock values ~1.7e12, three orders below the cliff, so
// it never exercised this. With serialize_large_number_types_as_bigints(true),
// decode emits a BigInt and encode reads it back — the full u64 domain is lossless.
#[wasm_bindgen_test]
fn u64_fields_round_trip_above_safe_integer() {
    // A value far above 2^53-1 (9_007_199_254_740_991), within u64. Build the
    // typed ClientMsg Ping{t} with a real BigInt (JSON cannot express one).
    let big: u64 = 18_000_000_000_000_000_000;
    let ping = js_sys::Object::new();
    let inner = js_sys::Object::new();
    js_sys::Reflect::set(
        &inner,
        &JsValue::from_str("t"),
        &JsValue::from(js_sys::BigInt::from(big)),
    )
    .unwrap();
    js_sys::Reflect::set(&ping, &JsValue::from_str("Ping"), &inner).unwrap();

    // encode -> §4A frame -> decode -> re-encode must be byte-identical, proving
    // the u64 survived the JS boundary BOTH ways (encode-from-BigInt, decode-as-BigInt).
    let frame = encode_client(ping.into()).expect("encode big-u64 Ping");
    let decoded = decode_client(&frame).expect("decode big-u64 Ping");

    // SF1 (fix-round review): assert the MECHANISM directly, not just byte
    // identity — the decoded `t` must be a JS BigInt carrying the exact value.
    // Without serialize_large_number_types_as_bigints(true) it would decode as a
    // plain (lossy) JS number; this makes the test fail SPECIFICALLY if that
    // serializer option is ever dropped, which is the point of a B1 regression guard.
    let inner_out = js_sys::Reflect::get(&decoded, &JsValue::from_str("Ping")).unwrap();
    let t_out = js_sys::Reflect::get(&inner_out, &JsValue::from_str("t")).unwrap();
    assert!(
        t_out.is_bigint(),
        "u64 field must decode as a BigInt, not a (lossy) number"
    );
    assert_eq!(
        js_sys::BigInt::from(t_out),
        js_sys::BigInt::from(big),
        "decoded u64 BigInt must equal the original value exactly"
    );

    let reframed = encode_client(decoded).expect("re-encode big-u64 Ping");
    assert_eq!(
        reframed, frame,
        "u64 > 2^53 must round-trip byte-identically"
    );
}
