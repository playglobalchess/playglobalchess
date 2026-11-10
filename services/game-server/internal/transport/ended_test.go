package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// #102: a player who misses GAME_END can still learn the result.

// noServerMsg asserts that no NEW frame of type `unwanted` arrives on c within
// `wait` (consuming what arrived).
func (f *fakeConn) noServerMsg(t *testing.T, unwanted codec.ServerMsgType, wait time.Duration) {
	t.Helper()
	time.Sleep(wait)
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.seen < len(f.out) {
		m, err := codec.DecodeServer(f.out[f.seen])
		f.seen++
		if err != nil {
			t.Fatalf("decode server frame: %v", err)
		}
		if m.Type == unwanted {
			t.Fatalf("unexpected server message type %d: %+v", m.Type, m)
		}
	}
}

// reconnect opens a new connection for userID and authenticates it.
func reconnect(t *testing.T, h *harness, ctx context.Context, userID string) *fakeConn {
	t.Helper()
	c := newFakeConn()
	h.handle(ctx, c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: userID}})
	return c
}

func resync(t *testing.T, c *fakeConn, gameID string) {
	t.Helper()
	c.feedClient(t, codec.ClientMsg{Type: codec.CMResync, GameID: gameID})
}

// wantEnded checks an ended STATE for gameID, seen by seat, with the given
// termination and result.
func wantEnded(t *testing.T, m codec.ServerMsg, gameID string, seat uint8, term game.Termination, rv []uint8) {
	t.Helper()
	if m.State == nil || m.State.GameID != gameID {
		t.Fatalf("STATE for %+v, want game %s", m.State, gameID)
	}
	b := m.State.State
	if b.Status != codec.StatusEnded {
		t.Fatalf("status = %d, want ENDED", b.Status)
	}
	if b.YouSeat != seat {
		t.Fatalf("you_seat = %d, want %d", b.YouSeat, seat)
	}
	if b.Termination != uint8(term) {
		t.Fatalf("termination = 0x%02x, want 0x%02x", b.Termination, uint8(term))
	}
	if len(b.ResultVector) != len(rv) {
		t.Fatalf("result = %v, want %v", b.ResultVector, rv)
	}
	for i := range rv {
		if b.ResultVector[i] != rv[i] {
			t.Fatalf("result = %v, want %v", b.ResultVector, rv)
		}
	}
	if b.RunningSeat != codec.NoSeat {
		t.Fatalf("running_seat = %d, want none for an ended game", b.RunningSeat)
	}
}

// disconnect closes c and lets its Handle teardown unbind the seat.
func disconnect(c *fakeConn) {
	c.Close()
	time.Sleep(30 * time.Millisecond)
}

// Resign while the opponent is offline: the opponent's reconnect gets the
// ended STATE exactly once, and RESYNC still returns it afterwards.
func TestMissedEnd_ResignWhileOffline(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	disconnect(cb)

	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	rv := make([]uint8, 2)
	rv[seatA], rv[seatB] = uint8(game.ResultLoss), uint8(game.ResultWin)

	cb2 := reconnect(t, h, ctx, "bob")
	wantEnded(t, cb2.waitForServer(t, codec.SMState, 2*time.Second), gameID, seatB, game.TermResign, rv)
	cb2.noServerMsg(t, codec.SMState, 50*time.Millisecond) // once, not per game frame

	// The push is not repeated on the next reconnect...
	disconnect(cb2)
	cb3 := reconnect(t, h, ctx, "bob")
	cb3.noServerMsg(t, codec.SMState, 100*time.Millisecond)
	// ...but RESYNC always answers.
	resync(t, cb3, gameID)
	wantEnded(t, cb3.waitForServer(t, codec.SMState, 2*time.Second), gameID, seatB, game.TermResign, rv)
}

// A seat that saw GAME_END live is not pushed the ending again on reconnect
// (it may be seeking a new game); RESYNC still answers.
func TestMissedEnd_NotPushedToSeatThatSawIt(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	disconnect(cb)
	cb2 := reconnect(t, h, ctx, "bob")
	cb2.noServerMsg(t, codec.SMState, 100*time.Millisecond)
	resync(t, cb2, gameID)
	rv := make([]uint8, 2)
	rv[seatA], rv[seatB] = uint8(game.ResultLoss), uint8(game.ResultWin)
	wantEnded(t, cb2.waitForServer(t, codec.SMState, 2*time.Second), gameID, seatB, game.TermResign, rv)
}

// Mate delivered while the mated player is offline (fool's mate).
func TestMissedEnd_MateWhileOffline(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	whiteUser := "alice"
	if seatA != 0 {
		whiteUser = "bob"
	}
	moveAndAwaitBoth(t, white, black, gameID, 1, "f2f3")
	moveAndAwaitBoth(t, black, white, gameID, 2, "e7e5")
	moveAndAwaitBoth(t, white, black, gameID, 3, "g2g4")
	disconnect(white)
	move(t, black, gameID, 4, "d8h4")
	black.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	w2 := reconnect(t, h, ctx, whiteUser)
	wantEnded(t, w2.waitForServer(t, codec.SMState, 2*time.Second), gameID, 0, game.TermMate,
		[]uint8{uint8(game.ResultLoss), uint8(game.ResultWin)})
}

// A draw agreed while the offering player is offline.
func TestMissedEnd_DrawWhileOffline(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	playTwoPlies(t, white, black, gameID)
	cb.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	ca.waitForServer(t, codec.SMDrawOffered, 2*time.Second)
	disconnect(cb)
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID}) // accepts
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	cb2 := reconnect(t, h, ctx, "bob")
	wantEnded(t, cb2.waitForServer(t, codec.SMState, 2*time.Second), gameID, seatB, game.TermDrawAgree,
		[]uint8{uint8(game.ResultDraw), uint8(game.ResultDraw)})
}

// The flag ticker ends the game while the side to move is offline (the common
// case: a player who vanishes loses on time and must still see that).
func TestMissedEnd_FlagWhileOffline(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 20 * time.Millisecond
	h.useRealClock()
	ctx := context.Background()
	ca, cb := newFakeConn(), newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	ma := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	gameID := ma.Matched.GameID
	white, whiteUser := ca, "alice"
	if ma.Matched.SeatIdx != 0 {
		white, whiteUser = cb, "bob"
	}
	disconnect(white)
	time.Sleep(300 * time.Millisecond) // White's 80 ms clock runs out; the ticker ends the game.
	if _, live := h.srv.reg.Get(gameID); live {
		t.Fatal("game still live after the flag")
	}

	w2 := reconnect(t, h, ctx, whiteUser)
	m := w2.waitForServer(t, codec.SMState, 2*time.Second)
	term := game.Termination(m.State.State.Termination)
	if term != game.TermFlag && term != game.TermTimeoutVsInsufficient {
		t.Fatalf("termination = 0x%02x, want FLAG or TIMEOUT_VS_INSUFFICIENT", uint8(term))
	}
	wantEnded(t, m, gameID, 0, term, m.State.State.ResultVector)
}

// RESYNC of an ended game by someone who was not seated in it, or of a game
// that does not exist, is UNKNOWN_GAME (no way to probe other players' games).
func TestResyncEnded_OnlyForSeatedPlayers(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	eve := reconnect(t, h, ctx, "eve")
	for _, gid := range []string{gameID, "f47ac10b-58cc-4372-a567-0e02b2c3d479"} {
		resync(t, eve, gid)
		e := eve.waitForServer(t, codec.SMError, 2*time.Second)
		if e.Error.Code != codec.ECUnknownGame {
			t.Fatalf("RESYNC %s: error code 0x%02x, want UNKNOWN_GAME", gid, e.Error.Code)
		}
	}
	eve.noServerMsg(t, codec.SMState, 50*time.Millisecond)
}

// The missed-ending record expires after missedEndTTL and is capped.
func TestMissedEnd_RecordIsBounded(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	s := h.srv

	s.recordMissedEnd("u1", "g1", 0)
	h.clk.add(missedEndTTL - time.Second)
	s.recordMissedEnd("u2", "g2", 1)
	h.clk.add(2 * time.Second) // g1 is past its TTL, g2 is not
	if got := s.takeMissedEnds("u1"); len(got) != 0 {
		t.Fatalf("u1 missed endings = %v, want none (expired)", got)
	}
	if got := s.takeMissedEnds("u2"); got["g2"] != 1 || len(got) != 1 {
		t.Fatalf("u2 missed endings = %v, want g2 at seat 1", got)
	}

	// Everything expires: map and FIFO are empty again (no leak).
	s.recordMissedEnd("u3", "g3", 0)
	h.clk.add(missedEndTTL)
	s.recordMissedEnd("u4", "g4", 0) // prunes u3
	h.clk.add(missedEndTTL)
	s.takeMissedEnds("nobody") // prunes u4
	s.mu.Lock()
	n, q := len(s.missedEnds), len(s.missedOrder)
	s.mu.Unlock()
	if n != 0 || q != 0 {
		t.Fatalf("after expiry: %d users, %d queued, want 0 and 0", n, q)
	}

	// The cap drops the oldest first.
	defer func(old int) { maxMissedEnds = old }(maxMissedEnds)
	maxMissedEnds = 2
	s.recordMissedEnd("a", "ga", 0)
	s.recordMissedEnd("b", "gb", 0)
	s.recordMissedEnd("c", "gc", 0)
	if got := s.takeMissedEnds("a"); len(got) != 0 {
		t.Fatalf("oldest entry kept past the cap: %v", got)
	}
	if got := s.takeMissedEnds("c"); len(got) != 1 {
		t.Fatalf("newest entry dropped: %v", got)
	}
}
