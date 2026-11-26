package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// #125: a command sent after Run has exited must fail, never block.
func TestDispatchAfterRunExitDoesNotBlock(t *testing.T) {
	s, _, cancel := seedLive(t, startFEN, false, time.Now)
	cancel()
	time.Sleep(100 * time.Millisecond)
	res := make(chan error, 1)
	go func() { _, err := s.TickFlagCheck(context.Background()); res <- err }()
	select {
	case err := <-res:
		if !errors.Is(err, errSessionClosed) {
			t.Fatalf("err = %v, want errSessionClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TickFlagCheck blocked after Run exited")
	}
}

// #125: an out-of-range seat is refused before any append.
func TestOutOfRangeSeatRefusedBeforeAppend(t *testing.T) {
	s, store, cancel := seedLive(t, startFEN, false, time.Now)
	defer cancel()
	ctx := context.Background()
	before, _ := store.Load(ctx, game.StreamIDFor(gameID))
	if err := s.Resign(ctx, 9); !errors.Is(err, game.ErrSeatOutOfRange) {
		t.Fatalf("Resign(9) = %v, want ErrSeatOutOfRange", err)
	}
	if err := s.Abort(ctx, 9); !errors.Is(err, game.ErrSeatOutOfRange) {
		t.Fatalf("Abort(9) = %v, want ErrSeatOutOfRange", err)
	}
	after, _ := store.Load(ctx, game.StreamIDFor(gameID))
	if len(after) != len(before) {
		t.Fatalf("stream grew from %d to %d events", len(before), len(after))
	}
	if _, err := game.FoldWithEngine(ctx, after, game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}, mustOracle(t)); err != nil {
		t.Fatalf("stream no longer folds: %v", err)
	}
}

// The join-timeout abort uses the system sentinel and stays allowed.
func TestAbortBySystemActorAllowed(t *testing.T) {
	s, _, cancel := seedLive(t, startFEN, false, time.Now)
	defer cancel()
	if err := s.Abort(context.Background(), game.SystemActor); err != nil {
		t.Fatalf("Abort(SystemActor) = %v", err)
	}
}

func mustOracle(t *testing.T) game.LegalityOracle {
	t.Helper()
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	return o
}

type panicCmd struct {
	err  error
	done chan struct{}
}

func (c *panicCmd) apply(_ context.Context, s *Session) {
	s.st.Ply = 99 // half-updated projection, then the bug
	panic("boom")
}
func (c *panicCmd) fail(err error) { c.err = err }
func (c *panicCmd) finish()        { close(c.done) }

// #125: a panicking command yields an error to its caller, and the session
// rebuilds from the store and keeps serving.
func TestCommandPanicReturnsErrorAndSessionRecovers(t *testing.T) {
	s, _, cancel := seedLive(t, startFEN, false, time.Now)
	defer cancel()
	ctx := context.Background()
	c := &panicCmd{done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(c.err, errCommandPanic) {
		t.Fatalf("err = %v, want errCommandPanic", c.err)
	}
	if _, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil {
		t.Fatalf("move after a panic: %v", err)
	}
	if st, _ := readState(ctx, s); st.Ply != 1 {
		t.Fatalf("ply = %d after the move, want 1", st.Ply)
	}
}

// hangStore blocks writes until the command context ends.
type hangStore struct {
	*game.MemStore
	hang atomic.Bool
}

func (h *hangStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	if h.hang.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return h.MemStore.Append(ctx, id, seq, ev)
}

// #125: a hung store call is bounded by the command deadline and does not
// freeze the game.
func TestHungStoreBoundedByCommandTimeout(t *testing.T) {
	_, mem, cancel0 := seedLive(t, startFEN, false, time.Now)
	cancel0()
	hs := &hangStore{MemStore: mem}
	s, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: hs, Oracle: mustOracle(t),
		Opts:           game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}},
		CommandTimeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(rctx)
	hs.hang.Store(true)
	start := time.Now()
	if _, err := s.SubmitMove(context.Background(), 0, 1, "e2e4", 1); err == nil {
		t.Fatal("move against a hung store succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("hung call took %v, want about the 150ms deadline", d)
	}
	hs.hang.Store(false)
	if _, err := s.SubmitMove(context.Background(), 0, 1, "e2e4", 1); err != nil {
		t.Fatalf("game stayed frozen after the store recovered: %v", err)
	}
}

type cancelCmd struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *cancelCmd) apply(context.Context, *Session) { c.cancel() }
func (c *cancelCmd) fail(error)                      {}
func (c *cancelCmd) finish()                         { close(c.done) }

// A command that finished is never reported as closed, even when Run exits
// right after it (review of #208: done and quit were both ready).
func TestFinishedCommandNotReportedClosed(t *testing.T) {
	for i := 0; i < 100; i++ {
		s, _, cancel := seedLiveRaw(t, startFEN, false, time.Now)
		c := &cancelCmd{cancel: cancel, done: make(chan struct{})}
		if err := s.dispatch(context.Background(), c, c.done); err != nil {
			t.Fatalf("run %d: finished command reported %v", i, err)
		}
	}
}

// ambiguousStore commits the write, then reports the deadline error, and
// fails reads once the context has ended: the re-read cannot confirm.
type ambiguousStore struct {
	*game.MemStore
	on atomic.Bool
}

func (a *ambiguousStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	if a.on.Load() {
		if err := a.MemStore.Append(context.Background(), id, seq, ev); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return a.MemStore.Append(ctx, id, seq, ev)
}

func (a *ambiguousStore) Load(ctx context.Context, id string) ([]game.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.MemStore.Load(ctx, id)
}

// A write that landed but could not be confirmed leaves the projection to be
// rebuilt, so the next command sees it instead of a stale state.
func TestUnconfirmedWriteRebuildsBeforeNextCommand(t *testing.T) {
	_, mem, cancel0 := seedLive(t, startFEN, false, time.Now)
	cancel0()
	as := &ambiguousStore{MemStore: mem}
	s, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: as, Oracle: mustOracle(t),
		Opts:           game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}},
		CommandTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(rctx)
	as.on.Store(true)
	if _, err := s.SubmitMove(context.Background(), 0, 1, "e2e4", 1); err == nil {
		t.Fatal("unconfirmed write reported success")
	}
	as.on.Store(false)
	snap, err := s.StateSnapshot(context.Background())
	if err != nil || snap.Ply != 1 {
		t.Fatalf("snapshot after the unconfirmed write: ply=%d err=%v, want ply 1", snap.Ply, err)
	}
}

type holdCmd struct {
	release chan struct{}
	done    chan struct{}
	started chan struct{} // closed when the actor starts running it (optional)
	after   func()        // runs on the actor after release (optional)
}

func (c *holdCmd) apply(context.Context, *Session) {
	if c.started != nil {
		close(c.started)
	}
	<-c.release
	if c.after != nil {
		c.after()
	}
}
func (c *holdCmd) fail(error) {}
func (c *holdCmd) finish()    { close(c.done) }

// #183: a MOVE queued behind a busy actor is charged to its receipt time.
func TestQueuedMoveNotChargedTheWait(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	for i, uci := range []string{"e2e4", "e7e5"} {
		if o, err := s.SubmitMove(ctx, uint8(i), uint32(i+1), uci, 0); err != nil || !o.Accepted {
			t.Fatalf("opening %s: %+v %v", uci, o, err)
		}
	}
	fc.add(time.Second)
	received := uint64(fc.now().UnixMilli())
	// The actor is busy for 20 s of fake time: hold advances the clock on the
	// actor itself, so the move (one actor, one command at a time) can only be
	// decided after the wait, whatever the goroutine scheduling.
	hold := &holdCmd{
		release: make(chan struct{}), done: make(chan struct{}), started: make(chan struct{}),
		after: func() { fc.add(20 * time.Second) },
	}
	go func() { _ = s.dispatch(ctx, hold, hold.done) }()
	<-hold.started
	type res struct {
		out game.MoveOutcome
		err error
	}
	got := make(chan res, 1)
	go func() {
		o, err := s.SubmitMoveReceived(ctx, 0, 3, "g1f3", received)
		got <- res{o, err}
	}()
	close(hold.release)

	r := <-got
	if r.err != nil || !r.out.Accepted {
		t.Fatalf("move: %+v %v", r.out, r.err)
	}
	if r.out.ClockAfterMs != 302_000 { // 300000 - 1000 think + 3000 increment
		t.Fatalf("clock_after = %d, want 302000: the 20 s queue wait was charged", r.out.ClockAfterMs)
	}
}
