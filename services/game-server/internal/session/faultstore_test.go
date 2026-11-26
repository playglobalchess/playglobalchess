package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

var errFault = errors.New("fault: injected store failure")

// faultSession builds a running session over a FaultStore that wraps a seeded
// MemStore. The returned stop checks the persistence invariant, then cancels.
func faultSession(t *testing.T) (*Session, *FaultStore, *fakeClock, func()) {
	t.Helper()
	fc := &fakeClock{t: time.Unix(0, 0)}
	_, mem, cancel0 := seedLiveRaw(t, startFEN, false, fc.now)
	cancel0()
	fs := NewFaultStore(mem)
	s, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: fs, Oracle: mustOracle(t), Now: fc.now,
		Opts: game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	go s.Run(rctx)
	return s, fs, fc, func() {
		if !t.Failed() {
			assertRebuildMatches(t, s, fs)
		}
		cancel()
	}
}

func moveEvents(t *testing.T, store game.EventStore) int {
	t.Helper()
	evs, err := store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == game.EvMovePlayed {
			n++
		}
	}
	return n
}

// A single-event write that commits and then errors (a timeout after the store
// accepted it, #89/#95): the command DID happen, so it is acked once.
func TestCommitThenErrorSingleAppendIsAcked(t *testing.T) {
	s, fs, fc, stop := faultSession(t)
	defer stop()
	ctx := context.Background()
	fc.add(time.Second)
	fs.CommitThenError(errFault)
	out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err != nil || !out.Accepted {
		t.Fatalf("commit-then-error move: accepted=%v err=%v, want acked", out.Accepted, err)
	}
	if fs.Fired() != 1 || moveEvents(t, fs) != 1 {
		t.Fatalf("fired=%d moves=%d, want 1 fault and exactly one stored move", fs.Fired(), moveEvents(t, fs))
	}
}

// The same for a batched write (resign appends Resigned + GameEnded).
func TestCommitThenErrorBatchIsAcked(t *testing.T) {
	s, fs, fc, stop := faultSession(t)
	defer stop()
	ctx := context.Background()
	fc.add(time.Second)
	if o, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !o.Accepted {
		t.Fatalf("setup move: %+v %v", o, err)
	}
	fs.CommitThenError(errFault)
	if err := s.Resign(ctx, 1); err != nil {
		t.Fatalf("commit-then-error resign: %v, want acked", err)
	}
	if snap, _ := s.StateSnapshot(ctx); !snap.Ended {
		t.Fatal("resign was committed but the session is not ended")
	}
}

// A write that fails before committing changes nothing, for both write paths,
// and the same command works once the store is back.
func TestErrorBeforeCommitChangesNothing(t *testing.T) {
	s, fs, fc, stop := faultSession(t)
	defer stop()
	ctx := context.Background()
	fc.add(time.Second)
	fs.ErrorBeforeCommit(errFault)
	if _, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err == nil {
		t.Fatal("move over a failing store succeeded")
	}
	if n := moveEvents(t, fs); n != 0 {
		t.Fatalf("%d moves stored by a write that failed before committing", n)
	}
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted {
		t.Fatalf("retry after the store recovered: %+v %v", out, err)
	}
	fs.ErrorBeforeCommit(errFault)
	if err := s.Resign(ctx, 1); err == nil {
		t.Fatal("resign over a failing store succeeded")
	}
	if snap, _ := s.StateSnapshot(ctx); snap.Ended {
		t.Fatal("a failed batch write ended the game")
	}
}

// Commit-then-error whose confirming re-read is STALE (one event behind): the
// command is not confirmed, but the retry must be a re-ack of the committed
// move, never a second copy (#95/#96).
func TestCommitThenErrorWithStaleReReadRetriesAsReack(t *testing.T) {
	s, fs, fc, stop := faultSession(t)
	defer stop()
	ctx := context.Background()
	fc.add(time.Second)
	fs.CommitThenError(errFault)
	fs.StaleReads(1)
	_, _ = s.SubmitMove(ctx, 0, 1, "e2e4", 1) // acked or not: both are allowed here
	fc.add(time.Second)
	out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err != nil || !out.Accepted {
		t.Fatalf("resend of a committed move: %+v %v, want a re-ack", out, err)
	}
	if fs.Fired() != 2 {
		t.Fatalf("fired=%d, want the commit fault and the stale read", fs.Fired())
	}
	if n := moveEvents(t, fs); n != 1 {
		t.Fatalf("%d MovePlayed events stored, want exactly 1", n)
	}
	if snap, _ := s.StateSnapshot(ctx); snap.Ply != 1 {
		t.Fatalf("ply = %d, want 1", snap.Ply)
	}
}
