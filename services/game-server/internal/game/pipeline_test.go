package game

import (
	"context"
	"errors"
	"testing"
)

// seedLiveGame appends a created+joined+joined stream to the store and returns
// its game id and stream id.
func seedLiveGame(t *testing.T, store *MemStore, rated bool) (string, string) {
	t.Helper()
	gameID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	streamID := StreamIDFor(gameID)
	ctx := context.Background()
	for _, ev := range liveStream(rated) {
		if _, err := store.Append(ctx, streamID, ev); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
	return gameID, streamID
}

func TestMemStoreSeqMonotonic(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	sid := "game/x"
	for want := uint32(0); want < 5; want++ {
		got, err := store.Append(ctx, sid, evSeat(EvDrawOffered, 0))
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if got != want {
			t.Fatalf("seq = %d, want %d", got, want)
		}
	}
	evs, err := store.Load(ctx, sid)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("loaded %d events, want 5", len(evs))
	}
	// Load returns a copy: mutating it must not affect the store.
	evs[0] = Event{}
	again, _ := store.Load(ctx, sid)
	if again[0].Type != EvDrawOffered {
		t.Error("Load did not return a defensive copy")
	}
}

func TestMemStoreUnknownStream(t *testing.T) {
	store := NewMemStore()
	evs, err := store.Load(context.Background(), "game/nope")
	if err != nil {
		t.Fatalf("unknown stream err: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("unknown stream returned %d events, want 0", len(evs))
	}
}

// TestPipelineAcceptsLegalMove: the happy path appends exactly one MovePlayed
// and returns Accepted with the projected clock.
func TestPipelineAcceptsLegalMove(t *testing.T) {
	store := NewMemStore()
	gameID, streamID := seedLiveGame(t, store, false)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)

	p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}
	out, err := p.SubmitMove(context.Background(), MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 10_000,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !out.Accepted || out.Ply != 1 {
		t.Fatalf("outcome = %+v, want accepted ply 1", out)
	}
	if out.ClockAfterMs != 293_000 {
		t.Errorf("clock_after_ms = %d, want 293000", out.ClockAfterMs)
	}
	if out.NewFEN != "fen-after-e4" {
		t.Errorf("NewFEN = %q, want fen-after-e4", out.NewFEN)
	}
	// The move path makes exactly ONE engine round-trip (validate this move) —
	// NOT O(ply) replays. This is the hot-path cost contract.
	if o.calls != 1 {
		t.Errorf("oracle.Legality called %d times, want exactly 1 (no stream replay)", o.calls)
	}
	// Exactly one MovePlayed appended.
	evs, _ := store.Load(context.Background(), streamID)
	moves := 0
	for _, e := range evs {
		if e.Type == EvMovePlayed {
			moves++
		}
	}
	if moves != 1 {
		t.Errorf("appended %d MovePlayed events, want 1", moves)
	}
}

// TestPipelineRejections covers each MOVE_REJECTED reason without appending.
func TestPipelineRejections(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown game", func(t *testing.T) {
		p := &Pipeline{Oracle: newFakeOracle(), Store: NewMemStore(), Opts: tc53opts()}
		out, err := p.SubmitMove(ctx, MoveIntent{GameID: "ghost", SeatIdx: 0, PlyExpected: 1, UCI: "e2e4"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !out.Rejected || out.Reason != RejectUnknownGame {
			t.Fatalf("outcome = %+v, want UNKNOWN_GAME", out)
		}
	})

	t.Run("illegal move appends nothing", func(t *testing.T) {
		store := NewMemStore()
		gameID, streamID := seedLiveGame(t, store, false)
		p := &Pipeline{Oracle: newFakeOracle(), Store: store, Opts: tc53opts()} // nothing legal
		out, err := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !out.Rejected || out.Reason != RejectIllegal {
			t.Fatalf("outcome = %+v, want ILLEGAL", out)
		}
		evs, _ := store.Load(ctx, streamID)
		if len(evs) != 3 { // only the seed events
			t.Errorf("illegal move appended an event: %d events", len(evs))
		}
	})

	t.Run("not your turn", func(t *testing.T) {
		store := NewMemStore()
		gameID, _ := seedLiveGame(t, store, false)
		o := newFakeOracle()
		o.allow("e7e5", "x", nil)
		p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}
		// seat 1 tries to move first (turn is seat 0).
		out, _ := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 1, PlyExpected: 1, UCI: "e7e5"})
		if !out.Rejected || out.Reason != RejectNotYourTurn {
			t.Fatalf("outcome = %+v, want NOT_YOUR_TURN", out)
		}
	})

	t.Run("stale ply", func(t *testing.T) {
		store := NewMemStore()
		gameID, _ := seedLiveGame(t, store, false)
		o := newFakeOracle()
		o.allow("e2e4", "x", nil)
		p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}
		// expects ply 2 when the server is at ply 0 (next is 1) and no ply-2 move exists.
		out, _ := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 2, UCI: "e2e4"})
		if !out.Rejected || out.Reason != RejectStalePly {
			t.Fatalf("outcome = %+v, want STALE_PLY", out)
		}
		if out.PlyExpected != 0 {
			t.Errorf("STALE_PLY echoed ply = %d, want server's current 0", out.PlyExpected)
		}
	})

	t.Run("seat out of range", func(t *testing.T) {
		store := NewMemStore()
		gameID, _ := seedLiveGame(t, store, false)
		p := &Pipeline{Oracle: newFakeOracle(), Store: store, Opts: tc53opts()}
		out, _ := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 9, PlyExpected: 1, UCI: "e2e4"})
		if !out.Rejected || out.Reason != RejectNotYourSeat {
			t.Fatalf("outcome = %+v, want NOT_YOUR_SEAT", out)
		}
	})

	t.Run("move after game over", func(t *testing.T) {
		store := NewMemStore()
		gameID, streamID := seedLiveGame(t, store, false)
		_, _ = store.Append(ctx, streamID, evSeat(EvResigned, 0))
		_, _ = store.Append(ctx, streamID, evEnded(TermResign, []Result{ResultLoss, ResultWin}))
		o := newFakeOracle()
		o.allow("e2e4", "x", nil)
		p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}
		out, _ := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 1, PlyExpected: 1, UCI: "e2e4"})
		if !out.Rejected || out.Reason != RejectGameOver {
			t.Fatalf("outcome = %+v, want GAME_OVER", out)
		}
	})
}

// TestPipelineIdempotentRetry: a duplicate MOVE on an already-persisted ply
// re-acks without a second append (§5).
func TestPipelineIdempotentRetry(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	gameID, streamID := seedLiveGame(t, store, false)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}

	in := MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 10_000}
	first, err := p.SubmitMove(ctx, in)
	if err != nil || !first.Accepted {
		t.Fatalf("first move: %+v err=%v", first, err)
	}
	callsAfterFirst := o.calls
	// Retry the same (game, ply): must re-ack, not append again — and must NOT
	// re-validate against the engine (the move is already persisted).
	retry, err := p.SubmitMove(ctx, in)
	if err != nil {
		t.Fatalf("retry err: %v", err)
	}
	if !retry.Accepted || retry.Ply != 1 || retry.ClockAfterMs != first.ClockAfterMs {
		t.Fatalf("retry outcome = %+v, want same accepted move", retry)
	}
	if o.calls != callsAfterFirst {
		t.Errorf("idempotent retry made %d extra oracle call(s), want 0", o.calls-callsAfterFirst)
	}
	evs, _ := store.Load(ctx, streamID)
	moves := 0
	for _, e := range evs {
		if e.Type == EvMovePlayed {
			moves++
		}
	}
	if moves != 1 {
		t.Errorf("idempotent retry appended a duplicate: %d moves", moves)
	}
}

// TestPersistBeforeAck is the NFR-7 contract test: when the store fails to
// commit after the engine validated the move, the pipeline returns an ERROR and
// NO accepted outcome, and nothing is appended.
func TestPersistBeforeAck(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	gameID, streamID := seedLiveGame(t, store, false)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}

	persistErr := errors.New("disk on fire")
	store.FailNextAppend(persistErr)

	out, err := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 10_000})
	if err == nil {
		t.Fatal("expected persistence error, got nil")
	}
	if !errors.Is(err, persistErr) {
		t.Errorf("error chain lost the cause: %v", err)
	}
	if out.Accepted {
		t.Fatal("move was ACKED despite persist failure (NFR-7 violation)")
	}
	// Crucially, no MovePlayed was committed — a flaky commit must not leave the
	// move half-applied.
	evs, _ := store.Load(ctx, streamID)
	for _, e := range evs {
		if e.Type == EvMovePlayed {
			t.Fatal("a MovePlayed was persisted on a failed commit")
		}
	}
}

// TestPipelineSurfacesTerminal: the engine's terminal verdict (mate) flows out
// so the caller can append GameEnded{MATE}.
func TestPipelineSurfacesTerminal(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	gameID, _ := seedLiveGame(t, store, false)
	o := newFakeOracle()
	mate := TermMate
	o.allow("e2e4", "fen-after-e4", &mate)
	p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}

	out, err := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 1_000})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !out.Accepted || out.Terminal == nil || *out.Terminal != TermMate {
		t.Fatalf("outcome = %+v, want accepted + terminal MATE", out)
	}
}

func tc53opts() FoldOptions { return FoldOptions{TimeControl: tc53()} }

// TestPipelineFlagOnReceive: a move arriving after the mover's time expired
// yields a Flagged outcome (not accept, not reject) and appends nothing.
func TestPipelineFlagOnReceive(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	gameID, streamID := seedLiveGame(t, store, false)
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	// 1-second base so a 2-second move flags.
	p := &Pipeline{Oracle: o, Store: store, Opts: FoldOptions{TimeControl: clockTC(1_000, 0)}}

	out, err := p.SubmitMove(ctx, MoveIntent{GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: "startpos", ElapsedMs: 2_000})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !out.Flagged || out.Accepted || out.Rejected {
		t.Fatalf("outcome = %+v, want Flagged only", out)
	}
	if out.FlaggedSeat != 0 {
		t.Errorf("FlaggedSeat = %d, want 0", out.FlaggedSeat)
	}
	evs, _ := store.Load(ctx, streamID)
	for _, e := range evs {
		if e.Type == EvMovePlayed {
			t.Fatal("a flagged move must not append a MovePlayed")
		}
	}
}

// TestPipelineMovePathIsConstantOracleCost is the regression guard for the
// O(ply^2) hot-path finding: the authoritative move path must cost exactly ONE
// engine round-trip per move, independent of how many moves precede it (it does
// NOT replay the stream through the oracle to rebuild the position). We play a
// sequence of moves and assert each SubmitMove makes exactly one Legality call.
func TestPipelineMovePathIsConstantOracleCost(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	gameID, _ := seedLiveGame(t, store, false)
	o := newFakeOracle()
	// A short alternating sequence; the FEN is opaque to the stub, we only care
	// that each accepted move advances the position the live-session threads.
	ucis := []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1b5", "a7a6"}
	for _, u := range ucis {
		o.allow(u, "fen-"+u, nil)
	}
	p := &Pipeline{Oracle: o, Store: store, Opts: tc53opts()}

	curFEN := "startpos"
	for i, u := range ucis {
		before := o.calls
		out, err := p.SubmitMove(ctx, MoveIntent{
			GameID:      gameID,
			SeatIdx:     uint8(i % 2),
			PlyExpected: uint32(i + 1),
			UCI:         u,
			CurFEN:      curFEN,
			ElapsedMs:   1_000,
		})
		if err != nil {
			t.Fatalf("move %d (%s): %v", i+1, u, err)
		}
		if !out.Accepted {
			t.Fatalf("move %d (%s): outcome = %+v, want accepted", i+1, u, out)
		}
		if got := o.calls - before; got != 1 {
			t.Fatalf("move %d (%s): %d oracle calls, want exactly 1 (no O(ply) replay)", i+1, u, got)
		}
		// The live-session advances its cached position from the verdict.
		curFEN = out.NewFEN
	}
	// Total engine round-trips must be linear in move count (len(ucis)), not
	// quadratic (sum 1..N). With the old FoldWithEngine path this would be
	// 1+2+...+6 = 21; the correct path is exactly 6.
	if o.calls != len(ucis) {
		t.Errorf("total oracle calls = %d, want %d (linear, not quadratic)", o.calls, len(ucis))
	}
}
