package game

import (
	"context"
	"testing"
)

// conflictStore wraps a MemStore to simulate a cross-node seq race: on the first
// append the inject hook lands a COMPETING event at the pipeline's expected seq,
// so the pipeline's conditional append conflicts and must run its §3.2/Q-GS4E
// recovery (reload → idempotent re-ack vs re-sequence a valid loser). This is the
// only way to exercise that recovery without a real multi-node deployment.
type conflictStore struct {
	*MemStore
	armed    bool
	injected bool
	inject   func(m *MemStore)
}

func (c *conflictStore) Append(ctx context.Context, streamID string, expectedSeq uint32, ev Event) error {
	if c.armed && !c.injected {
		c.injected = true
		if c.inject != nil {
			c.inject(c.MemStore) // competing writer lands at expectedSeq first
		}
	}
	return c.MemStore.Append(ctx, streamID, expectedSeq, ev)
}

func seedConflict(t *testing.T) (context.Context, *MemStore, *conflictStore, string, string, int) {
	t.Helper()
	ctx := context.Background()
	mem := NewMemStore()
	cs := &conflictStore{MemStore: mem}
	gameID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	streamID := StreamIDFor(gameID)
	for i, ev := range liveStream(false) {
		if err := mem.Append(ctx, streamID, uint32(i), ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return ctx, mem, cs, gameID, streamID, len(liveStream(false))
}

// A valid move that loses the seq to a DIFFERENT competing event must be
// RE-SEQUENCED at the new head, never dropped (NFR-2 / Q-GS4E).
func TestPipelineSeqConflict_ValidLoserReSequenced(t *testing.T) {
	ctx, mem, cs, gameID, streamID, base := seedConflict(t)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	p := &Pipeline{Oracle: o, Store: cs, Opts: tc53opts()}

	// A competing DrawOffered lands at seq `base`, so the move's conditional
	// append at `base` conflicts and must re-sequence to base+1.
	cs.armed = true
	cs.inject = func(m *MemStore) {
		if err := m.Append(ctx, streamID, uint32(base), evSeat(EvDrawOffered, 1)); err != nil {
			t.Errorf("inject competing event: %v", err)
		}
	}

	out, err := p.SubmitMove(ctx, MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 1000, ServerTS: 1,
	})
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Accepted {
		t.Fatalf("valid move not accepted after seq conflict: %+v", out)
	}
	events, _ := mem.Load(ctx, streamID)
	if len(events) != base+2 {
		t.Fatalf("stream len %d, want %d (competing event + re-sequenced move)", len(events), base+2)
	}
	mv, mvSeq := findMove(events, out.Ply)
	if mv == nil {
		t.Fatalf("the valid move was DROPPED — not in the stream (NFR-2 violation)")
	}
	// AppliedSeq must be the move's REAL seq (where it sits), not the next free
	// seq — the re-sequenced move landed at base+1 (after the competing event).
	if out.AppliedSeq != mvSeq {
		t.Fatalf("AppliedSeq %d != the move's real seq %d (F1)", out.AppliedSeq, mvSeq)
	}
	if mvSeq != uint32(base+1) {
		t.Fatalf("re-sequenced move at seq %d, want %d", mvSeq, base+1)
	}
}

// If OUR move already landed (duplicate/resend that lost to our own prior
// append), recovery is an idempotent re-ack with NO second append.
func TestPipelineSeqConflict_OwnMoveIdempotentReAck(t *testing.T) {
	ctx, mem, cs, gameID, streamID, base := seedConflict(t)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	p := &Pipeline{Oracle: o, Store: cs, Opts: tc53opts()}

	// The competing writer injects OUR SAME move (ply 1) at seq base, so on
	// reload the pipeline finds its own move already present → idempotent re-ack.
	cs.armed = true
	cs.inject = func(m *MemStore) {
		if err := m.Append(ctx, streamID, uint32(base), Event{
			Type: EvMovePlayed, MovePlayed: &MovePlayed{Ply: 1, SeatIdx: 0, UCI: "e2e4", ClockAfterMs: 59000, ServerTS: 1},
		}); err != nil {
			t.Errorf("inject own move: %v", err)
		}
	}

	out, err := p.SubmitMove(ctx, MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 1000, ServerTS: 1,
	})
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Accepted {
		t.Fatalf("idempotent re-ack not accepted: %+v", out)
	}
	// AppliedSeq must point at where our move actually landed (base), not the
	// next free seq (F1).
	if out.AppliedSeq != uint32(base) {
		t.Fatalf("re-ack AppliedSeq %d != the move's real seq %d (F1)", out.AppliedSeq, base)
	}
	events, _ := mem.Load(ctx, streamID)
	count := 0
	for _, e := range events {
		if e.Type == EvMovePlayed && e.MovePlayed.Ply == 1 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("idempotent re-ack DOUBLE-appended: %d moves at ply 1, want 1", count)
	}
}
