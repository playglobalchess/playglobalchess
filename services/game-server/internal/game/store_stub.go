package game

import (
	"context"
	"fmt"
	"sync"
)

// MemStore is the GS-1 in-memory EventStore double (SPEC-S2-GAME-SERVER §2c:
// "STUB with an in-memory slice"). The real store product is ADR-10 / later
// chunk. It commits synchronously, which satisfies the persist-before-ack
// ORDERING contract (NFR-7) at the interface level: Append returns only after
// the event is appended, so a caller that waits for Append before acking cannot
// ack before persistence. A real store substitutes a durable commit for the
// in-memory append without changing the contract or the pipeline logic.
//
// It is safe for concurrent use (no global mutable state — each MemStore is an
// explicit value a caller owns, mutex-guarded internally).
type MemStore struct {
	mu      sync.Mutex
	streams map[string][]Event
	// failAppend, when set, makes the next Append fail without committing —
	// used by the persist-before-ack tests to simulate a durability failure
	// (SPEC-S2-GAME-SERVER §5 "persist fails after validation"), proving the
	// pipeline does not ack on a failed commit.
	failAppend error
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{streams: make(map[string][]Event)}
}

// FailNextAppend arms the store to fail the next Append with err (test seam).
func (m *MemStore) FailNextAppend(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failAppend = err
}

// Append commits one event at the caller-supplied expectedSeq (SPEC-S2-PROTOCOL
// §4A.8: strictly increasing, no gaps). It honors context cancellation before
// committing. If expectedSeq is not the stream's next free seq (already
// occupied, or a gap), it commits nothing and returns ErrSeqConflict — the
// in-memory analogue of the DynamoDB conditional-put rejection (Q-GS4E). On the
// armed-failure path it commits nothing and returns the error, so the caller
// must not ack (NFR-7).
func (m *MemStore) Append(ctx context.Context, streamID string, expectedSeq uint32, ev Event) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memstore: context: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAppend != nil {
		err := m.failAppend
		m.failAppend = nil
		return fmt.Errorf("memstore: append failed (not committed): %w", err)
	}
	if int(expectedSeq) != len(m.streams[streamID]) {
		return fmt.Errorf("memstore: expected seq %d, next is %d: %w",
			expectedSeq, len(m.streams[streamID]), ErrSeqConflict)
	}
	m.streams[streamID] = append(m.streams[streamID], ev)
	return nil
}

// AppendBatch commits evs atomically at consecutive seqs from expectedSeq. If
// any target seq is occupied (or the batch would not start at the next free
// seq), it commits NOTHING and returns ErrSeqConflict — the all-or-nothing
// analogue of DynamoDB TransactWriteItems (SPEC-GS4 §3.4). Empty slice: no-op.
func (m *MemStore) AppendBatch(ctx context.Context, streamID string, expectedSeq uint32, evs []Event) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memstore: context: %w", err)
	}
	if len(evs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAppend != nil {
		err := m.failAppend
		m.failAppend = nil
		return fmt.Errorf("memstore: append-batch failed (not committed): %w", err)
	}
	if int(expectedSeq) != len(m.streams[streamID]) {
		return fmt.Errorf("memstore: batch expected seq %d, next is %d: %w",
			expectedSeq, len(m.streams[streamID]), ErrSeqConflict)
	}
	// All-or-nothing: append to a copy conceptually, but since we hold the lock
	// and the only failure above is the seq check, a straight append is atomic
	// w.r.t. other callers.
	m.streams[streamID] = append(m.streams[streamID], evs...)
	return nil
}

// Load returns a copy of the ordered event slice for a stream (empty for an
// unknown stream, no error). A copy is returned so a caller folding the stream
// cannot mutate the store's backing array.
func (m *MemStore) Load(ctx context.Context, streamID string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: context: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.streams[streamID]
	out := make([]Event, len(src))
	copy(out, src)
	return out, nil
}
