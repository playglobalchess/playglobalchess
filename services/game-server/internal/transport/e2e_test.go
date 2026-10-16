package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
)

// TestE2E_FullGameToMate is the F-01 proof: two in-process fake clients SEEK, get
// MATCHED, and play a real Scholar's-mate game to a GAME_END{MATE} over the ACTUAL
// transport + session + store + matchmaking — no browser, no socket, but every
// server component in the real path. This is the "a move travels client→server→
// client and the whole loop works" demonstration.
func TestE2E_FullGameToMate(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	// Scholar's mate: 1.e4 e5 2.Bc4 Nc6 3.Qh5 Nf6?? 4.Qxf7#
	moves := []struct {
		c   *fakeConn
		ply uint32
		uci string
	}{
		{white, 1, "e2e4"}, {black, 2, "e7e5"},
		{white, 3, "f1c4"}, {black, 4, "b8c6"},
		{white, 5, "d1h5"}, {black, 6, "g8f6"},
		{white, 7, "h5f7"}, // Qxf7#
	}
	for _, m := range moves {
		opp := black
		if m.c == black {
			opp = white
		}
		moveAndAwaitBoth(t, m.c, opp, gameID, m.ply, m.uci)
	}
	// Both clients receive GAME_END{MATE}; white (the mover) wins.
	geW := white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if geW.GameEnd.Termination != 0x01 || geB.GameEnd.Termination != 0x01 { // MATE
		t.Fatalf("termination = 0x%02x/0x%02x, want MATE(0x01)", geW.GameEnd.Termination, geB.GameEnd.Termination)
	}
	whiteSeat := seatA
	if seatA != 0 {
		whiteSeat = seatB
	}
	if geW.GameEnd.ResultVector[whiteSeat] != 2 { // 2 = win
		t.Fatalf("white (mover) result = %d, want win(2)", geW.GameEnd.ResultVector[whiteSeat])
	}
}

// TestE2E_GameEndsByResign: a few moves then a resign — both clients see
// GAME_END{RESIGN} over the real transport.
func TestE2E_GameEndsByResign(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")
	moveAndAwaitBoth(t, black, white, gameID, 2, "e7e5")
	// White resigns.
	white.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	geW := white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if geW.GameEnd.Termination != 0x02 || geB.GameEnd.Termination != 0x02 { // RESIGN
		t.Fatalf("termination = 0x%02x/0x%02x, want RESIGN(0x02)", geW.GameEnd.Termination, geB.GameEnd.Termination)
	}
}

// TestE2E_GameEndsByDraw: moves then a mutual draw offer — both clients see
// GAME_END{DRAW_AGREE} over the real transport.
func TestE2E_GameEndsByDraw(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")
	moveAndAwaitBoth(t, black, white, gameID, 2, "e7e5")
	// White offers a draw; black agrees (second offer = agreement).
	white.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	time.Sleep(20 * time.Millisecond)
	black.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	geW := white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if geW.GameEnd.Termination != 0x05 || geB.GameEnd.Termination != 0x05 { // DRAW_AGREE
		t.Fatalf("termination = 0x%02x/0x%02x, want DRAW_AGREE(0x05)", geW.GameEnd.Termination, geB.GameEnd.Termination)
	}
}
