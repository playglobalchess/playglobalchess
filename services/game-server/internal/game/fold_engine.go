package game

import (
	"context"
	"fmt"
)

// FoldWithEngine is Fold with an engine verdict for each MovePlayed that is
// LIVE when it is applied (§3.2): the move is checked from the current
// history-tip position, so a move later undone by a TakebackRewound is
// replayed from the right position and then truncated. This is the rebuild
// path (session construction, seq-conflict reload); the move path makes one
// engine call per move in Decide.
func FoldWithEngine(ctx context.Context, events []Event, opts FoldOptions, oracle LegalityOracle) (State, error) {
	if oracle == nil {
		return State{}, fmt.Errorf("foldWithEngine: nil oracle")
	}
	s := initialState()
	for i := range events {
		var v *Verdict
		if events[i].Type == EvMovePlayed && events[i].MovePlayed != nil && s.Phase == PhaseLive {
			mv := events[i].MovePlayed
			lv, err := oracle.Legality(ctx, s.CurFEN, mv.UCI, s.PriorKeys())
			if err != nil {
				return State{}, fmt.Errorf("foldWithEngine: replay ply %d: %w", mv.Ply, err)
			}
			if !lv.Legal {
				// A persisted move the engine now rejects is a corrupt stream.
				return State{}, fmt.Errorf("foldWithEngine: persisted move %q at ply %d is illegal from %q", mv.UCI, mv.Ply, s.CurFEN)
			}
			v = &Verdict{FEN: lv.NewFEN, Zobrist: lv.NewZobrist, Terminal: lv.Terminal}
		}
		next, err := Evolve(s, events[i], v, opts)
		if err != nil {
			return State{}, fmt.Errorf("foldWithEngine: event %d (%s): %w", i, events[i].Type, err)
		}
		s = next
	}
	return s, nil
}

// FlagTermination decides FLAG vs TIMEOUT_VS_INSUFFICIENT for a flagged seat:
// whether the OPPONENT can still mate is a material judgement, so the engine
// answers it (NFR-5). v1 is 2-seat.
func FlagTermination(ctx context.Context, oracle LegalityOracle, curFEN string, flaggedSeat uint8, seatCount int) (Termination, error) {
	if oracle == nil {
		return 0, fmt.Errorf("flagTermination: nil oracle")
	}
	if seatCount != 2 {
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
