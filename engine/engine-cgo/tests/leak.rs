//! cgo leak test (SPEC-S2-GAME-SERVER-GS2 §8): a counting global allocator
//! asserts every buffer handed out by a `pgc_engine_apply` success is released
//! by exactly one `pgc_engine_free` — the whole seam's allocations balance.
//!
//! This lives in its own test binary because `#[global_allocator]` is
//! process-wide; isolating it keeps the fuzz/unit tests on the system allocator.

use pgc_engine_cgo::*;
use std::alloc::{GlobalAlloc, Layout, System};
use std::ptr;
use std::sync::atomic::{AtomicI64, Ordering};

static LIVE_BYTES: AtomicI64 = AtomicI64::new(0);
static LIVE_BLOCKS: AtomicI64 = AtomicI64::new(0);

struct Counting;

// SAFETY: delegates every call to System; only adds atomic counters around it.
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let p = unsafe { System.alloc(layout) };
        if !p.is_null() {
            LIVE_BYTES.fetch_add(layout.size() as i64, Ordering::Relaxed);
            LIVE_BLOCKS.fetch_add(1, Ordering::Relaxed);
        }
        p
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        LIVE_BYTES.fetch_sub(layout.size() as i64, Ordering::Relaxed);
        LIVE_BLOCKS.fetch_sub(1, Ordering::Relaxed);
        unsafe { System.dealloc(ptr, layout) };
    }
}

#[global_allocator]
static ALLOC: Counting = Counting;

const START: &[u8] = b"rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

/// Apply one legal move and free the output. Returns the status.
fn apply_and_free(fen: &[u8], uci: &[u8]) -> i32 {
    let mut out: *mut u8 = ptr::null_mut();
    let mut out_len: usize = 0;
    let mut st = 0i32;
    let mut claim = 0i32;
    let mut zob = 0u64;
    // SAFETY: valid slices + valid out locals.
    let rc = unsafe {
        pgc_engine_apply(
            fen.as_ptr(),
            fen.len(),
            uci.as_ptr(),
            uci.len(),
            ptr::null(),
            0,
            &mut out,
            &mut out_len,
            &mut st,
            &mut claim,
            &mut zob,
        )
    };
    if rc == PGC_ENG_OK {
        assert!(!out.is_null() && out_len > 0, "OK must hand out a buffer");
        // SAFETY: free exactly the buffer the success handed us.
        unsafe { pgc_engine_free(out, out_len) };
    }
    rc
}

#[test]
fn apply_outputs_are_fully_freed() {
    // Baseline after warm-up (the allocator counts test-harness allocations too;
    // we measure the DELTA across a batch that must net to zero).
    let legal_moves = [
        &b"e2e4"[..],
        &b"g1f3"[..],
        &b"d2d4"[..],
        &b"b1c3"[..],
        &b"e2e3"[..],
    ];

    // Warm up once so lazy statics etc. are allocated before we snapshot.
    assert_eq!(apply_and_free(START, legal_moves[0]), PGC_ENG_OK);

    let bytes_before = LIVE_BYTES.load(Ordering::Relaxed);
    let blocks_before = LIVE_BLOCKS.load(Ordering::Relaxed);

    for _ in 0..5_000 {
        for mv in &legal_moves {
            let rc = apply_and_free(START, mv);
            assert_eq!(rc, PGC_ENG_OK, "move {mv:?} should be legal from start");
        }
    }
    // Also exercise error paths (which must allocate NOTHING that outlives them).
    for _ in 0..5_000 {
        let _ = apply_and_free(START, b"zzzz"); // malformed
        let _ = apply_and_free(START, b"e2e5"); // illegal
        let _ = apply_and_free(b"bad fen", b"e2e4"); // bad fen
    }

    let bytes_after = LIVE_BYTES.load(Ordering::Relaxed);
    let blocks_after = LIVE_BLOCKS.load(Ordering::Relaxed);

    assert_eq!(
        bytes_after, bytes_before,
        "live bytes grew by {} — a pgc_engine_apply output was not freed",
        bytes_after - bytes_before
    );
    assert_eq!(
        blocks_after, blocks_before,
        "live blocks grew by {} — an allocation leaked across the seam",
        blocks_after - blocks_before
    );
}
