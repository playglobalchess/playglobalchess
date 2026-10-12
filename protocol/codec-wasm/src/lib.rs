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
//! ## Surface: byte-oriented `reencode_*(bytes) -> bytes`
//!
//! `encode(decode(bytes))` done entirely in Rust. This is the drift-proof core
//! the §4.2 parity test asserts: decode a committed golden frame and prove it
//! re-encodes to the identical bytes, on the web runtime (the NFR-10 proof for
//! web). Nothing crosses the JS boundary except the bytes, so parity cannot be
//! weakened by a JS-side type-mapping bug.
//!
//! The web app also wants to work with the message/event types as typed JS
//! objects. That **typed TS wrapper is a web-app concern** (clients/web, S4) and
//! is intentionally NOT in this chunk: exposing the §4A types as JS values would
//! require adding `serde::{Serialize,Deserialize}` derives to the frozen
//! `pgc-protocol` top-level enums (which are postcard-encoded by manual dispatch
//! today, not serde-derived) — a change to the frozen core crate for a
//! convenience surface. It layers cleanly on top of these byte functions later;
//! this chunk delivers the parity proof (PLAN-CODEC §4.4) and the same compiled
//! codec the web app will call.
//!
//! ## No panics across the boundary
//!
//! Every function returns `Result<_, JsError>`; a decode failure becomes a
//! thrown JS exception, never a wasm trap/abort.

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
