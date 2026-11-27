package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// endStreamPlainly appends a GameEnded with no index rows (persist's fallback)
// and drops the game, as after a restart.
func endStreamPlainly(t *testing.T, h *harness, st game.IndexedStore, gameID string) {
	t.Helper()
	ctx := context.Background()
	h.srv.dropGame(gameID)
	sid := game.StreamIDFor(gameID)
	evs, err := st.Load(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	end := game.Event{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: []game.Result{game.ResultDraw, game.ResultDraw}, Termination: game.TermAbort}}
	if err := st.AppendBatch(ctx, sid, uint32(len(evs)), []game.Event{end}); err != nil {
		t.Fatal(err)
	}
}

// Review round 3, item 1: a refused login does not lose a stale-row ending. The
// stale row of game 1 stays until its ending is actually sent.
func TestRefusedLoginKeepsTheStaleRowEnding(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	_, _, g1, _, _ := pair(t, h, ctx, "alice", "bob")
	endStreamPlainly(t, h, st, g1)
	// g2 is older than g1, so g1 (stale) is read first and g2's start then
	// fails: the login is refused after g1 was resolved but before its ending
	// was pushed.
	g2 := "01000000-0000-7000-8000-00000000eeee"
	seedGame(t, st, g2, "blitz-5+3", "alice", "carol")
	h.srv.reg.store = failStream{&flakyLoad{EventStore: h.store}, game.StreamIDFor(g2)}
	c := reconnect(t, h, ctx, "alice")
	c.waitForServer(t, codec.SMError, 2*time.Second)
	if rows, _ := st.UserLiveGames(ctx, "alice", 10); len(rows) != 2 {
		t.Fatalf("alice's live rows after a refused login: %v, want both kept", rows)
	}
	// The store is back: the next login pushes g1's ending and restores g2.
	h.srv.reg.store = h.store
	c2 := reconnect(t, h, ctx, "alice")
	got := map[string]uint8{}
	for i := 0; i < 2; i++ {
		m := c2.waitForServer(t, codec.SMState, 2*time.Second)
		got[m.State.GameID] = m.State.State.Status
	}
	if got[g1] != codec.StatusEnded || got[g2] == codec.StatusEnded {
		t.Fatalf("states after recovery: %v", got)
	}
}

// Item 2: a game that can never be played (its time control does not resolve)
// does not lock the user out: its row is dropped and the login goes through.
func TestUnplayableGameDoesNotLockTheUserOut(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	seedGame(t, st, "018f0000-0000-7000-8000-00000000dddd", "no-such-tc", "dave", "erin")
	c := reconnect(t, h, ctx, "dave")
	c.noServerMsg(t, codec.SMError, 200*time.Millisecond)
	if rows, _ := st.UserLiveGames(ctx, "dave", 10); len(rows) != 0 {
		t.Fatalf("dave's unplayable row was kept: %v", rows)
	}
	authAndSeekOn(t, c) // the login went through: SEEK is accepted
}

// authAndSeekOn sends a SEEK on an already authenticated connection.
func authAndSeekOn(t *testing.T, c *fakeConn) {
	t.Helper()
	c.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: "blitz", TimeControl: "blitz-5+3"}})
	c.noServerMsg(t, codec.SMError, 100*time.Millisecond)
}

// seedGame writes a game's opening with its index rows (as the matchmaker does).
func seedGame(t *testing.T, st game.IndexedStore, gid, tc, white, black string) {
	t.Helper()
	open := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{PoolID: "p", Seats: []game.Seat{{SeatIdx: 0}, {SeatIdx: 1, Color: 1}}, StartFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", TimeControl: tc}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: white}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: black}},
	}
	cfg := game.IndexConfig{Shards: 4, NodeID: "node-a", Incarnation: "inc-1", RecentTTL: time.Hour}
	ops, err := cfg.OpeningOps(gid, []string{white, black})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendIndexed(context.Background(), game.StreamIDFor(gid), 0, open, ops); err != nil {
		t.Fatal(err)
	}
}

// Item 3: a refused login on a second connection leaves the user's healthy
// first connection registered (MATCHED still reaches it).
func TestRefusedLoginKeepsTheOtherConnection(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	reconnect(t, h, ctx, "frank").noServerMsg(t, codec.SMError, 50*time.Millisecond)
	seedGame(t, st, "01ff0000-0000-7000-8000-00000000ffff", "blitz-5+3", "frank", "gina")
	h.srv.reg.store = &flakyLoad{EventStore: h.store, fails: 1}
	c2 := reconnect(t, h, ctx, "frank")
	c2.waitForServer(t, codec.SMError, 2*time.Second)
	time.Sleep(50 * time.Millisecond) // the refused connection's teardown
	if _, ok := h.srv.userConn("frank"); !ok {
		t.Fatal("a refused login removed the user's healthy connection")
	}
}

// Item 5: consuming an ending also drops a stale live row of the same user.
func TestConsumeRecentDropsAStaleLiveRow(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "018f0000-0000-7000-8000-00000000abcd"
	put := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "hank", GameID: gid}
	if err := st.RepairIndex(ctx, []game.IndexOp{put}); err != nil {
		t.Fatal(err)
	}
	h.srv.consumeRecent(ctx, gid, []string{"hank"})
	if rows, _ := st.UserLiveGames(ctx, "hank", 5); len(rows) != 0 {
		t.Fatalf("stale live row left after consuming: %v", rows)
	}
}

// failStream fails every Load of one stream; everything else passes through.
type failStream struct {
	*flakyLoad
	stream string
}

func (f failStream) Load(ctx context.Context, stream string) ([]game.Event, error) {
	if stream == f.stream {
		return nil, errOutage
	}
	return f.flakyLoad.EventStore.Load(ctx, stream)
}
