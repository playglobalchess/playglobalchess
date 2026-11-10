package game

import (
	"errors"
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
		{"takeback in rated", append(liveStream(true), evMove(1, 0, "e2e4"), evMove(2, 1, "e7e5"), evSeat(EvTakebackProposed, 1)), "rated"},
		{"takeback with no moves", append(liveStream(false), evSeat(EvTakebackProposed, 0)), "needs ply >= 2"},
		{"takeback rewound no request", append(liveStream(false), evMove(1, 0, "e2e4"), evMove(2, 1, "e7e5"), evRewound(1, 0)), "no pending request"},
		{"takeback declined no request", append(liveStream(false), evSeat(EvTakebackDeclined, 1)), "no pending request"},
		{"second takeback while one pends", append(liveStream(false), evMove(1, 0, "a"), evMove(2, 1, "b"), evSeat(EvTakebackProposed, 0), evSeat(EvTakebackProposed, 1)), "already pending"},
		{"draw offered by other seat while pending", append(liveStream(false), evSeat(EvDrawOffered, 0), evSeat(EvDrawOffered, 1)), "that is an accept"},
		{"draw declined with nothing pending", append(liveStream(false), evSeat(EvDrawDeclined, 1)), "no open draw offer"},
		{"clock flagged twice", append(liveStream(false), evSeat(EvClockFlagged, 0), evSeat(EvClockFlagged, 1)), "already flagged"},
		{"move after clock flagged", append(liveStream(false), evSeat(EvClockFlagged, 0), evMove(1, 0, "e2e4")), "after ClockFlagged"},
		{"clock started twice", append(liveStream(false), evClockStarted(5), evClockStarted(6)), "already started"},
		{"clock started before live", []Event{evCreated(false, "x", "s"), evClockStarted(5)}, "must be Live"},
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

	// The OFFERER's own move keeps the offer (GS-7 §6.2); the RESPONDER's clears it.
	kept := append(liveStream(false),
		evSeat(EvDrawOffered, 0),
		evMove(1, 0, "e2e4"),
	)
	st, err = Fold(kept, baseOpts())
	if err != nil {
		t.Fatalf("offerer move fold: %v", err)
	}
	if st.DrawOfferBy != 0 {
		t.Errorf("DrawOfferBy = %d, want 0 (offerer's own move keeps the offer)", st.DrawOfferBy)
	}
	st, err = Fold(append(kept, evMove(2, 1, "e7e5")), baseOpts())
	if err != nil {
		t.Fatalf("responder move fold: %v", err)
	}
	if st.DrawOfferBy != NoSeat {
		t.Errorf("DrawOfferBy = %d, want -1 (responder's move clears the offer)", st.DrawOfferBy)
	}
	if st.LastDrawOfferPly[0] != 0 {
		t.Errorf("LastDrawOfferPly[0] = %d, want 0 (offer made at ply 0)", st.LastDrawOfferPly[0])
	}

	// A repeat DrawOffered by the same seat while its offer pends is idempotent:
	// state is unchanged (the event is still a stream event, so NextSeq moves).
	one, _ := Fold(append(liveStream(false), evMove(1, 0, "a"), evMove(2, 1, "b"), evSeat(EvDrawOffered, 0)), baseOpts())
	rep, err := Fold(append(liveStream(false), evMove(1, 0, "a"), evMove(2, 1, "b"), evSeat(EvDrawOffered, 0), evSeat(EvDrawOffered, 0)), baseOpts())
	if err != nil {
		t.Fatalf("repeat offer fold: %v", err)
	}
	if rep.DrawOfferBy != 0 || rep.LastDrawOfferPly[0] != 2 || rep.NextSeq != one.NextSeq+1 {
		t.Errorf("repeat offer: by=%d last=%d next=%d, want 0/2/%d", rep.DrawOfferBy, rep.LastDrawOfferPly[0], rep.NextSeq, one.NextSeq+1)
	}

	// A decline clears the offer but keeps the cooldown anchor.
	dec, err := Fold(append(liveStream(false), evMove(1, 0, "a"), evMove(2, 1, "b"), evSeat(EvDrawOffered, 0), evSeat(EvDrawDeclined, 1)), baseOpts())
	if err != nil {
		t.Fatalf("decline fold: %v", err)
	}
	if dec.DrawOfferBy != NoSeat || dec.LastDrawOfferPly[0] != 2 {
		t.Errorf("after decline: by=%d last=%d, want -1/2", dec.DrawOfferBy, dec.LastDrawOfferPly[0])
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
	half, err := Fold(events[:len(events)-1], baseOpts())
	if err != nil {
		t.Fatalf("half-terminal fold: %v", err)
	}
	if half.HalfTerminal == nil || *half.HalfTerminal != 1 {
		t.Fatalf("HalfTerminal = %v, want seat 1", half.HalfTerminal)
	}
	if half.Clock.RemainingMs[1] != 0 || half.Clock.FlaggedSeat != 1 || half.Phase != PhaseLive {
		t.Errorf("after ClockFlagged: clock=%v flagged=%d phase=%s, want seat1 zeroed, flagged 1, still Live",
			half.Clock.RemainingMs, half.Clock.FlaggedSeat, half.Phase)
	}
	if half.RunningSeat() != NoSeat {
		t.Errorf("RunningSeat after flag = %d, want none", half.RunningSeat())
	}
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatalf("flag path fold: %v", err)
	}
	if st.HalfTerminal != nil {
		t.Errorf("HalfTerminal not cleared by GameEnded")
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

func evRewound(toPly uint32, by uint8) Event {
	return Event{Type: EvTakebackRewound, TakebackRewound: &TakebackRewound{SeatIdx: by, ToPly: toPly, ServerTS: 77}}
}

// TestLegacyTakebackRefused: the pre-GS-7 events stay decodable but never fold.
func TestLegacyTakebackRefused(t *testing.T) {
	for _, typ := range []EventType{EvTakebackReq, EvTakebackAccepted} {
		stream := append(liveStream(false), evMove(1, 0, "e2e4"), evSeat(typ, 1))
		_, err := Fold(stream, baseOpts())
		if !errors.Is(err, ErrLegacyTakeback) {
			t.Errorf("%s: err = %v, want ErrLegacyTakeback", typ, err)
		}
	}
}

// TestNextSeqCountsEveryEvent: NextSeq is +1 per applied event, whatever its type.
func TestNextSeqCountsEveryEvent(t *testing.T) {
	events := append(liveStream(false),
		evClockStarted(10),
		evMoveClk(1, 0, "a", 1), evMoveClk(2, 1, "b", 1),
		evSeat(EvDrawOffered, 0), evSeat(EvDrawOffered, 0), evSeat(EvDrawDeclined, 1),
		evSeat(EvTakebackProposed, 0), evSeat(EvTakebackDeclined, 1),
		evSeat(EvTakebackProposed, 1), evRewound(1, 0),
		evSeat(EvResigned, 0), evEnded(TermResign, []Result{ResultLoss, ResultWin}),
	)
	s := initialState()
	for i, ev := range events {
		var err error
		s, err = Evolve(s, ev, nil, baseOpts())
		if err != nil {
			t.Fatalf("event %d (%s): %v", i, ev.Type, err)
		}
		if int(s.NextSeq) != i+1 {
			t.Fatalf("after event %d NextSeq = %d, want %d", i, s.NextSeq, i+1)
		}
	}
}

// TestEvolveDoesNotMutateInput: Evolve returns a new value; a held State is
// unaffected by later transitions (History, LastDrawOfferPly, Clock are copied).
func TestEvolveDoesNotMutateInput(t *testing.T) {
	base, err := Fold(append(liveStream(false), evClockStarted(1), evMoveClk(1, 0, "a", 10), evMoveClk(2, 1, "b", 20)), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	snapHist := len(base.History)
	snapClock := append([]int64(nil), base.Clock.RemainingMs...)
	for _, ev := range []Event{evSeat(EvDrawOffered, 0), evSeat(EvTakebackProposed, 0), evMoveClk(3, 0, "c", 5)} {
		if _, err := Evolve(base, ev, nil, baseOpts()); err != nil {
			t.Fatalf("%s: %v", ev.Type, err)
		}
	}
	rewound, err := Evolve(func() State {
		s, _ := Evolve(base, evSeat(EvTakebackProposed, 0), nil, baseOpts())
		return s
	}(), evRewound(0, 1), nil, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if rewound.Ply != 0 {
		t.Fatalf("rewound ply = %d", rewound.Ply)
	}
	if len(base.History) != snapHist || base.LastDrawOfferPly[0] != -1 || base.DrawOfferBy != NoSeat || base.PendingTakeback != nil {
		t.Errorf("input state mutated: %+v", base)
	}
	for i, v := range snapClock {
		if base.Clock.RemainingMs[i] != v {
			t.Errorf("input clock mutated at seat %d", i)
		}
	}
}

// TestTakebackRewoundValidation: Evolve itself refuses a rewind the §5.3 rule
// does not give, so an unfoldable rewind can never be appended.
func TestTakebackRewoundValidation(t *testing.T) {
	pre := append(liveStream(false), evMoveClk(1, 0, "a", 1), evMoveClk(2, 1, "b", 2), evMoveClk(3, 0, "c", 3), evMoveClk(4, 1, "d", 4))
	// seat 1 (made ply 4) proposes at ply 4 -> k=1 -> to_ply 3.
	good := append(append([]Event(nil), pre...), evSeat(EvTakebackProposed, 1))
	for _, c := range []struct {
		name string
		ev   Event
		sub  string
	}{
		{"wrong to_ply (k=2 instead of 1)", evRewound(2, 0), "to_ply 2"},
		{"to_ply beyond ply", evRewound(9, 0), "to_ply 9"},
		{"proposer accepts own", evRewound(3, 1), "own request"},
		{"seat out of range", evRewound(3, 7), "out of range"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Fold(append(append([]Event(nil), good...), c.ev), baseOpts())
			if err == nil || !strings.Contains(err.Error(), c.sub) {
				t.Fatalf("err = %v, want %q", err, c.sub)
			}
		})
	}
	st, err := Fold(append(good, evRewound(3, 0)), baseOpts())
	if err != nil {
		t.Fatalf("correct rewind: %v", err)
	}
	if st.Ply != 3 || st.TurnIdx != 1 || len(st.History) != 3 || st.PendingTakeback != nil {
		t.Errorf("after rewind: ply=%d turn=%d hist=%d pend=%v", st.Ply, st.TurnIdx, len(st.History), st.PendingTakeback)
	}
}

// TestRewindDropsStaleDrawOffer: a rewind clears a pending offer and forgets
// cooldown anchors above the rewound ply.
func TestRewindDropsStaleDrawOffer(t *testing.T) {
	events := append(liveStream(false),
		evMoveClk(1, 0, "a", 1), evMoveClk(2, 1, "b", 2),
		evSeat(EvDrawOffered, 0), evSeat(EvDrawDeclined, 1),
		evMoveClk(3, 0, "c", 3), evMoveClk(4, 1, "d", 4),
		evSeat(EvDrawOffered, 1),      // anchor at ply 4, pends
		evSeat(EvTakebackProposed, 0), // proposer made ply 3, accepter (1) to move... at_ply 4 by 0 -> k=2
		evRewound(2, 1),
	)
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if st.DrawOfferBy != NoSeat {
		t.Errorf("DrawOfferBy = %d, want cleared by rewind", st.DrawOfferBy)
	}
	if st.LastDrawOfferPly[1] != -1 || st.LastDrawOfferPly[0] != 2 {
		t.Errorf("anchors = %v, want [2 -1]", st.LastDrawOfferPly)
	}
}
