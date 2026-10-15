//! WebAssembly binding for the PlayGlobalChess wire + event codec (ADR-14,
//! OPEN-C1 = wasm DECIDED). This crate compiles the pure `pgc-protocol` codec to
//! wasm via `wasm-pack`, so the web client runs the **exact same** encode/decode
//! the server (cgo) and mobile (UniFFI) run — parity by construction, not by a
//! re-implementation (NFR-5, NFR-10, the whole point of ADR-14).
//!
//! It adds **no codec logic**: every function delegates to `pgc_protocol`. The
//! pure crate stays wasm-free (it is also the Go-cgo and mobile-UniFFI
//! consumer); all wasm/JS machinery lives here.
//!
//! ## Surface: byte-oriented `reencode_*(bytes) -> bytes` + the typed S4a seam
//!
//! `encode(decode(bytes))` done entirely in Rust. This is the drift-proof core
//! the §4.2 parity test asserts: decode a committed golden frame and prove it
//! re-encodes to the identical bytes, on the web runtime (the NFR-10 proof for
//! web). Nothing crosses the JS boundary except the bytes, so parity cannot be
//! weakened by a JS-side type-mapping bug.
//!
//! The S4a message seam (below) adds the typed direction the web client needs:
//! `encodeClient(msg) -> bytes` / `decodeServer(frame) -> msg` (+ the mirrors
//! `encodeServer`/`decodeClient` for the client's own tests), the wasm analog of
//! the GS-6a/GS-6 Go cgo seam (`pgc_decode_client`/`pgc_encode_server`/…). The
//! §4A FRAME is produced ONLY by the Rust `pgc_protocol::encode_*` functions —
//! the TS side never encodes a §4A byte (NFR-5). The typed `ClientMsg`/
//! `ServerMsg` cross the boundary as plain JS objects via `serde-wasm-bindgen`,
//! using the SAME serde derives the Go postcard intermediate already relies on;
//! so the typed object is a convenience view, and the wire bytes stay
//! Rust-owned. Proven byte-identical against the shared golden corpus by the
//! node parity test (`tests/web_parity.rs`), exactly like the Rust/Go seams.
//!
//! ## No panics across the boundary
//!
//! Every function returns `Result<_, JsError>`; a decode failure becomes a
//! thrown JS exception, never a wasm trap/abort.

use serde::Serialize;
use wasm_bindgen::prelude::*;

/// Map a `pgc_protocol::CodecError` into a JS exception, preserving the message.
fn js_err(e: pgc_protocol::CodecError) -> JsError {
    JsError::new(&e.to_string())
}

/// Decode a client→server frame and re-encode it; the result must equal the
/// input for any committed golden frame (byte-identity on the web runtime).
#[wasm_bindgen]
pub fn reencode_client(frame: &[u8]) -> Result<Vec<u8>, JsError> {
    let msg = pgc_protocol::decode_client(frame).map_err(js_err)?;
    pgc_protocol::encode_client(&msg).map_err(js_err)
}

/// Decode a server→client frame and re-encode it (see [`reencode_client`]).
#[wasm_bindgen]
pub fn reencode_server(frame: &[u8]) -> Result<Vec<u8>, JsError> {
    let msg = pgc_protocol::decode_server(frame).map_err(js_err)?;
    pgc_protocol::encode_server(&msg).map_err(js_err)
}

/// Decode a stored event and re-encode it (see [`reencode_client`]).
#[wasm_bindgen]
pub fn reencode_event(bytes: &[u8]) -> Result<Vec<u8>, JsError> {
    let env = pgc_protocol::decode_event(bytes).map_err(js_err)?;
    pgc_protocol::encode_event(&env).map_err(js_err)
}

/// The protocol major version this codec speaks (§4A.1), exposed so the web app
/// can assert it matches the server's without hard-coding the byte.
#[wasm_bindgen]
pub fn protocol_version() -> u8 {
    pgc_protocol::VER_V1
}

// === S4a — typed message seam (SPEC-S4-WEB-F01 §3.0) ========================
//
// The wasm analog of the GS-6a/GS-6 Go cgo message seam. These four functions
// are the ONLY way the web client builds/reads §4A messages: the §4A FRAME is
// produced solely by `pgc_protocol::encode_{client,server}` in Rust (NFR-5 —
// the TS side never encodes a §4A byte). The typed `ClientMsg`/`ServerMsg`
// cross the boundary as plain JS objects via `serde-wasm-bindgen`, reusing the
// exact serde derives the Go postcard intermediate already depends on — so the
// JS object is a view, not a second wire encoding. Round-trip is proven
// byte-identical against the shared golden corpus by `tests/web_parity.rs`.
//
// Direction mapping (mirrors the Go seam exactly):
//   client NEEDS: encodeClient (build C->S), decodeServer (read S->C)
//   test  MIRROR: encodeServer (build S->C), decodeClient (read C->S)

/// Map a `serde_wasm_bindgen` (de)serialization failure into a JS exception.
/// A malformed/incomplete JS message object (missing a required field, a wrong
/// variant shape) becomes a thrown error, never a wasm trap.
fn serde_err(e: serde_wasm_bindgen::Error) -> JsError {
    JsError::new(&e.to_string())
}

/// The serializer used by both `decode*` directions. `json_compatible()` makes
/// Rust structs become plain JS objects (not `Map`s) — the ergonomic shape the
/// TS client expects. **`serialize_large_number_types_as_bigints(true)` is
/// load-bearing, not cosmetic:** without it `json_compatible()` emits `u64`
/// (and `i64`) as a plain JS `number`, which only holds integers up to 2^53−1,
/// so a `Ping.t`/`Pong.t`/`server_t` value above `Number.MAX_SAFE_INTEGER`
/// would SILENTLY lose its low bits on decode and fail to re-encode. Those
/// fields are `u64` on the wire (§4A.3) and carry monotonic-clock ms, which a
/// non-wall-clock or µs/ns source pushes past 2^53. Emitting them as `BigInt`
/// makes the whole `u64` domain lossless in both directions; the TS type for a
/// `u64` wire field is therefore `bigint`. (A caller that hands a plain JS
/// number for a u64 field still works while it is ≤ 2^53−1, since the
/// deserializer accepts a number OR a BigInt within u64 bounds.)
fn server_serializer() -> serde_wasm_bindgen::Serializer {
    serde_wasm_bindgen::Serializer::json_compatible().serialize_large_number_types_as_bigints(true)
}

/// **S4a (client needs).** Build a §4A client→server FRAME from a typed JS
/// `ClientMsg` object. JS object → `ClientMsg` (serde) → `encode_client` → §4A
/// bytes. The §4A bytes are produced entirely in Rust (NFR-5). Throws if the JS
/// object is not a valid `ClientMsg` shape, or if encoding fails.
#[wasm_bindgen(js_name = encodeClient)]
pub fn encode_client(msg: JsValue) -> Result<Vec<u8>, JsError> {
    let msg: pgc_protocol::ClientMsg = serde_wasm_bindgen::from_value(msg).map_err(serde_err)?;
    pgc_protocol::encode_client(&msg).map_err(js_err)
}

/// **S4a (client needs).** Read a §4A server→client FRAME into a typed JS
/// `ServerMsg` object. §4A bytes → `decode_server` (Rust) → `ServerMsg` → JS
/// object. Throws if the frame is not a valid §4A server frame.
#[wasm_bindgen(js_name = decodeServer)]
pub fn decode_server(frame: &[u8]) -> Result<JsValue, JsError> {
    let msg = pgc_protocol::decode_server(frame).map_err(js_err)?;
    msg.serialize(&server_serializer()).map_err(serde_err)
}

/// **S4a (test mirror).** Build a §4A server→client FRAME from a typed JS
/// `ServerMsg` object — the mirror of [`decode_server`], for the client's own
/// tests (and a future JS test harness), exactly as GS-6 added the client
/// mirror for its e2e. The §4A bytes are produced in Rust (NFR-5).
#[wasm_bindgen(js_name = encodeServer)]
pub fn encode_server(msg: JsValue) -> Result<Vec<u8>, JsError> {
    let msg: pgc_protocol::ServerMsg = serde_wasm_bindgen::from_value(msg).map_err(serde_err)?;
    pgc_protocol::encode_server(&msg).map_err(js_err)
}

/// **S4a (test mirror).** Read a §4A client→server FRAME into a typed JS
/// `ClientMsg` object — the mirror of [`encode_client`], for tests. §4A bytes →
/// `decode_client` (Rust) → `ClientMsg` → JS object.
#[wasm_bindgen(js_name = decodeClient)]
pub fn decode_client(frame: &[u8]) -> Result<JsValue, JsError> {
    let msg = pgc_protocol::decode_client(frame).map_err(js_err)?;
    msg.serialize(&server_serializer()).map_err(serde_err)
}
