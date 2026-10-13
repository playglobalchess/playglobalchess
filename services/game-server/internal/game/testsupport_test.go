package game

import (
	"context"
	"fmt"

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
