package session

import (
	"context"
	"testing"
	"time"
)

// SPEC-S4-WEB-F01 §3.3.1 C9: StateSnapshot.RemainingMs is the remaining time as
// of the instant the snapshot is built. For the side to move that is
// RemainingMs[turn] - (now - lastMoveAt), floored at 0; other seats unchanged;
// no subtraction once the game has ended. Uses the session's injected fake now.

const baseMs = int64(300_000) // seedLive: blitz 5+3

func TestSnapshotChargesSideToMoveForThinkTime(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(7 * time.Second) // White has thought for 7 s since the game began
	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got, want := snap.RemainingMs[0], baseMs-7_000; got != want {
		t.Errorf("side to move RemainingMs[0] = %d, want %d", got, want)
	}
	if got := snap.RemainingMs[1]; got != baseMs {
		t.Errorf("non-side-to-move RemainingMs[1] = %d, want unchanged %d", got, baseMs)
	}
}

func TestSnapshotChargesTheNewSideToMoveAfterAMove(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(2 * time.Second)
	if o, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !o.Accepted {
		t.Fatalf("setup move not accepted: %+v", o)
	}
	whiteAfter := baseMs - 2_000 + 3_000 // charged 2 s, +3 s Fischer increment
	fc.add(5 * time.Second)              // Black thinks for 5 s
	snap, _ := s.StateSnapshot(ctx)
	if got := snap.RemainingMs[1]; got != baseMs-5_000 {
		t.Errorf("black (to move) RemainingMs[1] = %d, want %d", got, baseMs-5_000)
	}
	if got := snap.RemainingMs[0]; got != whiteAfter {
		t.Errorf("white (not to move) RemainingMs[0] = %d, want unchanged %d", got, whiteAfter)
	}
}

func TestSnapshotFloorsAtZeroAndDoesNotMutateTheClock(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(400 * time.Second) // more than White's whole clock
	snap, _ := s.StateSnapshot(ctx)
	if got := snap.RemainingMs[0]; got != 0 {
		t.Errorf("overdrawn side RemainingMs[0] = %d, want floor 0", got)
	}
	if snap.Ended {
		t.Error("a snapshot must only report the clock; the flag is decided by TickFlagCheck")
	}
}

func TestSnapshotIsReadOnlyNotCumulative(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(1 * time.Second)
	s.StateSnapshot(ctx)
	fc.add(1 * time.Second)
	snap, _ := s.StateSnapshot(ctx)
	// 2 s since the (only) baseline, not 1 + 2: the first snapshot charged nothing.
	if got, want := snap.RemainingMs[0], baseMs-2_000; got != want {
		t.Errorf("second snapshot RemainingMs[0] = %d, want %d (snapshots must not accumulate)", got, want)
	}
	// And the real charge on the next move still sees the full 2 s.
	if o, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !o.Accepted {
		t.Fatalf("move not accepted: %+v", o)
	}
	after, _ := s.StateSnapshot(ctx)
	if got, want := after.RemainingMs[0], baseMs-2_000+3_000; got != want {
		t.Errorf("after the move RemainingMs[0] = %d, want %d", got, want)
	}
}

func TestSnapshotOfEndedGameIsNotCharged(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(3 * time.Second)
	if err := s.Resign(ctx, 1); err != nil {
		t.Fatalf("resign: %v", err)
	}
	ended, _ := s.StateSnapshot(ctx)
	if !ended.Ended {
		t.Fatal("game should be ended")
	}
	fc.add(60 * time.Second) // time passing after the end must not be charged
	later, _ := s.StateSnapshot(ctx)
	for seat := range later.RemainingMs {
		if later.RemainingMs[seat] != ended.RemainingMs[seat] {
			t.Errorf("seat %d RemainingMs changed after the game ended: %d -> %d",
				seat, ended.RemainingMs[seat], later.RemainingMs[seat])
		}
	}
}

func TestSnapshotNeverAddsTimeWhenTheClockStepsBackwards(t *testing.T) {
	fc := &fakeClock{t: time.Unix(1000, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	fc.add(-30 * time.Second) // wall clock stepped back
	snap, _ := s.StateSnapshot(context.Background())
	if got := snap.RemainingMs[0]; got != baseMs {
		t.Errorf("RemainingMs[0] = %d after a backwards step, want %d (never above the stored value)", got, baseMs)
	}
}
