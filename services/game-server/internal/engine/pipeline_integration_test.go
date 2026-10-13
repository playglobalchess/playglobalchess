package engine

import (
	"context"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// This file is the §8 "pipeline integration (unit, no network)" slice: it drives
// the GS-1 move pipeline with the REAL engine (CgoOracle) instead of the GS-1
// test double, proving the two chunks compose — a legal move is accepted with
// the engine's resulting position, and the engine's terminal verdict flows out.
// No network, no real store (the GS-1 in-memory stub), no real transport.

// streamIDFor mirrors the game package's internal convention ("game/{id}");
// SubmitMove derives the same from MoveIntent.GameID, so the seed must match.
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
	for _, e := range evs {
		if _, err := store.Append(ctx, sid, e); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
}

func tc53() clock.TimeControl { return clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000} }

// TestPipelineWithRealEngine: a legal first move is accepted against the real
// engine and the server advances its position from the returned NewFEN.
func TestPipelineWithRealEngine(t *testing.T) {
	store := game.NewMemStore()
	gameID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	seedLive(t, store, gameID, startFEN)

	p := &game.Pipeline{Oracle: newOracle(t), Store: store, Opts: game.FoldOptions{TimeControl: tc53()}}

	out, err := p.SubmitMove(context.Background(), game.MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e4", CurFEN: startFEN, ElapsedMs: 1_000,
	})
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Accepted || out.Ply != 1 {
		t.Fatalf("outcome = %+v, want accepted ply 1", out)
	}
	if out.NewFEN == "" || out.NewFEN == startFEN {
		t.Errorf("NewFEN = %q, want the post-e4 position from the engine", out.NewFEN)
	}
	if out.Terminal != nil {
		t.Errorf("1.e4 terminal = %v, want nil", *out.Terminal)
	}
}

// TestPipelineRealEngineRejectsIllegal: an illegal move is MOVE_REJECTED{ILLEGAL}
// by the real engine, appending nothing.
func TestPipelineRealEngineRejectsIllegal(t *testing.T) {
	store := game.NewMemStore()
	gameID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	seedLive(t, store, gameID, startFEN)

	p := &game.Pipeline{Oracle: newOracle(t), Store: store, Opts: game.FoldOptions{TimeControl: tc53()}}
	out, err := p.SubmitMove(context.Background(), game.MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "e2e5", CurFEN: startFEN, ElapsedMs: 1_000,
	})
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Rejected || out.Reason != game.RejectIllegal {
		t.Fatalf("outcome = %+v, want MOVE_REJECTED{ILLEGAL}", out)
	}
	// Nothing appended beyond the 3 seed events.
	evs, _ := store.Load(context.Background(), streamIDFor(gameID))
	if len(evs) != 3 {
		t.Errorf("illegal move appended an event: %d events", len(evs))
	}
}

// TestPipelineRealEngineMate: a real Scholar's-mate finishing move is accepted
// AND carries the engine's MATE terminal out to the caller.
func TestPipelineRealEngineMate(t *testing.T) {
	store := game.NewMemStore()
	gameID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	// Position after 1.e4 e5 2.Bc4 Nc6 3.Qf3 (white to move Qxf7#); seed a game
	// whose StartFEN is this (GS-1 folds from StartFEN, and the engine validates
	// against CurFEN, so a custom start is fine for this unit).
	preMate := "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/5Q2/PPPP1PPP/RNB1K1NR w KQkq - 2 3"
	seedLive(t, store, gameID, preMate)

	p := &game.Pipeline{Oracle: newOracle(t), Store: store, Opts: game.FoldOptions{TimeControl: tc53()}}
	out, err := p.SubmitMove(context.Background(), game.MoveIntent{
		GameID: gameID, SeatIdx: 0, PlyExpected: 1, UCI: "f3f7", CurFEN: preMate, ElapsedMs: 1_000,
	})
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Accepted {
		t.Fatalf("outcome = %+v, want accepted", out)
	}
	if out.Terminal == nil || *out.Terminal != game.TermMate {
		t.Fatalf("terminal = %v, want MATE", out.Terminal)
	}
}
