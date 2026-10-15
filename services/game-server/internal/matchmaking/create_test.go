package matchmaking

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// memPool is an in-memory SeekPool for the pure matchmaker tests (no Redis). It
// models the claim/release/remove lifecycle: a claimed seeker is not offered to
// another TryClaim until released. The Redis impl (redis.go) is proven against a
// real engine in the gated integration test.
type memPool struct {
	mu      sync.Mutex
	waiting map[string]Seeker // seekID -> seeker (not claimed)
	claimed map[string]bool   // seekID -> claimed
}

func newMemPool() *memPool {
	return &memPool{waiting: map[string]Seeker{}, claimed: map[string]bool{}}
}

func (p *memPool) Enqueue(_ context.Context, s Seeker) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting[s.SeekID] = s
	return nil
}

func (p *memPool) TryClaim(_ context.Context, newSeeker Seeker, w Window, now time.Time) (Seeker, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var candidates []Seeker
	for id, s := range p.waiting {
		if p.claimed[id] {
			continue
		}
		candidates = append(candidates, s)
	}
	best, ok := bestMatch(newSeeker, candidates, w, now)
	if !ok {
		return Seeker{}, false, nil
	}
	// Reserve both (the new seeker may not be enqueued yet; mark it claimed too).
	p.claimed[best.SeekID] = true
	p.claimed[newSeeker.SeekID] = true
	return best, true, nil
}

func (p *memPool) Release(_ context.Context, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.claimed, id)
	}
	return nil
}

func (p *memPool) Remove(_ context.Context, _ string, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.waiting, id)
		delete(p.claimed, id)
	}
	return nil
}

func (p *memPool) Cancel(_ context.Context, poolID, id string) error {
	return p.Remove(context.Background(), poolID, id)
}

// --- helpers ----------------------------------------------------------------

func newTestMatchmaker(t *testing.T, coinWhiteIsNew bool) (*Matchmaker, *memPool, *game.MemStore) {
	t.Helper()
	pool := newMemPool()
	store := game.NewMemStore()
	m := New(pool, store, SeedRatingSource{})
	m.now = func() time.Time { return time.Unix(2000, 0) }
	m.coin = func() bool { return coinWhiteIsNew } // deterministic
	return m, pool, store
}

func seeker(id, user string) Seeker {
	return Seeker{SeekID: id, UserID: user, PoolID: "blitz-5+3", TimeControl: "blitz-5+3", Rated: true}
}

// A first seeker with no partner is enqueued, no match.
func TestSeek_NoPartnerEnqueues(t *testing.T) {
	m, pool, store := newTestMatchmaker(t, true)
	ctx := context.Background()
	_, matched, err := m.Seek(ctx, seeker("s1", "alice"))
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	if matched {
		t.Fatal("first seeker matched with nobody")
	}
	pool.mu.Lock()
	_, waiting := pool.waiting["s1"]
	pool.mu.Unlock()
	if !waiting {
		t.Fatal("first seeker was not enqueued")
	}
	// No game created.
	evs, _ := store.Load(ctx, game.StreamIDFor("any"))
	if len(evs) != 0 {
		t.Fatal("a game was created with only one seeker")
	}
}

// Two compatible seekers pair, creating an opening stream that folds to LIVE
// with correct seats/colors.
func TestSeek_PairCreatesLiveGame(t *testing.T) {
	m, pool, store := newTestMatchmaker(t, true) // new seeker (bob) = white
	ctx := context.Background()
	// Alice seeks first (enqueued), Bob seeks and pairs.
	if _, matched, err := m.Seek(ctx, seeker("s1", "alice")); err != nil || matched {
		t.Fatalf("alice seek: matched=%v err=%v", matched, err)
	}
	match, matched, err := m.Seek(ctx, seeker("s2", "bob"))
	if err != nil {
		t.Fatalf("bob seek: %v", err)
	}
	if !matched {
		t.Fatal("bob did not pair with alice")
	}
	// Opening stream: exactly GameCreated + 2 PlayerJoined at seqs 0,1,2.
	evs, err := store.Load(ctx, match.StreamID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("opening stream has %d events, want 3", len(evs))
	}
	if evs[0].Type != game.EvGameCreated || evs[1].Type != game.EvPlayerJoined || evs[2].Type != game.EvPlayerJoined {
		t.Fatalf("opening stream shape wrong: %v %v %v", evs[0].Type, evs[1].Type, evs[2].Type)
	}
	// coin=white-is-new → bob is white (seat 0), alice black (seat 1).
	if match.Players[0] != "bob" || match.Players[1] != "alice" {
		t.Fatalf("seat assignment wrong: %+v", match.Players)
	}
	// The stream folds to a LIVE game (pure fold is enough to check phase/seats).
	st, err := game.Fold(evs, game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}})
	if err != nil {
		t.Fatalf("fold opening: %v", err)
	}
	if st.Phase != game.PhaseLive {
		t.Fatalf("opening folds to phase %s, want Live", st.Phase)
	}
	// Both seekers removed from the pool after the match (Remove finalized).
	pool.mu.Lock()
	n := len(pool.waiting)
	pool.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d seekers left in pool after match, want 0", n)
	}
}

// If the opening AppendBatch fails, the claims are released (both pairable
// again) and no game is created.
func TestSeek_CreateFailureReleasesClaims(t *testing.T) {
	m, pool, store := newTestMatchmaker(t, true)
	ctx := context.Background()
	_, _, _ = m.Seek(ctx, seeker("s1", "alice"))
	// Arm the store to fail the opening append.
	store.FailNextAppend(errForced)
	_, matched, err := m.Seek(ctx, seeker("s2", "bob"))
	if matched || err == nil {
		t.Fatalf("expected creation failure, got matched=%v err=%v", matched, err)
	}
	// Claims released → both pairable again (nothing claimed).
	pool.mu.Lock()
	anyClaimed := len(pool.claimed) > 0
	pool.mu.Unlock()
	if anyClaimed {
		t.Fatal("claims not released after creation failure")
	}
}

// Two compatible seekers arriving CONCURRENTLY must pair — not both miss and
// stall (the mutual-miss race, F1). With enqueue-before-claim + the atomic
// claim, exactly one Seek creates the game; the other is consumed into it.
func TestSeek_ConcurrentMutualPair(t *testing.T) {
	m, _, store := newTestMatchmaker(t, true)
	ctx := context.Background()
	var wg sync.WaitGroup
	matches := make([]bool, 2)
	seekers := []Seeker{seeker("sa", "alice"), seeker("sb", "bob")}
	for i, s := range seekers {
		wg.Add(1)
		go func(i int, s Seeker) {
			defer wg.Done()
			_, matched, err := m.Seek(ctx, s)
			if err != nil {
				t.Errorf("seek %s: %v", s.SeekID, err)
			}
			matches[i] = matched
		}(i, s)
	}
	wg.Wait()
	wins := 0
	for _, mt := range matches {
		if mt {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent mutual seek: %d matches reported, want exactly 1 (0 = mutual-miss stall)", wins)
	}
	if n := store.StreamCount(); n != 1 {
		t.Fatalf("concurrent mutual seek created %d games, want exactly 1", n)
	}
}

var errForced = &forcedErr{}

type forcedErr struct{}

func (*forcedErr) Error() string { return "forced append failure" }
