// MemoryPool is an in-process SeekPool with no external dependency — the
// local/dev/e2e counterpart to RedisPool. It holds the waiting set and claim
// flags in a mutex-guarded map, implementing the same TryClaim-then-Remove
// claim protocol RedisPool does (reserve both seekers atomically; Release on
// failure; Remove only after the opening stream is durably appended — Q-MM6).
// It is NOT for multi-node production (no cross-process visibility, no TTL
// self-expiry); a single node, a local dev run, or an e2e harness that needs a
// real WebSocket server without standing up Redis.
package matchmaking

import (
	"context"
	"sync"
	"time"
)

// MemoryPool implements SeekPool entirely in memory.
type MemoryPool struct {
	mu      sync.Mutex
	waiting map[string]Seeker
	claimed map[string]bool
}

// NewMemoryPool returns an empty in-memory pool.
func NewMemoryPool() *MemoryPool {
	return &MemoryPool{waiting: map[string]Seeker{}, claimed: map[string]bool{}}
}

// Enqueue adds a seeker to the waiting set.
func (p *MemoryPool) Enqueue(_ context.Context, s Seeker) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting[s.SeekID] = s
	return nil
}

// TryClaim atomically reserves the first same-pool waiting partner (flipping
// both to claimed) without removing either. It mirrors RedisPool's atomic CLAIM
// PROTOCOL (reserve-both-then-remove-later, so a concurrent node cannot double-
// claim) but NOT its pairing POLICY: it deliberately IGNORES the Window and
// time args and pairs on PoolID alone — the rating/compatibility window that
// RedisPool.bestMatch enforces is NOT applied here. So a test using this pool
// exercises the claim protocol and the §4A flow, but canNOT prove window-based
// pairing; that remains RedisPool's own test surface. Returns the partner and
// true, or false if none qualifies.
func (p *MemoryPool) TryClaim(_ context.Context, newSeeker Seeker, _ Window, _ time.Time) (Seeker, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.waiting {
		if id == newSeeker.SeekID || p.claimed[id] {
			continue
		}
		if s.PoolID != newSeeker.PoolID || s.TimeControl != newSeeker.TimeControl {
			continue // never pair different clocks (#93)
		}
		p.claimed[id] = true
		p.claimed[newSeeker.SeekID] = true
		return s, true, nil
	}
	return Seeker{}, false, nil
}

// Release undoes claims so the seekers become pairable again.
func (p *MemoryPool) Release(_ context.Context, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.claimed, id)
	}
	return nil
}

// Remove finalizes a match: delete pool membership and claim for the seekers.
func (p *MemoryPool) Remove(_ context.Context, _ string, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.waiting, id)
		delete(p.claimed, id)
	}
	return nil
}

// Cancel withdraws a single unmatched seeker.
func (p *MemoryPool) Cancel(_ context.Context, _, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.waiting, id)
	delete(p.claimed, id)
	return nil
}
