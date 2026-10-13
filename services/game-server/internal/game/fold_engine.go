package game

import (
	"context"
	"fmt"
)

// FoldWithEngine is Fold plus live-board (CurFEN) projection. The pure Fold
// cannot derive a chess position from a UCI move (that is chess logic, owned by
// the engine — NFR-5), so it leaves CurFEN at StartFEN. FoldWithEngine replays
// each MovePlayed's UCI through the LegalityOracle to advance CurFEN, which is
// what §3.5 reconnection resync (STATE{fen,...}) and §3.6 legality validation
// need. Everything else (phase/ply/turn/clock/offers/terminal) is identical to
// Fold — the two agree on every non-FEN field by construction.
//
// Determinism (NFR / §2 acceptance): given the same events, options, and a
// deterministic oracle (the real engine is deterministic — G-A), the projected
// State including CurFEN is identical every call.
func FoldWithEngine(ctx context.Context, events []Event, opts FoldOptions, oracle LegalityOracle) (State, error) {
	if oracle == nil {
		return State{}, fmt.Errorf("foldWithEngine: nil oracle")
	}
	// First run the pure fold so all transition validation + phase/ply/turn/
	// clock projection is shared with Fold (one state machine, no divergence).
	st, err := Fold(events, opts)
	if err != nil {
		return State{}, err
	}
	// Then project CurFEN by replaying the moves through the engine.
	fen := st.StartFEN
	for i := range events {
		if events[i].Type != EvMovePlayed || events[i].MovePlayed == nil {
			continue
		}
		v, err := oracle.Legality(ctx, fen, events[i].MovePlayed.UCI)
		if err != nil {
			return State{}, fmt.Errorf("foldWithEngine: replay ply %d: %w", events[i].MovePlayed.Ply, err)
		}
		if !v.Legal {
			// A persisted move that the engine now rejects is a corrupt stream —
			// the move was legal when appended (the pipeline validated it). Refuse
			// to project rather than silently diverge.
			return State{}, fmt.Errorf("foldWithEngine: persisted move %q at ply %d is illegal from %q", events[i].MovePlayed.UCI, events[i].MovePlayed.Ply, fen)
		}
		fen = v.NewFEN
	}
	st.CurFEN = fen
	return st, nil
}

// FlagTermination decides whether a flagged seat ends the game as FLAG or
// TIMEOUT_VS_INSUFFICIENT (SPEC-S2-GAME-SERVER §3.3). The decision hinges on
// whether the OPPONENT of the flagged seat has sufficient mating material — a
// chess-material judgement, so it is answered by the engine, never computed in
// Go (NFR-5). For v1 2-seat, the opponent is the other seat.
//
// It does not append any event; it returns the Termination the caller puts on
// the GameEnded it appends after the ClockFlagged.
func FlagTermination(ctx context.Context, oracle LegalityOracle, curFEN string, flaggedSeat uint8, seatCount int) (Termination, error) {
	if oracle == nil {
		return 0, fmt.Errorf("flagTermination: nil oracle")
	}
	if seatCount != 2 {
		// v1 is 2-seat (SPEC-S2-PROTOCOL §1). Guard rather than silently
		// mis-handle N-seat, which is a later chunk.
		return 0, fmt.Errorf("flagTermination: v1 supports 2 seats, got %d", seatCount)
	}
	opponent := uint8((int(flaggedSeat) + 1) % seatCount)
	sufficient, err := oracle.HasSufficientMaterial(ctx, curFEN, opponent)
	if err != nil {
		return 0, fmt.Errorf("flagTermination: material check: %w", err)
	}
	if sufficient {
		return TermFlag, nil
	}
	return TermTimeoutVsInsufficient, nil
}
