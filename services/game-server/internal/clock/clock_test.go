package clock

import (
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		tc      TimeControl
		wantErr bool
	}{
		{"blitz 5+3", TimeControl{BaseMs: 300_000, IncrementMs: 3_000}, false},
		{"bullet 1+0", TimeControl{BaseMs: 60_000}, false},
		{"daily per-move only", TimeControl{PerMoveMs: 86_400_000}, false},
		{"base+delay", TimeControl{BaseMs: 120_000, PerMoveMs: 2_000}, false},
		{"zero everything", TimeControl{}, true},
		{"negative base", TimeControl{BaseMs: -1}, true},
		{"negative increment", TimeControl{BaseMs: 1000, IncrementMs: -1}, true},
		{"negative per-move", TimeControl{BaseMs: 1000, PerMoveMs: -1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tc.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestNew(t *testing.T) {
	tc := TimeControl{BaseMs: 300_000, IncrementMs: 3_000}
	c, err := New(tc, 2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(c.RemainingMs) != 2 {
		t.Fatalf("seat count = %d, want 2", len(c.RemainingMs))
	}
	for i, r := range c.RemainingMs {
		if r != 300_000 {
			t.Errorf("seat %d start = %d, want 300000", i, r)
		}
	}
	if c.Flagged() {
		t.Error("fresh clock should not be flagged")
	}
	if c.FlaggedSeat != -1 {
		t.Errorf("FlaggedSeat = %d, want -1", c.FlaggedSeat)
	}

	if _, err := New(tc, 0); err == nil {
		t.Error("New with 0 seats: expected error")
	}
	if _, err := New(TimeControl{}, 2); err == nil {
		t.Error("New with invalid tc: expected error")
	}
}

func TestApply(t *testing.T) {
	cases := []struct {
		name     string
		tc       TimeControl
		seat     int
		elapsed  int64
		wantRem  int64 // expected remaining for the moving seat
		wantFlag bool
		wantErr  bool
	}{
		{
			name: "increment added after move",
			tc:   TimeControl{BaseMs: 300_000, IncrementMs: 3_000},
			seat: 0, elapsed: 10_000,
			wantRem: 300_000 - 10_000 + 3_000, // 293000
		},
		{
			name: "no increment",
			tc:   TimeControl{BaseMs: 60_000},
			seat: 0, elapsed: 5_000,
			wantRem: 55_000,
		},
		{
			name: "delay absorbs small move fully, no charge",
			tc:   TimeControl{BaseMs: 120_000, PerMoveMs: 2_000},
			seat: 0, elapsed: 1_500, // under the 2s delay
			wantRem: 120_000, // nothing charged, delay does not bank
		},
		{
			name: "delay absorbs part, excess charged",
			tc:   TimeControl{BaseMs: 120_000, PerMoveMs: 2_000},
			seat: 0, elapsed: 5_000, // 2s absorbed, 3s charged
			wantRem: 117_000,
		},
		{
			name: "exact zero on completed move is NOT a flag",
			tc:   TimeControl{BaseMs: 10_000},
			seat: 0, elapsed: 10_000,
			wantRem: 0, wantFlag: false,
		},
		{
			name: "exact zero then increment",
			tc:   TimeControl{BaseMs: 10_000, IncrementMs: 2_000},
			seat: 0, elapsed: 10_000,
			wantRem: 2_000, wantFlag: false,
		},
		{
			name: "exceeding time flags, clamped to 0, no increment",
			tc:   TimeControl{BaseMs: 10_000, IncrementMs: 2_000},
			seat: 0, elapsed: 10_001,
			wantRem: 0, wantFlag: true,
		},
		{
			name: "flag with delay: delay absorbs then overrun",
			tc:   TimeControl{BaseMs: 5_000, PerMoveMs: 2_000},
			seat: 0, elapsed: 8_000, // 2s absorbed, 6s charged vs 5s -> flag
			wantRem: 0, wantFlag: true,
		},
		{
			name: "negative elapsed is an error",
			tc:   TimeControl{BaseMs: 10_000},
			seat: 0, elapsed: -1,
			wantErr: true,
		},
		{
			name: "seat out of range is an error",
			tc:   TimeControl{BaseMs: 10_000},
			seat: 5, elapsed: 1_000,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, err := New(c.tc, 2)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			next, err := cl.Apply(c.tc, c.seat, c.elapsed)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if c.wantFlag {
				if !errors.Is(err, ErrFlagged) {
					t.Fatalf("expected ErrFlagged, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if next.RemainingMs[c.seat] != c.wantRem {
				t.Errorf("remaining = %d, want %d", next.RemainingMs[c.seat], c.wantRem)
			}
			if next.Flagged() != c.wantFlag {
				t.Errorf("Flagged() = %v, want %v", next.Flagged(), c.wantFlag)
			}
			if c.wantFlag && next.FlaggedSeat != c.seat {
				t.Errorf("FlaggedSeat = %d, want %d", next.FlaggedSeat, c.seat)
			}
		})
	}
}

// TestApplyDoesNotMutateReceiver proves Apply is a pure value transform (needed
// for deterministic re-folds / takeback).
func TestApplyDoesNotMutateReceiver(t *testing.T) {
	tc := TimeControl{BaseMs: 300_000, IncrementMs: 3_000}
	cl, _ := New(tc, 2)
	before := cl.RemainingMs[0]
	_, err := cl.Apply(tc, 0, 10_000)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cl.RemainingMs[0] != before {
		t.Errorf("receiver mutated: %d -> %d", before, cl.RemainingMs[0])
	}
}

// TestApplyOnFlaggedRejected proves a game cannot continue after a flag.
func TestApplyOnFlaggedRejected(t *testing.T) {
	tc := TimeControl{BaseMs: 1_000}
	cl, _ := New(tc, 2)
	flagged, err := cl.Apply(tc, 0, 2_000)
	if !errors.Is(err, ErrFlagged) {
		t.Fatalf("expected flag, got %v", err)
	}
	if _, err := flagged.Apply(tc, 1, 100); err == nil {
		t.Error("Apply after flag: expected error, got nil")
	}
}

// TestFlagBoundary nails the exact boundary (reaches 0 vs goes below 0).
func TestFlagBoundary(t *testing.T) {
	tc := TimeControl{BaseMs: 1_000}
	cl, _ := New(tc, 2)
	// Exactly 1000ms used: lands on 0, not a flag.
	at0, err := cl.Apply(tc, 0, 1_000)
	if err != nil {
		t.Fatalf("exact-zero move errored: %v", err)
	}
	if at0.Flagged() {
		t.Error("landing on exactly 0 must not flag")
	}
	// One ms over: flags.
	over, err := cl.Apply(tc, 0, 1_001)
	if !errors.Is(err, ErrFlagged) {
		t.Fatalf("1ms over should flag, got %v", err)
	}
	if over.RemainingMs[0] != 0 {
		t.Errorf("flagged remaining = %d, want 0", over.RemainingMs[0])
	}
}
