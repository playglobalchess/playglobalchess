//! C-ABI (cgo) binding for the PlayGlobalChess wire + event codec (ADR-14,
//! ADR-15, PLAN-S2-CODEC v1.1 §4.5). The Go game-server links this crate's
//! `staticlib` and calls these functions in-process, so it runs the **exact
//! same** `pgc-protocol` codec as the server-core, mobile (UniFFI), and web
//! (wasm) — parity by construction (NFR-5, NFR-10).
//!
//! ## Surface — byte-oriented (Q-GS1 DECIDED)
//!
//! `pgc_reencode_{client,server,event}(buf, len, out, out_len)` = decode then
//! re-encode, entirely in Rust: returns the canonical frame bytes for a valid
//! input. This is the parity primitive the Go contract test asserts
//! (decode→re-encode == input for every committed golden vector) and the shape
//! the game-server uses on the hot path (it mostly relays frames; it reads the
//! few routing/authz fields through narrow accessors added later as needed).
//! No per-message C struct graph crosses the boundary — smallest `unsafe`
//! surface (ADR-15 rationale).
//!
//! ## Memory ownership (the one unsafe seam)
//!
//! Rust ALLOCATES every `*out` buffer (via `Vec` → leaked box) and Go MUST hand
//! it back to [`pgc_free`] after copying — Go never frees a Rust pointer itself
//! and never passes a Go pointer this code retains (the cgo pointer rule). Every
//! `unsafe` block below documents its contract.
//!
//! ## No panic across the boundary
//!
//! A panic unwinding across `extern "C"` is UB, so every entry point wraps its
//! body in `catch_unwind` and maps a panic (which the codec does not produce —
//! decode is total — but defensively) to [`PGC_ERR_PANIC`]. Normal decode
//! failures return the typed [`pgc_protocol::CodecError`] discriminant.

#![deny(unsafe_op_in_unsafe_fn)]

use std::panic::catch_unwind;
use std::slice;

use pgc_protocol::CodecError;

// --- status codes returned by the reencode functions ------------------------
/// Success.
pub const PGC_OK: i32 = 0;
// CodecError discriminants (stable, mirrored on the Go side). Kept distinct from
// 0 and from the panic sentinel.
const PGC_ERR_TOO_SHORT: i32 = 1;
const PGC_ERR_UNSUPPORTED_VERSION: i32 = 2;
const PGC_ERR_UNKNOWN_TYPE: i32 = 3;
const PGC_ERR_WRONG_DIRECTION: i32 = 4;
const PGC_ERR_TOO_LARGE: i32 = 5;
const PGC_ERR_MALFORMED: i32 = 6;
/// A null output pointer or a null input with non-zero length was passed.
const PGC_ERR_NULL_ARG: i32 = 98;
/// A panic was caught at the boundary (should never happen — decode is total).
pub const PGC_ERR_PANIC: i32 = 99;

fn code_of(e: &CodecError) -> i32 {
    match e {
        CodecError::TooShort(_) => PGC_ERR_TOO_SHORT,
        CodecError::UnsupportedVersion(_) => PGC_ERR_UNSUPPORTED_VERSION,
        CodecError::UnknownType(_) => PGC_ERR_UNKNOWN_TYPE,
        CodecError::WrongDirection(_, _) => PGC_ERR_WRONG_DIRECTION,
        CodecError::TooLarge(_) => PGC_ERR_TOO_LARGE,
        CodecError::MalformedPayload(_) => PGC_ERR_MALFORMED,
    }
}

/// Hand the produced bytes back to the caller: allocate a boxed slice, write its
/// pointer + length into the out-params, and `forget` it so Rust does not free
/// it — the caller owns it until it calls [`pgc_free`] with the SAME ptr+len.
fn hand_out(bytes: Vec<u8>, out: *mut *mut u8, out_len: *mut usize) {
    let mut boxed = bytes.into_boxed_slice();
    let ptr = boxed.as_mut_ptr();
    let len = boxed.len();
    std::mem::forget(boxed);
    // SAFETY: `out`/`out_len` are non-null (checked by the caller wrapper before
    // calling us) and point to writable `*mut u8` / `usize` the caller owns.
    unsafe {
        *out = ptr;
        *out_len = len;
    }
}

/// The shared body of the three reencode entry points, generic over the codec
/// function pair. Reads `len` bytes from `buf`, runs `f`, hands the result out.
fn reencode_impl(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
    f: impl FnOnce(&[u8]) -> Result<Vec<u8>, CodecError> + std::panic::UnwindSafe,
) -> i32 {
    if out.is_null() || out_len.is_null() || (buf.is_null() && len != 0) {
        return PGC_ERR_NULL_ARG;
    }
    let result = catch_unwind(|| {
        // SAFETY: when len==0 we pass an empty slice (buf may be null, allowed);
        // otherwise the caller guarantees `buf` points to `len` initialized
        // bytes it keeps alive for this call (cgo passes a Go []byte's backing
        // array by pointer+len for the duration of the call). We only read.
        let input: &[u8] = if len == 0 {
            &[]
        } else {
            unsafe { slice::from_raw_parts(buf, len) }
        };
        f(input)
    });
    match result {
        Ok(Ok(bytes)) => {
            hand_out(bytes, out, out_len);
            PGC_OK
        }
        Ok(Err(e)) => code_of(&e),
        Err(_) => PGC_ERR_PANIC,
    }
}

/// Decode a client→server frame and re-encode it. On `PGC_OK`, `*out`/`*out_len`
/// hold a Rust-allocated buffer the caller must release with [`pgc_free`].
///
/// # Safety
/// `buf` must point to `len` readable bytes (or be null iff `len==0`);
/// `out`/`out_len` must be non-null, writable, and caller-owned.
#[no_mangle]
pub unsafe extern "C" fn pgc_reencode_client(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    reencode_impl(buf, len, out, out_len, |b| {
        pgc_protocol::decode_client(b).and_then(|m| pgc_protocol::encode_client(&m))
    })
}

/// Decode a server→client frame and re-encode it. See [`pgc_reencode_client`].
///
/// # Safety
/// Same contract as [`pgc_reencode_client`].
#[no_mangle]
pub unsafe extern "C" fn pgc_reencode_server(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    reencode_impl(buf, len, out, out_len, |b| {
        pgc_protocol::decode_server(b).and_then(|m| pgc_protocol::encode_server(&m))
    })
}

/// Decode a stored event and re-encode it. See [`pgc_reencode_client`].
///
/// # Safety
/// Same contract as [`pgc_reencode_client`].
#[no_mangle]
pub unsafe extern "C" fn pgc_reencode_event(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    reencode_impl(buf, len, out, out_len, |b| {
        pgc_protocol::decode_event(b).and_then(|e| pgc_protocol::encode_event(&e))
    })
}

/// Free a buffer previously handed out by a `pgc_reencode_*` call. Must be
/// called exactly once per successful call, with the SAME `ptr` and `len` that
/// call wrote. A null `ptr` is a no-op.
///
/// # Safety
/// `ptr`/`len` must be exactly a pair produced by a `pgc_reencode_*` success and
/// not previously freed.
#[no_mangle]
pub unsafe extern "C" fn pgc_free(ptr: *mut u8, len: usize) {
    if ptr.is_null() {
        return;
    }
    // SAFETY: reconstruct the exact Box<[u8]> `hand_out` leaked (same ptr+len),
    // so dropping it frees precisely that allocation. The caller contract
    // guarantees ptr+len came from a reencode success and is freed once.
    unsafe {
        let slice = slice::from_raw_parts_mut(ptr, len);
        drop(Box::from_raw(slice as *mut [u8]));
    }
}

/// The protocol major version this codec speaks (§4A.1). Lets the Go server
/// assert it matches without hard-coding the byte.
#[no_mangle]
pub extern "C" fn pgc_protocol_version() -> u8 {
    pgc_protocol::VER_V1
}

#[cfg(test)]
mod tests {
    use super::*;

    fn reencode(
        f: unsafe extern "C" fn(*const u8, usize, *mut *mut u8, *mut usize) -> i32,
        input: &[u8],
    ) -> Result<Vec<u8>, i32> {
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut out_len: usize = 0;
        // SAFETY: valid input slice + valid out-params; test-local.
        let rc = unsafe { f(input.as_ptr(), input.len(), &mut out, &mut out_len) };
        if rc != PGC_OK {
            return Err(rc);
        }
        // SAFETY: on OK, out/out_len is a buffer we must copy then free.
        let bytes = unsafe { slice::from_raw_parts(out, out_len) }.to_vec();
        unsafe { pgc_free(out, out_len) };
        Ok(bytes)
    }

    fn from_hex(h: &str) -> Vec<u8> {
        (0..h.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&h[i..i + 2], 16).unwrap())
            .collect()
    }

    const CLIENT_MOVE_PROMO: &str = "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871";
    const SERVER_GAME_END: &str = "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817";
    const EVENT_RATING_APPLIED: &str = "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d";

    #[test]
    fn reencode_identity_for_golden_frames() {
        let c = from_hex(CLIENT_MOVE_PROMO);
        assert_eq!(reencode(pgc_reencode_client, &c).unwrap(), c);
        let s = from_hex(SERVER_GAME_END);
        assert_eq!(reencode(pgc_reencode_server, &s).unwrap(), s);
        let e = from_hex(EVENT_RATING_APPLIED);
        assert_eq!(reencode(pgc_reencode_event, &e).unwrap(), e);
    }

    #[test]
    fn malformed_returns_error_not_panic() {
        assert!(reencode(pgc_reencode_client, &[]).is_err()); // too short
        assert!(reencode(pgc_reencode_server, &[0x01, 0x09]).is_err()); // wrong direction
        assert!(reencode(pgc_reencode_event, &[0xff]).is_err());
    }

    #[test]
    fn null_args_are_rejected() {
        let input = from_hex(CLIENT_MOVE_PROMO);
        // null out pointer → NULL_ARG, no crash.
        let mut out_len: usize = 0;
        let rc = unsafe {
            pgc_reencode_client(
                input.as_ptr(),
                input.len(),
                std::ptr::null_mut(),
                &mut out_len,
            )
        };
        assert_eq!(rc, PGC_ERR_NULL_ARG);
    }

    #[test]
    fn version_is_one() {
        assert_eq!(pgc_protocol_version(), 1);
    }
}
