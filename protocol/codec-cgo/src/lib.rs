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
/// The GS-4a boundary intermediate (postcard of an `EventEnvelope`) handed to
/// `pgc_encode_event` could not be deserialized — the CALLER built a bad
/// envelope, as distinct from a bad §4A frame (`PGC_ERR_MALFORMED`). Kept
/// separate so the Go side can tell "my mapping produced junk" from "the stored
/// §4A bytes are corrupt".
const PGC_ERR_BAD_INTERMEDIATE: i32 = 97;

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

/// Shared body for the GS-4a event seam. Like [`reencode_impl`] but the closure
/// returns a status code directly (`Result<Vec<u8>, i32>`), so each entry point
/// maps its own errors — `pgc_encode_event` must distinguish a bad caller-built
/// intermediate (`PGC_ERR_BAD_INTERMEDIATE`) from a bad §4A frame, which the
/// generic `code_of` cannot express.
fn boundary_impl(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
    f: impl FnOnce(&[u8]) -> Result<Vec<u8>, i32> + std::panic::UnwindSafe,
) -> i32 {
    if out.is_null() || out_len.is_null() || (buf.is_null() && len != 0) {
        return PGC_ERR_NULL_ARG;
    }
    let result = catch_unwind(|| {
        // SAFETY: identical contract to `reencode_impl` — when len==0 we pass an
        // empty slice (buf may be null); otherwise the caller guarantees `buf`
        // points to `len` initialized bytes alive for the call. We only read.
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
        Ok(Err(code)) => code,
        Err(_) => PGC_ERR_PANIC,
    }
}

/// GS-4a: encode a `game.Event` for the durable store. IN = the postcard
/// **intermediate** (an `EventEnvelope` the Go server built, see
/// `pgc_protocol::encode_envelope_intermediate`); OUT = the canonical §4A event
/// frame (`pgc_protocol::encode_event`). This keeps §4A encoding entirely in
/// Rust (NFR-5): Go never writes a §4A byte, it only hands over an envelope.
/// A malformed intermediate → `PGC_ERR_BAD_INTERMEDIATE`; a §4A encode failure →
/// the usual `code_of` discriminant. On `PGC_OK`, `*out`/`*out_len` hold a
/// Rust-allocated buffer the caller must release with [`pgc_free`].
///
/// # Safety
/// Same contract as [`pgc_reencode_client`].
#[no_mangle]
pub unsafe extern "C" fn pgc_encode_event(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    boundary_impl(buf, len, out, out_len, |b| {
        let env =
            pgc_protocol::decode_envelope_intermediate(b).map_err(|_| PGC_ERR_BAD_INTERMEDIATE)?;
        pgc_protocol::encode_event(&env).map_err(|e| code_of(&e))
    })
}

/// GS-4a: decode a stored §4A event frame back to the postcard intermediate the
/// Go server reads (the inverse of [`pgc_encode_event`]). IN = §4A event frame;
/// OUT = postcard of the `EventEnvelope`. A bad §4A frame → the usual `code_of`
/// discriminant. On `PGC_OK`, `*out`/`*out_len` hold a Rust-allocated buffer the
/// caller must release with [`pgc_free`].
///
/// # Safety
/// Same contract as [`pgc_reencode_client`].
#[no_mangle]
pub unsafe extern "C" fn pgc_decode_event(
    buf: *const u8,
    len: usize,
    out: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    boundary_impl(buf, len, out, out_len, |b| {
        let env = pgc_protocol::decode_event(b).map_err(|e| code_of(&e))?;
        pgc_protocol::encode_envelope_intermediate(&env).map_err(|e| code_of(&e))
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

    // --- GS-4a event seam -----------------------------------------------------

    /// A golden §4A event frame → intermediate → §4A frame must be byte-identical
    /// (proves pgc_decode_event + pgc_encode_event compose to the identity on a
    /// real stored frame, i.e. the store round-trips to canonical wire bytes).
    #[test]
    fn event_seam_round_trips_golden_frame() {
        let golden = from_hex(EVENT_RATING_APPLIED);
        let intermediate = reencode(pgc_decode_event, &golden).unwrap();
        // The intermediate is NOT the §4A frame — it is a different (postcard-
        // of-envelope) byte string; only its re-encode must equal the frame.
        assert_ne!(
            intermediate, golden,
            "intermediate must differ from the §4A frame"
        );
        let reframed = reencode(pgc_encode_event, &intermediate).unwrap();
        assert_eq!(
            reframed, golden,
            "encode(decode(frame)) must reproduce the §4A frame"
        );
    }

    /// A malformed intermediate handed to pgc_encode_event is reported as the
    /// distinct BAD_INTERMEDIATE code, not MALFORMED (the Go side must tell a bad
    /// caller-built envelope from corrupt stored §4A bytes).
    #[test]
    fn encode_event_bad_intermediate_is_distinct_code() {
        // 0xff.. is not a valid postcard EventEnvelope.
        let err = reencode(pgc_encode_event, &[0xff, 0xff, 0xff]).unwrap_err();
        assert_eq!(err, PGC_ERR_BAD_INTERMEDIATE);
        // And a corrupt §4A frame into pgc_decode_event is a *different* class.
        let dec_err = reencode(pgc_decode_event, &[0xff]).unwrap_err();
        assert_ne!(dec_err, PGC_ERR_BAD_INTERMEDIATE);
        assert_ne!(dec_err, PGC_OK);
    }

    /// Null out-pointer into the new seam → NULL_ARG, no crash.
    #[test]
    fn event_seam_null_args_rejected() {
        let input = from_hex(EVENT_RATING_APPLIED);
        let mut out_len: usize = 0;
        let rc = unsafe {
            pgc_encode_event(
                input.as_ptr(),
                input.len(),
                std::ptr::null_mut(),
                &mut out_len,
            )
        };
        assert_eq!(rc, PGC_ERR_NULL_ARG);
    }

    /// The §4A golden frame still reencodes to itself through the ORIGINAL
    /// frame→frame seam — proves adding Serialize/Deserialize to the envelope did
    /// not perturb the frozen §4A encoding (freeze test).
    #[test]
    fn adding_derive_did_not_change_event_frame() {
        let e = from_hex(EVENT_RATING_APPLIED);
        assert_eq!(reencode(pgc_reencode_event, &e).unwrap(), e);
    }

    /// ABI fuzz sweep (ADR-15 convention): fire ~100k malformed / random /
    /// oversize inputs at BOTH new seam fns. Asserts: no crash (a panic would
    /// abort the test binary), and every non-OK result is a TYPED non-zero code
    /// — never a silent 0 with no buffer. Every PGC_OK buffer goes through the
    /// `reencode` helper's copy-then-`pgc_free` path, so a DOUBLE free would
    /// abort here; a single-free LEAK, however, is NOT proven by this test alone
    /// — that needs the sweep run under a leak checker (ASan/valgrind) in CI,
    /// which this unit test does not itself enforce.
    #[test]
    fn event_seam_abi_fuzz_sweep() {
        // A cheap deterministic LCG so the sweep is reproducible without a dep.
        let mut state: u64 = 0x9E3779B97F4A7C15;
        let mut next = || {
            state = state
                .wrapping_mul(6364136223846793005)
                .wrapping_add(1442695040888963407);
            state
        };
        let valid_frame = from_hex(EVENT_RATING_APPLIED);
        let valid_inter = reencode(pgc_decode_event, &valid_frame).unwrap();

        let mut ok_encode = 0u64;
        let mut ok_decode = 0u64;
        for i in 0..100_000u64 {
            // Build a pseudo-random buffer, occasionally seeded from a valid one
            // + mutations and occasionally oversize, to hit both the happy and
            // the reject branches.
            let r = next();
            let len = match r % 8 {
                0 => 0,
                1 => (r >> 3) as usize % 4,             // tiny
                7 => 4096 + ((r >> 3) as usize % 4096), // oversize
                _ => (r >> 3) as usize % 64,
            };
            let mut buf = Vec::with_capacity(len);
            // Seed a quarter of iterations from a valid artifact, then corrupt.
            let seed: &[u8] = if i % 4 == 0 {
                &valid_inter
            } else if i % 4 == 1 {
                &valid_frame
            } else {
                &[]
            };
            for j in 0..len {
                let b = if j < seed.len() && (next() % 3 != 0) {
                    seed[j]
                } else {
                    (next() % 256) as u8
                };
                buf.push(b);
            }

            // encode seam: input is a (usually bad) intermediate.
            match reencode(pgc_encode_event, &buf) {
                Ok(bytes) => {
                    ok_encode += 1;
                    // A success must round-trip: decoding the produced §4A frame
                    // and re-encoding must reproduce the same frame.
                    let inter2 = reencode(pgc_decode_event, &bytes).unwrap();
                    let bytes2 = reencode(pgc_encode_event, &inter2).unwrap();
                    assert_eq!(bytes, bytes2, "fuzz: encode output not stable");
                }
                Err(code) => {
                    assert!(code != PGC_OK, "fuzz: non-OK must be a typed non-zero code");
                    assert!(
                        code == PGC_ERR_BAD_INTERMEDIATE
                            || code == PGC_ERR_TOO_SHORT
                            || code == PGC_ERR_UNSUPPORTED_VERSION
                            || code == PGC_ERR_UNKNOWN_TYPE
                            || code == PGC_ERR_WRONG_DIRECTION
                            || code == PGC_ERR_TOO_LARGE
                            || code == PGC_ERR_MALFORMED
                            || code == PGC_ERR_PANIC,
                        "fuzz: unexpected encode code {code}"
                    );
                    // A panic should never happen — decode/deser are total.
                    assert_ne!(code, PGC_ERR_PANIC, "fuzz: panic at the boundary");
                }
            }

            // decode seam: input is a (usually bad) §4A frame.
            match reencode(pgc_decode_event, &buf) {
                Ok(_) => ok_decode += 1,
                Err(code) => {
                    assert!(code != PGC_OK);
                    assert_ne!(code, PGC_ERR_PANIC, "fuzz: panic at the boundary");
                }
            }
        }
        // Sanity: the valid-seeded iterations should produce SOME successes, so
        // the sweep actually exercised the happy branch, not only rejects.
        assert!(
            ok_encode > 0,
            "fuzz never hit a valid encode — sweep is not exercising success"
        );
        let _ = ok_decode;
    }
}
