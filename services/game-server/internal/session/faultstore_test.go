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

func countType(t *testing.T, store game.EventStore, typ game.EventType) int {
	t.Helper()
	evs, err := store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestCommitThenErrorEveryCommand (#95): for every ending or batch command, a
// write that commits and then errors (with the confirming re-read either
// working or stale) leaves a stream that folds, holds exactly one ending (or
// one takeback rewind), and a session equal to a fresh fold of it after the
// command is resent or the next tick runs. assertRebuildMatches runs at stop.
func TestCommitThenErrorEveryCommand(t *testing.T) {
	play := func(t *testing.T, s *Session, fc *fakeClock, ucis ...string) {
		t.Helper()
		for i, u := range ucis {
			fc.add(time.Second)
			if o, err := s.SubmitMove(context.Background(), uint8(i%2), uint32(i+1), u, 1); err != nil || !o.Accepted {
				t.Fatalf("setup move %s: %+v %v", u, o, err)
			}
		}
	}
	type tc struct {
		name  string
		setup func(*testing.T, *Session, *fakeClock)
		act   func(*Session, *fakeClock) error // the faulted command, then its resend
		check func(*testing.T, *FaultStore)
	}
	ended := func(t *testing.T, fs *FaultStore) {
		t.Helper()
		if n := countType(t, fs, game.EvGameEnded); n != 1 {
			t.Fatalf("%d GameEnded events, want exactly 1", n)
		}
	}
	ctx := context.Background()
	var atPly uint32
	cases := []tc{
		{"flag", func(t *testing.T, s *Session, fc *fakeClock) {
			play(t, s, fc, "e2e4", "e7e5")
			fc.add(400 * time.Second)
		},
			func(s *Session, _ *fakeClock) error { _, err := s.TickFlagCheck(ctx); return err }, ended},
		{"resign", func(t *testing.T, s *Session, fc *fakeClock) { play(t, s, fc, "e2e4", "e7e5") },
			func(s *Session, _ *fakeClock) error { return s.Resign(ctx, 1) }, ended},
		{"abort", func(*testing.T, *Session, *fakeClock) {},
			func(s *Session, _ *fakeClock) error { return s.Abort(ctx, 0) }, ended},
		{"draw-accept", func(t *testing.T, s *Session, fc *fakeClock) {
			play(t, s, fc, "e2e4", "e7e5")
			if err := s.OfferDraw(ctx, 0); err != nil {
				t.Fatal(err)
			}
		}, func(s *Session, _ *fakeClock) error { return s.AcceptDraw(ctx, 1) }, ended},
		{"takeback-accept", func(t *testing.T, s *Session, fc *fakeClock) {
			play(t, s, fc, "e2e4", "e7e5", "g1f3")
			r, err := s.ProposeTakeback(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			atPly = r.AtPly
		}, func(s *Session, _ *fakeClock) error { _, err := s.AcceptTakeback(ctx, 1, atPly); return err },
			func(t *testing.T, fs *FaultStore) {
				t.Helper()
				if n := countType(t, fs, game.EvTakebackRewound); n != 1 {
					t.Fatalf("%d TakebackRewound events, want exactly 1", n)
				}
			}},
	}
	for _, c := range cases {
		for _, stale := range []bool{false, true} {
			name := c.name + "/reread-ok"
			if stale {
				name = c.name + "/reread-stale"
			}
			t.Run(name, func(t *testing.T) {
				s, fs, fc, stop := faultSession(t)
				defer stop()
				c.setup(t, s, fc)
				fs.CommitThenError(errFault)
				if stale {
					fs.StaleReads(1)
				}
				first := c.act(s, fc)
				if !stale && first != nil {
					t.Fatalf("committed write with a working re-read: %v, want acked", first)
				}
				fc.add(time.Second)
				_ = c.act(s, fc) // the resend / next tick: success or a clean refusal, never a second write
				snap, err := s.StateSnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_ = snap
				c.check(t, fs)
			})
		}
	}
}
