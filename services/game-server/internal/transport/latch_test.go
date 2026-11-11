package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// #88: a GAME_END broadcast that does not go out must not latch the claim and
// leak the session, the indices and the flag ticker.

// tickerWatch records which games' flag tickers have exited.
type tickerWatch struct {
	mu     sync.Mutex
	exited map[string]bool
}

func watchTickers(h *harness) *tickerWatch {
	w := &tickerWatch{exited: map[string]bool{}}
	h.srv.tickerExited = func(gameID string) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.exited[gameID] = true
	}
	return w
}

func (w *tickerWatch) gone(gameID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exited[gameID]
}

// wantReclaimed waits until every trace of gameID is gone from the node: the
// live session, the seat bindings, the user index, the end claim, the join
// signal, and the flag ticker goroutine.
func wantReclaimed(t *testing.T, h *harness, w *tickerWatch, gameID string) {
	t.Helper()
	waitUntil(t, 2*time.Second, "the game to be reclaimed", func() bool {
		if _, live := h.srv.reg.Get(gameID); live {
			return false
		}
		s := h.srv
		s.mu.Lock()
		defer s.mu.Unlock()
		for gs := range s.conns {
			if gs.gameID == gameID {
				return false
			}
		}
		for _, games := range s.userGames {
			if _, ok := games[gameID]; ok {
				return false
			}
		}
		_, claimed := s.endBroadcast[gameID]
		_, joined := s.joined[gameID]
		return !claimed && !joined
	})
	waitUntil(t, 2*time.Second, "the flag ticker to exit", func() bool { return w.gone(gameID) })
}

// Every ending path reclaims the game and stops its ticker.
func TestEveryEndingReclaimsTheGame(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, h *harness, white, black *fakeConn, gameID string)
	}{
		{"mate", func(t *testing.T, h *harness, white, black *fakeConn, gameID string) {
			_, _ = playAllButLast(t, white, black, gameID, foolsMate)
			move(t, black, gameID, 4, "d8h4")
		}},
		{"resign", func(t *testing.T, h *harness, white, black *fakeConn, gameID string) {
			white.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
		}},
		{"abort", func(t *testing.T, h *harness, white, black *fakeConn, gameID string) {
			white.feedClient(t, codec.ClientMsg{Type: codec.CMAbort, GameID: gameID})
		}},
		{"draw", func(t *testing.T, h *harness, white, black *fakeConn, gameID string) {
			playTwoPlies(t, white, black, gameID)
			white.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
			black.waitForServer(t, codec.SMDrawOffered, 2*time.Second)
			black.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			defer h.cancel()
			h.srv.flagInterval = 10 * time.Millisecond
			w := watchTickers(h)
			ctx := context.Background()
			ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
			white, black := whiteBlack(ca, cb, seatA, seatB)
			tc.end(t, h, white, black, gameID)
			white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
			black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
			wantReclaimed(t, h, w, gameID)
		})
	}
	t.Run("flag", func(t *testing.T) {
		h := newHarness(t)
		defer h.cancel()
		h.srv.flagInterval = 20 * time.Millisecond
		h.useRealClock()
		w := watchTickers(h)
		ctx := context.Background()
		ca, cb := newFakeConn(), newFakeConn()
		h.handle(ctx, ca)
		h.handle(ctx, cb)
		seekTC(t, ca, "alice", "tiny")
		time.Sleep(20 * time.Millisecond)
		seekTC(t, cb, "bob", "tiny")
		m := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
		cb.waitForServer(t, codec.SMMatched, 2*time.Second)
		ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
		cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
		wantReclaimed(t, h, w, m.Matched.GameID)
	})
}

// The game ended but its GAME_END broadcast never went out (the failure #88
// describes: a broadcast that gave up after the session committed the end).
// The flag ticker sees the session is over and broadcasts it: both seats get
// GAME_END, and the game is reclaimed.
func TestTickerRepairsAnEndingThatWasNotBroadcast(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 10 * time.Millisecond
	w := watchTickers(h)
	ctx := context.Background()
	ca, cb, gameID, seatA, _ := pair(t, h, ctx, "alice", "bob")
	sess, ok := h.srv.reg.Get(gameID)
	if !ok {
		t.Fatal("no live session")
	}
	// End the game on the session directly: no transport path broadcasts it.
	if err := sess.Resign(ctx, seatA); err != nil {
		t.Fatalf("resign: %v", err)
	}
	if ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second); ge.GameEnd.Termination != 0x02 {
		t.Fatalf("termination = 0x%02x, want RESIGN", ge.GameEnd.Termination)
	}
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	wantReclaimed(t, h, w, gameID)
	ca.noServerMsg(t, codec.SMGameEnd, 50*time.Millisecond) // once
}

// A broadcast that does not go out releases its claim, so a later one can.
func TestBroadcastThatDoesNotGoOutReleasesTheClaim(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	claimed := func(id string) bool {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		_, ok := h.srv.endBroadcast[id]
		return ok
	}
	// A live game (not ended), and a game this node does not have.
	for _, id := range []string{gameID, "f47ac10b-58cc-4372-a567-0e02b2c3d479"} {
		if got := h.srv.broadcastGameEnd(id); got != endNotSent {
			t.Fatalf("broadcast %s = %v, want endNotSent", id, got)
		}
		if claimed(id) {
			t.Fatalf("the claim on %s stayed latched after a broadcast that did not go out", id)
		}
	}
	// While another path holds it, a caller is told so and changes nothing.
	h.srv.claimGameEnd(gameID)
	if got := h.srv.broadcastGameEnd(gameID); got != endHeld {
		t.Fatalf("broadcast with the claim held = %v, want endHeld", got)
	}
	if !claimed(gameID) {
		t.Fatal("a caller that found the claim held released it")
	}
}

// The GAME_END result comes from the session, not a store read: with every
// store read failing, both seats still get the full result, and a seat that is
// offline is still recorded as having missed it (review finding).
func TestGameEndCarriesTheResultWhenStoreReadsFail(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	disconnect(cb)
	h.srv.reg.store = failingLoadStore{h.store}
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if len(ge.GameEnd.ResultVector) != 2 || ge.GameEnd.ResultVector[seatA] != uint8(game.ResultLoss) || ge.GameEnd.ResultVector[seatB] != uint8(game.ResultWin) {
		t.Fatalf("result = %v, want the resigner losing", ge.GameEnd.ResultVector)
	}
	if got := h.srv.takeMissedEnds("bob"); got[gameID] != seatB {
		t.Fatalf("bob's missed endings = %v, want %s at seat %d", got, gameID, seatB)
	}
}

// If the claim stays held (a holder that never finishes), the ticker does not
// spin forever: after its retry bound it drops the ended game, so the session
// and the goroutine are reclaimed (the result stays in the stream).
func TestTickerDropsAnEndedGameItCannotBroadcast(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 5 * time.Millisecond
	h.srv.endReadTimeout = 20 * time.Millisecond // keeps the drop bound at 10 ticks
	w := watchTickers(h)
	ctx := context.Background()
	_, _, gameID, seatA, _ := pair(t, h, ctx, "alice", "bob")
	h.srv.claimGameEnd(gameID) // held by a holder that never finishes
	sess, _ := h.srv.reg.Get(gameID)
	if err := sess.Resign(ctx, seatA); err != nil {
		t.Fatalf("resign: %v", err)
	}
	wantReclaimed(t, h, w, gameID)
}
