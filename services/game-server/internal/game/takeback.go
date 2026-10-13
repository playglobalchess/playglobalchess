package game

import (
	"context"
	"fmt"
)

// This file implements the takeback re-fold (SPEC-S2-GAME-SERVER §3.2: a
// takeback "rolls the live projection back by re-folding to ply-n" — the stream
// is append-only, so a takeback is a new event, not a deletion; the ROLLED-BACK
// board/clock projection is produced by re-folding the retained event prefix up
// to the pre-move point). F-01.4: unrated/friendly only (already enforced in the
// TakebackReq/TakebackAccepted transitions).

// RefoldTakeback produces the post-takeback projection for a game whose stream,
// in order, is: the full history `events`, followed conceptually by a
// TakebackReq + TakebackAccepted that undo the last `plies` half-moves. It does
// NOT mutate the stream (append-only); it computes the projection the server
// shows after the takeback by re-folding the prefix with the last `plies`
// MovePlayed events (and anything appended after the earliest undone move)
// dropped.
//
// Concretely: it finds the stream index of the MovePlayed whose Ply ==
// (currentPly - plies + 1) — the first move being taken back — and re-folds only
// the events BEFORE it, then returns that State. Because Fold is deterministic,
// re-folding the same prefix always yields the same rolled-back State, including
// the clock (the clock is a projection of the folded MovePlayed events, so
// un-folding the moves restores the pre-move clock automatically — there is no
// separate clock-rewind rule, which is the clean property §3.3 relies on).
//
// Preconditions (else error): the game is unrated and LIVE, plies >= 1, and
// there are at least `plies` moves to undo.
func RefoldTakeback(events []Event, opts FoldOptions, plies uint32) (State, error) {
	prefix, err := takebackPrefix(events, opts, plies)
	if err != nil {
		return State{}, err
	}
	// Re-fold the retained prefix. Because the per-move clock is projected from
	// each MovePlayed's stored ClockAfterMs, dropping the undone moves rewinds
	// the clock to exactly its pre-move value with no separate clock-rewind
	// rule — the clean property §3.3 relies on.
	return Fold(prefix, opts)
}

// RefoldTakebackWithEngine is RefoldTakeback plus the engine-projected CurFEN +
// Zobrist history (via FoldWithEngine) for the SAME retained prefix. The live
// session (GS-3) uses this so there is exactly ONE cut algorithm and ONE
// projection — the pure RefoldTakeback and this cannot pick different cut points
// (which they would if the caller recomputed the cut itself, e.g. by counting
// plies from the end: after a prior takeback the stream holds undone MovePlayed
// events whose ply numbers recur, so a from-the-end count and this ply-targeted
// cut disagree). Preconditions and cut are identical to RefoldTakeback.
func RefoldTakebackWithEngine(ctx context.Context, events []Event, opts FoldOptions, plies uint32, oracle LegalityOracle) (State, error) {
	prefix, err := takebackPrefix(events, opts, plies)
	if err != nil {
		return State{}, err
	}
	return FoldWithEngine(ctx, prefix, opts, oracle)
}

// takebackPrefix is the single source of truth for the retained-event prefix a
// takeback re-folds: the events BEFORE the first MovePlayed whose Ply ==
// (currentPly - plies + 1). Both the pure and engine-backed re-folds use it, so
// they can never diverge on the cut point. It also enforces the F-01.4
// preconditions.
func takebackPrefix(events []Event, opts FoldOptions, plies uint32) ([]Event, error) {
	cur, err := Fold(events, opts)
	if err != nil {
		return nil, fmt.Errorf("takeback: current fold: %w", err)
	}
	if cur.Rated {
		return nil, fmt.Errorf("takeback: rated game (F-01.4 unrated only)")
	}
	if cur.Phase != PhaseLive {
		return nil, fmt.Errorf("takeback: game not Live (phase %s)", cur.Phase)
	}
	if plies == 0 {
		return nil, fmt.Errorf("takeback: plies must be >= 1")
	}
	if plies > cur.Ply {
		return nil, fmt.Errorf("takeback: cannot undo %d plies, only %d played", plies, cur.Ply)
	}

	targetPly := cur.Ply - plies + 1 // ply of the first move being undone
	cutIdx := -1
	for i := range events {
		if events[i].Type == EvMovePlayed && events[i].MovePlayed != nil && events[i].MovePlayed.Ply == targetPly {
			cutIdx = i
			break
		}
	}
	if cutIdx < 0 {
		return nil, fmt.Errorf("takeback: move with ply %d not found", targetPly)
	}
	return events[:cutIdx], nil
}
