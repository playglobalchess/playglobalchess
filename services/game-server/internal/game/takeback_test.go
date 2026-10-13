package game

import (
	"context"
	"strings"
	"testing"
)

// TestRefoldTakeback proves a takeback rolls the projection back to ply-n,
// including the clock (which rewinds automatically because it is a projection
// of the folded moves' stored ClockAfterMs).
func TestRefoldTakeback(t *testing.T) {
	// unrated game, 3 moves played with stored clock_after_ms:
	// seat0: 293000 (move 1), 285000 (move 3); seat1: 291000 (move 2).
	events := append(liveStream(false),
		evMoveClk(1, 0, "e2e4", 293_000),
		evMoveClk(2, 1, "e7e5", 291_000),
		evMoveClk(3, 0, "g1f3", 285_000),
	)
	opts := baseOpts()

	full, err := Fold(events, opts)
	if err != nil {
		t.Fatalf("full fold: %v", err)
	}
	if full.Ply != 3 {
		t.Fatalf("precondition ply = %d, want 3", full.Ply)
	}
	if full.Clock.RemainingMs[0] != 285_000 {
		t.Fatalf("precondition seat0 clock = %d, want 285000", full.Clock.RemainingMs[0])
	}

	// Take back 1 ply (undo move 3): projection at ply 2, seat0 clock reverts to
	// its move-1 value 293000, turn back to seat0.
	back1, err := RefoldTakeback(events, opts, 1)
	if err != nil {
		t.Fatalf("takeback 1: %v", err)
	}
	if back1.Ply != 2 {
		t.Errorf("after undo-1 ply = %d, want 2", back1.Ply)
	}
	if back1.TurnIdx != 0 {
		t.Errorf("after undo-1 turn = %d, want 0", back1.TurnIdx)
	}
	if back1.Clock.RemainingMs[0] != 293_000 {
		t.Errorf("after undo-1 seat0 clock = %d, want 293000", back1.Clock.RemainingMs[0])
	}

	// Take back 2 plies (undo moves 2 and 3): back to ply 1, seat1 clock reverts
	// to full base (never moved after undo), seat0 reflects its move 1.
	back2, err := RefoldTakeback(events, opts, 2)
	if err != nil {
		t.Fatalf("takeback 2: %v", err)
	}
	if back2.Ply != 1 {
		t.Errorf("after undo-2 ply = %d, want 1", back2.Ply)
	}
	if back2.Clock.RemainingMs[1] != 300_000 {
		t.Errorf("after undo-2 seat1 clock = %d, want 300000 (full base)", back2.Clock.RemainingMs[1])
	}
	if back2.Clock.RemainingMs[0] != 293_000 {
		t.Errorf("after undo-2 seat0 clock = %d, want 293000", back2.Clock.RemainingMs[0])
	}
}

func TestRefoldTakebackErrors(t *testing.T) {
	live := append(liveStream(false), evMove(1, 0, "e2e4"))
	cases := []struct {
		name   string
		events []Event
		plies  uint32
		errSub string
	}{
		{"rated rejected", append(liveStream(true), evMove(1, 0, "e2e4")), 1, "rated"},
		{"zero plies", live, 0, ">= 1"},
		{"too many plies", live, 5, "cannot undo"},
		{"not live", append(liveStream(false), evSeat(EvResigned, 0), evEnded(TermResign, []Result{ResultLoss, ResultWin})), 1, "not Live"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := RefoldTakeback(c.events, baseOpts(), c.plies)
			if err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Fatalf("want error containing %q, got %v", c.errSub, err)
			}
		})
	}
}

// TestFoldWithEngineProjectsFEN proves CurFEN advances via the oracle, while the
// pure Fold leaves it at StartFEN.
func TestFoldWithEngineProjectsFEN(t *testing.T) {
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	o.allow("e7e5", "fen-after-e5", nil)

	events := append(liveStream(false), evMove(1, 0, "e2e4"), evMove(2, 1, "e7e5"))

	pure, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatalf("pure fold: %v", err)
	}
	if pure.CurFEN != "startpos" {
		t.Errorf("pure fold CurFEN = %q, want startpos (no engine)", pure.CurFEN)
	}

	eng, err := FoldWithEngine(context.Background(), events, baseOpts(), o)
	if err != nil {
		t.Fatalf("engine fold: %v", err)
	}
	if eng.CurFEN != "fen-after-e5" {
		t.Errorf("engine fold CurFEN = %q, want fen-after-e5", eng.CurFEN)
	}
	// Non-FEN fields must agree between the two folds.
	if eng.Ply != pure.Ply || eng.TurnIdx != pure.TurnIdx || eng.Phase != pure.Phase {
		t.Errorf("engine/pure folds disagree on non-FEN fields")
	}
}

// TestFoldWithEngineCorruptStream: a persisted move the engine rejects is a
// corrupt stream and must error, not silently diverge.
func TestFoldWithEngineCorruptStream(t *testing.T) {
	o := newFakeOracle() // nothing allowed -> every move illegal
	events := append(liveStream(false), evMove(1, 0, "e2e4"))
	if _, err := FoldWithEngine(context.Background(), events, baseOpts(), o); err == nil {
		t.Error("corrupt stream (engine rejects persisted move): expected error")
	}
}

// TestFlagTermination proves the FLAG vs TIMEOUT_VS_INSUFFICIENT decision is
// delegated to the engine's material judgement (NFR-5).
func TestFlagTermination(t *testing.T) {
	ctx := context.Background()

	// Opponent (seat0) HAS sufficient material -> plain FLAG.
	o1 := newFakeOracle()
	o1.material["pos#0"] = true
	term, err := FlagTermination(ctx, o1, "pos", 1 /*flagged*/, 2)
	if err != nil {
		t.Fatalf("flag term: %v", err)
	}
	if term != TermFlag {
		t.Errorf("sufficient-material flag term = %d, want FLAG", term)
	}

	// Opponent (seat0) LACKS sufficient material -> TIMEOUT_VS_INSUFFICIENT.
	o2 := newFakeOracle() // material defaults to false
	term, err = FlagTermination(ctx, o2, "pos", 1, 2)
	if err != nil {
		t.Fatalf("flag term insufficient: %v", err)
	}
	if term != TermTimeoutVsInsufficient {
		t.Errorf("insufficient-material flag term = %d, want TIMEOUT_VS_INSUFFICIENT", term)
	}

	// N-seat is guarded (v1 is 2-seat).
	if _, err := FlagTermination(ctx, o1, "pos", 0, 3); err == nil {
		t.Error("3-seat flag term: expected guard error")
	}
}
