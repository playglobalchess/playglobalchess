// Package clock is the deterministic per-seat time model for a live game
// (SPEC-S2-GAME-SERVER §3.3). It is the sole clock authority: given a time
// control and an ordered record of which seat moved and how much wall time each
// move consumed, it computes remaining-ms per seat and detects a flag (a seat
// reaching 0 before completing its move).
//
// Scope (GS-1): PURE arithmetic only. No wall-clock reads, no timers, no
// network. Elapsed time is supplied by the caller (the server samples the clock
// against server_ts; that sampling is a later chunk). This keeps the model
// deterministic and table-testable: the same (TimeControl, sequence of
// per-move elapsed values) always yields the same clocks.
//
// Out of scope here (gated behind interfaces / later chunks):
//   - TIMEOUT_VS_INSUFFICIENT: whether a flag ends as FLAG or
//     TIMEOUT_VS_INSUFFICIENT depends on the opponent having insufficient
//     mating material, which only the S1 engine may answer (NFR-5). This
//     package reports the flag; the game layer asks the engine which
//     termination applies. The clock never computes material.
//   - Pool-id -> TimeControl expansion (GET /v1/pools); the expanded struct is
//     an INPUT to this package (SPEC-S2-GAME-SERVER §3.3).
package clock

import (
	"errors"
	"fmt"
)

// TimeControl is the expanded time control for one game (SPEC-S2-GAME-SERVER
// §3.3). A TimeControlId string is expanded to this once, elsewhere; this
// package receives the expanded form and never fetches it.
type TimeControl struct {
	// BaseMs is each seat's starting time in milliseconds.
	BaseMs int64
	// IncrementMs is Fischer increment: added to a seat's remaining time AFTER
	// it completes a move (so time can grow). Zero for no increment.
	IncrementMs int64
	// PerMoveMs is a per-move time allowance that does NOT accumulate (the
	// "daily"/simple-delay style, SPEC-S2-GAME-SERVER §3.3 "(daily) per-move").
	// When > 0, up to PerMoveMs of a move's elapsed time is absorbed by the
	// allowance before it is charged against the seat's remaining base time —
	// unused delay is forfeited, it does not bank. Zero means no delay.
	PerMoveMs int64
}

// Validate rejects a nonsensical time control. A game must give each seat some
// time to think: either a positive base, or (for a pure per-move/daily control)
// a positive per-move allowance.
func (tc TimeControl) Validate() error {
	if tc.BaseMs < 0 || tc.IncrementMs < 0 || tc.PerMoveMs < 0 {
		return fmt.Errorf("clock: negative time control component: %+v", tc)
	}
	if tc.BaseMs == 0 && tc.PerMoveMs == 0 {
		return fmt.Errorf("clock: time control gives no thinking time: %+v", tc)
	}
	return nil
}

// ErrFlagged is returned by Apply when the moving seat's elapsed time exceeds
// what it had available: the seat's clock reaches 0 before the move completes
// (SPEC-S2-GAME-SERVER §3.3 flag detection). The resulting Clock has that
// seat's RemainingMs clamped to 0 and Flagged recorded.
var ErrFlagged = errors.New("clock: seat flagged (time expired)")

// Clock holds the per-seat remaining time for one game. seat_idx is 0-based and
// indexes RemainingMs (SPEC-S2-PROTOCOL §3.3; v1 is 2 seats but this is sized
// to the seat count given to New). It is a value type: Apply returns a new
// Clock rather than mutating, so a fold/re-fold (takeback) is side-effect-free
// and a Clock can be snapshotted freely.
type Clock struct {
	// RemainingMs[seat] is that seat's remaining base time. Index out of range
	// is a programming error (the game layer validates seat_idx first).
	RemainingMs []int64
	// FlaggedSeat is the seat that ran out of time, or -1 if none has.
	FlaggedSeat int
}

// New builds a fresh clock: every seat starts at BaseMs. seatCount must be
// positive. The returned Clock has no seat flagged.
func New(tc TimeControl, seatCount int) (Clock, error) {
	if err := tc.Validate(); err != nil {
		return Clock{}, err
	}
	if seatCount <= 0 {
		return Clock{}, fmt.Errorf("clock: seat count must be positive, got %d", seatCount)
	}
	rem := make([]int64, seatCount)
	for i := range rem {
		rem[i] = tc.BaseMs
	}
	return Clock{RemainingMs: rem, FlaggedSeat: -1}, nil
}

// Flagged reports whether some seat has run out of time.
func (c Clock) Flagged() bool { return c.FlaggedSeat >= 0 }

// clone returns a deep copy so Apply never mutates its receiver's backing array.
func (c Clock) clone() Clock {
	rem := make([]int64, len(c.RemainingMs))
	copy(rem, c.RemainingMs)
	return Clock{RemainingMs: rem, FlaggedSeat: c.FlaggedSeat}
}

// CloneWith returns a copy of the clock with one seat's remaining time set to
// remainingMs. It is used by the game fold to project a seat's clock from a
// stored MovePlayed.ClockAfterMs (the authoritative post-move remaining the
// server already computed with Apply) rather than recomputing from elapsed time
// — the stored event is the truth. It performs no flag detection (a stored
// ClockAfterMs is by construction a non-flagging value; a flag is recorded as a
// separate ClockFlagged event). A negative remainingMs is clamped to 0.
func (c Clock) CloneWith(seat int, remainingMs int64) Clock {
	next := c.clone()
	if seat >= 0 && seat < len(next.RemainingMs) {
		if remainingMs < 0 {
			remainingMs = 0
		}
		next.RemainingMs[seat] = remainingMs
	}
	return next
}

// Apply charges elapsedMs of thinking time to seat for one completed move and
// returns the resulting clock (SPEC-S2-GAME-SERVER §3.3 "applies base/increment/
// delay deterministically on each MovePlayed").
//
// elapsedMs is the wall time the seat spent on this move, supplied by the
// caller (never read from a client — the server times it). It must be >= 0.
//
// Order of operations (fixed, deterministic):
//  1. A per-move delay allowance (PerMoveMs) absorbs the first PerMoveMs of
//     elapsed time; only the excess is charged against RemainingMs. Unused
//     delay is forfeited (it does not bank — that is the difference from
//     increment).
//  2. The charged amount is subtracted from the seat's RemainingMs.
//  3. If RemainingMs would go below 0, the seat has FLAGGED: RemainingMs is
//     clamped to 0, FlaggedSeat is set, increment is NOT granted, and
//     ErrFlagged is returned alongside the (clamped) clock so the caller can
//     drive the ClockFlagged -> GameEnded transition.
//  4. Otherwise, if the move did not flag, IncrementMs is added AFTER the move
//     (Fischer increment grows time).
//
// A clock that is already flagged rejects further moves (the game is over).
func (c Clock) Apply(tc TimeControl, seat int, elapsedMs int64) (Clock, error) {
	if err := tc.Validate(); err != nil {
		return c, err
	}
	if seat < 0 || seat >= len(c.RemainingMs) {
		return c, fmt.Errorf("clock: seat %d out of range [0,%d)", seat, len(c.RemainingMs))
	}
	if elapsedMs < 0 {
		return c, fmt.Errorf("clock: negative elapsed time %d", elapsedMs)
	}
	if c.Flagged() {
		return c, fmt.Errorf("clock: game already ended (seat %d flagged)", c.FlaggedSeat)
	}

	next := c.clone()

	// (1) delay allowance absorbs the leading PerMoveMs of elapsed time.
	charged := elapsedMs - tc.PerMoveMs
	if charged < 0 {
		charged = 0
	}

	// (2) charge against remaining base time.
	next.RemainingMs[seat] -= charged

	// (3) flag detection.
	if next.RemainingMs[seat] < 0 {
		next.RemainingMs[seat] = 0
		next.FlaggedSeat = seat
		return next, ErrFlagged
	}

	// (4) grant increment only on a completed, non-flagging move.
	next.RemainingMs[seat] += tc.IncrementMs
	return next, nil
}
