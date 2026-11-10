package game

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// GS-7 spec §9. Every decision is applied with Evolve through the sim helper, and
// the stream is re-folded after each one (see testsupport_test.go).

const (
	white uint8 = 0
	black uint8 = 1
)

// tableSim plays the §5.4 worked example (5+3): ply1 W 301000, ply2 B 298000,
// ply3 W 294000, ply4 B 297000, ply5 W 291000. Think times are chosen so the
// real clock arithmetic yields exactly those post-move values.
func tableSim(t *testing.T, upTo uint32) *sim {
	t.Helper()
	m := newSim(t, false)
	think := []uint64{2_000, 5_000, 10_000, 4_000, 6_000}
	for i := uint32(0); i < upTo; i++ {
		m.move(think[i])
	}
	want := []int64{301_000, 298_000, 294_000, 297_000, 291_000}
	for i := uint32(0); i < upTo; i++ {
		if got := int64(m.s.History[i].ClockAfterMs); got != want[i] {
			t.Fatalf("ply %d clock_after = %d, want %d", i+1, got, want[i])
		}
	}
	return m
}

func rewoundOf(t *testing.T, d Decision) *TakebackRewound {
	t.Helper()
	if d.Effect != EffectTakebackRewound || len(d.Events) != 1 || d.Events[0].Type != EvTakebackRewound {
		t.Fatalf("decision = effect %v events %d, want one TakebackRewound", d.Effect, len(d.Events))
	}
	return d.Events[0].TakebackRewound
}

func TestTakebackClockTable(t *testing.T) {
	cases := []struct {
		name       string
		proposeAt  uint32 // ply at which the request is made
		proposer   uint8
		extraPlies uint32 // plies played after the proposal, before the accept
		toPly      uint32
		w, b       int64
	}{
		{"B proposes after ply4, W accepts", 4, black, 0, 3, 294_000, 297_000},
		{"W proposes after ply4, B accepts", 4, white, 0, 2, 294_000, 298_000},
		{"W proposes after ply4, plays ply5, B accepts", 4, white, 1, 2, 294_000, 298_000},
		{"W proposes at ply2, B accepts", 2, white, 0, 0, 301_000, 300_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := tableSim(t, c.proposeAt)
			d := m.do(CmdProposeTakeback{By: c.proposer}, 1_000)
			if d.Effect != EffectTakebackProposed || d.AtPly != c.proposeAt {
				t.Fatalf("propose: effect %v at_ply %d", d.Effect, d.AtPly)
			}
			if c.extraPlies == 1 {
				m.move(6_000) // the proposer's own ply 5 -> W 291000
				if m.s.PendingTakeback == nil {
					t.Fatal("proposer's own move cleared the request")
				}
			}
			d = m.do(CmdAcceptTakeback{By: 1 - c.proposer, AtPly: c.proposeAt}, 500)
			if got := rewoundOf(t, d).ToPly; got != c.toPly {
				t.Fatalf("to_ply = %d, want %d", got, c.toPly)
			}
			if m.s.Ply != c.toPly || len(m.s.History) != int(c.toPly) {
				t.Fatalf("ply %d hist %d, want %d", m.s.Ply, len(m.s.History), c.toPly)
			}
			if m.s.Clock.RemainingMs[white] != c.w || m.s.Clock.RemainingMs[black] != c.b {
				t.Fatalf("clocks W %d B %d, want W %d B %d", m.s.Clock.RemainingMs[0], m.s.Clock.RemainingMs[1], c.w, c.b)
			}
			if m.s.LastChangeTS != m.now || m.s.PendingTakeback != nil || m.s.RunningSeat() != int(m.s.TurnIdx) {
				t.Fatalf("baseline/pending/running wrong: ts %d now %d pend %v", m.s.LastChangeTS, m.now, m.s.PendingTakeback)
			}
		})
	}
}

// (b) Evolve rejects a TakebackRewound whose to_ply the §5.3 rule does not give.
func TestEvolveRejectsWrongToPly(t *testing.T) {
	m := tableSim(t, 4)
	m.do(CmdProposeTakeback{By: black}, 1_000) // right answer: 3
	for _, wrong := range []uint32{0, 1, 2, 4, 5} {
		if _, err := Evolve(m.s, evRewound(wrong, white), nil, m.opts); err == nil {
			t.Errorf("to_ply %d accepted, want rejected", wrong)
		}
	}
	if _, err := Evolve(m.s, evRewound(3, white), nil, m.opts); err != nil {
		t.Errorf("correct to_ply 3 rejected: %v", err)
	}
}

// (c) draw rules.
func TestDrawOfferRules(t *testing.T) {
	t.Run("offer before ply 2 is ignored with no events", func(t *testing.T) {
		m := newSim(t, false)
		for ply := uint32(0); ply < 2; ply++ {
			for _, by := range []uint8{white, black} {
				d := m.do(CmdOfferDraw{By: by}, 10)
				if d.Effect != EffectIgnored || len(d.Events) != 0 {
					t.Fatalf("ply %d by %d: effect %v events %d", ply, by, d.Effect, len(d.Events))
				}
			}
			m.move(1_000)
		}
	})

	t.Run("offer at ply 2 is allowed", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		d := m.do(CmdOfferDraw{By: white}, 10)
		if d.Effect != EffectDrawOffered || len(d.Events) != 1 || m.s.DrawOfferBy != int(white) || m.s.LastDrawOfferPly[white] != 2 {
			t.Fatalf("effect %v by %d last %v", d.Effect, m.s.DrawOfferBy, m.s.LastDrawOfferPly)
		}
	})

	// Cooldown boundary on an exact state: last == ply-20 ignored, ply-21 allowed.
	t.Run("cooldown boundary", func(t *testing.T) {
		for _, ply := range []uint32{20, 21} {
			m := newSim(t, false)
			m.playTo(ply)
			for _, c := range []struct {
				last    int64
				allowed bool
			}{
				{int64(ply) - 20, false},
				{int64(ply) - 21, true},
				{int64(ply) - 19, false},
				{-1, true},
			} {
				s := m.s
				s.LastDrawOfferPly = []int64{c.last, -1}
				d, err := m.tryState(s, CmdOfferDraw{By: white})
				if err != nil {
					t.Fatal(err)
				}
				if c.allowed && (d.Effect != EffectDrawOffered || len(d.Events) != 1) {
					t.Errorf("ply %d last %d: effect %v, want offered", ply, c.last, d.Effect)
				}
				if !c.allowed && (d.Effect != EffectIgnored || len(d.Events) != 0) {
					t.Errorf("ply %d last %d: effect %v events %d, want ignored", ply, c.last, d.Effect, len(d.Events))
				}
			}
		}
	})

	// The same boundary reached through real events: offer at ply 2, decline.
	t.Run("cooldown boundary via stream", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: white}, 10)
		m.do(CmdDeclineDraw{By: black}, 10)
		m.playTo(22) // 22-20 == 2 -> still cooling down
		if d := m.do(CmdOfferDraw{By: white}, 10); d.Effect != EffectIgnored || len(d.Events) != 0 {
			t.Fatalf("ply 22: effect %v events %d, want ignored", d.Effect, len(d.Events))
		}
		m.playTo(23) // 23-21 == 2 -> allowed
		if d := m.do(CmdOfferDraw{By: white}, 10); d.Effect != EffectDrawOffered {
			t.Fatalf("ply 23: effect %v, want offered", d.Effect)
		}
		// The other seat has its own cooldown: it never offered, so it may offer.
		m.do(CmdDeclineDraw{By: black}, 10)
		if d := m.do(CmdOfferDraw{By: black}, 10); d.Effect != EffectDrawOffered {
			t.Fatalf("other seat blocked by someone else's cooldown: %v", d.Effect)
		}
	})

	// Unsigned-underflow regression: at ply < 20, ply-20 wraps if computed in
	// uint32; a re-offer inside the cooldown must still be ignored.
	t.Run("re-offer below ply 20 is ignored", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: white}, 10)
		m.do(CmdDeclineDraw{By: black}, 10)
		for _, ply := range []uint32{2, 3, 5, 19} {
			m.playTo(ply)
			d := m.do(CmdOfferDraw{By: white}, 10)
			if d.Effect != EffectIgnored || len(d.Events) != 0 || m.s.DrawOfferBy != NoSeat {
				t.Fatalf("ply %d: effect %v events %d by %d, want ignored", ply, d.Effect, len(d.Events), m.s.DrawOfferBy)
			}
		}
		// And a seat that never offered is unaffected at the same ply.
		if d := m.do(CmdOfferDraw{By: black}, 10); d.Effect != EffectDrawOffered {
			t.Fatalf("first offer of the other seat: %v", d.Effect)
		}
	})

	t.Run("repeat by the offerer is a no-op", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: white}, 10)
		d := m.do(CmdOfferDraw{By: white}, 10)
		if d.Effect != EffectNone || len(d.Events) != 0 {
			t.Fatalf("effect %v events %d", d.Effect, len(d.Events))
		}
	})

	t.Run("accept is allowed inside the accepter's own cooldown", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: black}, 10) // black's anchor = 2
		m.do(CmdDeclineDraw{By: white}, 10)
		m.playTo(4)
		m.do(CmdOfferDraw{By: white}, 10)
		if d, err := m.try(CmdOfferDraw{By: black}, 10); err != nil || d.Effect != EffectEnded {
			t.Fatalf("offer-as-accept: %v %v", d.Effect, err)
		}
		d := m.do(CmdAcceptDraw{By: black}, 10)
		if d.Effect != EffectEnded || len(d.Events) != 2 || d.Events[0].Type != EvDrawAccepted ||
			d.Events[1].Type != EvGameEnded || d.Events[1].GameEnded.Termination != TermDrawAgree {
			t.Fatalf("decision %+v", d)
		}
		if m.s.Phase != PhaseEnded {
			t.Fatalf("phase %s", m.s.Phase)
		}
	})

	t.Run("mutual offer is an accept, no extra DrawOffered", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: white}, 10)
		d := m.do(CmdOfferDraw{By: black}, 10)
		if len(d.Events) != 2 || d.Events[0].Type != EvDrawAccepted || d.Events[1].Type != EvGameEnded {
			t.Fatalf("events %+v", d.Events)
		}
	})

	t.Run("accept with nothing to accept", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		if _, err := m.try(CmdAcceptDraw{By: white}, 0); !errors.Is(err, ErrNoDrawToAccept) {
			t.Fatalf("err = %v", err)
		}
		m.do(CmdOfferDraw{By: white}, 10)
		if _, err := m.try(CmdAcceptDraw{By: white}, 0); !errors.Is(err, ErrNoDrawToAccept) {
			t.Fatalf("own offer: err = %v", err)
		}
	})

	t.Run("decline by responder and by offerer", func(t *testing.T) {
		for _, by := range []uint8{black, white} {
			m := newSim(t, false)
			m.playTo(2)
			m.do(CmdOfferDraw{By: white}, 10)
			d := m.do(CmdDeclineDraw{By: by}, 10)
			if d.Effect != EffectDrawDeclined || len(d.Events) != 1 || d.Events[0].Type != EvDrawDeclined ||
				d.Events[0].SeatAction.SeatIdx != by || m.s.DrawOfferBy != NoSeat {
				t.Fatalf("by %d: %+v offer %d", by, d, m.s.DrawOfferBy)
			}
		}
	})

	t.Run("decline with nothing pending is ignored", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		d := m.do(CmdDeclineDraw{By: black}, 10)
		if d.Effect != EffectIgnored || len(d.Events) != 0 {
			t.Fatalf("effect %v events %d", d.Effect, len(d.Events))
		}
	})

	t.Run("offerer's move keeps the offer, responder's clears it", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdOfferDraw{By: white}, 10) // white offers; white is to move
		m.move(1_000)                     // white's own move
		if m.s.DrawOfferBy != int(white) {
			t.Fatalf("offerer's move cleared the offer")
		}
		m.move(1_000) // black (responder) moves
		if m.s.DrawOfferBy != NoSeat {
			t.Fatalf("responder's move kept the offer")
		}
	})

	t.Run("finished game: draw commands are no-ops", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdResign{By: white}, 10)
		for _, cmd := range []Command{CmdOfferDraw{By: black}, CmdAcceptDraw{By: black}, CmdDeclineDraw{By: black}} {
			d, err := m.try(cmd, 0)
			if err != nil || len(d.Events) != 0 {
				t.Fatalf("%T after end: %v %v", cmd, d, err)
			}
		}
	})
}

// tryState decides cmd against an explicitly supplied state (for exact-boundary
// checks that would otherwise need dozens of plies of setup).
func (m *sim) tryState(s State, cmd Command) (Decision, error) {
	return Decide(context.Background(), s, cmd, m.now, m.opts, m.o)
}

// (d) MOVE idempotency: the §4 rows, in order.
func TestMoveIdempotencyTable(t *testing.T) {
	// A fresh game after 4 plies: W a, B b, W c, B d (uci m1..m4).
	build := func(t *testing.T) *sim {
		m := newSim(t, false)
		m.playTo(4)
		return m
	}
	decide := func(m *sim, c CmdMove) MoveOutcome {
		t.Helper()
		d, err := m.try(c, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Events) != 0 && !d.Move.Accepted {
			t.Fatalf("a rejection produced events: %+v", d.Events)
		}
		return d.Move
	}
	m := build(t)
	m.o.allow("zz", "fen-zz", nil)

	rej := func(name string, c CmdMove, want RejectReason) {
		t.Run(name, func(t *testing.T) {
			o := decide(m, c)
			if !o.Rejected || o.Reason != want {
				t.Fatalf("outcome %+v, want %v", o, want)
			}
			if o.PlyExpected != 4 {
				t.Errorf("PlyExpected echo = %d, want server ply 4", o.PlyExpected)
			}
		})
	}
	rej("1 seat out of range", CmdMove{Seat: 9, PlyExpected: 5, UCI: "zz"}, RejectNotYourSeat)
	rej("1 seat out of range on a played ply", CmdMove{Seat: 2, PlyExpected: 1, UCI: "m1"}, RejectNotYourSeat)
	rej("2 played ply by the other seat", CmdMove{Seat: black, PlyExpected: 1, UCI: "m1"}, RejectNotYourSeat)
	rej("2 played ply by the other seat, black's ply", CmdMove{Seat: white, PlyExpected: 4, UCI: "m4"}, RejectNotYourSeat)
	rej("4 same seat different uci", CmdMove{Seat: white, PlyExpected: 3, UCI: "zz"}, RejectStalePly)
	rej("6 wrong seat at ply+1", CmdMove{Seat: black, PlyExpected: 5, UCI: "zz"}, RejectNotYourTurn)
	rej("7 ply zero", CmdMove{Seat: white, PlyExpected: 0, UCI: "zz"}, RejectStalePly)
	rej("7 ply beyond ply+1", CmdMove{Seat: white, PlyExpected: 6, UCI: "zz"}, RejectStalePly)
	rej("illegal", CmdMove{Seat: white, PlyExpected: 5, UCI: "not-allowed"}, RejectIllegal)

	t.Run("3 same seat same uci is re-acked at that move's seq", func(t *testing.T) {
		for ply := uint32(1); ply <= 4; ply++ {
			e := m.s.History[ply-1]
			o := decide(m, CmdMove{Seat: e.Seat, PlyExpected: ply, UCI: e.UCI})
			if !o.Accepted || !o.Reacked || o.AppliedSeq != e.Seq || o.Ply != ply || o.UCI != e.UCI || o.EndedGame {
				t.Fatalf("ply %d: %+v (move seq %d)", ply, o, e.Seq)
			}
			// The seq really is where that MovePlayed sits in the stream.
			if ev := m.events[o.AppliedSeq]; ev.Type != EvMovePlayed || ev.MovePlayed.Ply != ply {
				t.Fatalf("ply %d: seq %d holds %s", ply, o.AppliedSeq, ev.Type)
			}
			d, _ := m.try(CmdMove{Seat: e.Seat, PlyExpected: ply, UCI: e.UCI}, 100)
			if len(d.Events) != 0 || d.Effect != EffectNone {
				t.Fatalf("re-ack produced events/effect: %+v", d)
			}
		}
	})

	t.Run("accepted move reports its real seq", func(t *testing.T) {
		m := build(t)
		next := m.s.NextSeq
		d := m.moveUCI(white, "zz", 1_000)
		if d.Move.AppliedSeq != next || m.events[next].Type != EvMovePlayed {
			t.Fatalf("AppliedSeq %d, want %d", d.Move.AppliedSeq, next)
		}
		if got := m.s.History[4].Seq; got != next {
			t.Fatalf("History seq %d, want %d", got, next)
		}
	})

	t.Run("5 ended game, other ply is GAME_OVER", func(t *testing.T) {
		m := build(t)
		m.do(CmdResign{By: black}, 10)
		for _, c := range []CmdMove{
			{Seat: white, PlyExpected: 5, UCI: "zz"},
			{Seat: black, PlyExpected: 5, UCI: "zz"},
			{Seat: white, PlyExpected: 9, UCI: "zz"},
		} {
			o := decide(m, c)
			if !o.Rejected || o.Reason != RejectGameOver {
				t.Fatalf("%+v: outcome %+v, want GAME_OVER", c, o)
			}
		}
		// A played ply is still re-acked after an ordinary end, without EndedGame
		// for a non-final ply.
		o := decide(m, CmdMove{Seat: white, PlyExpected: 3, UCI: "m3"})
		if !o.Reacked || o.EndedGame {
			t.Fatalf("re-ack of ply 3 after resign: %+v", o)
		}
	})

	t.Run("flagged half-terminal state rejects new plies as GAME_OVER", func(t *testing.T) {
		m := build(t)
		s := m.s
		seat := uint8(0)
		s.HalfTerminal = &seat
		d, err := m.tryState(s, CmdMove{Seat: white, PlyExpected: 5, UCI: "zz"})
		if err != nil || !d.Move.Rejected || d.Move.Reason != RejectGameOver {
			t.Fatalf("%+v %v", d.Move, err)
		}
	})

	t.Run("move before the game exists", func(t *testing.T) {
		d, err := Decide(context.Background(), initialState(), CmdMove{Seat: 0, PlyExpected: 1, UCI: "x"}, 1, baseOpts(), newFakeOracle())
		if err != nil || !d.Move.Rejected || d.Move.Reason != RejectUnknownGame {
			t.Fatalf("%+v %v", d.Move, err)
		}
	})

	t.Run("after a takeback a re-ack at a recurring ply reports the NEW move", func(t *testing.T) {
		m := build(t) // plies 1..4 = m1..m4
		oldSeq3 := m.s.History[2].Seq
		m.do(CmdProposeTakeback{By: black}, 10) // black made ply 4 -> to_ply 3
		m.do(CmdAcceptTakeback{By: white, AtPly: 4}, 10)
		if m.s.Ply != 3 {
			t.Fatalf("ply %d", m.s.Ply)
		}
		// Rewind to 3 keeps m3; take another back so ply 3 recurs.
		m.do(CmdProposeTakeback{By: white}, 10) // white made ply 3 -> at 3, k=1 -> to_ply 2
		m.do(CmdAcceptTakeback{By: black, AtPly: 3}, 10)
		if m.s.Ply != 2 {
			t.Fatalf("ply %d", m.s.Ply)
		}
		d := m.moveUCI(white, "zz", 1_000) // a DIFFERENT move at ply 3
		newSeq := d.Move.AppliedSeq
		if newSeq == oldSeq3 {
			t.Fatalf("new move reuses the undone move's seq %d", newSeq)
		}
		o := decide(m, CmdMove{Seat: white, PlyExpected: 3, UCI: "zz"})
		if !o.Reacked || o.UCI != "zz" || o.AppliedSeq != newSeq {
			t.Fatalf("re-ack of the new move: %+v (want seq %d)", o, newSeq)
		}
		// The undone move at that ply is now stale, not re-acked.
		o = decide(m, CmdMove{Seat: white, PlyExpected: 3, UCI: "m3"})
		if !o.Rejected || o.Reason != RejectStalePly {
			t.Fatalf("resent undone move: %+v, want STALE_PLY", o)
		}
	})

	t.Run("terminal move gives MovePlayed+GameEnded in one decision; resend is re-acked with EndedGame", func(t *testing.T) {
		m := build(t)
		mate := TermMate
		m.o.allow("mate", "fen-mate", &mate)
		d := m.do(CmdMove{Seat: white, PlyExpected: 5, UCI: "mate"}, 1_000)
		if len(d.Events) != 2 || d.Events[0].Type != EvMovePlayed || d.Events[1].Type != EvGameEnded {
			t.Fatalf("events %+v", d.Events)
		}
		if !d.Move.Accepted || !d.Move.EndedGame || d.Move.Terminal == nil || *d.Move.Terminal != TermMate || d.Effect != EffectMove {
			t.Fatalf("outcome %+v effect %v", d.Move, d.Effect)
		}
		if rv := d.Events[1].GameEnded.ResultVector; rv[white] != ResultWin || rv[black] != ResultLoss {
			t.Fatalf("result vector %v", rv)
		}
		if m.s.Phase != PhaseEnded || m.s.Termination != TermMate || len(m.s.History) != 5 {
			t.Fatalf("phase %s term %d hist %d", m.s.Phase, m.s.Termination, len(m.s.History))
		}
		seq := d.Move.AppliedSeq
		o := decide(m, CmdMove{Seat: white, PlyExpected: 5, UCI: "mate"})
		if !o.Accepted || !o.Reacked || !o.EndedGame || o.AppliedSeq != seq || o.Terminal == nil {
			t.Fatalf("resent mating move: %+v (seq %d)", o, seq)
		}
		dd, _ := m.try(CmdMove{Seat: white, PlyExpected: 5, UCI: "mate"}, 100)
		if len(dd.Events) != 0 {
			t.Fatalf("resend appended %d events", len(dd.Events))
		}
		// An earlier ply re-ack does not claim to have ended the game.
		if o := decide(m, CmdMove{Seat: black, PlyExpected: 4, UCI: "m4"}); !o.Reacked || o.EndedGame {
			t.Fatalf("ply 4 re-ack: %+v", o)
		}
	})

	t.Run("a flagged mover gets Flagged and a ClockFlagged+GameEnded batch", func(t *testing.T) {
		m := build(t)
		m.o.material[m.s.CurFEN+"#1"] = true
		d := m.do(CmdMove{Seat: white, PlyExpected: 5, UCI: "zz"}, 10_000_000)
		if !d.Move.Flagged || d.Move.FlaggedSeat != white || len(d.Events) != 2 ||
			d.Events[0].Type != EvClockFlagged || d.Events[1].GameEnded.Termination != TermFlag {
			t.Fatalf("%+v %+v", d.Move, d.Events)
		}
		if m.s.Phase != PhaseEnded || m.s.Ply != 4 {
			t.Fatalf("phase %s ply %d", m.s.Phase, m.s.Ply)
		}
	})

	t.Run("one engine call per move", func(t *testing.T) {
		m := build(t)
		m.o.allow("one", "fen-one", nil)
		before := m.o.calls
		m.moveUCI(white, "one", 1_000)
		if got := m.o.calls - before; got != 1 {
			t.Fatalf("%d oracle calls, want 1", got)
		}
		before = m.o.calls
		decide(m, CmdMove{Seat: white, PlyExpected: 5, UCI: "one"}) // re-ack
		if m.o.calls != before {
			t.Fatalf("a re-ack called the engine")
		}
	})

	t.Run("oracle failure surfaces as an error with no events", func(t *testing.T) {
		m := build(t)
		m.o.fail = errors.New("engine down")
		d, err := m.try(CmdMove{Seat: white, PlyExpected: 5, UCI: "zz"}, 100)
		if err == nil || len(d.Events) != 0 {
			t.Fatalf("err %v events %d", err, len(d.Events))
		}
	})
}

// (e) takeback.
func TestTakebackRules(t *testing.T) {
	t.Run("propose before ply 2 is ignored", func(t *testing.T) {
		m := newSim(t, false)
		for i := 0; i < 2; i++ {
			d := m.do(CmdProposeTakeback{By: white}, 10)
			if d.Effect != EffectIgnored || len(d.Events) != 0 {
				t.Fatalf("ply %d: %v %d", m.s.Ply, d.Effect, len(d.Events))
			}
			m.move(1_000)
		}
	})

	t.Run("responder's TAKEBACK while one pends is ignored, proposer's repeat is a no-op", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: white}, 10)
		d := m.do(CmdProposeTakeback{By: black}, 10)
		if d.Effect != EffectIgnored || len(d.Events) != 0 || m.s.PendingTakeback.By != white {
			t.Fatalf("responder: %v %d %+v", d.Effect, len(d.Events), m.s.PendingTakeback)
		}
		d = m.do(CmdProposeTakeback{By: white}, 10)
		if d.Effect != EffectNone || len(d.Events) != 0 {
			t.Fatalf("repeat: %v %d", d.Effect, len(d.Events))
		}
	})

	t.Run("accept with wrong at_ply, by the proposer, or with nothing pending is ignored", func(t *testing.T) {
		m := tableSim(t, 4)
		if d := m.do(CmdAcceptTakeback{By: black, AtPly: 4}, 10); d.Effect != EffectIgnored || len(d.Events) != 0 {
			t.Fatalf("nothing pending: %v", d.Effect)
		}
		m.do(CmdProposeTakeback{By: white}, 10)
		for _, c := range []CmdAcceptTakeback{
			{By: black, AtPly: 3},
			{By: black, AtPly: 5},
			{By: black, AtPly: 0},
			{By: white, AtPly: 4}, // the proposer cannot accept
		} {
			d := m.do(c, 10)
			if d.Effect != EffectIgnored || len(d.Events) != 0 || m.s.PendingTakeback == nil {
				t.Fatalf("%+v: effect %v events %d pend %v", c, d.Effect, len(d.Events), m.s.PendingTakeback)
			}
		}
	})

	t.Run("accept after the side to move has flagged ends the game, no rewind", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: black}, 10) // W to move
		m.o.material[m.s.CurFEN+"#1"] = true
		d := m.do(CmdAcceptTakeback{By: white, AtPly: 4}, 10_000_000)
		if d.Effect != EffectEnded || len(d.Events) != 2 || d.Events[0].Type != EvClockFlagged ||
			d.Events[0].SeatAction.SeatIdx != white || d.Events[1].Type != EvGameEnded {
			t.Fatalf("decision %+v", d)
		}
		for _, ev := range d.Events {
			if ev.Type == EvTakebackRewound {
				t.Fatal("flagged player got a rewind")
			}
		}
		if m.s.Phase != PhaseEnded || m.s.Ply != 4 || m.s.Termination != TermFlag {
			t.Fatalf("phase %s ply %d term %d", m.s.Phase, m.s.Ply, m.s.Termination)
		}
	})

	t.Run("rated game refuses with ErrTakebackNotAllowed", func(t *testing.T) {
		m := newSim(t, true)
		m.playTo(4)
		for _, cmd := range []Command{CmdProposeTakeback{By: white}, CmdProposeTakeback{By: black}} {
			if _, err := m.try(cmd, 10); !errors.Is(err, ErrTakebackNotAllowed) {
				t.Fatalf("%T: err = %v", cmd, err)
			}
		}
	})

	t.Run("a second takeback after a first works", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: white}, 10)
		m.do(CmdAcceptTakeback{By: black, AtPly: 4}, 10) // to 2
		if m.s.Ply != 2 {
			t.Fatalf("ply %d", m.s.Ply)
		}
		m.move(1_000)
		m.move(1_000)
		m.move(1_000) // ply 5
		m.do(CmdProposeTakeback{By: white}, 10)
		d := m.do(CmdAcceptTakeback{By: black, AtPly: 5}, 10) // W made ply 5 -> k=1 -> 4
		if got := rewoundOf(t, d).ToPly; got != 4 || m.s.Ply != 4 {
			t.Fatalf("to_ply %d ply %d, want 4", got, m.s.Ply)
		}
		// Rewinding to ply 0 and then proposing is refused (needs ply >= 2).
		m = tableSim(t, 2)
		m.do(CmdProposeTakeback{By: white}, 10)
		m.do(CmdAcceptTakeback{By: black, AtPly: 2}, 10)
		if m.s.Ply != 0 {
			t.Fatalf("ply %d", m.s.Ply)
		}
		if d := m.do(CmdProposeTakeback{By: white}, 10); d.Effect != EffectIgnored {
			t.Fatalf("at ply 0: %v", d.Effect)
		}
	})

	t.Run("responder's move clears the request, proposer's does not", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: white}, 10) // W (proposer) to move
		m.move(1_000)                           // proposer's ply 5
		if m.s.PendingTakeback == nil || m.s.PendingTakeback.AtPly != 4 {
			t.Fatalf("proposer's move cleared it: %+v", m.s.PendingTakeback)
		}
		m.move(1_000) // responder's ply 6
		if m.s.PendingTakeback != nil {
			t.Fatalf("responder's move kept it")
		}
		// A stale accept now is ignored, not an error.
		if d := m.do(CmdAcceptTakeback{By: black, AtPly: 4}, 10); d.Effect != EffectIgnored {
			t.Fatalf("stale accept: %v", d.Effect)
		}
	})

	t.Run("decline by responder and cancel by proposer", func(t *testing.T) {
		for _, by := range []uint8{black, white} {
			m := tableSim(t, 4)
			m.do(CmdProposeTakeback{By: white}, 10)
			if d := m.do(CmdDeclineTakeback{By: by, AtPly: 3}, 10); d.Effect != EffectIgnored {
				t.Fatalf("wrong at_ply: %v", d.Effect)
			}
			d := m.do(CmdDeclineTakeback{By: by, AtPly: 4}, 10)
			if d.Effect != EffectTakebackDeclined || len(d.Events) != 1 || m.s.PendingTakeback != nil {
				t.Fatalf("by %d: %+v", by, d)
			}
		}
		m := tableSim(t, 4)
		if d := m.do(CmdDeclineTakeback{By: black, AtPly: 4}, 10); d.Effect != EffectIgnored {
			t.Fatalf("nothing pending: %v", d.Effect)
		}
	})

	t.Run("a rewind clears a pending draw offer", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: white}, 10)
		m.do(CmdOfferDraw{By: black}, 10)
		m.do(CmdAcceptTakeback{By: black, AtPly: 4}, 10)
		if m.s.DrawOfferBy != NoSeat {
			t.Fatalf("offer survived the rewind")
		}
	})

	t.Run("finished game: takeback commands are no-ops", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdResign{By: white}, 10)
		for _, cmd := range []Command{CmdProposeTakeback{By: black}, CmdAcceptTakeback{By: black, AtPly: 4}, CmdDeclineTakeback{By: black, AtPly: 4}} {
			if d, err := m.try(cmd, 0); err != nil || len(d.Events) != 0 {
				t.Fatalf("%T: %v %v", cmd, d, err)
			}
		}
	})
}

// (f) clock start and charging.
func TestClockStartAndCharging(t *testing.T) {
	t.Run("ClockStarted once, then no-op", func(t *testing.T) {
		m := &sim{t: t, o: newFakeOracle(), opts: baseOpts(), s: initialState(), now: 5_000}
		for _, ev := range liveStream(false) {
			m.apply(ev, nil)
		}
		if m.s.ClockStartedSet || m.s.ElapsedMs(9_999_999) != 0 || m.s.RunningSeat() != NoSeat {
			t.Fatalf("clock should not run before start: %+v", m.s)
		}
		if cl := m.s.ClocksAt(9_999_999, baseOpts().TimeControl); cl[0] != 300_000 || cl[1] != 300_000 {
			t.Fatalf("clocks before start %v", cl)
		}
		d := m.do(CmdStartClock{}, 0)
		if d.Effect != EffectClockStarted || len(d.Events) != 1 || d.Events[0].Type != EvClockStarted ||
			d.Events[0].ClockStarted.ServerTS != 5_000 {
			t.Fatalf("first: %+v", d)
		}
		if !m.s.ClockStartedSet || m.s.LastChangeTS != 5_000 {
			t.Fatalf("state after start: %v %d", m.s.ClockStartedSet, m.s.LastChangeTS)
		}
		d = m.do(CmdStartClock{}, 1_000)
		if d.Effect != EffectNone || len(d.Events) != 0 {
			t.Fatalf("second: %+v", d)
		}
		if m.s.LastChangeTS != 5_000 {
			t.Fatalf("second start moved the baseline to %d", m.s.LastChangeTS)
		}
	})

	t.Run("not started before live or after the end", func(t *testing.T) {
		s := initialState()
		for _, ev := range liveStream(false)[:2] {
			var err error
			if s, err = Evolve(s, ev, nil, baseOpts()); err != nil {
				t.Fatal(err)
			}
		}
		if d, _ := Decide(context.Background(), s, CmdStartClock{}, 1, baseOpts(), newFakeOracle()); len(d.Events) != 0 {
			t.Fatalf("started a clock in %s", s.Phase)
		}
		m := newSim(t, false)
		m.do(CmdResign{By: white}, 10)
		if d, _ := m.try(CmdStartClock{}, 0); len(d.Events) != 0 {
			t.Fatal("start clock after end")
		}
	})

	t.Run("ElapsedMs never negative", func(t *testing.T) {
		m := newSim(t, false)
		if m.s.ElapsedMs(m.now-1) != 0 || m.s.ElapsedMs(m.now) != 0 || m.s.ElapsedMs(m.now+7) != 7 {
			t.Fatal("ElapsedMs wrong around the baseline")
		}
	})

	t.Run("ClocksAt charges only the running seat", func(t *testing.T) {
		m := newSim(t, false)
		tc := baseOpts().TimeControl
		cl := m.s.ClocksAt(m.now+10_000, tc) // white to move
		if cl[0] != 290_000 || cl[1] != 300_000 {
			t.Fatalf("clocks %v", cl)
		}
		m.move(2_000) // white: 300000-2000+3000
		cl = m.s.ClocksAt(m.now+10_000, tc)
		if cl[0] != 301_000 || cl[1] != 290_000 {
			t.Fatalf("after white moved: %v", cl)
		}
		// The per-move delay absorbs the first part of the think.
		tcDelay := tc
		tcDelay.PerMoveMs = 4_000
		if cl = m.s.ClocksAt(m.now+10_000, tcDelay); cl[1] != 294_000 {
			t.Fatalf("with delay: %v", cl)
		}
		// Floored at zero, and the stored state is unchanged.
		if cl = m.s.ClocksAt(m.now+10_000_000, tc); cl[1] != 0 || cl[0] != 301_000 {
			t.Fatalf("floored: %v", cl)
		}
		if m.s.Clock.RemainingMs[1] != 300_000 {
			t.Fatal("ClocksAt mutated the clock")
		}
		// Nobody runs after the end.
		m.do(CmdResign{By: black}, 10)
		if cl = m.s.ClocksAt(m.now+10_000, tc); cl[0] != 301_000 || cl[1] != 300_000 {
			t.Fatalf("after end: %v", cl)
		}
	})

	t.Run("tick flag only charges the side to move and ends the game", func(t *testing.T) {
		m := tableSim(t, 3) // black to move, 298000 left
		if d := m.do(CmdTickFlag{}, 100_000); len(d.Events) != 0 {
			t.Fatal("ticked a flag with time left")
		}
		m.o.material[m.s.CurFEN+"#0"] = true
		d := m.do(CmdTickFlag{}, 400_000)
		if d.Effect != EffectEnded || len(d.Events) != 2 || d.Events[0].SeatAction.SeatIdx != black ||
			d.Events[1].GameEnded.Termination != TermFlag {
			t.Fatalf("%+v", d)
		}
		if m.s.Clock.RemainingMs[black] != 0 {
			t.Fatalf("flagged clock %d", m.s.Clock.RemainingMs[black])
		}
		if d := m.do(CmdTickFlag{}, 10); len(d.Events) != 0 {
			t.Fatal("tick after the end")
		}
	})

	t.Run("tick after a takeback charges the rewound side to move", func(t *testing.T) {
		m := tableSim(t, 4)
		m.do(CmdProposeTakeback{By: white}, 10) // k=2 -> to 2, white to move with 294000
		m.do(CmdAcceptTakeback{By: black, AtPly: 4}, 10)
		if m.s.TurnIdx != white {
			t.Fatalf("turn %d", m.s.TurnIdx)
		}
		m.o.material[m.s.CurFEN+"#1"] = true
		d := m.do(CmdTickFlag{}, 300_000)
		if len(d.Events) != 2 || d.Events[0].SeatAction.SeatIdx != white {
			t.Fatalf("%+v", d)
		}
	})
}

// (g) FoldWithEngine replays only live moves from the right position.
func TestFoldWithEngineAfterTakeback(t *testing.T) {
	m := newSim(t, false)
	m.playTo(4) // m1..m4
	m.do(CmdProposeTakeback{By: black}, 10)
	m.do(CmdAcceptTakeback{By: white, AtPly: 4}, 10) // to ply 3
	m.moveUCI(black, "alt4", 1_000)                  // a different move at the recurring ply 4

	// An oracle that records the (fen, uci) it is asked about, so the replay
	// position at every step is observable.
	type call struct{ fen, uci string }
	var seen []call
	rec := &recordingOracle{fakeOracle: m.o, hook: func(fen, uci string) { seen = append(seen, call{fen, uci}) }}

	got, err := FoldWithEngine(context.Background(), m.events, m.opts, rec)
	if err != nil {
		t.Fatalf("FoldWithEngine: %v", err)
	}
	var ucis []string
	for _, e := range got.History {
		ucis = append(ucis, e.UCI)
	}
	if fmt.Sprint(ucis) != "[m1 m2 m3 alt4]" {
		t.Fatalf("History %v, want [m1 m2 m3 alt4]", ucis)
	}
	if got.Ply != 4 || got.CurFEN != "fen-alt4" {
		t.Fatalf("ply %d fen %q", got.Ply, got.CurFEN)
	}
	stateEqual(t, m.s, got)
	if got.History[3].FEN != "fen-alt4" || got.History[2].FEN != "fen-m3" {
		t.Fatalf("history fens: %q %q", got.History[2].FEN, got.History[3].FEN)
	}
	// Replay: m1..m4 once, then alt4 from the position after m3 (not after m4).
	want := []call{{"startpos", "m1"}, {"fen-m1", "m2"}, {"fen-m2", "m3"}, {"fen-m3", "m4"}, {"fen-m3", "alt4"}}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("engine calls %v, want %v", seen, want)
	}
	// Pure Fold agrees on everything the engine does not supply.
	pure, err := Fold(m.events, m.opts)
	if err != nil {
		t.Fatal(err)
	}
	stateEqual(t, pure, got)
	if pure.CurFEN != "startpos" {
		t.Errorf("pure fold CurFEN = %q, want startpos", pure.CurFEN)
	}
}

type recordingOracle struct {
	*fakeOracle
	hook func(fen, uci string)
}

func (r *recordingOracle) Legality(ctx context.Context, fen, uci string, prior []uint64) (LegalityVerdict, error) {
	r.hook(fen, uci)
	return r.fakeOracle.Legality(ctx, fen, uci, prior)
}

func TestFoldWithEngineBasics(t *testing.T) {
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	o.allow("e7e5", "fen-after-e5", nil)
	events := append(liveStream(false), evMove(1, 0, "e2e4"), evMove(2, 1, "e7e5"))

	eng, err := FoldWithEngine(context.Background(), events, baseOpts(), o)
	if err != nil {
		t.Fatal(err)
	}
	if eng.CurFEN != "fen-after-e5" || len(eng.PriorKeys()) != 2 || eng.LastMoveUCI() != "e7e5" {
		t.Errorf("fen %q keys %d last %q", eng.CurFEN, len(eng.PriorKeys()), eng.LastMoveUCI())
	}
	// A persisted move the engine rejects is a corrupt stream.
	if _, err := FoldWithEngine(context.Background(), events, baseOpts(), newFakeOracle()); err == nil {
		t.Error("corrupt stream: expected error")
	}
	if _, err := FoldWithEngine(context.Background(), events, baseOpts(), nil); err == nil {
		t.Error("nil oracle: expected error")
	}
}

func TestFinishHalfTerminal(t *testing.T) {
	// A stream ending in ClockFlagged (no GameEnded) is closed by the command.
	o := newFakeOracle()
	o.allow("e2e4", "fen-after-e4", nil)
	o.material["fen-after-e4#0"] = true
	events := append(liveStream(false), evClockStarted(1), evMove(1, 0, "e2e4"), evSeat(EvClockFlagged, 1))
	s, err := FoldWithEngine(context.Background(), events, baseOpts(), o)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decide(context.Background(), s, CmdFinishHalfTerminal{}, 9, baseOpts(), o)
	if err != nil || len(d.Events) != 1 || d.Events[0].GameEnded.Termination != TermFlag {
		t.Fatalf("%+v %v", d, err)
	}
	next, err := Evolve(s, d.Events[0], nil, baseOpts())
	if err != nil || next.Phase != PhaseEnded || next.HalfTerminal != nil {
		t.Fatalf("%v phase %s", err, next.Phase)
	}

	// A stream whose last move was terminal but has no GameEnded.
	mate := TermMate
	o.allow("e7e5", "fen-mate", &mate)
	events = append(liveStream(false), evClockStarted(1), evMove(1, 0, "e2e4"), evMove(2, 1, "e7e5"))
	s, err = FoldWithEngine(context.Background(), events, baseOpts(), o)
	if err != nil {
		t.Fatal(err)
	}
	d, err = Decide(context.Background(), s, CmdFinishHalfTerminal{}, 9, baseOpts(), o)
	if err != nil || len(d.Events) != 1 || d.Events[0].GameEnded.Termination != TermMate ||
		d.Events[0].GameEnded.ResultVector[black] != ResultWin {
		t.Fatalf("%+v %v", d, err)
	}

	// Nothing to finish on a healthy live game.
	if d, _ := Decide(context.Background(), m0(t).s, CmdFinishHalfTerminal{}, 9, baseOpts(), o); len(d.Events) != 0 {
		t.Fatal("finished a healthy game")
	}
}

func m0(t *testing.T) *sim { return newSim(t, false) }

func TestFlagTermination(t *testing.T) {
	ctx := context.Background()
	o1 := newFakeOracle()
	o1.material["pos#0"] = true
	if term, err := FlagTermination(ctx, o1, "pos", 1, 2); err != nil || term != TermFlag {
		t.Errorf("sufficient: %d %v", term, err)
	}
	if term, err := FlagTermination(ctx, newFakeOracle(), "pos", 1, 2); err != nil || term != TermTimeoutVsInsufficient {
		t.Errorf("insufficient: %d %v", term, err)
	}
	if _, err := FlagTermination(ctx, o1, "pos", 0, 3); err == nil {
		t.Error("3 seats: expected error")
	}
	if rv := FlagResultVector(TermFlag, 1, 2); rv[0] != ResultWin || rv[1] != ResultLoss {
		t.Errorf("flag rv %v", rv)
	}
	if rv := FlagResultVector(TermTimeoutVsInsufficient, 1, 2); rv[0] != ResultDraw || rv[1] != ResultDraw {
		t.Errorf("tvi rv %v", rv)
	}
}

func TestResignAbortDecisions(t *testing.T) {
	m := newSim(t, false)
	if _, err := m.try(CmdAbort{By: white, AbortBeforePly: 2}, 0); err != nil {
		t.Fatalf("abort at ply 0: %v", err)
	}
	m.playTo(2)
	if _, err := m.try(CmdAbort{By: white, AbortBeforePly: 2}, 0); !errors.Is(err, ErrAbortTooLate) {
		t.Fatalf("abort at ply 2: %v", err)
	}
	d := m.do(CmdResign{By: white}, 10)
	if d.Effect != EffectEnded || m.s.Termination != TermResign || m.s.ResultVector[white] != ResultLoss {
		t.Fatalf("%+v", d)
	}
	if d := m.do(CmdResign{By: black}, 10); len(d.Events) != 0 {
		t.Fatal("resign after end")
	}
	m2 := newSim(t, false)
	m2.do(CmdAbort{By: white, AbortBeforePly: 2}, 10)
	if m2.s.Termination != TermAbort {
		t.Fatalf("term %d", m2.s.Termination)
	}
}

func TestMemStore(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	for seq := uint32(0); seq < 5; seq++ {
		if err := store.Append(ctx, "game/x", seq, evSeat(EvDrawOffered, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []uint32{7, 2} {
		if err := store.Append(ctx, "game/x", seq, evSeat(EvDrawOffered, 0)); !errors.Is(err, ErrSeqConflict) {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	evs, _ := store.Load(ctx, "game/x")
	evs[0] = Event{}
	if again, _ := store.Load(ctx, "game/x"); len(again) != 5 || again[0].Type != EvDrawOffered {
		t.Error("Load is not a defensive copy")
	}
	if evs, err := store.Load(ctx, "game/none"); err != nil || len(evs) != 0 {
		t.Errorf("unknown stream: %v %v", evs, err)
	}
	boom := errors.New("boom")
	store.FailNextAppend(boom)
	if err := store.Append(ctx, "game/x", 5, evSeat(EvDrawOffered, 0)); !errors.Is(err, boom) {
		t.Errorf("armed failure: %v", err)
	}
	if err := store.Append(ctx, "game/x", 5, evSeat(EvDrawOffered, 0)); err != nil {
		t.Errorf("failure should be one-shot: %v", err)
	}
}

// TestReackAfterNonMoveEndIsNotEndedGame: a game that ended by resign AFTER its
// last move re-acks that move without EndedGame (no GAME_END re-send), §4.
func TestReackAfterNonMoveEndIsNotEndedGame(t *testing.T) {
	m := newSim(t, false)
	m.playTo(2)
	last := m.s.History[len(m.s.History)-1]
	m.do(CmdResign{By: 0}, 0)
	if m.s.Phase != PhaseEnded {
		t.Fatalf("resign did not end the game")
	}
	d, err := m.try(CmdMove{Seat: last.Seat, PlyExpected: 2, UCI: last.UCI}, 0)
	if err != nil || !d.Move.Reacked {
		t.Fatalf("re-ack after resign: %+v err=%v", d.Move, err)
	}
	if d.Move.EndedGame {
		t.Fatalf("EndedGame set for a move that did not end the game")
	}
}
