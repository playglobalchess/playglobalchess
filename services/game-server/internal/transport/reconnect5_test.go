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

// Fix-round review, S2-a: a game that ends after its first read but around the
// bind (still registered, so the bind succeeds) is shown as ended, not live.
func TestGameEndingAtTheBindIsShownEnded(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ac0000-0000-7000-8000-00000000c001"
	seedGame(t, st, gid, "blitz-5+3", "uma", "vic")
	var once atomic.Bool
	h.srv.beforeBind = func(id string) {
		if once.CompareAndSwap(false, true) {
			if sess, ok := h.srv.reg.Get(id); ok {
				_ = sess.Resign(ctx, 1)
			}
		}
	}
	c := reconnect(t, h, ctx, "uma")
	if m := c.waitForServer(t, codec.SMState, 2*time.Second); m.State.State.Status != codec.StatusEnded {
		t.Fatalf("a game that ended at the bind was shown as: %+v", m.State)
	}
}

// S2-b: a refused login consumes no missed ending. An ended game's recent row
// is read first; a live game that left the node during the login then refuses
// the login, and the recent row must still be there.
func TestRefusalAfterAnEndingConsumesNothing(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, ended, _, _ := pair(t, h, ctx, "walt", "xena")
	disconnect(ca)
	cb.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: ended})
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	live := "01ac0000-0000-7000-8000-00000000c002"
	seedGame(t, st, live, "blitz-5+3", "walt", "yuri")
	h.srv.afterBuild = func(id string) {
		if id == live {
			h.srv.dropGame(id)
		}
	}
	c := reconnect(t, h, ctx, "walt")
	c.waitForServer(t, codec.SMError, 2*time.Second)
	if r, _ := st.UserRecentGames(ctx, "walt", 5); len(r) != 1 || r[0].GameID != ended {
		t.Fatalf("recent rows after a refused login: %v, want the ending kept", r)
	}
}

// A refused login never takes a seat from the user's healthy connection: the
// refusal comes before any bind, so the teardown has nothing to unbind.
func TestRefusedLoginKeepsTheOtherConnectionSeated(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, _, gid, seatA, _ := pair(t, h, ctx, "zoe", "abe")
	other := "01ac0000-0000-7000-8000-00000000c003"
	seedGame(t, st, other, "blitz-5+3", "zoe", "bea")
	h.srv.afterBuild = func(id string) {
		if id == other {
			h.srv.dropGame(id)
		}
	}
	c2 := reconnect(t, h, ctx, "zoe")
	c2.waitForServer(t, codec.SMError, 2*time.Second)
	waitUntil(t, 2*time.Second, "the refused connection to close", func() bool {
		c2.mu.Lock()
		defer c2.mu.Unlock()
		return c2.closed
	})
	cc, ok := h.srv.connFor(gid, seatA)
	if pc, wrapped := cc.(*pumpedConn); wrapped {
		cc = pc.Conn
	}
	if !ok || cc != Conn(ca) {
		t.Fatal("after a refused login the seat is no longer held by the healthy connection")
	}
}

// S6-a: an event type this build does not know (a newer build wrote it) never
// makes a row deletable.
func TestUnknownEventIsNotPermanent(t *testing.T) {
	// The shape the rebuild path produces: ErrFold wrapping the fold error.
	err := fmt.Errorf("%w: %w", session.ErrFold, fmt.Errorf("event 3: %w 99", game.ErrUnknownEvent))
	if permanentRowErr(context.Background(), err) {
		t.Fatal("an unknown event type counted as a permanent error")
	}
}

// Confirming review, SF1: a live game that cannot be read after the bind
// refuses the login, so the client reconnects instead of sitting in a game it
// was never told about.
func TestUnreadableLiveGameAfterBindRefusesTheLogin(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ac0000-0000-7000-8000-00000000c004"
	seedGame(t, st, gid, "blitz-5+3", "cal", "dee")
	var once atomic.Bool
	h.srv.beforeBind = func(id string) {
		if once.CompareAndSwap(false, true) {
			if sess, ok := h.srv.reg.Get(id); ok {
				sess.Close() // still registered, but its actor no longer answers
			}
		}
	}
	c := reconnect(t, h, ctx, "cal")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("login with an unreadable live game: %+v", m.Error)
	}
	if rows, _ := st.UserLiveGames(ctx, "cal", 5); len(rows) != 1 {
		t.Fatalf("live rows: %v, want the row kept", rows)
	}
}
