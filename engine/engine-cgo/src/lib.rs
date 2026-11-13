//! C-ABI (cgo) binding for the PlayGlobalChess S1 engine (X-1, ADR-11, ADR-15,
//! SPEC-S2-GAME-SERVER-GS2). The Go game-server links this crate's `staticlib`
//! and calls these functions in-process, so it validates moves against the
//! **exact same** `pgc-engine` rules the client core and mobile bind — the one
//! legality authority (NFR-5). Delegate-only: no chess logic lives here.
//!
//! ## Surface (SPEC §4, byte/scalar-oriented — smallest unsafe surface, ADR-15)
//! - [`pgc_engine_apply`]: validate+apply one UCI move to a FEN, returning the
//!   resulting FEN + terminal status/claim + the resulting Zobrist key.
//! - [`pgc_engine_mating_material`]: the per-side FIDE-6.9 flag-branch query.
//! - [`pgc_engine_free`] / [`pgc_engine_abi_version`].
//!
//! ## Memory ownership (the one unsafe seam)
//! Rust ALLOCATES the single `*out_fen` buffer (via `Vec` → leaked box); the Go
//! caller copies it and hands it back to [`pgc_engine_free`] exactly once. Go
//! never frees a Rust pointer itself and never passes a Go pointer this code
//! retains (the cgo pointer rule). The scalar out-params are caller-owned stack
//! values. Every `unsafe` block documents its contract.
//!
//! ## Write-conditions (SPEC §4)
//! Every out-param of [`pgc_engine_apply`] is written ONLY on success, after all
//! fallible work has succeeded — so an error never leaves a partially-written
//! out set, and the caller reads no out-param on a non-OK return.
//!
//! ## No panic across the boundary
//! A panic unwinding across `extern "C"` is UB, so every entry point wraps its
//! body in `catch_unwind`. Before collapsing a caught panic to
//! [`PGC_ENG_ERR_PANIC`], the seam LOGS the payload (SPEC §5) so the bug signal
//! is not discarded with the return value — the ABI carries only an `i32`.

#![deny(unsafe_op_in_unsafe_fn)]

use std::panic::{catch_unwind, AssertUnwindSafe};
use std::slice;

use pgc_engine::{
    has_mating_material, status_with_history, uci_to_move, Color, DrawClaim, GameStatus, Position,
};

// --- status codes -----------------------------------------------------------
/// Success.
pub const PGC_ENG_OK: i32 = 0;
/// Invalid FEN.
pub const PGC_ENG_ERR_FEN: i32 = 1;
/// Unparseable / malformed UCI.
pub const PGC_ENG_ERR_MOVE: i32 = 2;
/// Parsed but illegal in this position.
pub const PGC_ENG_ERR_ILLEGAL: i32 = 3;
/// A null / !0-length-null argument.
pub const PGC_ENG_ERR_NULL_ARG: i32 = 98;
/// A panic was caught at the boundary (logged before collapsing — never UB).
pub const PGC_ENG_ERR_PANIC: i32 = 99;

// --- out_status / out_claim closed enums (1:1 with GameStatus / DrawClaim) ---
const PGC_ENG_STATUS_ONGOING: i32 = 0;
const PGC_ENG_STATUS_CHECKMATE: i32 = 1;
const PGC_ENG_STATUS_STALEMATE: i32 = 2;
const PGC_ENG_STATUS_INSUFFICIENT: i32 = 3;
const PGC_ENG_STATUS_FIFTYMOVE: i32 = 4;
const PGC_ENG_STATUS_REPETITION: i32 = 5;

const PGC_ENG_CLAIM_NONE: i32 = 0;
const PGC_ENG_CLAIM_CLAIMABLE: i32 = 1;
const PGC_ENG_CLAIM_AUTOMATIC: i32 = 2;

/// The engine ABI version. Bump on any surface change so the Go side can assert
/// it links the expected build.
const PGC_ENGINE_ABI_VERSION: u32 = 1;

/// Map the engine's `GameStatus` to the `(out_status, out_claim)` scalar pair.
/// The engine REPORTS these; the server (Go) maps them to the §4A.5 Termination
/// and owns the claim policy (detect-don't-govern — no governance here).
fn status_scalars(s: GameStatus) -> (i32, i32) {
    let claim = |c: DrawClaim| match c {
        DrawClaim::Claimable => PGC_ENG_CLAIM_CLAIMABLE,
        DrawClaim::Automatic => PGC_ENG_CLAIM_AUTOMATIC,
    };
    match s {
        GameStatus::Ongoing => (PGC_ENG_STATUS_ONGOING, PGC_ENG_CLAIM_NONE),
        GameStatus::Checkmate => (PGC_ENG_STATUS_CHECKMATE, PGC_ENG_CLAIM_NONE),
        GameStatus::Stalemate => (PGC_ENG_STATUS_STALEMATE, PGC_ENG_CLAIM_NONE),
        GameStatus::DrawInsufficientMaterial => (PGC_ENG_STATUS_INSUFFICIENT, PGC_ENG_CLAIM_NONE),
        GameStatus::DrawFiftyMove(c) => (PGC_ENG_STATUS_FIFTYMOVE, claim(c)),
        GameStatus::DrawRepetition(c) => (PGC_ENG_STATUS_REPETITION, claim(c)),
    }
}

/// Log a caught panic payload before collapsing it to `PGC_ENG_ERR_PANIC`, so
/// the bug signal is not thrown away (SPEC §5). The ABI returns only an i32; the
/// diagnostic lives here.
fn log_panic(func: &str, payload: &(dyn std::any::Any + Send)) {
    let msg = payload
        .downcast_ref::<&str>()
        .map(|s| s.to_string())
        .or_else(|| payload.downcast_ref::<String>().cloned())
        .unwrap_or_else(|| "<non-string panic payload>".to_string());
    eprintln!("pgc-engine-cgo: PANIC caught at boundary fn {func}: {msg}");
}

/// Hand `bytes` to the caller: leak a boxed slice and write ptr+len into the
/// out-params. The caller owns it until it calls [`pgc_engine_free`] with the
/// SAME ptr+len.
///
/// # Safety
/// `out`/`out_len` must be non-null, writable, caller-owned (checked by the
/// caller wrapper before this is called).
unsafe fn hand_out(bytes: Vec<u8>, out: *mut *mut u8, out_len: *mut usize) {
    let mut boxed = bytes.into_boxed_slice();
    let ptr = boxed.as_mut_ptr();
    let len = boxed.len();
    std::mem::forget(boxed);
    unsafe {
        *out = ptr;
        *out_len = len;
    }
}

/// Read a `(ptr,len)` byte argument as a `&str`, or `None` if it is not valid
/// UTF-8. `len==0` yields an empty string (ptr may be null).
///
/// # Safety
/// When `len != 0`, `ptr` must point to `len` readable bytes alive for the call.
unsafe fn str_arg<'a>(ptr: *const u8, len: usize) -> Option<&'a str> {
    let bytes: &[u8] = if len == 0 {
        &[]
    } else {
        // SAFETY: caller guarantees ptr points to len readable bytes it keeps
        // alive for the duration of this call (cgo passes a Go []byte's backing
        // array by pointer+len); we only read.
        unsafe { slice::from_raw_parts(ptr, len) }
    };
    std::str::from_utf8(bytes).ok()
}

/// Validate and apply one UCI move to a FEN. See `engine_cgo.h` for the full
/// out-parameter write-conditions contract.
///
/// # Safety
/// `fen`/`uci` point to their `len` bytes (null only iff len 0); `prior_keys`
/// points to `prior_keys_len` `u64`s (null only iff len 0); every out-pointer is
/// non-null, writable, caller-owned.
#[no_mangle]
pub unsafe extern "C" fn pgc_engine_apply(
    fen: *const u8,
    fen_len: usize,
    uci: *const u8,
    uci_len: usize,
    prior_keys: *const u64,
    prior_keys_len: usize,
    out_fen: *mut *mut u8,
    out_fen_len: *mut usize,
    out_status: *mut i32,
    out_claim: *mut i32,
    out_zobrist: *mut u64,
) -> i32 {
    // Null guard FIRST — never write an out-param, never deref a bad input.
    if out_fen.is_null()
        || out_fen_len.is_null()
        || out_status.is_null()
        || out_claim.is_null()
        || out_zobrist.is_null()
        || (fen.is_null() && fen_len != 0)
        || (uci.is_null() && uci_len != 0)
        || (prior_keys.is_null() && prior_keys_len != 0)
    {
        return PGC_ENG_ERR_NULL_ARG;
    }

    let result = catch_unwind(AssertUnwindSafe(|| {
        // SAFETY: validated non-null-or-zero-len above.
        let fen_s = match unsafe { str_arg(fen, fen_len) } {
            Some(s) => s,
            None => return PGC_ENG_ERR_FEN, // non-UTF-8 FEN is invalid
        };
        let uci_s = match unsafe { str_arg(uci, uci_len) } {
            Some(s) => s,
            None => return PGC_ENG_ERR_MOVE,
        };
        let prior: &[u64] = if prior_keys_len == 0 {
            &[]
        } else {
            // SAFETY: validated non-null when len != 0; read-only for the call.
            unsafe { slice::from_raw_parts(prior_keys, prior_keys_len) }
        };

        let mut pos = match Position::from_fen(fen_s) {
            Ok(p) => p,
            Err(_) => return PGC_ENG_ERR_FEN,
        };
        // uci_to_move validates legality: Malformed -> ERR_MOVE, Illegal ->
        // ERR_ILLEGAL. No chess logic in this crate — the engine decides.
        let mv = match uci_to_move(&pos, uci_s) {
            Ok(m) => m,
            Err(pgc_engine::MoveError::Malformed(_)) => return PGC_ENG_ERR_MOVE,
            Err(_) => return PGC_ENG_ERR_ILLEGAL,
        };
        pgc_engine::make(&mut pos, mv);
        let new_fen = pos.to_fen();
        let zob = pos.zobrist();
        let (st, claim) = status_scalars(status_with_history(&pos, prior));

        // SUCCESS: write every out-param now, after all fallible work. Buffer
        // last so a (impossible here) later failure can't leak it.
        // SAFETY: all out-pointers validated non-null above and caller-owned.
        unsafe {
            *out_status = st;
            *out_claim = claim;
            *out_zobrist = zob;
            hand_out(new_fen.into_bytes(), out_fen, out_fen_len);
        }
        PGC_ENG_OK
    }));

    match result {
        Ok(code) => code,
        Err(payload) => {
            log_panic("pgc_engine_apply", payload.as_ref());
            PGC_ENG_ERR_PANIC
        }
    }
}

/// Per-side mating-capacity query (SPEC §3.3 / FIDE 6.9).
///
/// # Safety
/// `fen` points to `fen_len` bytes (null only iff 0); `out_can_mate` is non-null,
/// writable, caller-owned.
#[no_mangle]
pub unsafe extern "C" fn pgc_engine_mating_material(
    fen: *const u8,
    fen_len: usize,
    color_idx: u8,
    out_can_mate: *mut i32,
) -> i32 {
    if out_can_mate.is_null() || (fen.is_null() && fen_len != 0) {
        return PGC_ENG_ERR_NULL_ARG;
    }
    let color = match color_idx {
        0 => Color::White,
        1 => Color::Black,
        _ => return PGC_ENG_ERR_NULL_ARG, // only 0/1 valid in v1 2-seat
    };

    let result = catch_unwind(AssertUnwindSafe(|| {
        // SAFETY: validated non-null-or-zero-len above.
        let fen_s = match unsafe { str_arg(fen, fen_len) } {
            Some(s) => s,
            None => return PGC_ENG_ERR_FEN,
        };
        let pos = match Position::from_fen(fen_s) {
            Ok(p) => p,
            Err(_) => return PGC_ENG_ERR_FEN,
        };
        let can = has_mating_material(&pos, color);
        // SAFETY: out_can_mate validated non-null, caller-owned.
        unsafe { *out_can_mate = i32::from(can) };
        PGC_ENG_OK
    }));

    match result {
        Ok(code) => code,
        Err(payload) => {
            log_panic("pgc_engine_mating_material", payload.as_ref());
            PGC_ENG_ERR_PANIC
        }
    }
}

/// Free a buffer previously handed out by a [`pgc_engine_apply`] success. Must
/// be called exactly once with the SAME ptr+len. A null `ptr` is a no-op.
///
/// # Safety
/// `ptr`/`len` must be exactly a pair produced by an apply success and not
/// previously freed.
#[no_mangle]
pub unsafe extern "C" fn pgc_engine_free(ptr: *mut u8, len: usize) {
    if ptr.is_null() {
        return;
    }
    // SAFETY: reconstruct the exact Box<[u8]> hand_out leaked (same ptr+len), so
    // dropping it frees precisely that allocation. Caller contract guarantees
    // ptr+len came from an apply success and is freed once.
    unsafe {
        let s = slice::from_raw_parts_mut(ptr, len);
        drop(Box::from_raw(s as *mut [u8]));
    }
}

/// The engine ABI version this crate speaks.
#[no_mangle]
pub extern "C" fn pgc_engine_abi_version() -> u32 {
    PGC_ENGINE_ABI_VERSION
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Call apply with a FEN+UCI, returning (code, fen, status, claim, zobrist).
    fn apply(fen: &str, uci: &str, prior: &[u64]) -> (i32, Option<String>, i32, i32, u64) {
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut out_len: usize = 0;
        let mut st: i32 = -1;
        let mut claim: i32 = -1;
        let mut zob: u64 = 0;
        let pk = if prior.is_empty() {
            std::ptr::null()
        } else {
            prior.as_ptr()
        };
        // SAFETY: valid slices + out-params; test-local.
        let rc = unsafe {
            pgc_engine_apply(
                fen.as_ptr(),
                fen.len(),
                uci.as_ptr(),
                uci.len(),
                pk,
                prior.len(),
                &mut out,
                &mut out_len,
                &mut st,
                &mut claim,
                &mut zob,
            )
        };
        let fen_out = if rc == PGC_ENG_OK {
            let bytes = unsafe { slice::from_raw_parts(out, out_len) }.to_vec();
            unsafe { pgc_engine_free(out, out_len) };
            Some(String::from_utf8(bytes).unwrap())
        } else {
            None
        };
        (rc, fen_out, st, claim, zob)
    }

    const START: &str = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

    #[test]
    fn legal_move_returns_new_fen_and_zobrist() {
        let (rc, fen, st, claim, zob) = apply(START, "e2e4", &[]);
        assert_eq!(rc, PGC_ENG_OK);
        assert_eq!(
            fen.unwrap(),
            // Engine emits strict exported FEN (OPEN-D): the ep target is `-`
            // unless an en-passant capture is actually available next move.
            "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1"
        );
        assert_eq!(st, PGC_ENG_STATUS_ONGOING);
        assert_eq!(claim, PGC_ENG_CLAIM_NONE);
        assert_ne!(zob, 0);
    }

    #[test]
    fn malformed_uci_is_err_move_no_write() {
        let (rc, fen, st, _, _) = apply(START, "zzzz", &[]);
        assert_eq!(rc, PGC_ENG_ERR_MOVE);
        assert!(fen.is_none());
        assert_eq!(st, -1, "out_status must be untouched on error");
    }

    #[test]
    fn illegal_move_is_err_illegal_no_write() {
        // e2e5 is a legal-form UCI but illegal (pawn can't jump 3).
        let (rc, fen, st, _, _) = apply(START, "e2e5", &[]);
        assert_eq!(rc, PGC_ENG_ERR_ILLEGAL);
        assert!(fen.is_none());
        assert_eq!(st, -1, "out_status must be untouched on error");
    }

    #[test]
    fn invalid_fen_is_err_fen() {
        let (rc, _, _, _, _) = apply("not a fen", "e2e4", &[]);
        assert_eq!(rc, PGC_ENG_ERR_FEN);
    }

    /// #120: semantically impossible positions are ERR_FEN over the C ABI,
    /// never applied (castling used to move an enemy piece from h1).
    #[test]
    fn impossible_positions_are_err_fen() {
        for (fen, uci) in [
            ("4k3/8/8/8/8/8/8/4K2n w K - 0 1", "e1g1"), // right without own rook
            ("4k3/8/8/8/8/8/8/8 w - - 0 1", "a1a2"),    // no white king
            ("4k2k/8/8/8/8/8/8/4K3 w - - 0 1", "e1e2"), // two black kings
            ("4k3/8/8/8/8/8/8/P3K3 w - - 0 1", "e1e2"), // pawn on rank 1
            ("4k3/8/8/8/8/8/8/4K2r b - - 0 1", "e8e7"), // side not to move in check
            ("4k3/8/8/8/4P3/8/8/4K3 w - e4 0 1", "e1e2"), // ep on the wrong rank
        ] {
            let (rc, _, _, _, _) = apply(fen, uci, &[]);
            assert_eq!(rc, PGC_ENG_ERR_FEN, "{fen}");
        }
    }

    #[test]
    fn scholars_mate_reports_checkmate() {
        // Position one move before Qxf7#.
        let pre = "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/5Q2/PPPP1PPP/RNB1K1NR w KQkq - 2 3";
        let (rc, _, st, claim, _) = apply(pre, "f3f7", &[]);
        assert_eq!(rc, PGC_ENG_OK);
        assert_eq!(st, PGC_ENG_STATUS_CHECKMATE);
        assert_eq!(claim, PGC_ENG_CLAIM_NONE);
    }

    #[test]
    fn null_out_is_rejected_not_crash() {
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut out_len: usize = 0;
        let mut claim: i32 = 0;
        let mut zob: u64 = 0;
        // null out_status -> NULL_ARG, no write, no crash.
        let rc = unsafe {
            pgc_engine_apply(
                START.as_ptr(),
                START.len(),
                "e2e4".as_ptr(),
                4,
                std::ptr::null(),
                0,
                &mut out,
                &mut out_len,
                std::ptr::null_mut(),
                &mut claim,
                &mut zob,
            )
        };
        assert_eq!(rc, PGC_ENG_ERR_NULL_ARG);
    }

    fn mating(fen: &str, color_idx: u8) -> (i32, i32) {
        let mut can: i32 = -1;
        let rc =
            unsafe { pgc_engine_mating_material(fen.as_ptr(), fen.len(), color_idx, &mut can) };
        (rc, can)
    }

    #[test]
    fn mating_material_per_side() {
        // K+Q vs K+N: White (0) can mate, Black (1) cannot.
        let fen = "4k1n1/8/8/8/8/8/8/3QK3 w - - 0 1";
        assert_eq!(mating(fen, 0), (PGC_ENG_OK, 1));
        assert_eq!(mating(fen, 1), (PGC_ENG_OK, 0));
        // #121: K+N vs K+P — Black CAN mate (the pawn blocks its own king).
        let kn_kp = "8/8/8/8/8/3n4/7P/5k1K b - - 0 1";
        assert_eq!(mating(kn_kp, 1), (PGC_ENG_OK, 1));
        // K+N vs bare K — cannot.
        assert_eq!(mating("4k3/8/8/8/8/8/8/4KN2 w - - 0 1", 0), (PGC_ENG_OK, 0));
    }

    #[test]
    fn mating_material_bad_color_rejected() {
        assert_eq!(mating(START, 2).0, PGC_ENG_ERR_NULL_ARG);
    }

    #[test]
    fn abi_version_is_one() {
        assert_eq!(pgc_engine_abi_version(), 1);
    }
}
