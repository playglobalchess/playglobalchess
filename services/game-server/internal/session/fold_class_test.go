package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// ctxErrOracle fails PositionKey with a wrapped context error, as a timed-out
// engine call does.
type ctxErrOracle struct{ game.LegalityOracle }

func (ctxErrOracle) PositionKey(context.Context, string) (uint64, error) {
	return 0, fmt.Errorf("engine: %w", context.DeadlineExceeded)
}

// A rebuild cut short by its context is not ErrFold, so no caller treats the
// stream as broken for good (PR #234 review round 4, B1). A real fold error is.
func TestFoldErrorClassification(t *testing.T) {
	ctx := context.Background()
	store := game.NewMemStore()
	sid := game.StreamIDFor(gameID)
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{PoolID: "p",
			Seats: []game.Seat{{SeatIdx: 0}, {SeatIdx: 1, Color: 1}}, StartFEN: startFEN, TimeControl: "blitz-5+3"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
		{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: 1, UCI: "e2e5"}}, // illegal
	} {
		if err := store.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatal(err)
		}
	}
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	_, err = NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: ctxErrOracle{o}, Opts: opts})
	if err == nil || errors.Is(err, ErrFold) {
		t.Fatalf("timed-out fold: err %v, want a non-ErrFold error", err)
	}
	_, err = NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts})
	if !errors.Is(err, ErrFold) {
		t.Fatalf("illegal persisted move: err %v, want ErrFold", err)
	}
}
