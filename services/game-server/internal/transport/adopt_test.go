package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/matchmaking"
)

// restartedNode builds a second node over the SAME store as h, as a process
// restart would: the first node is stopped (its root context is cancelled, so
// its tickers and sessions end), and the new one starts with no live state.
// Its clock reads `later` after the first node's.
func restartedNode(t *testing.T, h *harness, later time.Duration) *Server {
	t.Helper()
	h.cancel()
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg := NewRegistry(ctx, h.store, o, ConventionResolver{})
	srv := NewServer(DevAuthenticator{}, matchmaking.New(newFakePool(), h.store, nil), reg)
	clk := &fakeClock{t: h.clk.now().Add(later)}
	reg.now, srv.now = clk.now, clk.now
	srv.flagInterval = 10 * time.Millisecond
	if err := srv.SetIndex(game.IndexConfig{Shards: 4, NodeID: "node-a", Incarnation: "inc-2", RecentTTL: 10 * 365 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	return srv
}

// #98: a node that restarts adopts every live game, fills the seat-to-user map
// from the stream, and arms the deadline ticker, so a game whose clock ran out
// during the downtime ends even though nobody reconnects.
func TestAdoptAtStartArmsTheDeadlineTicker(t *testing.T) {
	h, st := indexedHarness(t)
	_, _, gameID := pairTiny(t, h) // alice and bob, two plies in, the old node's clock is frozen
	srv := restartedNode(t, h, 10*time.Minute)

	adopted, _, failed, err := srv.AdoptLive(context.Background(), 4)
	if err != nil || adopted != 1 || failed != 0 {
		t.Fatalf("AdoptLive: adopted=%d failed=%d err=%v, want 1/0/nil", adopted, failed, err)
	}
	if users := srv.seatUsers(gameID); len(users) != 2 {
		t.Fatalf("seat-to-user map after adoption = %v, want both seats", users)
	}
	waitUntil(t, 5*time.Second, "the adopted game to end on its deadline", func() bool {
		evs, err := st.Load(context.Background(), game.StreamIDFor(gameID))
		return err == nil && game.HasEnding(evs)
	})
	// The ending also removed the live row (2b), so a second start adopts nothing.
	waitUntil(t, 5*time.Second, "the live row to go", func() bool {
		rows, _ := st.UserLiveGames(context.Background(), "alice", 10)
		return len(rows) == 0
	})
}

// Adoption needs the index, and a store without one is refused.
func TestAdoptNeedsTheIndex(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	if _, _, _, err := h.srv.AdoptLive(context.Background(), 4); err == nil {
		t.Fatal("AdoptLive ran with the index off")
	}
}

// running reports whether the game's flag ticker goroutine is running.
func running(s *Server, gameID string) bool {
	s.tickerMu.Lock()
	defer s.tickerMu.Unlock()
	_, ok := s.tickers[gameID]
	return ok
}

// Starting a running game again shares its session and starts no second
// ticker: exactly one goroutine checks the game.
func TestStartGameArmsOneTicker(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	var mu sync.Mutex
	exits := 0
	h.srv.tickerExited = func(string) { mu.Lock(); exits++; mu.Unlock() }
	_, _, gameID := pairTiny(t, h)
	for i := 0; i < 3; i++ {
		if _, err := h.srv.startGame(context.Background(), gameID, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	h.srv.dropGame(gameID)
	waitUntil(t, 2*time.Second, "the ticker to exit", func() bool { return !running(h.srv, gameID) })
	time.Sleep(100 * time.Millisecond) // let any second goroutine exit too
	mu.Lock()
	defer mu.Unlock()
	if exits != 1 {
		t.Fatalf("%d ticker goroutines ran for one game, want 1", exits)
	}
}

// B1 (review): a game dropped and then rebuilt gets a ticker again, and no
// entry is left behind for the dropped game.
func TestDropThenRebuildRearmsTheTicker(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	w := watchTickers(h)
	_, _, gameID := pairTiny(t, h)
	h.srv.dropGame(gameID)
	waitUntil(t, 2*time.Second, "the old ticker to exit", func() bool { return w.gone(gameID) && !running(h.srv, gameID) })
	if h.srv.gt.tracked(gameID) {
		t.Fatal("transport entry left for a dropped game")
	}
	if _, err := h.srv.startGame(context.Background(), gameID, "", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, "a ticker for the rebuilt game", func() bool { return running(h.srv, gameID) })
	if users := h.srv.seatUsers(gameID); len(users) != 2 {
		t.Fatalf("seat map after rebuild = %v", users)
	}
}

// S2 (review): adoption skips a game whose stream already ended (a stale live
// row) and starts nothing for it.
func TestAdoptSkipsAnEndedGame(t *testing.T) {
	h, st := indexedHarness(t)
	_, _, gameID := pairTiny(t, h)
	// Leave the live row in place, as a refused index write would, and end the
	// stream with a plain write.
	evs, err := st.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	end := game.Event{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: []game.Result{game.ResultDraw, game.ResultDraw}, Termination: game.TermDrawAgree}}
	if err := st.AppendBatch(context.Background(), game.StreamIDFor(gameID), uint32(len(evs)), []game.Event{end}); err != nil {
		t.Fatal(err)
	}
	srv := restartedNode(t, h, time.Minute)
	adopted, skipped, failed, err := srv.AdoptLive(context.Background(), 4)
	if err != nil || adopted != 0 || skipped != 1 || failed != 0 {
		t.Fatalf("AdoptLive: adopted=%d skipped=%d failed=%d err=%v, want 0/1/0", adopted, skipped, failed, err)
	}
	if _, live := srv.reg.Get(gameID); live || running(srv, gameID) || srv.gt.tracked(gameID) {
		t.Fatal("an ended game was started")
	}
	// GS-8 4.4: adoption drops the game's live_games row. Each player's own
	// live row stays for their login, which pushes the ending once, and no
	// recent row is written (it could re-create one a player consumed).
	if l, _ := st.LiveGames(context.Background(), game.IndexShard(gameID, 4)); len(l) != 0 {
		t.Fatalf("live_games still lists the ended game: %v", l)
	}
	for _, u := range []string{"alice", "bob"} {
		if rows, _ := st.UserRecentGames(context.Background(), u, 10); len(rows) != 0 {
			t.Fatalf("adoption wrote recent rows for %s: %v", u, rows)
		}
	}
	c := authOn(t, srv, "alice")
	if m := c.waitForServer(t, codec.SMState, 2*time.Second); m.State.GameID != gameID || m.State.State.Status != codec.StatusEnded {
		t.Fatalf("alice's login after adoption: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "alice's stale live row to go", func() bool {
		rows, _ := st.UserLiveGames(context.Background(), "alice", 10)
		return len(rows) == 0
	})
	authOn(t, srv, "alice").noServerMsg(t, codec.SMState, 150*time.Millisecond)
	if adopted, skipped, _, _ := srv.AdoptLive(context.Background(), 4); adopted+skipped != 0 {
		t.Fatalf("second AdoptLive found %d games, want none", adopted+skipped)
	}
}

// Drops, rebuilds and restarts racing each other: once they stop, the live game
// has exactly one ticker, and a dropped game leaves no transport entry.
func TestTickerHammerDropRebuildStart(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	_, _, gameID := pairTiny(t, h)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if (i+j)%3 == 0 {
					h.srv.dropGame(gameID)
				} else {
					_, _ = h.srv.startGame(ctx, gameID, "", nil)
				}
			}
		}(i)
	}
	wg.Wait()
	if _, err := h.srv.startGame(ctx, gameID, "", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, "exactly one ticker for the live game", func() bool {
		h.srv.tickerMu.Lock()
		defer h.srv.tickerMu.Unlock()
		_, ok := h.srv.tickers[gameID]
		return ok && len(h.srv.tickers) == 1
	})
	h.srv.dropGame(gameID)
	waitUntil(t, 2*time.Second, "the ticker to exit after the last drop", func() bool { return !running(h.srv, gameID) })
	if h.srv.gt.tracked(gameID) {
		t.Fatal("transport entry left after the last drop")
	}
}

// A game whose first start fails is retried once and adopted on the second pass.
func TestAdoptRetriesAFailedStartOnce(t *testing.T) {
	h, _ := indexedHarness(t)
	_, _, gameID := pairTiny(t, h)
	srv := restartedNode(t, h, time.Second)
	flaky := &flakyLoad{EventStore: srv.reg.store, fails: 1}
	srv.reg.store = flaky
	adopted, skipped, failed, err := srv.AdoptLive(context.Background(), 4)
	if err != nil || adopted != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("AdoptLive: adopted=%d skipped=%d failed=%d err=%v, want 1/0/0", adopted, skipped, failed, err)
	}
	if !running(srv, gameID) {
		t.Fatal("the retried game has no ticker")
	}
}

// flakyLoad fails the first n stream loads, then passes through. It keeps the
// IndexedStore methods of the store it wraps.
type flakyLoad struct {
	game.EventStore
	mu    sync.Mutex
	fails int
}

func (f *flakyLoad) Load(ctx context.Context, stream string) ([]game.Event, error) {
	f.mu.Lock()
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return nil, errOutage
	}
	return f.EventStore.Load(ctx, stream)
}

func (f *flakyLoad) inner() game.IndexedStore { return f.EventStore.(game.IndexedStore) }

func (f *flakyLoad) AppendIndexed(ctx context.Context, id string, seq uint32, evs []game.Event, ops []game.IndexOp) error {
	return f.inner().AppendIndexed(ctx, id, seq, evs, ops)
}
func (f *flakyLoad) LiveGames(ctx context.Context, shard int) ([]game.LiveEntry, error) {
	return f.inner().LiveGames(ctx, shard)
}
func (f *flakyLoad) UserLiveGames(ctx context.Context, u string, n int) ([]game.UserEntry, error) {
	return f.inner().UserLiveGames(ctx, u, n)
}
func (f *flakyLoad) UserRecentGames(ctx context.Context, u string, n int) ([]game.UserEntry, error) {
	return f.inner().UserRecentGames(ctx, u, n)
}

// A drop that lands after startGame built the session and before it filled the
// seat map leaves no transport entry and no ticker for the dropped game.
func TestDropBetweenBuildAndSeatFillLeavesNoEntry(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	_, _, gameID := pairTiny(t, h)
	h.srv.dropGame(gameID)
	waitUntil(t, 2*time.Second, "the old ticker to exit", func() bool { return !running(h.srv, gameID) })
	h.srv.afterBuild = func(id string) { h.srv.dropGame(id) }
	_, _ = h.srv.startGame(context.Background(), gameID, "", nil)
	h.srv.afterBuild = nil
	if h.srv.gt.tracked(gameID) {
		t.Fatal("a transport entry was recreated for a dropped game")
	}
	time.Sleep(50 * time.Millisecond)
	if running(h.srv, gameID) {
		t.Fatal("a ticker is running for a dropped game")
	}
}
func (f *flakyLoad) RepairIndex(ctx context.Context, ops []game.IndexOp) error {
	return f.inner().RepairIndex(ctx, ops)
}
