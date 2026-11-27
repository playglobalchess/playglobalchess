package transport

import (
	"context"
	"testing"
	"time"

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

	adopted, failed, err := srv.AdoptLive(context.Background(), 4)
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
	if _, _, err := h.srv.AdoptLive(context.Background(), 4); err == nil {
		t.Fatal("AdoptLive ran with the index off")
	}
}

// A second AdoptLive (or a reconnect start) of a running game shares its
// session and does not arm a second ticker.
func TestStartGameArmsOneTicker(t *testing.T) {
	h, _ := indexedHarness(t)
	defer h.cancel()
	_, _, gameID := pairTiny(t, h)
	for i := 0; i < 3; i++ {
		if _, err := h.srv.startGame(context.Background(), gameID, roleResync); err != nil {
			t.Fatal(err)
		}
	}
	sh := h.srv.gt.shard(gameID)
	sh.mu.Lock()
	armed := sh.games[gameID].ticker
	sh.mu.Unlock()
	if !armed {
		t.Fatal("ticker latch not set")
	}
}
