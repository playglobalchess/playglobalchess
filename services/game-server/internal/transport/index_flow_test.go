package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// indexedHarness is newHarness with the live-game index on for both the
// matchmaker (the opening write) and the registry (every ending). The harness
// clock sits in 2024, so the recent-games TTL is long enough to outlast the
// store's wall-clock expiry check.
func indexedHarness(t *testing.T) (*harness, game.IndexedStore) {
	t.Helper()
	h := newHarness(t)
	cfg := game.IndexConfig{Shards: 4, NodeID: "node-a", Incarnation: "inc-1", RecentTTL: 10 * 365 * 24 * time.Hour}
	if err := h.srv.mm.SetIndex(cfg); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.reg.SetIndex(cfg); err != nil {
		t.Fatal(err)
	}
	return h, h.store.(game.IndexedStore)
}

// A whole game through the real server: the opening indexes both players, and
// a resign removes the live rows and leaves the recent rows.
func TestIndexedGameFromSeekToResign(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")

	for user, seat := range map[string]uint8{"alice": seatA, "bob": seatB} {
		rows, err := st.UserLiveGames(ctx, user, 10)
		if err != nil || len(rows) != 1 || rows[0].GameID != gameID || rows[0].Seat != seat {
			t.Fatalf("live rows for %s: %v %v, want %s at seat %d", user, rows, err, gameID, seat)
		}
	}
	shard := game.IndexShard(gameID, 4)
	live, err := st.LiveGames(ctx, shard)
	if err != nil || len(live) != 1 || live[0].GameID != gameID || live[0].OwnerNode != "node-a" {
		t.Fatalf("live_games: %v %v", live, err)
	}

	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	for _, user := range []string{"alice", "bob"} {
		if rows, _ := st.UserLiveGames(ctx, user, 10); len(rows) != 0 {
			t.Fatalf("%s still has live rows after the game ended: %v", user, rows)
		}
		rows, err := st.UserRecentGames(ctx, user, 10)
		if err != nil || len(rows) != 1 || rows[0].GameID != gameID {
			t.Fatalf("recent rows for %s: %v %v, want %s", user, rows, err, gameID)
		}
	}
	if live, _ := st.LiveGames(ctx, shard); len(live) != 0 {
		t.Fatalf("live_games still lists the ended game: %v", live)
	}
}

// SetIndex fails as a whole: a store with no index support leaves both off.
func TestServerSetIndexIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	cfg := game.IndexConfig{Shards: 4, NodeID: "n", Incarnation: "i", RecentTTL: time.Hour}
	if err := h.srv.SetIndex(game.IndexConfig{}); err == nil {
		t.Fatal("accepted an invalid config")
	}
	if h.srv.reg.index != nil {
		t.Fatal("registry left on after a failed SetIndex")
	}
	if err := h.srv.SetIndex(cfg); err != nil || h.srv.reg.index == nil {
		t.Fatalf("SetIndex: %v", err)
	}
}
