// Package engine is the Go game-server's in-process binding to the one
// PlayGlobalChess rules engine (X-1, ADR-11, ADR-15, SPEC-S2-GAME-SERVER-GS2).
// It links the pgc-engine-cgo staticlib via cgo and calls the SAME legality +
// terminal detection the client core and mobile bind — so the server is the
// single legality authority (NFR-5) and holds no chess logic of its own.
//
// CgoOracle implements game.LegalityOracle (the GS-1 seam): the move pipeline
// (internal/game) validates every move through this against the real engine,
// replacing the GS-1 test-double.
//
// The staticlib (libpgc_engine_cgo.a) is produced by `make lib` (see the
// Makefile) and staged into ./lib/ — a gitignored build output the cgo LDFLAGS
// below resolve against. The C header is NOT copied: cgo includes it directly
// from the canonical crate source (engine/engine-cgo/include) via the CFLAGS -I
// below, so there is no second header copy to drift.
//
// If you run `go build/test/vet ./...` directly and see a linker error like
// "library 'pgc_engine_cgo' not found", the staticlib has not been built yet —
// run `make lib` (or `make test`) first. CI runs `make test`, which builds it.
package engine

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../engine/engine-cgo/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lpgc_engine_cgo
#include <stdlib.h>
#include "engine_cgo.h"
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// CgoOracle is the real game.LegalityOracle: the S1 engine over cgo. It is a
// stateless value (the engine is pure); construct one and share it. No global
// mutable state.
type CgoOracle struct{}

// New returns a CgoOracle. It also asserts the linked engine ABI version matches
// what this binding expects, so a stale .a fails fast rather than mis-decoding.
func New() (CgoOracle, error) {
	if v := uint32(C.pgc_engine_abi_version()); v != expectedABIVersion {
		return CgoOracle{}, fmt.Errorf("engine-cgo: linked ABI version %d, expected %d", v, expectedABIVersion)
	}
	return CgoOracle{}, nil
}

// expectedABIVersion mirrors PGC_ENGINE_ABI_VERSION in the crate; bump together.
const expectedABIVersion = 1

var _ game.LegalityOracle = CgoOracle{}

// Legality validates a UCI move against a FEN via the engine and, when legal,
// returns the resulting position, its Zobrist key, and any terminal condition.
// It makes exactly one engine round-trip (the move path's O(1) oracle cost,
// GS-1 PR #34).
//
// priorKeys is the folded Zobrist history BEFORE the resulting position
// (status_with_history's convention). The live session (GS-3) threads it so the
// engine can detect threefold/fivefold REPETITION (Q-GS5). A nil/empty history
// means repetition is simply not detectable this call — every other terminal
// (mate / stalemate / insufficient / 75-move) still is.
func (CgoOracle) Legality(ctx context.Context, fen, uci string, priorKeys []uint64) (game.LegalityVerdict, error) {
	if err := ctx.Err(); err != nil {
		return game.LegalityVerdict{}, err
	}

	fenPtr, fenLen := bytesOf(fen)
	uciPtr, uciLen := bytesOf(uci)
	keysPtr, keysLen := u64sOf(priorKeys)

	var out *C.uint8_t
	var outLen C.size_t
	var status, claim C.int32_t
	var zobrist C.uint64_t

	rc := C.pgc_engine_apply(
		fenPtr, fenLen,
		uciPtr, uciLen,
		keysPtr, keysLen, // prior_keys: the folded history, so repetition is seen (Q-GS5)
		&out, &outLen,
		&status, &claim,
		&zobrist,
	)

	switch int32(rc) {
	case C.PGC_ENG_OK:
		defer C.pgc_engine_free(out, outLen)
		newFEN := C.GoStringN((*C.char)(unsafe.Pointer(out)), C.int(outLen))
		return game.LegalityVerdict{
			Legal:      true,
			NewFEN:     newFEN,
			NewZobrist: uint64(zobrist),
			Terminal:   terminalOf(int32(status), int32(claim)),
		}, nil
	case C.PGC_ENG_ERR_MOVE, C.PGC_ENG_ERR_ILLEGAL:
		// Both are "not a legal move in this position" to the pipeline, which
		// emits MOVE_REJECTED{ILLEGAL}. No event, no error.
		return game.LegalityVerdict{Legal: false}, nil
	default:
		return game.LegalityVerdict{}, errOf(int32(rc))
	}
}

// HasSufficientMaterial reports whether seatIdx has mating material (the per-side
// FIDE-6.9 flag-branch query, SPEC §3.3). v1 2-seat: seat 0 -> White, seat 1 ->
// Black (seat->color is the server's mapping; the engine is color-centric).
func (CgoOracle) HasSufficientMaterial(ctx context.Context, fen string, seatIdx uint8) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if seatIdx > 1 {
		return false, fmt.Errorf("engine-cgo: seat_idx %d out of range (v1 is 2-seat)", seatIdx)
	}
	fenPtr, fenLen := bytesOf(fen)
	var canMate C.int32_t
	rc := C.pgc_engine_mating_material(fenPtr, fenLen, C.uint8_t(seatIdx), &canMate)
	if int32(rc) != C.PGC_ENG_OK {
		return false, errOf(int32(rc))
	}
	return canMate != 0, nil
}

// terminalOf maps the engine's (status, claim) scalars to the §4A.5 Termination
// the pipeline puts on a GameEnded — Q-GS2E-2: GOVERNANCE lives in Go, not the C
// seam. Only AUTOMATIC terminals end the game from a move; a *claimable* draw
// (threefold / 50-move) is availability the server surfaces as an offer, NOT an
// auto-end, so it maps to nil here (the game continues until a claim event).
func terminalOf(status, claim int32) *game.Termination {
	var t game.Termination
	switch status {
	case C.PGC_ENG_STATUS_CHECKMATE:
		t = game.TermMate
	case C.PGC_ENG_STATUS_STALEMATE:
		t = game.TermStalemate
	case C.PGC_ENG_STATUS_INSUFFICIENT:
		t = game.TermInsufficient
	case C.PGC_ENG_STATUS_FIFTYMOVE:
		if claim != C.PGC_ENG_CLAIM_AUTOMATIC {
			return nil // 50-move is claimable, not auto-terminal
		}
		t = game.TermFiftyMove
	case C.PGC_ENG_STATUS_REPETITION:
		if claim != C.PGC_ENG_CLAIM_AUTOMATIC {
			return nil // threefold is claimable, not auto-terminal
		}
		t = game.TermRepetition
	default: // ONGOING or anything non-terminal
		return nil
	}
	return &t
}

// errOf maps a non-OK, non-legality status to a Go error (I/O-class failures the
// pipeline surfaces as ERROR{INTERNAL}).
func errOf(rc int32) error {
	switch rc {
	case C.PGC_ENG_ERR_FEN:
		return fmt.Errorf("engine-cgo: invalid FEN")
	case C.PGC_ENG_ERR_NULL_ARG:
		return fmt.Errorf("engine-cgo: null argument")
	case C.PGC_ENG_ERR_PANIC:
		return fmt.Errorf("engine-cgo: panic at engine boundary (see engine stderr log)")
	default:
		return fmt.Errorf("engine-cgo: unknown error %d", rc)
	}
}

// bytesOf returns a cgo (ptr,len) view of s's bytes for the duration of the
// call. For an empty string it returns (nil, 0), which the C side accepts.
func bytesOf(s string) (*C.uint8_t, C.size_t) {
	if len(s) == 0 {
		return nil, 0
	}
	// unsafe.StringData's bytes are pinned for this cgo call; the engine only
	// reads them.
	return (*C.uint8_t)(unsafe.Pointer(unsafe.StringData(s))), C.size_t(len(s))
}

// u64sOf returns a cgo (ptr,len) view of a []uint64 for the duration of the
// call (the engine reads it as const uint64_t*). Empty slice -> (nil, 0), which
// the C side accepts (prior_keys may be NULL iff prior_keys_len == 0).
func u64sOf(ks []uint64) (*C.uint64_t, C.size_t) {
	if len(ks) == 0 {
		return nil, 0
	}
	// &ks[0] is pinned for this cgo call; the engine only reads the slice.
	return (*C.uint64_t)(unsafe.Pointer(&ks[0])), C.size_t(len(ks))
}
