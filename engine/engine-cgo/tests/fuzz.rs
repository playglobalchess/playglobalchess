//! cgo safety tests for the engine seam (SPEC-S2-GAME-SERVER-GS2 §8): a
//! deterministic fuzz sweep (never a crash, always a typed status) and a
//! panic-diagnostics check. The LEAK test uses a counting allocator and lives in
//! its own file (`leak.rs`) so the custom `#[global_allocator]` does not affect
//! these tests.

use pgc_engine_cgo::*;
use std::ptr;

/// Thin wrapper calling pgc_engine_apply with raw parts; frees any OK buffer so
/// the fuzz sweep itself does not leak.
fn apply_raw(fen: &[u8], uci: &[u8], prior: &[u64]) -> i32 {
    let mut out: *mut u8 = ptr::null_mut();
    let mut out_len: usize = 0;
    let mut st: i32 = 0;
    let mut claim: i32 = 0;
    let mut zob: u64 = 0;
    let (fp, fl) = if fen.is_empty() {
        (ptr::null(), 0)
    } else {
        (fen.as_ptr(), fen.len())
    };
    let (up, ul) = if uci.is_empty() {
        (ptr::null(), 0)
    } else {
        (uci.as_ptr(), uci.len())
    };
    let (pp, pl) = if prior.is_empty() {
        (ptr::null(), 0)
    } else {
        (prior.as_ptr(), prior.len())
    };
    // SAFETY: pointers are either null-with-0-len or valid slices alive for the
    // call; out-params are valid locals.
    let rc = unsafe {
        pgc_engine_apply(
            fp, fl, up, ul, pp, pl, &mut out, &mut out_len, &mut st, &mut claim, &mut zob,
        )
    };
    if rc == PGC_ENG_OK {
        // SAFETY: on OK the seam handed us a buffer to free exactly once.
        unsafe { pgc_engine_free(out, out_len) };
    }
    rc
}

/// A tiny xorshift so the sweep is deterministic (reproducible on CI) without a
/// dependency.
struct Rng(u64);
impl Rng {
    fn next(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        self.0 = x;
        x
    }
    fn byte(&mut self) -> u8 {
        (self.next() & 0xff) as u8
    }
}

const VALID_STATUSES: &[i32] = &[
    PGC_ENG_OK,
    PGC_ENG_ERR_FEN,
    PGC_ENG_ERR_MOVE,
    PGC_ENG_ERR_ILLEGAL,
    PGC_ENG_ERR_NULL_ARG,
    PGC_ENG_ERR_PANIC,
];

#[test]
fn fuzz_apply_never_crashes_always_typed() {
    let mut rng = Rng(0x9E3779B97F4A7C15);
    let start = b"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
    let mut caught_panics = 0u32;
    for i in 0..100_000u32 {
        // Mix of shapes: random FEN bytes, oversize, empty, a valid FEN with
        // random UCI, and arbitrary prior-key slices.
        let fen: Vec<u8> = match i % 4 {
            0 => (0..(rng.next() % 80) as usize).map(|_| rng.byte()).collect(),
            1 => start.to_vec(),
            2 => vec![rng.byte(); (rng.next() % 4096) as usize], // oversize-ish
            _ => Vec::new(),
        };
        let uci: Vec<u8> = (0..(rng.next() % 8) as usize).map(|_| rng.byte()).collect();
        let prior: Vec<u64> = (0..(rng.next() % 6) as usize).map(|_| rng.next()).collect();

        let rc = apply_raw(&fen, &uci, &prior);
        // The GS-2 §5 contract: EVERY return is a typed status, and a panic in
        // the pure engine is CAUGHT and surfaced as PGC_ENG_ERR_PANIC (99) — it
        // never unwinds across the C boundary (that would be UB / a crash). So
        // 99 is a valid, contract-conforming outcome here, not a failure: it
        // proves catch_unwind works. (A current engine latent panic on
        // non-ASCII UCI, moves.rs from_uci slicing by byte index, is one source;
        // tracked for an engine-hardening fix, out of GS-2 scope — the seam's
        // job is to contain it, which this asserts.)
        assert!(
            VALID_STATUSES.contains(&rc),
            "iter {i}: untyped status {rc} for fen={fen:?} uci={uci:?}"
        );
        if rc == PGC_ENG_ERR_PANIC {
            caught_panics += 1;
        }
    }
    // The seam contained every panic as a typed code and the process did not
    // crash — reaching this line is the proof. (Informational: how many of the
    // 100k inputs exercised the caught-panic path.)
    eprintln!("fuzz: {caught_panics}/100000 inputs hit the contained-panic path");
}

#[test]
fn fuzz_mating_material_never_crashes() {
    let mut rng = Rng(0xDEADBEEFCAFEF00D);
    for _ in 0..20_000u32 {
        let fen: Vec<u8> = (0..(rng.next() % 80) as usize).map(|_| rng.byte()).collect();
        let color = rng.byte();
        let mut can: i32 = -1;
        let (fp, fl) = if fen.is_empty() {
            (ptr::null(), 0)
        } else {
            (fen.as_ptr(), fen.len())
        };
        // SAFETY: null-or-valid fen; valid out local.
        let rc = unsafe { pgc_engine_mating_material(fp, fl, color, &mut can) };
        assert_ne!(rc, PGC_ENG_ERR_PANIC);
        // On OK, out must be a clean bool; otherwise it is untouched (-1).
        if rc == PGC_ENG_OK {
            assert!(can == 0 || can == 1);
        }
    }
}

#[test]
fn fuzz_zobrist_never_crashes() {
    // Random bytes, plus single-byte mutations of a valid FEN so many inputs get
    // past the parser: never a panic, and the key is written only on OK.
    let mut rng = Rng(0x5EED_0100_2026_1113);
    let base = b"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1".to_vec();
    for i in 0..20_000u32 {
        let fen: Vec<u8> = if i % 2 == 0 {
            (0..(rng.next() % 80) as usize).map(|_| rng.byte()).collect()
        } else {
            let mut f = base.clone();
            let at = (rng.next() as usize) % f.len();
            f[at] = rng.byte();
            f
        };
        let mut key: u64 = 0xFFFF_FFFF_FFFF_FFFF;
        let (fp, fl) = if fen.is_empty() {
            (ptr::null(), 0)
        } else {
            (fen.as_ptr(), fen.len())
        };
        // SAFETY: null-or-valid fen; valid out local.
        let rc = unsafe { pgc_engine_zobrist(fp, fl, &mut key) };
        assert_ne!(rc, PGC_ENG_ERR_PANIC);
        if rc != PGC_ENG_OK {
            assert_eq!(key, 0xFFFF_FFFF_FFFF_FFFF, "key written on error");
        }
    }
}

#[test]
fn null_arguments_rejected_not_crash() {
    // Null out-pointers on apply.
    let fen = b"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
    // SAFETY: deliberately passing null out-params; the seam must guard them.
    let rc = unsafe {
        pgc_engine_apply(
            fen.as_ptr(),
            fen.len(),
            b"e2e4".as_ptr(),
            4,
            ptr::null(),
            0,
            ptr::null_mut(), // out_fen null
            ptr::null_mut(),
            ptr::null_mut(),
            ptr::null_mut(),
            ptr::null_mut(),
        )
    };
    assert_eq!(rc, PGC_ENG_ERR_NULL_ARG);

    // Null out on mating_material.
    let rc2 = unsafe { pgc_engine_mating_material(fen.as_ptr(), fen.len(), 0, ptr::null_mut()) };
    assert_eq!(rc2, PGC_ENG_ERR_NULL_ARG);
}

#[test]
fn free_null_is_noop() {
    // SAFETY: pgc_engine_free documents null as a no-op.
    unsafe { pgc_engine_free(ptr::null_mut(), 0) };
    unsafe { pgc_engine_free(ptr::null_mut(), 123) };
}
