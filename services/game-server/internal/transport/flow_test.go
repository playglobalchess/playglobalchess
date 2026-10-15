package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// pair sets up a matched game between two fresh conns and returns them plus the
// game id and each conn's seat. It drives AUTHENTICATE+SEEK for both and waits
// for MATCHED on each.
func pair(t *testing.T, h *harness, ctx context.Context, userA, userB string) (ca, cb *fakeConn, gameID string, seatA, seatB uint8) {
	t.Helper()
	ca = newFakeConn()
	cb = newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	authAndSeek(t, ca, userA, "blitz")
	time.Sleep(20 * time.Millisecond) // let A enqueue first so B claims A
	authAndSeek(t, cb, userB, "blitz")
	ma := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	mb := cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	return ca, cb, ma.Matched.GameID, ma.Matched.SeatIdx, mb.Matched.SeatIdx
}

// connForSeat returns whichever of ca/cb holds seat 0 (white, to move first).
func whiteBlack(ca, cb *fakeConn, seatA, seatB uint8) (white, black *fakeConn) {
	if seatA == 0 {
		return ca, cb
	}
	return cb, ca
}

func move(t *testing.T, c *fakeConn, gameID string, ply uint32, uci string) {
	t.Helper()
	c.feedClient(t, codec.ClientMsg{Type: codec.CMMove, Move: &codec.MovePayload{
		GameID: gameID, PlyExpected: ply, UCI: uci,
	}})
}

// TestMoveAcceptedAndRejected drives the move hot path.
func TestMoveAcceptedAndRejected(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	// White plays e2e4 (ply 1) → MOVE_ACCEPTED.
	move(t, white, gameID, 1, "e2e4")
	acc := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if acc.MoveAccepted.Ply != 1 {
		t.Fatalf("accepted ply = %d, want 1", acc.MoveAccepted.Ply)
	}
	// Black plays an ILLEGAL move → MOVE_REJECTED{ILLEGAL}.
	move(t, black, gameID, 2, "e2e4") // not black's pawn / occupied
	rej := black.waitForServer(t, codec.SMMoveRejected, 2*time.Second)
	if rej.MoveRejected.Reason != codec.ReasonIllegal {
		t.Fatalf("reject reason = 0x%02x, want ILLEGAL(0x01)", rej.MoveRejected.Reason)
	}
	// White (not black's turn) tries to move again → NOT_YOUR_TURN or STALE_PLY.
	// Black then plays a legal reply to keep going.
	move(t, black, gameID, 2, "e7e5")
	black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
}

// TestMoveNotSeated: a MOVE from a conn not seated in the game → NOT_YOUR_SEAT.
func TestMoveNotSeated(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")

	// A third authenticated conn that is not in the game.
	cx := newFakeConn()
	h.handle(ctx, cx)
	cx.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "mallory"}})
	time.Sleep(20 * time.Millisecond)
	move(t, cx, gameID, 1, "e2e4")
	rej := cx.waitForServer(t, codec.SMMoveRejected, 2*time.Second)
	if rej.MoveRejected.Reason != codec.ReasonNotYourSeat {
		t.Fatalf("reason = 0x%02x, want NOT_YOUR_SEAT(0x05)", rej.MoveRejected.Reason)
	}
}

// TestResignEndsGameBothSeats: a resign ends the game and both seats get GAME_END.
func TestResignEndsGameBothSeats(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, _ := pair(t, h, ctx, "alice", "bob")

	// seatA resigns.
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if ge.GameEnd.Termination != 0x02 || geB.GameEnd.Termination != 0x02 { // RESIGN
		t.Fatalf("termination = 0x%02x/0x%02x, want RESIGN(0x02)", ge.GameEnd.Termination, geB.GameEnd.Termination)
	}
	// Resigner (seatA) loses.
	if ge.GameEnd.ResultVector[seatA] != 0 { // 0 = loss
		t.Fatalf("resigner result = %d, want loss(0)", ge.GameEnd.ResultVector[seatA])
	}
}

// TestAbortBeforeMove: an abort before any move ends the game ABORT (all draw).
func TestAbortBeforeMove(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMAbort, GameID: gameID})
	ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if ge.GameEnd.Termination != 0x04 { // ABORT
		t.Fatalf("termination = 0x%02x, want ABORT(0x04)", ge.GameEnd.Termination)
	}
}

// TestDrawOfferAndAccept: an OFFER_DRAW then the other seat's OFFER_DRAW (the
// §4A has no accept msg — a second offer by the other seat is the agreement)
// ends the game DRAW_AGREE.
func TestDrawOfferAndAccept(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, _, _ := pair(t, h, ctx, "alice", "bob")

	ca.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	time.Sleep(20 * time.Millisecond) // let the offer land
	cb.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if ge.GameEnd.Termination != 0x05 { // DRAW_AGREE
		t.Fatalf("termination = 0x%02x, want DRAW_AGREE(0x05)", ge.GameEnd.Termination)
	}
	for i, r := range ge.GameEnd.ResultVector {
		if r != 1 { // draw
			t.Fatalf("draw result[%d] = %d, want draw(1)", i, r)
		}
	}
}

// TestReconnectPushesState: a dropped seat reconnects (new conn, same user,
// re-AUTHENTICATE) and the server pushes STATE for the live game (Option A).
func TestReconnectPushesState(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, _ := whiteBlack(ca, cb, seatA, seatB)
	// Play one move so the resync STATE has something non-trivial.
	move(t, white, gameID, 1, "e2e4")
	white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)

	// Alice's connection drops; she reconnects on a NEW conn, re-authenticates.
	ca.Close()
	time.Sleep(20 * time.Millisecond)
	ca2 := newFakeConn()
	h.handle(ctx, ca2)
	ca2.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
	st := ca2.waitForServer(t, codec.SMState, 2*time.Second)
	if st.State.GameID != gameID {
		t.Fatalf("resync STATE game = %s, want %s", st.State.GameID, gameID)
	}
	if st.State.State.FEN == "" {
		t.Fatal("resync STATE has empty FEN")
	}
}

// TestHeartbeat: PING → PONG echoing t.
func TestHeartbeat(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	c := newFakeConn()
	h.handle(ctx, c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
	c.feedClient(t, codec.ClientMsg{Type: codec.CMPing, Ping: &codec.PingPayload{T: 12345}})
	pong := c.waitForServer(t, codec.SMPong, 2*time.Second)
	if pong.Pong.T != 12345 {
		t.Fatalf("pong echoed t = %d, want 12345", pong.Pong.T)
	}
}

// TestJoinTimeoutAborts: a MATCHED game whose seats never bind a connection is
// aborted after the join window (GS-5 ghost-claim close). We construct exactly
// that state: create the opening stream + a live session via the registry (as a
// pairing would), bind NO seat connections, arm the join timeout with a tiny
// window, and assert the session is aborted (ended=ABORT) and dropped.
func TestJoinTimeoutAborts(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.joinTTL = 40 * time.Millisecond
	ctx := context.Background()

	// Create an opening stream directly (what matchmaking.createGame produces):
	// GameCreated + 2 PlayerJoined, so NewSession folds it LIVE.
	gameID := "ghost-game-1"
	sid := game.StreamIDFor(gameID)
	opening := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz", Rated: false,
			Seats:       []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN:    "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
			TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	}
	if err := h.store.AppendBatch(ctx, sid, 0, opening); err != nil {
		t.Fatalf("seed opening: %v", err)
	}
	if _, err := h.srv.reg.GetOrCreate(gameID, "blitz-5+3"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	// Arm the join timeout with NO seats bound → it must abort the ghost game.
	// deliverMatched registers the join signal before arming; mirror that here.
	h.srv.registerJoin(gameID)
	h.srv.armJoinTimeout(gameID)
	// Wait past the window, then assert the session is gone (aborted + dropped).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.srv.reg.Get(gameID); !ok {
			// Dropped — confirm the stream recorded an ABORT.
			evs, _ := h.store.Load(ctx, sid)
			for _, e := range evs {
				if e.Type == game.EvGameEnded && e.GameEnded.Termination == 0x04 { // ABORT
					return // success
				}
			}
			t.Fatalf("game dropped but stream has no ABORT GameEnded: %+v", evs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ghost game was not aborted/dropped within the window")
}

// TestJoinedGameNotAborted: the negative — a game whose seats ARE bound must NOT
// be aborted by the join timer (normal play/forfeit rules apply instead).
func TestJoinedGameNotAborted(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.joinTTL = 40 * time.Millisecond
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, _ := whiteBlack(ca, cb, seatA, seatB)
	time.Sleep(120 * time.Millisecond) // past the join window
	// Game must still be live (seats were bound) — a move still works.
	move(t, white, gameID, 1, "e2e4")
	white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
}

// TestJoinedThenBothDisconnectNotAborted is the C1 regression: a game that was
// JOINED (both seats bound) and then has BOTH connections drop within the join
// window must NOT be aborted/dropped — the join signal already fired, so a
// transient double-disconnect falls under the clock/forfeit+reconnect path
// (§3.5), not the ghost-claim abort. Before the fix, the timer re-checked
// current connectivity and wrongly killed the live game.
func TestJoinedThenBothDisconnectNotAborted(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.joinTTL = 60 * time.Millisecond
	ctx := context.Background()
	ca, cb, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	// Both players drop immediately (within the join window).
	ca.Close()
	cb.Close()
	time.Sleep(150 * time.Millisecond) // well past the window
	// The game must still be live and reconstructable — it was JOINED, so the
	// ghost-abort must not have fired.
	if _, ok := h.srv.reg.Get(gameID); !ok {
		t.Fatal("live game was dropped after a transient double-disconnect (C1 regression)")
	}
	// And the durable stream has NO Aborted event.
	evs, _ := h.store.Load(ctx, game.StreamIDFor(gameID))
	for _, e := range evs {
		if e.Type == game.EvAborted {
			t.Fatalf("live game was wrongly ABORTED on a transient double-disconnect (C1)")
		}
	}
	// A reconnect still resyncs it.
	ca2 := newFakeConn()
	h.handle(ctx, ca2)
	ca2.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
	st := ca2.waitForServer(t, codec.SMState, 2*time.Second)
	if st.State.GameID != gameID {
		t.Fatalf("reconnect resync game = %s, want %s", st.State.GameID, gameID)
	}
}

// TestTakebackPushesStateToBoth is the S2 regression: a successful TAKEBACK does
// not end the game and §4A has no takeback frame, so the transport must push
// STATE to BOTH seats (else each client's board silently diverges). The default
// pool is unrated (authAndSeek sets Rated:false), so Takeback is allowed.
func TestTakebackPushesStateToBoth(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	// Play two plies so there is a move to take back.
	move(t, white, gameID, 1, "e2e4")
	white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	move(t, black, gameID, 2, "e7e5")
	black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	// Black requests a takeback (its move e5 is undone).
	black.feedClient(t, codec.ClientMsg{Type: codec.CMTakeback, GameID: gameID})
	// BOTH seats must receive a STATE reflecting the rolled-back position.
	stW := white.waitForServer(t, codec.SMState, 2*time.Second)
	stB := black.waitForServer(t, codec.SMState, 2*time.Second)
	if stW.State.GameID != gameID || stB.State.GameID != gameID {
		t.Fatalf("takeback STATE game mismatch: %s / %s", stW.State.GameID, stB.State.GameID)
	}
}
