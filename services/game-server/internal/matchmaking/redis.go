package matchmaking

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisPool is the ADR-10 ephemeral seek pool: a sorted set per pool (score =
// rating, member = seek_id) + a hash per seek carrying the seeker fields, both
// TTL'd so an abandoned seek self-expires. It implements SeekPool.
//
// The one concurrency guarantee it must provide is that two nodes cannot both
// CLAIM the same waiting partner. The pairing DECISION (bestMatch) is pure and
// runs in Go over a snapshot read of the pool; the CLAIM is a Lua
// compare-and-set that marks both seek_ids claimed only if neither is already
// claimed — so a losing node's claim fails and it retries against the updated
// set. Redis holds no game truth (ADR-10): a node loss drops only unmatched or
// claimed-but-uncommitted seekers (their TTL lapses, they re-seek).
type RedisPool struct {
	rdb *redis.Client
	// seekTTL bounds how long a seek (and a claim) lingers before self-expiring.
	seekTTL time.Duration
}

// NewRedisPool connects, PINGs (fail-fast on an unreachable Redis — a seek must
// never be accepted against a dead pool), and returns the pool.
func NewRedisPool(ctx context.Context, addr string, seekTTL time.Duration) (*RedisPool, error) {
	if seekTTL <= 0 {
		seekTTL = 2 * time.Minute
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("matchmaking: redis unreachable at %s: %w", addr, err)
	}
	return &RedisPool{rdb: rdb, seekTTL: seekTTL}, nil
}

// Close releases the client.
func (p *RedisPool) Close() error { return p.rdb.Close() }

func poolKey(poolID string) string  { return "mm:pool:" + poolID }
func seekKey(seekID string) string  { return "mm:seek:" + seekID }
func claimKey(seekID string) string { return "mm:claim:" + seekID }

// Enqueue adds the seeker to its pool's sorted set (score = rating) and stores
// its fields in a TTL'd hash. ZADD + HSET + two EXPIREs in a pipeline.
func (p *RedisPool) Enqueue(ctx context.Context, s Seeker) error {
	pipe := p.rdb.TxPipeline()
	pipe.ZAdd(ctx, poolKey(s.PoolID), redis.Z{Score: float64(s.Rating), Member: s.SeekID})
	pipe.HSet(ctx, seekKey(s.SeekID), map[string]interface{}{
		"user_id": s.UserID, "pool_id": s.PoolID, "time_control": s.TimeControl,
		"rating": s.Rating, "rated": boolToStr(s.Rated), "enqueued_at": s.EnqueuedAt.UnixNano(),
	})
	pipe.Expire(ctx, seekKey(s.SeekID), p.seekTTL)
	// The pool member cannot carry its own TTL (ZSET members don't), so a stale
	// member is pruned lazily on read (readPool drops members whose hash expired).
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("matchmaking: redis enqueue: %w", err)
	}
	return nil
}

// readPool loads the current UNCLAIMED waiting seekers for a pool, pruning any
// pool member whose seek hash has expired (lazy TTL cleanup for ZSET members).
// Reads are PIPELINED: one ZRANGE, then one pipeline fetching every member's
// hash + claim-existence in a single round-trip (not 2N serial RTTs — F2), then
// one pipeline to prune dead members.
func (p *RedisPool) readPool(ctx context.Context, poolID string) ([]Seeker, error) {
	ids, err := p.rdb.ZRange(ctx, poolKey(poolID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("matchmaking: redis zrange: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// One pipeline: HGETALL + EXISTS(claim) for every member.
	pipe := p.rdb.Pipeline()
	hcmds := make([]*redis.MapStringStringCmd, len(ids))
	ccmds := make([]*redis.IntCmd, len(ids))
	for i, id := range ids {
		hcmds[i] = pipe.HGetAll(ctx, seekKey(id))
		ccmds[i] = pipe.Exists(ctx, claimKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("matchmaking: redis readpool pipeline: %w", err)
	}

	var out []Seeker
	var deadIDs []string // members whose hash expired — prune below
	for i, id := range ids {
		h := hcmds[i].Val()
		if len(h) == 0 {
			// Hash TTL-expired: the seeker is gone. Prune the lingering ZSET
			// member and any stale claim, regardless of a lingering claim key (a
			// claim must never shield a dead member — §3 fix).
			deadIDs = append(deadIDs, id)
			continue
		}
		if ccmds[i].Val() > 0 {
			continue // live but currently claimed — skip
		}
		out = append(out, hashToSeeker(id, h))
	}

	if len(deadIDs) > 0 {
		// Prune in one pipeline. A prune failure is NOT fatal to the read (the
		// members are already excluded from `out`), but we surface it rather than
		// swallow it so orphaned-member cleanup failures are visible (F3). The
		// error is attached to the successful read as a best-effort signal; the
		// caller (and, once X-14 lands, a metric) can act on it.
		prune := p.rdb.Pipeline()
		for _, id := range deadIDs {
			prune.ZRem(ctx, poolKey(poolID), id)
			prune.Del(ctx, claimKey(id))
		}
		if _, perr := prune.Exec(ctx); perr != nil && !errors.Is(perr, redis.Nil) {
			return out, fmt.Errorf("matchmaking: redis prune (read succeeded, %d dead members not reclaimed): %w", len(deadIDs), perr)
		}
	}
	return out, nil
}

// claimScript atomically marks BOTH seek_ids claimed iff NEITHER is currently
// claimed. KEYS[1]=claim:a KEYS[2]=claim:b, ARGV[1]=ttlSeconds. Returns 1 on
// success, 0 if either was already claimed (the caller retries).
var claimScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
redis.call('SET', KEYS[1], '1', 'EX', ARGV[1])
redis.call('SET', KEYS[2], '1', 'EX', ARGV[1])
return 1
`)

// TryClaim reads the pool, picks the pure-core winner, and atomically claims the
// pair. If the atomic claim loses a race (either seeker got claimed between the
// read and the claim), it retries against a fresh read (bounded).
func (p *RedisPool) TryClaim(ctx context.Context, newSeeker Seeker, w Window, now time.Time) (Seeker, bool, error) {
	const maxAttempts = 8
	for attempt := 0; attempt < maxAttempts; attempt++ {
		waiting, err := p.readPool(ctx, newSeeker.PoolID)
		if err != nil {
			return Seeker{}, false, err
		}
		partner, ok := bestMatch(newSeeker, waiting, w, now)
		if !ok {
			return Seeker{}, false, nil // no partner now
		}
		ttl := strconv.Itoa(int(p.seekTTL.Seconds()))
		res, err := claimScript.Run(ctx, p.rdb,
			[]string{claimKey(newSeeker.SeekID), claimKey(partner.SeekID)}, ttl).Int()
		if err != nil {
			return Seeker{}, false, fmt.Errorf("matchmaking: redis claim: %w", err)
		}
		if res == 1 {
			return partner, true, nil // won the claim
		}
		// Lost the race — the chosen partner was claimed by another node; retry.
	}
	// Contention did not resolve in the bound — treat as "no match now" so the
	// seeker stays enqueued and retries on the next counter-seek, rather than
	// erroring the whole seek.
	return Seeker{}, false, nil
}

// Release clears claim markers so the seekers are pairable again.
func (p *RedisPool) Release(ctx context.Context, seekIDs ...string) error {
	keys := make([]string, 0, len(seekIDs))
	for _, id := range seekIDs {
		keys = append(keys, claimKey(id))
	}
	if len(keys) == 0 {
		return nil
	}
	if err := p.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("matchmaking: redis release: %w", err)
	}
	return nil
}

// Remove finalizes a match: delete the seekers' pool membership, hash, and
// claim. poolID is passed in (the caller knows it), so removal never depends on
// a possibly-expired hash to learn the pool — ZREM always fires.
func (p *RedisPool) Remove(ctx context.Context, poolID string, seekIDs ...string) error {
	pipe := p.rdb.TxPipeline()
	for _, id := range seekIDs {
		pipe.ZRem(ctx, poolKey(poolID), id)
		pipe.Del(ctx, seekKey(id), claimKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("matchmaking: redis remove: %w", err)
	}
	return nil
}

// Cancel withdraws a single unmatched seeker (same effect as Remove for one id).
func (p *RedisPool) Cancel(ctx context.Context, poolID, seekID string) error {
	return p.Remove(ctx, poolID, seekID)
}

// --- hash <-> seeker ---------------------------------------------------------

func hashToSeeker(seekID string, h map[string]string) Seeker {
	rating, _ := strconv.Atoi(h["rating"])
	enq, _ := strconv.ParseInt(h["enqueued_at"], 10, 64)
	return Seeker{
		SeekID: seekID, UserID: h["user_id"], PoolID: h["pool_id"],
		TimeControl: h["time_control"], Rating: rating, Rated: h["rated"] == "1",
		EnqueuedAt: time.Unix(0, enq),
	}
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// compile-time check
var _ SeekPool = (*RedisPool)(nil)
