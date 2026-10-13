package game

import (
	"strings"
	"testing"
)

// TestLifecycleHappyPath walks SEEKING-equivalent create -> MATCHED -> LIVE ->
// ENDED and asserts the phase projection at each step.
func TestLifecycleHappyPath(t *testing.T) {
	steps := []struct {
		name  string
		event Event
		phase Phase
	}{
		{"created", evCreated(false, "blitz-5+3", "startpos"), PhaseMatched},
		{"seat0 joined", evJoined(0), PhaseMatched},
		{"seat1 joined -> live", evJoined(1), PhaseLive},
		{"move 1 (white)", evMove(1, 0, "e2e4"), PhaseLive},
		{"move 2 (black)", evMove(2, 1, "e7e5"), PhaseLive},
		{"resign intent", evSeat(EvResigned, 1), PhaseLive},
		{"ended RESIGN", evEnded(TermResign, []Result{ResultWin, ResultLoss}), PhaseEnded},
	}
	var events []Event
	for _, s := range steps {
		events = append(events, s.event)
		st, err := Fold(events, baseOpts())
		if err != nil {
			t.Fatalf("%s: fold err: %v", s.name, err)
		}
		if st.Phase != s.phase {
			t.Errorf("%s: phase = %s, want %s", s.name, st.Phase, s.phase)
		}
	}
}

// TestFoldDeterministic proves the §2 acceptance "same events -> same state".
func TestFoldDeterministic(t *testing.T) {
	// Stored clock_after_ms per move: seat0 293000 then 288000; seat1 291000.
	events := append(liveStream(false),
		evMoveClk(1, 0, "e2e4", 293_000),
		evMoveClk(2, 1, "e7e5", 291_000),
		evMoveClk(3, 0, "g1f3", 288_000),
	)
	opts := baseOpts()
	a, err := Fold(events, opts)
	if err != nil {
		t.Fatalf("fold a: %v", err)
	}
	b, err := Fold(events, opts)
	if err != nil {
		t.Fatalf("fold b: %v", err)
	}
	if a.Phase != b.Phase || a.Ply != b.Ply || a.TurnIdx != b.TurnIdx {
		t.Fatalf("non-deterministic phase/ply/turn: %+v vs %+v", a, b)
	}
	for i := range a.Clock.RemainingMs {
		if a.Clock.RemainingMs[i] != b.Clock.RemainingMs[i] {
			t.Fatalf("non-deterministic clock seat %d: %d vs %d", i, a.Clock.RemainingMs[i], b.Clock.RemainingMs[i])
		}
	}
	// The clock projection reads the stored ClockAfterMs: seat0 ends at 288000
	// (its last move), seat1 at 291000.
	if a.Clock.RemainingMs[0] != 288_000 {
		t.Errorf("seat0 clock = %d, want 288000", a.Clock.RemainingMs[0])
	}
	if a.Clock.RemainingMs[1] != 291_000 {
		t.Errorf("seat1 clock = %d, want 291000", a.Clock.RemainingMs[1])
	}
	if a.Ply != 3 || a.TurnIdx != 1 {
		t.Errorf("ply/turn = %d/%d, want 3/1", a.Ply, a.TurnIdx)
	}
}

// TestIllegalTransitions covers every rejected transition in §3.2: the fold
// must refuse an impossible stream rather than project nonsense.
func TestIllegalTransitions(t *testing.T) {
	cases := []struct {
		name   string
		events []Event
		errSub string
	}{
		{"GameCreated not first", []Event{evJoined(0)}, "must be Matched"},
		{"second GameCreated", []Event{evCreated(false, "x", "s"), evCreated(false, "y", "t")}, "must be first"},
		{"move before live", []Event{evCreated(false, "x", "s"), evMove(1, 0, "e2e4")}, "must be Live"},
		{"join twice same seat", []Event{evCreated(false, "x", "s"), evJoined(0), evJoined(0)}, "already joined"},
		{"join out of range", []Event{evCreated(false, "x", "s"), evJoined(5)}, "out of range"},
		{"move out of turn", append(liveStream(false), evMove(1, 1, "e7e5")), "out of turn"},
		{"non-sequential ply", append(liveStream(false), evMove(2, 0, "e2e4")), "not sequential"},
		{"draw accept with no offer", append(liveStream(false), evSeat(EvDrawAccepted, 1)), "no open draw offer"},
		{"draw accept own offer", append(liveStream(false), evSeat(EvDrawOffered, 0), evSeat(EvDrawAccepted, 0)), "cannot accept its own"},
		{"takeback in rated", append(liveStream(true), evMove(1, 0, "e2e4"), evSeat(EvTakebackReq, 1)), "rated"},
		{"takeback with no moves", append(liveStream(false), evSeat(EvTakebackReq, 0)), "no move to take back"},
		{"takeback accept no request", append(liveStream(false), evMove(1, 0, "e2e4"), evSeat(EvTakebackAccepted, 1)), "no open takeback"},
		{"rating before ended", append(liveStream(false), evRating()), "must be Ended"},
		{"game end wrong result len", append(liveStream(false), evEnded(TermMate, []Result{ResultWin})), "result_vector len"},
		{"game end unknown term", append(liveStream(false), evEnded(Termination(0xEE), []Result{ResultWin, ResultLoss})), "unknown termination"},
		{"clock flag before live", []Event{evCreated(false, "x", "s"), evSeat(EvClockFlagged, 0)}, "must be Live"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Fold(c.events, baseOpts())
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.errSub)
			}
			if !strings.Contains(err.Error(), c.errSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.errSub)
			}
		})
	}
}

// TestLegalInGameActions covers the accepted in-game action transitions.
func TestLegalInGameActions(t *testing.T) {
	// Draw offered then accepted (clears offer), then GameEnded DRAW_AGREE.
	draw := append(liveStream(false),
		evMove(1, 0, "e2e4"),
		evSeat(EvDrawOffered, 0),
		evSeat(EvDrawAccepted, 1),
		evEnded(TermDrawAgree, []Result{ResultDraw, ResultDraw}),
	)
	st, err := Fold(draw, baseOpts())
	if err != nil {
		t.Fatalf("draw agree fold: %v", err)
	}
	if st.Phase != PhaseEnded || st.Termination != TermDrawAgree {
		t.Errorf("phase/term = %s/%d, want Ended/DRAW_AGREE", st.Phase, st.Termination)
	}

	// A move declines a standing draw offer.
	declined := append(liveStream(false),
		evSeat(EvDrawOffered, 0),
		evMove(1, 0, "e2e4"),
	)
	st, err = Fold(declined, baseOpts())
	if err != nil {
		t.Fatalf("decline fold: %v", err)
	}
	if st.DrawOfferBy != -1 {
		t.Errorf("DrawOfferBy = %d, want -1 (move should clear offer)", st.DrawOfferBy)
	}

	// Abort from MATCHED is allowed.
	abortMatched := []Event{evCreated(false, "x", "s"), evJoined(0), evAborted(0), evEnded(TermAbort, []Result{ResultDraw, ResultDraw})}
	st, err = Fold(abortMatched, baseOpts())
	if err != nil {
		t.Fatalf("abort-from-matched fold: %v", err)
	}
	if st.Phase != PhaseEnded || st.Termination != TermAbort {
		t.Errorf("abort: phase/term = %s/%d, want Ended/ABORT", st.Phase, st.Termination)
	}

	// System abort (by=0xFF).
	sysAbort := []Event{evCreated(false, "x", "s"), evJoined(0), evAborted(SystemActor)}
	if _, err := Fold(sysAbort, baseOpts()); err != nil {
		t.Fatalf("system abort fold: %v", err)
	}
}

// TestRatingAppliedTerminal covers rating fold + idempotent compensating event.
func TestRatingAppliedTerminal(t *testing.T) {
	events := append(liveStream(true),
		evMove(1, 0, "e2e4"),
		evSeat(EvResigned, 1),
		evEnded(TermResign, []Result{ResultWin, ResultLoss}),
		evRating(),
	)
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatalf("rating fold: %v", err)
	}
	if !st.RatingApplied {
		t.Error("RatingApplied flag not set")
	}
	// A compensating second RatingApplied is tolerated (immutability/correction).
	st2, err := Fold(append(events, evRating()), baseOpts())
	if err != nil {
		t.Fatalf("compensating rating fold: %v", err)
	}
	if !st2.RatingApplied {
		t.Error("RatingApplied flag lost after compensating event")
	}
	// RatingApplied on an unrated game is rejected.
	unrated := append(liveStream(false),
		evSeat(EvResigned, 1),
		evEnded(TermResign, []Result{ResultWin, ResultLoss}),
		evRating(),
	)
	if _, err := Fold(unrated, baseOpts()); err == nil {
		t.Error("rating on unrated game: expected error")
	}
}

// TestClockFlaggedPath covers the ClockFlagged -> GameEnded{FLAG} transition.
func TestClockFlaggedPath(t *testing.T) {
	events := append(liveStream(false),
		evMove(1, 0, "e2e4"),
		evSeat(EvClockFlagged, 1),
		evEnded(TermFlag, []Result{ResultWin, ResultLoss}),
	)
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatalf("flag path fold: %v", err)
	}
	if st.Phase != PhaseEnded || st.Termination != TermFlag {
		t.Errorf("phase/term = %s/%d, want Ended/FLAG", st.Phase, st.Termination)
	}
}

// TestMoveAfterEndRejected: no event may follow GameEnded except RatingApplied.
func TestMoveAfterEndRejected(t *testing.T) {
	ended := append(liveStream(false),
		evSeat(EvResigned, 0),
		evEnded(TermResign, []Result{ResultLoss, ResultWin}),
	)
	if _, err := Fold(append(ended, evMove(1, 1, "e2e4")), baseOpts()); err == nil {
		t.Error("move after GameEnded: expected error")
	}
}
