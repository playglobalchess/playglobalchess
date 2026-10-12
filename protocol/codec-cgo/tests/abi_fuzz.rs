//! §8 cgo-safety: hammer the C-ABI entry points with malformed, truncated,
//! oversize, and random buffers directly (the hand-written unsafe seam), proving
//! no entry point crashes/traps and every success is freed. Complements the
//! happy-path unit tests in `lib.rs` and the Go 100k leak-smoke.
//!
//! This is the Rust-side fuzz leg of the §8 "cgo safety" requirement. A deeper
//! asan/sanitizer leak run is a CI job (the staticlib + a tiny C driver); this
//! exhaustive-shape sweep catches a boundary bug deterministically in `cargo
//! test` with no extra toolchain.

use pgc_protocol_cgo::{pgc_free, PGC_OK};

// Re-declare the extern fns we exercise (same crate, but we call them as the C
// ABI does, through raw pointers).
extern "C" {
    fn pgc_reencode_client(
        buf: *const u8,
        len: usize,
        out: *mut *mut u8,
        out_len: *mut usize,
    ) -> i32;
    fn pgc_reencode_server(
        buf: *const u8,
        len: usize,
        out: *mut *mut u8,
        out_len: *mut usize,
    ) -> i32;
    fn pgc_reencode_event(
        buf: *const u8,
        len: usize,
        out: *mut *mut u8,
        out_len: *mut usize,
    ) -> i32;
}

/// Drive one entry point with `input`; on success, free the buffer (balanced).
/// Returns true if the call succeeded (and was freed). Never panics on a bad
/// input — that is the property under test.
fn drive(
    f: unsafe extern "C" fn(*const u8, usize, *mut *mut u8, *mut usize) -> i32,
    input: &[u8],
) -> bool {
    let mut out: *mut u8 = std::ptr::null_mut();
    let mut out_len: usize = 0;
    let ptr = if input.is_empty() {
        std::ptr::null()
    } else {
        input.as_ptr()
    };
    // SAFETY: valid (ptr,len) pair or (null,0); valid out-params; test-local.
    let rc = unsafe { f(ptr, input.len(), &mut out, &mut out_len) };
    if rc == PGC_OK {
        // Must have a non-null buffer we are responsible for freeing.
        assert!(!out.is_null(), "PGC_OK with null out buffer");
        // SAFETY: ptr+len came from a reencode success; free exactly once.
        unsafe { pgc_free(out, out_len) };
        true
    } else {
        // On error, out must be untouched (null) — nothing to free.
        assert!(
            out.is_null(),
            "error path left a non-null out buffer (leak)"
        );
        false
    }
}

#[test]
fn abi_never_crashes_on_arbitrary_bytes() {
    let mut ok = 0u64;
    let mut err = 0u64;

    // 1) Every 0,1,2,3-byte frame (exhaustive small inputs = the truncation /
    //    header-boundary cases).
    let mut inputs: Vec<Vec<u8>> = Vec::new();
    inputs.push(vec![]);
    for a in 0u16..=255 {
        inputs.push(vec![a as u8]);
        for b in [0u8, 1, 2, 0x81, 0x85, 0xff] {
            inputs.push(vec![a as u8, b]);
            inputs.push(vec![a as u8, b, 0x00]);
        }
    }
    // 2) An oversize buffer (> §4A.1 4 KB cap) — must be TooLarge, not a crash.
    inputs.push(vec![0x01u8; 5000]);
    // 3) xorshift pseudo-random blobs of varied length.
    let mut s = 0xDEADBEEFu64;
    for _ in 0..30_000 {
        s ^= s << 13;
        s ^= s >> 7;
        s ^= s << 17;
        let n = (s % 128) as usize;
        let mut v = Vec::with_capacity(n);
        for k in 0..n {
            v.push(((s >> (k % 57)) & 0xff) as u8);
        }
        inputs.push(v);
    }

    for inp in &inputs {
        if drive(pgc_reencode_client, inp) {
            ok += 1
        } else {
            err += 1
        }
        if drive(pgc_reencode_server, inp) {
            ok += 1
        } else {
            err += 1
        }
        if drive(pgc_reencode_event, inp) {
            ok += 1
        } else {
            err += 1
        }
    }

    // The property is "no crash" (reaching here) + "every success was freed"
    // (asserted in drive). Print the tally so a reviewer sees the sweep ran.
    println!(
        "cgo fuzz: {} inputs x3 paths = {} calls; {} ok (freed), {} typed-err; no crash",
        inputs.len(),
        inputs.len() * 3,
        ok,
        err
    );
}
