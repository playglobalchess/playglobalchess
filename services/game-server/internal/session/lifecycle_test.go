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
