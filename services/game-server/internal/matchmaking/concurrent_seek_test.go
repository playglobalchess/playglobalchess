package matchmaking

import (
	"context"
	"sync"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Two compatible seekers posted concurrently through the full Matchmaker must
// produce exactly one game, never 0 or 2. Runs under -race (and -count=N) to
// exercise the claim protocol. NOTE: MemoryPool pairs on PoolID alone and has no
// network/visibility lag, so this guards the Matchmaker orchestration and the
// claim protocol, not the Redis-specific behaviour (that is
// TestRedis_ConcurrentMutualPairCreatesOneGame, gated on REDIS_ENDPOINT).
func TestSeek_ConcurrentMutualPairCreatesExactlyOneGame(t *testing.T) {
	ctx := context.Background()
	store := game.NewMemStore()
	m := New(NewMemoryPool(), store, SeedRatingSource{})
	m.coin = func() bool { return true }

	seekers := []Seeker{
		{SeekID: "ma", UserID: "alice", PoolID: "p", TimeControl: "blitz-5+3", Rated: true},
		{SeekID: "mb", UserID: "bob", PoolID: "p", TimeControl: "blitz-5+3", Rated: true},
	}
	var wg sync.WaitGroup
	matched := make([]bool, len(seekers))
	start := make(chan struct{})
	for i, s := range seekers {
		wg.Add(1)
		go func(i int, s Seeker) {
			defer wg.Done()
			<-start
			_, ok, err := m.Seek(ctx, s)
			if err != nil {
				t.Errorf("seek %s: %v", s.SeekID, err)
			}
			matched[i] = ok
		}(i, s)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, ok := range matched {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d seekers reported a match, want exactly 1", wins)
	}
	if n := store.StreamCount(); n != 1 {
		t.Fatalf("created %d games, want 1", n)
	}
}
