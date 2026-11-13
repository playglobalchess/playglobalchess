package engine

import (
	"context"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// This file is the §8 "pipeline integration (unit, no network)" slice: it drives
// game.Decide + game.Evolve (GS-7, replacing the deleted game.Pipeline) with the
// REAL engine (CgoOracle) instead of the GS-1 test double, proving the two
// chunks compose — a legal move is accepted with the engine's resulting
// position, and the engine's terminal verdict flows out. No network, no real
// store (the GS-1 in-memory stub), no real transport.

// streamIDFor mirrors the game package's internal convention ("game/{id}").
func streamIDFor(gameID string) string { return "game/" + gameID }

// seedLive appends GameCreated + both PlayerJoined so the game is LIVE.
func seedLive(t *testing.T, store *game.MemStore, gameID, startFEN string) {
	t.Helper()
	ctx := context.Background()
	sid := streamIDFor(gameID)
	evs := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID:      "blitz-standard",
			Rated:       false,
			Seats:       []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN:    startFEN,
			TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	}
	for i, e := range evs {
		if err := store.Append(ctx, sid, uint32(i), e); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
}

func tc53() clock.TimeControl { return clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000} }

const itGameID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

// liveState seeds a LIVE game from startpos, folds it with the real engine and
// starts its clock (what a session does on first build), returning the state,
// the store and the options.
func liveState(t *testing.T, startpos string) (game.State, *game.MemStore, game.FoldOptions) {
	t.Helper()
	ctx := context.Background()
	store := game.NewMemStore()
	seedLive(t, store, itGameID, startpos)
	opts := game.FoldOptions{TimeControl: tc53()}
	o := newOracle(t)
	evs, err := store.Load(ctx, streamIDFor(itGameID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st, err := game.FoldWithEngine(ctx, evs, opts, o)
	if err != nil {
		t.Fatalf("FoldWithEngine: %v", err)
	}
	st = apply(t, store, st, game.CmdStartClock{}, 0, opts)
	return st, store, opts
}

// apply runs Decide, appends the batch at NextSeq (as the session does) and
// Evolves each event with its verdict. It fails the test on a Decide error.
func apply(t *testing.T, store *game.MemStore, st game.State, cmd game.Command, nowMs uint64, opts game.FoldOptions) game.State {
	t.Helper()
	st, _ = decideApply(t, store, st, cmd, nowMs, opts)
	return st
}

func decideApply(t *testing.T, store *game.MemStore, st game.State, cmd game.Command, nowMs uint64, opts game.FoldOptions) (game.State, game.Decision) {
	t.Helper()
	ctx := context.Background()
	d, err := game.Decide(ctx, st, cmd, nowMs, opts, newOracle(t))
	if err != nil {
		t.Fatalf("Decide(%T): %v", cmd, err)
	}
	if len(d.Events) > 0 {
		if err := store.AppendBatch(ctx, streamIDFor(itGameID), st.NextSeq, d.Events); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	for i, ev := range d.Events {
		st, err = game.Evolve(st, ev, d.Verdicts[i], opts)
		if err != nil {
			t.Fatalf("Evolve %s: %v", ev.Type, err)
		}
	}
	return st, d
}

// TestPipelineWithRealEngine: a legal first move is accepted against the real
// engine and the evolved state advances to the post-e4 position.
func TestPipelineWithRealEngine(t *testing.T) {
	st, store, opts := liveState(t, startFEN)
	st, d := decideApply(t, store, st, game.CmdMove{Seat: 0, PlyExpected: 1, UCI: "e2e4"}, 1_000, opts)
	out := d.Move
	if !out.Accepted || out.Ply != 1 || out.Reacked {
		t.Fatalf("outcome = %+v, want a new accepted ply 1", out)
	}
	if out.NewFEN == "" || out.NewFEN == startFEN {
		t.Errorf("NewFEN = %q, want the post-e4 position from the engine", out.NewFEN)
	}
	if out.Terminal != nil {
		t.Errorf("1.e4 terminal = %v, want nil", *out.Terminal)
	}
	if st.CurFEN != out.NewFEN || st.Ply != 1 || st.LastMoveUCI() != "e2e4" {
		t.Errorf("evolved state = {%q %d %q}, want {%q 1 e2e4}", st.CurFEN, st.Ply, st.LastMoveUCI(), out.NewFEN)
	}
	// The folded store agrees with the evolved state (live == Fold(store)).
	evs, _ := store.Load(context.Background(), streamIDFor(itGameID))
	folded, err := game.FoldWithEngine(context.Background(), evs, opts, newOracle(t))
	if err != nil || folded.CurFEN != st.CurFEN || folded.Ply != st.Ply {
		t.Errorf("FoldWithEngine(store) = {%q %d} %v, want {%q %d}", folded.CurFEN, folded.Ply, err, st.CurFEN, st.Ply)
	}
	// A resend of the same move is an idempotent re-ack: nothing appended.
	n := len(evs)
	_, d2 := decideApply(t, store, st, game.CmdMove{Seat: 0, PlyExpected: 1, UCI: "e2e4"}, 2_000, opts)
	if !d2.Move.Accepted || !d2.Move.Reacked {
		t.Errorf("resend = %+v, want Accepted+Reacked", d2.Move)
	}
	if after, _ := store.Load(context.Background(), streamIDFor(itGameID)); len(after) != n {
		t.Errorf("re-ack appended events: %d -> %d", n, len(after))
	}
}

// TestPipelineRealEngineRejectsIllegal: an illegal move is MOVE_REJECTED{ILLEGAL}
// by the real engine, appending nothing.
func TestPipelineRealEngineRejectsIllegal(t *testing.T) {
	st, store, opts := liveState(t, startFEN)
	before, _ := store.Load(context.Background(), streamIDFor(itGameID))
	st2, d := decideApply(t, store, st, game.CmdMove{Seat: 0, PlyExpected: 1, UCI: "e2e5"}, 1_000, opts)
	if !d.Move.Rejected || d.Move.Reason != game.RejectIllegal {
		t.Fatalf("outcome = %+v, want MOVE_REJECTED{ILLEGAL}", d.Move)
	}
	after, _ := store.Load(context.Background(), streamIDFor(itGameID))
	if len(after) != len(before) {
		t.Errorf("illegal move appended an event: %d -> %d events", len(before), len(after))
	}
	if st2.Ply != 0 || st2.CurFEN != startFEN {
		t.Errorf("state advanced on an illegal move: ply %d fen %q", st2.Ply, st2.CurFEN)
	}
}

// TestPipelineRealEngineMate: a real mating move is accepted AND carries the
// engine's MATE terminal out; MovePlayed and GameEnded land as one batch and
// the evolved state is ended.
func TestPipelineRealEngineMate(t *testing.T) {
	// Position after 1.e4 e5 2.Bc4 Nc6 3.Qf3 (white to move Qxf7#); a custom
	// StartFEN is fine (GS-1 folds from StartFEN, the engine validates CurFEN).
	preMate := "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/5Q2/PPPP1PPP/RNB1K1NR w KQkq - 2 3"
	st, store, opts := liveState(t, preMate)
	st, d := decideApply(t, store, st, game.CmdMove{Seat: 0, PlyExpected: 1, UCI: "f3f7"}, 1_000, opts)
	if !d.Move.Accepted {
		t.Fatalf("outcome = %+v, want accepted", d.Move)
	}
	if d.Move.Terminal == nil || *d.Move.Terminal != game.TermMate || !d.Move.EndedGame {
		t.Fatalf("terminal/ended = %v/%v, want MATE/true", d.Move.Terminal, d.Move.EndedGame)
	}
	if len(d.Events) != 2 || d.Events[0].Type != game.EvMovePlayed || d.Events[1].Type != game.EvGameEnded {
		t.Fatalf("batch = %+v, want [MovePlayed GameEnded]", d.Events)
	}
	if st.Phase != game.PhaseEnded || st.Termination != game.TermMate {
		t.Fatalf("evolved state phase/term = %v/%v, want Ended/MATE", st.Phase, st.Termination)
	}
	if rv := st.ResultVector; len(rv) != 2 || rv[0] != game.ResultWin || rv[1] != game.ResultLoss {
		t.Fatalf("result vector = %v, want [win loss]", rv)
	}
}

// TestOldStreamPastNewFivefoldStillFolds (#100): a stream written before #100
// can continue past the ply where the start position now reaches fivefold
// (ply 16 of the knight shuffle), because the old count did not see it. The
// rebuild must still fold it: the ply-16 verdict is recorded on that History
// entry only, and the game stays LIVE at ply 17 with no terminal on the last
// move, so the recovery path does not end it.
func TestOldStreamPastNewFivefoldStillFolds(t *testing.T) {
	ctx := context.Background()
	st, store, opts := liveState(t, "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1")
	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	// Plies 1-16 shuffle back to the start (its 5th occurrence is ply 16); ply
	// 17 leaves the cycle, which the old count allowed.
	uci := func(ply uint32) string {
		if ply == 17 {
			return "e2e4"
		}
		return cycle[(ply-1)%4]
	}
	var evs []game.Event
	for ply := uint32(1); ply <= 17; ply++ {
		evs = append(evs, game.Event{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{
			Ply: ply, SeatIdx: uint8((ply - 1) % 2), UCI: uci(ply),
			ClockAfterMs: 300_000, ServerTS: uint64(ply) * 1000,
		}})
	}
	if err := store.AppendBatch(ctx, streamIDFor(itGameID), st.NextSeq, evs); err != nil {
		t.Fatalf("append: %v", err)
	}
	all, err := store.Load(ctx, streamIDFor(itGameID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err := game.FoldWithEngine(ctx, all, opts, newOracle(t))
	if err != nil {
		t.Fatalf("FoldWithEngine on an old stream: %v", err)
	}
	if got.Phase != game.PhaseLive || got.Ply != 17 || got.LastMoveTerminal() != nil {
		t.Fatalf("phase %s ply %d last terminal %v, want LIVE at ply 17 with none", got.Phase, got.Ply, got.LastMoveTerminal())
	}
	if term := got.History[15].Terminal; term == nil || *term != game.TermRepetition {
		t.Fatalf("ply 16 verdict = %v, want REPETITION recorded on the entry", term)
	}
}
