package matchmaking

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Integration test against a REAL Redis, gated on REDIS_ENDPOINT so it runs in
// CI (where a redis service container is up) and skips locally. The in-memory
// memPool (create_test.go) proves the matchmaker logic everywhere; THIS proves
// the atomic-claim concurrency guarantee against a real Redis + the Lua script
// (Q-MM5). CI brings up `redis` and sets REDIS_ENDPOINT; the user can run it
// locally via `podman run -d -p 6379:6379 redis` then REDIS_ENDPOINT=127.0.0.1:6379.
func redisPool(t *testing.T) *RedisPool {
	t.Helper()
	addr := os.Getenv("REDIS_ENDPOINT")
	if addr == "" {
		t.Skip("REDIS_ENDPOINT not set — skipping real-Redis integration (runs in CI)")
	}
	p, err := NewRedisPool(context.Background(), addr, 2*time.Minute)
	if err != nil {
		t.Fatalf("NewRedisPool: %v", err)
	}
	// Isolate: flush is too blunt (shared CI redis); instead use a unique pool_id
	// per test via the seeker PoolID below, and clean up our own keys.
	return p
}

func intgSeeker(id, user, pool string, rating int, ageSec int) Seeker {
	return Seeker{
		SeekID: id, UserID: user, PoolID: pool, TimeControl: "blitz-5+3",
		Rating: rating, Rated: true,
		EnqueuedAt: time.Now().Add(-time.Duration(ageSec) * time.Second),
	}
}

// Enqueue → readPool → bestMatch round-trips through real Redis with correct
// fields (rating ordering, hash fidelity).
func TestRedis_EnqueueAndClaim(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	pool := "it-eq-" + t.Name()
	a := intgSeeker("ra", "alice", pool, 1500, 0)
	defer p.Remove(ctx, pool, "ra", "rb")

	if err := p.Enqueue(ctx, a); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// A compatible new seeker claims alice. Seek enqueues the claimer before
	// TryClaim (the mutual-miss fix), and the claim requires both to be waiting.
	b := intgSeeker("rb", "bob", pool, 1510, 0)
	if err := p.Enqueue(ctx, b); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	partner, ok, err := p.TryClaim(ctx, b, DefaultWindow, time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok || partner.SeekID != "ra" || partner.UserID != "alice" {
		t.Fatalf("claim returned %+v ok=%v, want alice/ra", partner, ok)
	}
}

// THE concurrency guarantee: two newcomers race while one partner waits. All
// three are enqueued (Seek's contract), so the newcomers may also pair with each
// other; what must hold is that no seeker is in two pairs (no double-match) and
// that some pair forms (no lost match). It no longer asserts a single winner:
// n1+n2 pairing while the partner keeps waiting is a correct outcome. This is
// what the Lua compare-and-set exists for.
func TestRedis_ConcurrentClaimNoDoubleMatch(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	pool := "it-race-" + t.Name()
	// One waiting partner, two newcomers both wanting it.
	partner := intgSeeker("rp", "partner", pool, 1500, 0)
	n1 := intgSeeker("rn1", "n1", pool, 1500, 0)
	n2 := intgSeeker("rn2", "n2", pool, 1500, 0)
	defer p.Remove(ctx, pool, "rp", "rn1", "rn2")

	// Seek enqueues each claimer before TryClaim, so all three are waiting. The
	// newcomers may also legitimately pair with each other; the guarantee is that
	// no seeker ends up in two pairs and the partner is not lost.
	for _, s := range []Seeker{partner, n1, n2} {
		if err := p.Enqueue(ctx, s); err != nil {
			t.Fatalf("enqueue %s: %v", s.SeekID, err)
		}
	}

	var wg sync.WaitGroup
	got := make([]string, 2) // the partner each newcomer claimed, "" if none
	for i, n := range []Seeker{n1, n2} {
		wg.Add(1)
		go func(i int, s Seeker) {
			defer wg.Done()
			won, ok, err := p.TryClaim(ctx, s, DefaultWindow, time.Now())
			if err != nil {
				t.Errorf("claim %s: %v", s.SeekID, err)
			}
			if ok {
				got[i] = won.SeekID
			}
		}(i, n)
	}
	wg.Wait()

	seen := map[string]int{}
	for i, g := range got {
		if g != "" {
			seen[g]++
			seen[[]string{"rn1", "rn2"}[i]]++
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("seeker %s is in %d pairs (double-match): claims %v", id, n, got)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("no claim won with three compatible seekers waiting (lost match): %v", got)
	}
}

// Release makes a claimed seeker pairable again; Remove finalizes (gone).
func TestRedis_ReleaseAndRemove(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	pool := "it-rr-" + t.Name()
	a := intgSeeker("rra", "alice", pool, 1500, 0)
	defer p.Remove(ctx, pool, "rra", "rrb")
	_ = p.Enqueue(ctx, a)
	b := intgSeeker("rrb", "bob", pool, 1500, 0)
	_ = p.Enqueue(ctx, b) // Seek enqueues the claimer first

	// Claim, then Release → alice claimable again.
	if _, ok, _ := p.TryClaim(ctx, b, DefaultWindow, time.Now()); !ok {
		t.Fatal("first claim failed")
	}
	if err := p.Release(ctx, "rra", "rrb"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, _ := p.TryClaim(ctx, b, DefaultWindow, time.Now()); !ok {
		t.Fatal("alice not claimable after release")
	}
	// Remove → gone from the pool.
	if err := p.Remove(ctx, pool, "rra"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	waiting, _ := p.readPool(ctx, pool)
	for _, s := range waiting {
		if s.SeekID == "rra" {
			t.Fatal("alice still in pool after Remove")
		}
	}
}

// Regression (review S1): Remove must ZREM the pool member even when the seek
// HASH has already TTL-expired — removal takes the pool id explicitly, so it no
// longer depends on reading pool_id from a dead hash. Before the fix, an
// expired-hash seeker's ZSET member was orphaned.
func TestRedis_RemoveZRemsEvenWhenHashExpired(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	pool := "it-exp-" + t.Name()
	s := intgSeeker("rex", "x", pool, 1500, 0)
	defer p.Remove(ctx, pool, "rex")
	if err := p.Enqueue(ctx, s); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Simulate the hash having TTL-expired while the ZSET member lingers.
	p.rdb.Del(ctx, seekKey("rex"))
	// Remove with the explicit pool id must still clear the ZSET member.
	if err := p.Remove(ctx, pool, "rex"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	n, _ := p.rdb.ZCard(ctx, poolKey(pool)).Result()
	if n != 0 {
		t.Fatalf("ZSET member orphaned after Remove with expired hash: ZCARD=%d, want 0", n)
	}
}

// Regression (review §3): readPool must prune a member whose hash expired even
// if a stale claim key lingers — a claim must not shield a dead seeker from
// cleanup. Before the fix, readPool skipped claimed members before the empty-
// hash check, so such a member lingered until its claim also expired.
func TestRedis_ReadPoolPrunesClaimedExpiredMember(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	pool := "it-prune-" + t.Name()
	defer p.Remove(ctx, pool, "rpr")
	// Member in the ZSET, a lingering claim, but NO hash (expired).
	p.rdb.ZAdd(ctx, poolKey(pool), redis.Z{Score: 1500, Member: "rpr"})
	p.rdb.Set(ctx, claimKey("rpr"), "1", time.Minute)
	// readPool should prune it (hash gone) rather than skip-on-claim.
	if _, err := p.readPool(ctx, pool); err != nil {
		t.Fatalf("readPool: %v", err)
	}
	n, _ := p.rdb.ZCard(ctx, poolKey(pool)).Result()
	if n != 0 {
		t.Fatalf("claimed+expired member not pruned: ZCARD=%d, want 0", n)
	}
	if c, _ := p.rdb.Exists(ctx, claimKey("rpr")).Result(); c != 0 {
		t.Fatal("stale claim not cleared on prune")
	}
}

// F1 against REAL Redis: two compatible seekers via the full Matchmaker,
// concurrently, must pair into exactly one game (no mutual-miss stall). This
// exercises the enqueue-before-claim ordering + the Lua atomic claim end to end.
func TestRedis_ConcurrentMutualPairCreatesOneGame(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	// Unique ids per run so -count=N runs cannot see each other's keys (#90/#76).
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	pool := "it-mutual-" + t.Name() + "-" + run
	ma, mb := "ma-"+run, "mb-"+run
	defer p.Remove(ctx, pool, ma, mb)

	store := game.NewMemStore()
	m := New(p, store, SeedRatingSource{})
	m.coin = func() bool { return true }

	var wg sync.WaitGroup
	matches := make([]bool, 2)
	seekers := []Seeker{
		{SeekID: ma, UserID: "alice", PoolID: pool, TimeControl: "blitz-5+3", Rated: true},
		{SeekID: mb, UserID: "bob", PoolID: pool, TimeControl: "blitz-5+3", Rated: true},
	}
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
		t.Fatalf("real-redis concurrent mutual seek: %d matches, want exactly 1 (0 = mutual-miss stall)", wins)
	}
	if n := store.StreamCount(); n != 1 {
		t.Fatalf("real-redis concurrent mutual seek created %d games, want 1", n)
	}
}

// TestRedis_ClaimOfRemovedSeekerFails is the deterministic #90 interleaving:
// B reads the pool and sees A waiting; meanwhile A is matched with C and the
// match is finalized (Remove deletes A's hash, membership AND claim key); B then
// claims A. B's claim must fail: A is no longer waiting. Before the fix it
// succeeded (the script checked only the claim keys) and a second game was made.
func TestRedis_ClaimOfRemovedSeekerFails(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	pool := "it-90-" + run
	a, b, c := "a-"+run, "b-"+run, "c-"+run
	defer p.Remove(ctx, pool, a, b, c)
	for _, s := range []Seeker{
		intgSeeker(a, "alice", pool, 1500, 0),
		intgSeeker(c, "carol", pool, 1500, 0),
		intgSeeker(b, "bob", pool, 1500, 0),
	} {
		if err := p.Enqueue(ctx, s); err != nil {
			t.Fatalf("enqueue %s: %v", s.SeekID, err)
		}
	}
	// B's stale snapshot: A is waiting and unclaimed.
	snap, err := p.readPool(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	sawA := false
	for _, s := range snap {
		sawA = sawA || s.SeekID == a
	}
	if !sawA {
		t.Fatalf("setup: B's read does not contain A: %+v", snap)
	}
	// A wins a claim with C, the game is created, and the match is finalized.
	if ok, err := p.claim(ctx, pool, c, a); err != nil || !ok {
		t.Fatalf("A/C claim: ok=%v err=%v", ok, err)
	}
	if err := p.Remove(ctx, pool, a, c); err != nil {
		t.Fatal(err)
	}
	// B now claims A from its stale read: must fail.
	ok, err := p.claim(ctx, pool, b, a)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("#90: B claimed A after A was matched and removed (a second game would be created)")
	}
	// And B itself is still claimable by a newcomer (its claim key was not left set).
	d := "d-" + run
	defer p.Remove(ctx, pool, d)
	if err := p.Enqueue(ctx, intgSeeker(d, "dave", pool, 1500, 0)); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.claim(ctx, pool, d, b); err != nil || !ok {
		t.Fatalf("B should still be claimable after the failed claim: ok=%v err=%v", ok, err)
	}
}

// A seeker whose hash expired but whose pool member lingers (no lazy prune yet)
// cannot be claimed either.
func TestRedis_ClaimOfExpiredSeekerFails(t *testing.T) {
	p := redisPool(t)
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	pool := "it-90x-" + run
	a, b := "a-"+run, "b-"+run
	defer p.Remove(ctx, pool, a, b)
	for _, s := range []Seeker{intgSeeker(a, "alice", pool, 1500, 0), intgSeeker(b, "bob", pool, 1500, 0)} {
		if err := p.Enqueue(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.rdb.Del(ctx, seekKey(a)).Err(); err != nil { // the hash TTL lapsed
		t.Fatal(err)
	}
	if ok, err := p.claim(ctx, pool, b, a); err != nil || ok {
		t.Fatalf("claim of an expired seeker: ok=%v err=%v, want false", ok, err)
	}
}
