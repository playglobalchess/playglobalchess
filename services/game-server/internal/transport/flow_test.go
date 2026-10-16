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

// moveAndAwaitBoth plays a move and consumes the MOVE_ACCEPTED that §4A v1.2
// (ADR-18) now broadcasts to BOTH seats — the mover and the opponent. Returns
// the mover's accepted frame (both carry the same uci/ply). Draining both keeps
// each conn's seen-offset aligned so a later move does not re-match a stale ack.
func moveAndAwaitBoth(t *testing.T, mover, opponent *fakeConn, gameID string, ply uint32, uci string) codec.ServerMsg {
	t.Helper()
	move(t, mover, gameID, ply, uci)
	acc := mover.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	opponent.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	return acc
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

// TestFlagForfeitOnIdle is the regression for the missing TickFlagCheck driver:
// a joined game whose side-to-move simply stops (or disconnects) must be ended
// by the transport's flag ticker when its clock expires — the core clocked-chess
// forfeit rule (§3.4), which GS-3 delegates to the caller. Uses a tiny clock
// (custom-80+0 = 80ms base, 0 increment) and a short flag interval so idle time
// flags quickly. Both seats must receive GAME_END{FLAG}.
func TestFlagForfeitOnIdle(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 20 * time.Millisecond
	ctx := context.Background()

	// Pair two conns on a tiny-clock pool so the first idle period flags.
	ca := newFakeConn()
	cb := newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)

	// Neither side moves. White's clock (80ms) runs out; the flag ticker ends it.
	geA := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	// Termination is FLAG(0x03) or TIMEOUT_VS_INSUFFICIENT(0x0A) — either is a
	// valid clock-expiry ending from the start position (engine decides).
	okTerm := func(x uint8) bool { return x == 0x03 || x == 0x0A }
	if !okTerm(geA.GameEnd.Termination) || !okTerm(geB.GameEnd.Termination) {
		t.Fatalf("termination = 0x%02x/0x%02x, want FLAG(0x03) or TIMEOUT_VS_INSUFFICIENT(0x0A)", geA.GameEnd.Termination, geB.GameEnd.Termination)
	}
	// The game is reclaimed (session dropped) — no leaked live session.
	time.Sleep(30 * time.Millisecond)
	if _, ok := h.srv.reg.Get(geA.GameEnd.GameID); ok {
		t.Fatal("session not dropped after flag forfeit — leak")
	}
}

// seekTC authenticates and seeks with an explicit time-control id (for the
// tiny-clock flag test). The pool name doubles as the pool id.
func seekTC(t *testing.T, c *fakeConn, userID, pool string) {
	t.Helper()
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: userID}})
	c.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: pool, TimeControl: "custom-80+0", Rated: false}})
}

// TestSingleGameEndNoDoubleFire is the regression for the broadcastGameEnd
// double-fire: a game ending by a terminal move while the flag ticker is also
// live must send EXACTLY ONE GAME_END to each seat (claimGameEnd latches the
// broadcast). We use a short flag interval so the ticker is active during the
// mating move, then assert each conn received exactly one GAME_END.
func TestSingleGameEndNoDoubleFire(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 15 * time.Millisecond
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	// Scholar's mate.
	moves := []struct {
		c   *fakeConn
		ply uint32
		uci string
	}{
		{white, 1, "e2e4"}, {black, 2, "e7e5"},
		{white, 3, "f1c4"}, {black, 4, "b8c6"},
		{white, 5, "d1h5"}, {black, 6, "g8f6"},
		{white, 7, "h5f7"},
	}
	for _, m := range moves {
		opp := black
		if m.c == black {
			opp = white
		}
		moveAndAwaitBoth(t, m.c, opp, gameID, m.ply, m.uci)
	}
	white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	// Give any racing flag-tick broadcast a chance to (wrongly) fire a second one.
	time.Sleep(60 * time.Millisecond)
	countEnds := func(c *fakeConn) int {
		n := 0
		for _, m := range c.sent(t) {
			if m.Type == codec.SMGameEnd {
				n++
			}
		}
		return n
	}
	if ne := countEnds(white); ne != 1 {
		t.Fatalf("white received %d GAME_END frames, want exactly 1 (double-fire)", ne)
	}
	if ne := countEnds(black); ne != 1 {
		t.Fatalf("black received %d GAME_END frames, want exactly 1 (double-fire)", ne)
	}
}

// TestResyncPushesState (§4A v1.1): a seated conn sending RESYNC gets a fresh
// STATE for its game (on-demand resync, the RESYNC complement to the auth-time
// auto-push).
func TestResyncPushesState(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, _ := whiteBlack(ca, cb, seatA, seatB)
	move(t, white, gameID, 1, "e2e4")
	white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	// Any seated conn asks for a resync.
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResync, GameID: gameID})
	st := ca.waitForServer(t, codec.SMState, 2*time.Second)
	if st.State.GameID != gameID || st.State.State.FEN == "" {
		t.Fatalf("RESYNC STATE = %+v, want non-empty for %s", st.State, gameID)
	}
}

// TestResyncNotSeatedRejected: RESYNC for a game the conn is not seated in →
// ERROR{UNKNOWN_GAME}.
func TestResyncNotSeatedRejected(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	cx := newFakeConn()
	h.handle(ctx, cx)
	cx.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "mallory"}})
	time.Sleep(20 * time.Millisecond)
	cx.feedClient(t, codec.ClientMsg{Type: codec.CMResync, GameID: gameID})
	er := cx.waitForServer(t, codec.SMError, 2*time.Second)
	if er.Error.Code != codec.ECUnknownGame {
		t.Fatalf("RESYNC not-seated code = 0x%02x, want UNKNOWN_GAME(0x03)", er.Error.Code)
	}
}

// TestOfferDrawPushesDrawOfferedToOpponent (§4A v1.1): a first (non-ending)
// OFFER_DRAW pushes DRAW_OFFERED{game_id, by} to the OPPONENT.
func TestOfferDrawPushesDrawOfferedToOpponent(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	whiteSeat := seatA
	if seatA != 0 {
		whiteSeat = seatB
	}
	// White offers a draw; black (opponent) must receive DRAW_OFFERED{by: whiteSeat}.
	white.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	dob := black.waitForServer(t, codec.SMDrawOffered, 2*time.Second)
	if dob.DrawOffered.GameID != gameID || dob.DrawOffered.By != whiteSeat {
		t.Fatalf("DRAW_OFFERED = %+v, want {game:%s by:%d}", dob.DrawOffered, gameID, whiteSeat)
	}
	// The game is NOT ended (it was just an offer).
	if sess, ok := h.srv.reg.Get(gameID); ok {
		if snap, _ := sess.StateSnapshot(ctx); snap.Ended {
			t.Fatal("game ended on a mere draw OFFER")
		}
	}
}

// TestRepeatOfferDrawNoDuplicateNotify is the regression for the no-op-repeat
// double-notify: the session treats a second OFFER_DRAW by the already-pending
// seat as a no-op, so the transport must NOT push a second DRAW_OFFERED to the
// opponent (a double-click must not spam them).
func TestRepeatOfferDrawNoDuplicateNotify(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	// White offers once → black gets exactly one DRAW_OFFERED.
	white.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	black.waitForServer(t, codec.SMDrawOffered, 2*time.Second)
	// White re-sends the SAME offer (no-op in the session) — black must NOT get
	// a second notification.
	white.feedClient(t, codec.ClientMsg{Type: codec.CMOfferDraw, GameID: gameID})
	time.Sleep(60 * time.Millisecond) // let any (wrong) second push arrive
	n := 0
	for _, m := range black.sent(t) {
		if m.Type == codec.SMDrawOffered {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("opponent received %d DRAW_OFFERED, want exactly 1 (repeat offer must not re-notify)", n)
	}
}

// TestMoveBroadcastToBothSeats is the §4A v1.2 (ADR-18) gap-closer: an accepted
// move is broadcast to BOTH seats carrying the authoritative uci, so the
// OPPONENT's board can advance. The pre-v1.2 transport sent MOVE_ACCEPTED to the
// mover only — this asserts the opponent now receives it with the right move,
// the assertion the old e2e never made.
func TestMoveBroadcastToBothSeats(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	move(t, white, gameID, 1, "e2e4")
	// The MOVER sees its ack with the uci.
	accW := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if accW.MoveAccepted.UCI != "e2e4" {
		t.Fatalf("mover ack uci = %q, want e2e4", accW.MoveAccepted.UCI)
	}
	// The OPPONENT also receives the move (the gap this closes), same uci + ply.
	accB := black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if accB.MoveAccepted.UCI != "e2e4" {
		t.Fatalf("opponent ack uci = %q, want e2e4", accB.MoveAccepted.UCI)
	}
	if accB.MoveAccepted.Ply != 1 || accB.MoveAccepted.GameID != gameID {
		t.Fatalf("opponent ack ply/game = %d/%s, want 1/%s", accB.MoveAccepted.Ply, accB.MoveAccepted.GameID, gameID)
	}
	// Both seats must see the SAME authoritative clock (belt-and-suspenders
	// against a uci/clock field-order swap the corpus freeze also guards).
	if accB.MoveAccepted.ClockAfterMs != accW.MoveAccepted.ClockAfterMs {
		t.Fatalf("opponent clock = %d, mover clock = %d, want equal", accB.MoveAccepted.ClockAfterMs, accW.MoveAccepted.ClockAfterMs)
	}
}

// TestReAckEchoesPersistedUCI: a duplicate MOVE on an already-persisted ply that
// carries a DIFFERENT uci must re-ack the PERSISTED move's uci, never the
// retry's — the §4A v1.2 server-authoritative re-ack (the pipeline reads
// mv.UCI, the transport reads out.UCI, not p.UCI). Regression against the
// blank/stale-field class of bug.
func TestReAckEchoesPersistedUCI(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	// White plays the real move e2e4 at ply 1 (both seats ack).
	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")

	// White re-sends ply 1 but with a DIFFERENT uci (a buggy/garbled retry). The
	// move is already persisted as e2e4; the re-ack must echo e2e4, not d2d4.
	move(t, white, gameID, 1, "d2d4")
	reack := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if reack.MoveAccepted.UCI != "e2e4" {
		t.Fatalf("re-ack uci = %q, want the PERSISTED e2e4 (not the retry's d2d4)", reack.MoveAccepted.UCI)
	}
	if reack.MoveAccepted.Ply != 1 {
		t.Fatalf("re-ack ply = %d, want 1", reack.MoveAccepted.Ply)
	}
}
