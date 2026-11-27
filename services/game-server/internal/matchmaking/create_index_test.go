package matchmaking

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// GS-8 section 4.3: the opening events and the live-game index rows commit in
// one write, so a game never exists without its rows or the rows without it.

var testIndex = game.IndexConfig{Shards: 4, NodeID: "node-a", Incarnation: "inc-1", RecentTTL: time.Hour}

// faultyOpening is a MemStore whose next AppendIndexed can commit and then
// report an error (an ambiguous commit), or fail without committing.
type faultyOpening struct {
	*game.MemStore
	mu         sync.Mutex
	commitThen error
	errBefore  error
	calls      int
}

func (s *faultyOpening) AppendIndexed(ctx context.Context, id string, seq uint32, evs []game.Event, ops []game.IndexOp) error {
	s.mu.Lock()
	s.calls++
	then, before := s.commitThen, s.errBefore
	s.commitThen, s.errBefore = nil, nil
	s.mu.Unlock()
	if before != nil {
		return before
	}
	err := s.MemStore.AppendIndexed(ctx, id, seq, evs, ops)
	if err == nil && then != nil {
		return then
	}
	return err
}

func indexedMatchmaker(t *testing.T) (*Matchmaker, *memPool, *faultyOpening) {
	t.Helper()
	pool := newMemPool()
	store := &faultyOpening{MemStore: game.NewMemStore()}
	m := New(pool, store, SeedRatingSource{})
	m.now = func() time.Time { return time.Unix(2000, 0) }
	m.coin = func() bool { return true } // the new seeker (bob) takes white
	if err := m.SetIndex(testIndex); err != nil {
		t.Fatal(err)
	}
	return m, pool, store
}

func pairAliceAndBob(t *testing.T, m *Matchmaker) (Match, bool, error) {
	t.Helper()
	ctx := context.Background()
	if _, matched, err := m.Seek(ctx, seeker("s1", "alice")); err != nil || matched {
		t.Fatalf("alice: matched=%v err=%v", matched, err)
	}
	return m.Seek(ctx, seeker("s2", "bob"))
}

func TestSeek_OpeningWritesTheIndexRows(t *testing.T) {
	m, _, store := indexedMatchmaker(t)
	match, matched, err := pairAliceAndBob(t, m)
	if err != nil || !matched {
		t.Fatalf("pair: matched=%v err=%v", matched, err)
	}
	ctx := context.Background()
	// bob is white (seat 0), alice black (seat 1).
	for user, seat := range map[string]uint8{"bob": 0, "alice": 1} {
		rows, err := store.UserLiveGames(ctx, user, 10)
		if err != nil || len(rows) != 1 || rows[0].GameID != match.GameID || rows[0].Seat != seat {
			t.Fatalf("user_live_games for %s: %v %v, want %s at seat %d", user, rows, err, match.GameID, seat)
		}
	}
	live, err := store.LiveGames(ctx, game.IndexShard(match.GameID, testIndex.Shards))
	if err != nil || len(live) != 1 || live[0].GameID != match.GameID ||
		live[0].OwnerNode != "node-a" || live[0].OwnerIncarnation != "inc-1" {
		t.Fatalf("live_games: %v %v, want %s owned by node-a/inc-1", live, err, match.GameID)
	}
	if evs, _ := store.Load(ctx, match.StreamID); len(evs) != 3 {
		t.Fatalf("%d opening events, want 3", len(evs))
	}
}

// With no index configured the opening is the plain batch it always was.
func TestSeek_NoIndexConfiguredWritesNoRows(t *testing.T) {
	m, _, store := newTestMatchmaker(t, true)
	match, matched, err := pairAliceAndBob(t, m)
	if err != nil || !matched {
		t.Fatalf("pair: matched=%v err=%v", matched, err)
	}
	if rows, _ := store.UserLiveGames(context.Background(), "alice", 10); len(rows) != 0 {
		t.Fatalf("index rows written with no index configured: %v (game %s)", rows, match.GameID)
	}
}

// The store commits the opening and then reports a timeout. The game exists, so
// the seekers must be told (the index would otherwise name a game no client
// knows), and the claims are not released.
func TestSeek_AmbiguousOpeningCommitIsPaired(t *testing.T) {
	m, pool, store := indexedMatchmaker(t)
	if _, matched, _ := m.Seek(context.Background(), seeker("s1", "alice")); matched {
		t.Fatal("alice matched alone")
	}
	store.commitThen = errors.New("fault: timeout after commit")
	match, matched, err := m.Seek(context.Background(), seeker("s2", "bob"))
	if err != nil || !matched {
		t.Fatalf("ambiguous opening: matched=%v err=%v, want paired", matched, err)
	}
	if rows, _ := store.UserLiveGames(context.Background(), "alice", 10); len(rows) != 1 || rows[0].GameID != match.GameID {
		t.Fatalf("alice's live rows after an ambiguous commit: %v", rows)
	}
	pool.mu.Lock()
	left := len(pool.waiting)
	pool.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d seeks still waiting after the game was created", left)
	}
}

// A write that fails before committing creates nothing: no stream, no rows, and
// both seekers become pairable again.
func TestSeek_FailedOpeningCreatesNothing(t *testing.T) {
	m, pool, store := indexedMatchmaker(t)
	ctx := context.Background()
	if _, matched, _ := m.Seek(ctx, seeker("s1", "alice")); matched {
		t.Fatal("alice matched alone")
	}
	store.errBefore = errors.New("fault: store down")
	if _, matched, err := m.Seek(ctx, seeker("s2", "bob")); err == nil || matched {
		t.Fatalf("a failing store: matched=%v err=%v, want an error", matched, err)
	}
	for _, u := range []string{"alice", "bob"} {
		if rows, _ := store.UserLiveGames(ctx, u, 10); len(rows) != 0 {
			t.Fatalf("%s has live rows for a game that was never created: %v", u, rows)
		}
	}
	pool.mu.Lock()
	claimed := len(pool.claimed)
	pool.mu.Unlock()
	if claimed != 0 {
		t.Fatalf("%d claims left after a failed creation", claimed)
	}
	// The store is back: the same two seeks pair.
	if _, matched, err := m.Seek(ctx, seeker("s3", "carol")); err != nil || !matched {
		t.Fatalf("pairing after recovery: matched=%v err=%v", matched, err)
	}
}

func TestSetIndexNeedsAnIndexedStore(t *testing.T) {
	m := New(newMemPool(), eventsOnly{game.NewMemStore()}, SeedRatingSource{})
	if err := m.SetIndex(testIndex); err == nil {
		t.Fatal("SetIndex accepted a store with no index support")
	}
	if err := New(newMemPool(), game.NewMemStore(), nil).SetIndex(game.IndexConfig{}); !errors.Is(err, game.ErrBadIndexConfig) {
		t.Fatalf("zero config: %v, want ErrBadIndexConfig", err)
	}
}

// eventsOnly hides the IndexedStore methods of the store it wraps.
type eventsOnly struct{ game.EventStore }

// A seq conflict on the opening means the game id is already in use. The write
// is abandoned and the other game is left alone: the "is it committed?" re-read
// must never mistake someone else's stream for this opening.
func TestSeek_GameIDCollisionIsAbandonedNotAdopted(t *testing.T) {
	m, pool, store := indexedMatchmaker(t)
	ctx := context.Background()
	const taken = "018f0000-0000-7000-8000-000000000001"
	m.newID = func() (string, error) { return taken, nil }
	// Someone else's game already sits at that id, with a 3-event opening.
	ops, err := testIndex.OpeningOps(taken, []string{"x", "y"})
	if err != nil {
		t.Fatal(err)
	}
	other := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{PoolID: "p", Seats: []game.Seat{{SeatIdx: 0}, {SeatIdx: 1, Color: 1}}, StartFEN: StandardStartFEN, TimeControl: "blitz-5+3"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "x"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "y"}},
	}
	if err := store.MemStore.AppendIndexed(ctx, game.StreamIDFor(taken), 0, other, ops); err != nil {
		t.Fatal(err)
	}
	if _, matched, _ := m.Seek(ctx, seeker("s1", "alice")); matched {
		t.Fatal("alice matched alone")
	}
	if _, matched, err := m.Seek(ctx, seeker("s2", "bob")); err == nil || matched || !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("a colliding id: matched=%v err=%v, want ErrSeqConflict and no match", matched, err)
	}
	if rows, _ := store.UserLiveGames(ctx, "alice", 10); len(rows) != 0 {
		t.Fatalf("alice was indexed into someone else's game: %v", rows)
	}
	if evs, _ := store.Load(ctx, game.StreamIDFor(taken)); len(evs) != 3 {
		t.Fatalf("the other game's stream has %d events, want its own 3", len(evs))
	}
	pool.mu.Lock()
	claimed := len(pool.claimed)
	pool.mu.Unlock()
	if claimed != 0 {
		t.Fatalf("%d claims left after the abandoned creation", claimed)
	}
}
