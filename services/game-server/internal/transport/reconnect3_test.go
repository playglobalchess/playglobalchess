package transport

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// timeoutOracle makes the first n PositionKey calls fail the way a cancelled
// or timed-out engine call does (a wrapped context error).
type timeoutOracle struct {
	game.LegalityOracle
	n atomic.Int32
}

func (o *timeoutOracle) PositionKey(ctx context.Context, fen string) (uint64, error) {
	if o.n.Add(-1) >= 0 {
		return 0, fmt.Errorf("engine: %w", context.DeadlineExceeded)
	}
	return o.LegalityOracle.PositionKey(ctx, fen)
}

// Review round 4, B1: a fold cut short by its context is not a broken stream.
// The login is refused and the live row is kept, so the next login restores it.
func TestTimedOutFoldKeepsTheLiveRow(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ab0000-0000-7000-8000-00000000b001"
	seedGame(t, st, gid, "blitz-5+3", "ivan", "jade")
	o := &timeoutOracle{LegalityOracle: h.srv.reg.oracle}
	o.n.Store(1)
	h.srv.reg.oracle = o
	c := reconnect(t, h, ctx, "ivan")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("error after a timed-out fold: %+v", m.Error)
	}
	if rows, _ := st.UserLiveGames(ctx, "ivan", 10); len(rows) != 1 {
		t.Fatalf("live rows after a timed-out fold: %v, want the row kept", rows)
	}
	m := reconnect(t, h, ctx, "ivan").waitForServer(t, codec.SMState, 2*time.Second)
	if m.State.GameID != gid || m.State.State.Status == codec.StatusEnded {
		t.Fatalf("STATE on the next login: %+v", m.State)
	}
}

// S1: a game dropped between its start and the seat lookup (a shutdown, say)
// leaves the in-memory seat map empty. That is not "the stream does not seat
// the user": the row is kept and the login is refused, not let through.
func TestGameDroppedDuringLoginKeepsTheRow(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ab0000-0000-7000-8000-00000000b002"
	seedGame(t, st, gid, "blitz-5+3", "kim", "lee")
	var once atomic.Bool
	h.srv.afterBuild = func(id string) {
		if once.CompareAndSwap(false, true) {
			h.srv.dropGame(id)
		}
	}
	c := reconnect(t, h, ctx, "kim")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("error after a drop during the login: %+v", m.Error)
	}
	if rows, _ := st.UserLiveGames(ctx, "kim", 10); len(rows) != 1 {
		t.Fatalf("live rows after a drop during the login: %v, want the row kept", rows)
	}
	h.srv.afterBuild = nil
	if m := reconnect(t, h, ctx, "kim").waitForServer(t, codec.SMState, 2*time.Second); m.State.GameID != gid {
		t.Fatalf("STATE on the next login: %+v", m.State)
	}
}

// B1 at the transport layer: a context error never makes a row deletable,
// even when something wrapped it as a fold error, and a done ctx never does.
func TestPermanentRowErrIgnoresContextErrors(t *testing.T) {
	ctx := context.Background()
	wrapped := fmt.Errorf("%w: %w", session.ErrFold, context.Canceled)
	if permanentRowErr(ctx, wrapped) {
		t.Fatal("a cancelled fold counted as permanent")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if permanentRowErr(done, fmt.Errorf("%w: x", errNoStream)) {
		t.Fatal("an error under a done ctx counted as permanent")
	}
	if !permanentRowErr(ctx, fmt.Errorf("%w: x", session.ErrFold)) {
		t.Fatal("a real fold error is permanent")
	}
}
