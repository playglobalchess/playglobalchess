package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// authOn opens a connection to srv and authenticates it as uid.
func authOn(t *testing.T, srv *Server, uid string) *fakeConn {
	t.Helper()
	c := newFakeConn()
	go srv.Handle(context.Background(), c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: uid}})
	return c
}

// #98: after a restart (no adoption run), a reconnecting player gets STATE for
// the live game found through the index, is seated again, and can move.
func TestReconnectAfterRestartFindsTheGameThroughTheIndex(t *testing.T) {
	h, _ := indexedHarness(t)
	ca, cb, gameID, seatA, seatB := pair(t, h, context.Background(), "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	playTwoPlies(t, white, black, gameID)
	srv := restartedNode(t, h, time.Second)

	whiteUID := "alice"
	if seatA != 0 {
		whiteUID = "bob"
	}
	c := authOn(t, srv, whiteUID)
	m := c.waitForServer(t, codec.SMState, 2*time.Second)
	if m.State.GameID != gameID || m.State.State.YouSeat != 0 || m.State.State.Status == codec.StatusEnded {
		t.Fatalf("STATE after the restart: %+v", m.State)
	}
	move(t, c, gameID, 3, "g1f3")
	if acc := c.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.Ply != 3 {
		t.Fatalf("move after the restart: %+v", acc.MoveAccepted)
	}
	if !running(srv, gameID) {
		t.Fatal("the game started by AUTHENTICATE has no flag ticker")
	}
}

// Closed tab, lost on time: the ending is pushed once from user_recent_games,
// then the row is consumed and a second login gets nothing.
func TestEndingMissedOfflineIsPushedOnceFromTheIndex(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, _, seatB := pair(t, h, ctx, "alice", "bob")
	disconnect(cb)
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	waitUntil(t, 2*time.Second, "alice's recent row to be consumed", func() bool {
		r, _ := st.UserRecentGames(ctx, "alice", 5)
		return len(r) == 0
	})
	c := reconnect(t, h, ctx, "bob")
	m := c.waitForServer(t, codec.SMState, 2*time.Second)
	if m.State.GameID != gameID || m.State.State.Status != codec.StatusEnded || m.State.State.YouSeat != seatB {
		t.Fatalf("bob's pushed ending: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "bob's recent row to be consumed", func() bool {
		r, _ := st.UserRecentGames(ctx, "bob", 5)
		return len(r) == 0
	})
	reconnect(t, h, ctx, "bob").noServerMsg(t, codec.SMState, 150*time.Millisecond)
	// alice saw GAME_END live, so she is not sent the ending again.
	reconnect(t, h, ctx, "alice").noServerMsg(t, codec.SMState, 150*time.Millisecond)
}

// A stale live row of an ended game (an ending written without its rows) is
// repaired at login, and the ending is pushed once.
func TestStaleLiveRowAtLoginIsRepairedAndPushed(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	h.srv.dropGame(gameID) // as after a restart: nothing live here
	sid := game.StreamIDFor(gameID)
	evs, err := st.Load(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	end := game.Event{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: []game.Result{game.ResultDraw, game.ResultDraw}, Termination: game.TermAbort}}
	if err := st.AppendBatch(ctx, sid, uint32(len(evs)), []game.Event{end}); err != nil {
		t.Fatal(err)
	}
	c := reconnect(t, h, ctx, "bob")
	if m := c.waitForServer(t, codec.SMState, 2*time.Second); m.State.State.Status != codec.StatusEnded {
		t.Fatalf("stale row at login: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "bob's rows to be repaired and consumed", func() bool {
		l, _ := st.UserLiveGames(ctx, "bob", 5)
		r, _ := st.UserRecentGames(ctx, "bob", 5)
		return len(l) == 0 && len(r) == 0
	})
}

// lookupFails is an index store whose user-live lookup fails.
type lookupFails struct{ *game.MemStore }

func (lookupFails) UserLiveGames(context.Context, string, int) ([]game.UserEntry, error) {
	return nil, errors.New("fault: index down")
}

// A failed lookup answers a retryable INTERNAL and closes the connection, so the
// user cannot SEEK into a second game.
func TestFailedIndexLookupRefusesTheConnection(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	h.srv.reg.store = lookupFails{h.store.(*game.MemStore)}
	c := reconnect(t, h, context.Background(), "carol")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("error after a failed lookup: %+v", m.Error)
	}
	waitUntil(t, 2*time.Second, "the connection to close", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.closed
	})
}

// A row is a hint: a user_live_games row naming a game the stream does not seat
// the user in gives no STATE and no seat.
func TestForgedLiveRowGivesNoSeat(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	forged := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "mallory", GameID: gameID, Seat: 0}
	if err := st.RepairIndex(ctx, []game.IndexOp{forged}); err != nil {
		t.Fatal(err)
	}
	c := reconnect(t, h, ctx, "mallory")
	c.noServerMsg(t, codec.SMState, 150*time.Millisecond)
	if _, ok := h.srv.seatOfUser(gameID, "mallory"); ok {
		t.Fatal("mallory was seated from a forged row")
	}
	move(t, c, gameID, 1, "e2e4")
	if m := c.waitForServer(t, codec.SMMoveRejected, 2*time.Second); m.MoveRejected.Reason != codec.ReasonNotYourSeat {
		t.Fatalf("mallory's move: %+v", m.MoveRejected)
	}
}

// B1 (review): one player's stale live row never re-creates a recent row the
// other player consumed when they saw GAME_END live.
func TestRepairNeverRecreatesAConsumedRecentRow(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, _ := pair(t, h, ctx, "alice", "bob")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	waitUntil(t, 2*time.Second, "both recent rows to be consumed", func() bool {
		a, _ := st.UserRecentGames(ctx, "alice", 5)
		b, _ := st.UserRecentGames(ctx, "bob", 5)
		return len(a) == 0 && len(b) == 0
	})
	stale := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "alice", GameID: gameID, Seat: seatA}
	if err := st.RepairIndex(ctx, []game.IndexOp{stale}); err != nil {
		t.Fatal(err)
	}
	c := reconnect(t, h, ctx, "alice")
	c.waitForServer(t, codec.SMState, 2*time.Second) // her own stale row: pushed once
	time.Sleep(100 * time.Millisecond)
	if b, _ := st.UserRecentGames(ctx, "bob", 5); len(b) != 0 {
		t.Fatalf("alice's login re-created bob's consumed recent row: %v", b)
	}
	reconnect(t, h, ctx, "bob").noServerMsg(t, codec.SMState, 150*time.Millisecond)
}

// B2 (review): a live game that cannot be started at login (a store error)
// refuses the login, so the user cannot SEEK into a second game.
func TestLiveGameStartFailureRefusesTheLogin(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	h.srv.dropGame(gameID) // as after a restart: the login must build it
	h.srv.reg.store = &flakyLoad{EventStore: h.store, fails: 1}
	c := reconnect(t, h, ctx, "alice")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("error after a failed start: %+v", m.Error)
	}
	waitUntil(t, 2*time.Second, "the connection to close", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.closed
	})
	if m := reconnect(t, h, ctx, "alice").waitForServer(t, codec.SMState, 2*time.Second); m.State.GameID != gameID {
		t.Fatalf("login after recovery: %+v", m.State)
	}
}

// S1 (review): one user in both seats consumes one row, not a refused write.
func TestConsumeRecentDedupesUsers(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "018f0000-0000-7000-8000-00000000cccc"
	put := game.IndexOp{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: "solo", GameID: gid, EndedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.RepairIndex(ctx, []game.IndexOp{put}); err != nil {
		t.Fatal(err)
	}
	h.srv.consumeRecent(ctx, gid, []string{"solo", "solo"})
	if r, _ := st.UserRecentGames(ctx, "solo", 5); len(r) != 0 {
		t.Fatalf("recent row left after consuming a repeated user: %v", r)
	}
}

type recentFails struct{ *game.MemStore }

func (recentFails) UserRecentGames(context.Context, string, int) ([]game.UserEntry, error) {
	return nil, errors.New("fault: index down")
}

func TestFailedRecentLookupRefusesTheConnection(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	h.srv.reg.store = recentFails{h.store.(*game.MemStore)}
	c := reconnect(t, h, context.Background(), "carol")
	if m := c.waitForServer(t, codec.SMError, 2*time.Second); m.Error.Code != codec.ECInternal {
		t.Fatalf("error after a failed recent lookup: %+v", m.Error)
	}
}
