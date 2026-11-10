package game

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

// fakeOracle is the LegalityOracle test double (SPEC-S2-GAME-SERVER §2c "STUB it
// with a test double"). It carries NO chess logic (NFR-5): it returns canned
// verdicts keyed by the (fen, uci) the test sets up, so tests drive the state
// machine without a real engine. A real engine replaces it later.
type fakeOracle struct {
	// legal maps uci -> verdict; a uci not present is treated as illegal.
	legal map[string]LegalityVerdict
	// material maps "fen#seat" -> sufficient; default false.
	material map[string]bool
	// fail, when set, makes every call return this error (I/O-failure path).
	fail error
	// calls counts Legality invocations (determinism assertions).
	calls int
	// lastPriorKeys records the priorKeys passed to the most recent Legality
	// call, so tests can assert the history is threaded through (Q-GS5).
	lastPriorKeys []uint64
}

func newFakeOracle() *fakeOracle {
	return &fakeOracle{legal: map[string]LegalityVerdict{}, material: map[string]bool{}}
}

// allow registers a uci as legal, producing newFEN and an optional terminal.
func (o *fakeOracle) allow(uci, newFEN string, terminal *Termination) {
	o.legal[uci] = LegalityVerdict{Legal: true, NewFEN: newFEN, Terminal: terminal}
}

func (o *fakeOracle) Legality(ctx context.Context, fen, uci string, priorKeys []uint64) (LegalityVerdict, error) {
	o.calls++
	o.lastPriorKeys = priorKeys
	if o.fail != nil {
		return LegalityVerdict{}, o.fail
	}
	if v, ok := o.legal[uci]; ok {
		return v, nil
	}
	return LegalityVerdict{Legal: false}, nil
}

func (o *fakeOracle) HasSufficientMaterial(ctx context.Context, fen string, seatIdx uint8) (bool, error) {
	if o.fail != nil {
		return false, o.fail
	}
	return o.material[fmt.Sprintf("%s#%d", fen, seatIdx)], nil
}

// --- event builders (keep the tests terse and intention-revealing) ---

func evCreated(rated bool, tcID, startFEN string) Event {
	return Event{Type: EvGameCreated, GameCreated: &GameCreated{
		PoolID:      "blitz-standard",
		Rated:       rated,
		Seats:       []Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
		StartFEN:    startFEN,
		TimeControl: tcID,
	}}
}

func evJoined(seat uint8) Event {
	return Event{Type: EvPlayerJoined, PlayerJoined: &PlayerJoined{SeatIdx: seat, UserID: fmt.Sprintf("user-%d", seat)}}
}

func evMove(ply uint32, seat uint8, uci string) Event {
	return Event{Type: EvMovePlayed, MovePlayed: &MovePlayed{Ply: ply, SeatIdx: seat, UCI: uci}}
}

// evMoveClk is evMove with an explicit post-move remaining time (clock_after_ms)
// for the mover — the authoritative value the fold projects.
func evMoveClk(ply uint32, seat uint8, uci string, clockAfterMs uint32) Event {
	return Event{Type: EvMovePlayed, MovePlayed: &MovePlayed{Ply: ply, SeatIdx: seat, UCI: uci, ClockAfterMs: clockAfterMs}}
}

func evSeat(t EventType, seat uint8) Event {
	return Event{Type: t, SeatAction: &SeatAction{SeatIdx: seat}}
}

func evAborted(by uint8) Event { return Event{Type: EvAborted, Aborted: &Aborted{By: by}} }

func evEnded(term Termination, rv []Result) Event {
	return Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: rv, Termination: term}}
}

func evRating() Event {
	return Event{Type: EvRatingApplied, RatingApplied: &RatingApplied{
		PoolID:  "blitz-standard",
		PerSeat: []SeatRating{{SeatIdx: 0, Before: 1500, After: 1510, RD: 60, Vol: 0.06}},
	}}
}

// a standard live game: created (unrated unless rated=true) + both joined.
func liveStream(rated bool) []Event {
	return []Event{evCreated(rated, "blitz-5+3", "startpos"), evJoined(0), evJoined(1)}
}

// a basic 5+3 time control for tests.
func tc53() clock.TimeControl {
	return clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}
}

// clockTC builds an arbitrary base+increment time control for tests.
func clockTC(baseMs, incMs int64) clock.TimeControl {
	return clock.TimeControl{BaseMs: baseMs, IncrementMs: incMs}
}

func baseOpts() FoldOptions { return FoldOptions{TimeControl: tc53()} }

func evClockStarted(ts uint64) Event {
	return Event{Type: EvClockStarted, ClockStarted: &ClockStarted{ServerTS: ts}}
}

// sim drives a game through Decide + Evolve exactly as the live session does
// (decide, evolve every event with its verdict, append), so a test proves decide
// and evolve agree. After every decision it re-folds the whole stream with the
// pure Fold and requires the same projection (a rebuild must equal the live
// state) and NextSeq == len(events).
type sim struct {
	t      *testing.T
	o      *fakeOracle
	opts   FoldOptions
	s      State
	events []Event
	now    uint64
	n      int // unique uci counter
}

func newSim(t *testing.T, rated bool) *sim {
	t.Helper()
	m := &sim{t: t, o: newFakeOracle(), opts: baseOpts(), s: initialState(), now: 1_000_000}
	for _, ev := range liveStream(rated) {
		m.apply(ev, nil)
	}
	d := m.do(CmdStartClock{}, 0)
	if d.Effect != EffectClockStarted {
		t.Fatalf("start clock effect = %v", d.Effect)
	}
	return m
}

func (m *sim) apply(ev Event, v *Verdict) {
	m.t.Helper()
	next, err := Evolve(m.s, ev, v, m.opts)
	if err != nil {
		m.t.Fatalf("evolve %s: %v", ev.Type, err)
	}
	m.s = next
	m.events = append(m.events, ev)
	if int(m.s.NextSeq) != len(m.events) {
		m.t.Fatalf("NextSeq = %d, want %d", m.s.NextSeq, len(m.events))
	}
}

// do advances the wall clock by advMs, decides cmd, evolves and appends.
func (m *sim) do(cmd Command, advMs uint64) Decision {
	m.t.Helper()
	m.now += advMs
	d, err := Decide(context.Background(), m.s, cmd, m.now, m.opts, m.o)
	if err != nil {
		m.t.Fatalf("decide %T: %v", cmd, err)
	}
	if len(d.Verdicts) != len(d.Events) {
		m.t.Fatalf("verdicts %d != events %d", len(d.Verdicts), len(d.Events))
	}
	for i, ev := range d.Events {
		m.apply(ev, d.Verdicts[i])
	}
	m.checkFold()
	return d
}

// try decides cmd WITHOUT applying (for error and no-op paths).
func (m *sim) try(cmd Command, advMs uint64) (Decision, error) {
	m.t.Helper()
	return Decide(context.Background(), m.s, cmd, m.now+advMs, m.opts, m.o)
}

// move plays a fresh unique legal uci for the side to move after thinkMs.
func (m *sim) move(thinkMs uint64) Decision {
	m.t.Helper()
	m.n++
	return m.moveUCI(m.s.TurnIdx, fmt.Sprintf("m%d", m.n), thinkMs)
}

func (m *sim) moveUCI(seat uint8, uci string, thinkMs uint64) Decision {
	m.t.Helper()
	if _, ok := m.o.legal[uci]; !ok {
		m.o.allow(uci, "fen-"+uci, nil)
	}
	d := m.do(CmdMove{Seat: seat, PlyExpected: m.s.Ply + 1, UCI: uci}, thinkMs)
	if !d.Move.Accepted || d.Move.Reacked {
		m.t.Fatalf("move %s not freshly accepted: %+v", uci, d.Move)
	}
	return d
}

func (m *sim) playTo(ply uint32) {
	m.t.Helper()
	for m.s.Ply < ply {
		m.move(1_000)
	}
}

// checkFold requires a pure Fold of the stream to succeed and equal the live
// state on everything the engine does not supply.
func (m *sim) checkFold() {
	m.t.Helper()
	f, err := Fold(m.events, m.opts)
	if err != nil {
		m.t.Fatalf("Fold(events) failed: %v", err)
	}
	stateEqual(m.t, m.s, f)
}

func stateEqual(t *testing.T, a, b State) {
	t.Helper()
	type view struct {
		Phase   Phase
		Ply     uint32
		Turn    uint8
		Rem     []int64
		Flagged int
		Draw    int
		Last    []int64
		Pend    *PendingTakeback
		Next    uint32
		TS      uint64
		Started bool
		Half    *uint8
		Term    Termination
		RV      []Result
		Hist    [][4]string
	}
	mk := func(s State) view {
		v := view{s.Phase, s.Ply, s.TurnIdx, s.Clock.RemainingMs, s.Clock.FlaggedSeat, s.DrawOfferBy,
			s.LastDrawOfferPly, s.PendingTakeback, s.NextSeq, s.LastChangeTS, s.ClockStartedSet,
			s.HalfTerminal, s.Termination, s.ResultVector, nil}
		for _, e := range s.History {
			v.Hist = append(v.Hist, [4]string{fmt.Sprint(e.Seat), e.UCI, fmt.Sprint(e.ClockAfterMs), fmt.Sprint(e.Seq)})
		}
		return v
	}
	if va, vb := mk(a), mk(b); !reflect.DeepEqual(va, vb) {
		t.Fatalf("live state and Fold disagree:\n live=%+v\n fold=%+v", va, vb)
	}
}
