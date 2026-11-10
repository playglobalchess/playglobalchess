package game

import (
	"context"
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

// GS-7 (SPEC-S2-GAME-SERVER-GS7 §3.3): ONE decide. Decide validates a command
// against the current State and returns the events to append; it mutates
// nothing and appends nothing. A MOVE makes exactly one engine call (the hot
// path), and its verdict is returned so the caller can Evolve without a second.
// The caller evolves the events BEFORE appending them (so an unfoldable event
// never reaches the store) and appends at State.NextSeq.

// RejectReason mirrors SPEC-S2-PROTOCOL §4A.4 (MOVE_REJECTED reason enum).
type RejectReason uint8

const (
	RejectIllegal     RejectReason = 0x01
	RejectStalePly    RejectReason = 0x02
	RejectGameOver    RejectReason = 0x03
	RejectNotYourTurn RejectReason = 0x04
	RejectNotYourSeat RejectReason = 0x05
	RejectUnknownGame RejectReason = 0x06
)

func (r RejectReason) String() string {
	switch r {
	case RejectIllegal:
		return "ILLEGAL"
	case RejectStalePly:
		return "STALE_PLY"
	case RejectGameOver:
		return "GAME_OVER"
	case RejectNotYourTurn:
		return "NOT_YOUR_TURN"
	case RejectNotYourSeat:
		return "NOT_YOUR_SEAT"
	case RejectUnknownGame:
		return "UNKNOWN_GAME"
	default:
		return "Reject(unknown)"
	}
}

// MoveOutcome is the decision for a MOVE. Exactly one of Accepted / Rejected /
// Flagged is true.
type MoveOutcome struct {
	Accepted bool
	// Reacked marks an Accepted outcome that is an idempotent re-ack of a move
	// already live at that ply (§4): nothing is appended. The transport sends
	// MOVE_ACCEPTED to the sender only. Callers tell a re-ack from a new move by
	// this flag, never by ply arithmetic.
	Reacked bool
	// EndedGame: the accepted (or re-acked) move ended the game.
	EndedGame bool

	Rejected bool
	Reason   RejectReason
	// PlyExpected echoes the server's current ply on a reject.
	PlyExpected uint32

	// Flagged: the mover's time ran out before the move arrived; the game ends
	// by flag and no MovePlayed is appended.
	Flagged     bool
	FlaggedSeat uint8

	AppliedSeq   uint32
	Ply          uint32
	ClockAfterMs uint32
	UCI          string // the live move at Ply (the persisted one on a re-ack)
	NewFEN       string
	NewZobrist   uint64
	Terminal     *Termination
}

// Command errors. Each fails one command and leaves state and store untouched.
var (
	// ErrGameNotLive: the action needs a live game (its events are Live-only).
	ErrGameNotLive = errors.New("game: action requires a live game")
	// ErrAbortTooLate: past abortBeforePly a quit is a resign, not an abort.
	ErrAbortTooLate = errors.New("game: too late to abort (game started) — resign instead")
	// ErrNoDrawToAccept: AcceptDraw found no pending offer from the other seat.
	ErrNoDrawToAccept = errors.New("game: no pending draw offer to accept")
	// ErrTakebackNotAllowed: takeback in a rated game (F-01.4) or a game whose
	// seat count is not 2. The transport maps it to PROTOCOL_VIOLATION.
	ErrTakebackNotAllowed = errors.New("game: takeback is allowed only in an unrated 2-seat game")
)

// Command is a validated request for Decide. Seats are the authenticated
// session's seat, never a client-asserted one.
type Command interface{ isCommand() }

type (
	CmdMove struct {
		Seat        uint8
		PlyExpected uint32
		UCI         string
	}
	CmdTickFlag   struct{}
	CmdStartClock struct{}
	CmdResign     struct{ By uint8 }
	CmdAbort      struct {
		By             uint8
		AbortBeforePly uint32
	}
	CmdOfferDraw       struct{ By uint8 }
	CmdAcceptDraw      struct{ By uint8 }
	CmdDeclineDraw     struct{ By uint8 }
	CmdProposeTakeback struct{ By uint8 }
	CmdAcceptTakeback  struct {
		By    uint8
		AtPly uint32
	}
	CmdDeclineTakeback struct {
		By    uint8
		AtPly uint32
	}
	// CmdFinishHalfTerminal closes a stream left half-terminal (a ClockFlagged
	// or a terminal move with no GameEnded), on rebuild.
	CmdFinishHalfTerminal struct{}
)

func (CmdMove) isCommand()               {}
func (CmdTickFlag) isCommand()           {}
func (CmdStartClock) isCommand()         {}
func (CmdResign) isCommand()             {}
func (CmdAbort) isCommand()              {}
func (CmdOfferDraw) isCommand()          {}
func (CmdAcceptDraw) isCommand()         {}
func (CmdDeclineDraw) isCommand()        {}
func (CmdProposeTakeback) isCommand()    {}
func (CmdAcceptTakeback) isCommand()     {}
func (CmdDeclineTakeback) isCommand()    {}
func (CmdFinishHalfTerminal) isCommand() {}

// Effect says what a decision did, for the transport's notifications.
type Effect uint8

const (
	// EffectNone: nothing changed and nothing needs saying (an idempotent
	// repeat, or an action after the game ended).
	EffectNone Effect = iota
	// EffectIgnored: nothing changed, but the sender's UI is out of step (a
	// stale accept, an offer the rules refuse); push STATE to the sender.
	EffectIgnored
	EffectMove
	EffectDrawOffered
	EffectDrawDeclined
	EffectTakebackProposed
	EffectTakebackDeclined
	EffectTakebackRewound
	EffectClockStarted
	// EffectEnded: the game ended (resign, abort, draw agreement, flag, mate,
	// recovery). A MOVE that ends the game reports EffectMove with EndedGame.
	EffectEnded
)

// Decision is Decide's result. Verdicts is parallel to Events (nil entries for
// non-move events).
type Decision struct {
	Events   []Event
	Verdicts []*Verdict
	Effect   Effect
	Move     MoveOutcome // for CmdMove
	AtPly    uint32      // the pending request's at_ply, for EffectTakebackProposed
}

func (d *Decision) add(ev Event, v *Verdict) {
	d.Events = append(d.Events, ev)
	d.Verdicts = append(d.Verdicts, v)
}

// Decide validates cmd against s at wall time nowMs (epoch ms, read once by
// the caller and used for every value this command writes, §3.1).
func Decide(ctx context.Context, s State, cmd Command, nowMs uint64, opts FoldOptions, oracle LegalityOracle) (Decision, error) {
	switch c := cmd.(type) {
	case CmdMove:
		return decideMove(ctx, s, c, nowMs, opts, oracle)
	case CmdTickFlag:
		return decideTick(ctx, s, nowMs, opts, oracle)
	case CmdStartClock:
		var d Decision
		if s.Phase == PhaseLive && !s.ClockStartedSet {
			d.add(Event{Type: EvClockStarted, ClockStarted: &ClockStarted{ServerTS: nowMs}}, nil)
			d.Effect = EffectClockStarted
		}
		return d, nil
	case CmdResign:
		return decideResign(s, c)
	case CmdAbort:
		return decideAbort(s, c)
	case CmdOfferDraw:
		return decideOfferDraw(s, c.By, false)
	case CmdAcceptDraw:
		return decideOfferDraw(s, c.By, true)
	case CmdDeclineDraw:
		return decideDeclineDraw(s, c)
	case CmdProposeTakeback:
		return decideProposeTakeback(s, c)
	case CmdAcceptTakeback:
		return decideAcceptTakeback(ctx, s, c, nowMs, opts, oracle)
	case CmdDeclineTakeback:
		return decideDeclineTakeback(s, c)
	case CmdFinishHalfTerminal:
		return decideFinishHalfTerminal(ctx, s, oracle)
	default:
		return Decision{}, fmt.Errorf("decide: unknown command %T", cmd)
	}
}

func rejectMove(r RejectReason, serverPly uint32) Decision {
	return Decision{Move: MoveOutcome{Rejected: true, Reason: r, PlyExpected: serverPly}}
}

// decideMove implements the §4 idempotency table (rows in order), then the
// flag check, then the one engine call.
func decideMove(ctx context.Context, s State, c CmdMove, nowMs uint64, opts FoldOptions, oracle LegalityOracle) (Decision, error) {
	if s.Phase == PhaseNone {
		return rejectMove(RejectUnknownGame, 0), nil
	}
	if int(c.Seat) >= s.seatCount() {
		return rejectMove(RejectNotYourSeat, s.Ply), nil
	}
	// A ply already live: re-ack the same move, refuse anything else. Checked
	// BEFORE the phase, so a resent mating move after the end is re-acked (#122).
	if c.PlyExpected >= 1 && c.PlyExpected <= s.Ply {
		e := s.History[c.PlyExpected-1]
		switch {
		case e.Seat != c.Seat:
			return rejectMove(RejectNotYourSeat, s.Ply), nil
		case e.UCI != c.UCI:
			return rejectMove(RejectStalePly, s.Ply), nil
		}
		return Decision{Effect: EffectNone, Move: MoveOutcome{
			Accepted: true, Reacked: true, AppliedSeq: e.Seq, Ply: c.PlyExpected, UCI: e.UCI,
			ClockAfterMs: e.ClockAfterMs, NewFEN: e.FEN, NewZobrist: e.Zobrist, Terminal: e.Terminal,
			EndedGame: s.Phase == PhaseEnded && c.PlyExpected == s.Ply,
		}}, nil
	}
	if s.Phase != PhaseLive || s.HalfTerminal != nil {
		return rejectMove(RejectGameOver, s.Ply), nil
	}
	if c.PlyExpected != s.Ply+1 {
		return rejectMove(RejectStalePly, s.Ply), nil
	}
	if c.Seat != s.TurnIdx {
		return rejectMove(RejectNotYourTurn, s.Ply), nil
	}
	// The flag check: a move that arrives after the mover's time ran out ends
	// the game by flag (no refund, no MovePlayed).
	cl, clkErr := s.Clock.Apply(opts.TimeControl, int(c.Seat), s.ElapsedMs(nowMs))
	if clkErr != nil {
		if errors.Is(clkErr, clock.ErrFlagged) {
			d, err := flagDecision(ctx, s, c.Seat, oracle)
			d.Move = MoveOutcome{Flagged: true, FlaggedSeat: c.Seat, Ply: s.Ply}
			return d, err
		}
		return Decision{}, fmt.Errorf("decide: clock: %w", clkErr)
	}
	if oracle == nil {
		return Decision{}, fmt.Errorf("decide: nil oracle")
	}
	lv, err := oracle.Legality(ctx, s.CurFEN, c.UCI, s.PriorKeys())
	if err != nil {
		return Decision{}, fmt.Errorf("decide: legality: %w", err)
	}
	if !lv.Legal {
		return rejectMove(RejectIllegal, s.Ply), nil
	}
	clockAfter := uint32(cl.RemainingMs[c.Seat])
	ply := s.Ply + 1
	v := &Verdict{FEN: lv.NewFEN, Zobrist: lv.NewZobrist, Terminal: lv.Terminal}
	d := Decision{Effect: EffectMove}
	d.add(Event{Type: EvMovePlayed, MovePlayed: &MovePlayed{
		Ply: ply, SeatIdx: c.Seat, UCI: c.UCI, ClockAfterMs: clockAfter, ServerTS: nowMs,
	}}, v)
	if lv.Terminal != nil {
		// Move plus GameEnded is one atomic batch (§3.7, #67).
		d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{
			ResultVector: WinnerFromMover(c.Seat, *lv.Terminal, s.seatCount()), Termination: *lv.Terminal,
		}}, nil)
	}
	d.Move = MoveOutcome{
		Accepted: true, AppliedSeq: s.NextSeq, Ply: ply, UCI: c.UCI, ClockAfterMs: clockAfter,
		NewFEN: lv.NewFEN, NewZobrist: lv.NewZobrist, Terminal: lv.Terminal, EndedGame: lv.Terminal != nil,
	}
	return d, nil
}

// decideTick is the idle flag check (§3.5): charge the side to move up to now
// from the CURRENT state, so after a rewind the right seat is charged.
func decideTick(ctx context.Context, s State, nowMs uint64, opts FoldOptions, oracle LegalityOracle) (Decision, error) {
	if s.RunningSeat() == NoSeat {
		return Decision{}, nil
	}
	if flagged, err := sideToMoveFlagged(s, nowMs, opts); err != nil || !flagged {
		return Decision{}, err
	}
	return flagDecision(ctx, s, s.TurnIdx, oracle)
}

func sideToMoveFlagged(s State, nowMs uint64, opts FoldOptions) (bool, error) {
	if s.RunningSeat() == NoSeat {
		return false, nil
	}
	_, err := s.Clock.Apply(opts.TimeControl, int(s.TurnIdx), s.ElapsedMs(nowMs))
	if err == nil {
		return false, nil
	}
	if errors.Is(err, clock.ErrFlagged) {
		return true, nil
	}
	return false, fmt.Errorf("decide: clock: %w", err)
}

// flagDecision ends the game by flag for seat: ClockFlagged + GameEnded as one
// batch, FLAG vs TIMEOUT_VS_INSUFFICIENT decided by the engine.
func flagDecision(ctx context.Context, s State, seat uint8, oracle LegalityOracle) (Decision, error) {
	term, err := FlagTermination(ctx, oracle, s.CurFEN, seat, s.seatCount())
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Effect: EffectEnded}
	d.add(Event{Type: EvClockFlagged, SeatAction: &SeatAction{SeatIdx: seat}}, nil)
	d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: FlagResultVector(term, seat, s.seatCount()), Termination: term}}, nil)
	return d, nil
}

func decideResign(s State, c CmdResign) (Decision, error) {
	if s.Phase == PhaseEnded {
		return Decision{}, nil
	}
	if s.Phase != PhaseLive {
		return Decision{}, ErrGameNotLive
	}
	if s.seatCount() != 2 {
		return Decision{}, ErrUnsupportedSeatCount
	}
	if int(c.By) >= s.seatCount() {
		return Decision{}, fmt.Errorf("decide: seat %d out of range", c.By)
	}
	rv := make([]Result, 2)
	rv[c.By], rv[1-c.By] = ResultLoss, ResultWin
	d := Decision{Effect: EffectEnded}
	d.add(Event{Type: EvResigned, SeatAction: &SeatAction{SeatIdx: c.By}}, nil)
	d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: rv, Termination: TermResign}}, nil)
	return d, nil
}

func decideAbort(s State, c CmdAbort) (Decision, error) {
	if s.Phase == PhaseEnded {
		return Decision{}, nil
	}
	if s.Phase != PhaseLive && s.Phase != PhaseMatched {
		return Decision{}, ErrGameNotLive
	}
	if s.Ply >= c.AbortBeforePly {
		return Decision{}, ErrAbortTooLate
	}
	if s.seatCount() != 2 {
		return Decision{}, ErrUnsupportedSeatCount
	}
	d := Decision{Effect: EffectEnded}
	d.add(Event{Type: EvAborted, Aborted: &Aborted{By: c.By}}, nil)
	d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: []Result{ResultDraw, ResultDraw}, Termination: TermAbort}}, nil)
	return d, nil
}

// drawCooldownPlies: a seat may not offer again within 20 plies of its last
// offer (§6.3).
const drawCooldownPlies = 20

// decideOfferDraw is OFFER_DRAW (§6.3), in order: accept the other seat's
// pending offer; no-op on a repeat; otherwise offer if ply >= 2 and outside the
// cooldown. acceptOnly (the explicit AcceptDraw) returns ErrNoDrawToAccept
// instead of offering.
func decideOfferDraw(s State, by uint8, acceptOnly bool) (Decision, error) {
	if s.Phase == PhaseEnded {
		return Decision{}, nil
	}
	if s.Phase != PhaseLive {
		return Decision{}, ErrGameNotLive
	}
	if s.seatCount() != 2 {
		return Decision{}, ErrUnsupportedSeatCount
	}
	if int(by) >= s.seatCount() {
		return Decision{}, fmt.Errorf("decide: seat %d out of range", by)
	}
	if s.DrawOfferBy != NoSeat && s.DrawOfferBy != int(by) {
		d := Decision{Effect: EffectEnded}
		d.add(Event{Type: EvDrawAccepted, SeatAction: &SeatAction{SeatIdx: by}}, nil)
		d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: []Result{ResultDraw, ResultDraw}, Termination: TermDrawAgree}}, nil)
		return d, nil
	}
	if acceptOnly {
		return Decision{}, ErrNoDrawToAccept
	}
	if s.DrawOfferBy == int(by) {
		return Decision{}, nil
	}
	if s.Ply < 2 {
		return Decision{Effect: EffectIgnored}, nil
	}
	if last := s.LastDrawOfferPly[by]; last >= 0 && last >= int64(s.Ply)-drawCooldownPlies {
		return Decision{Effect: EffectIgnored}, nil
	}
	d := Decision{Effect: EffectDrawOffered}
	d.add(Event{Type: EvDrawOffered, SeatAction: &SeatAction{SeatIdx: by}}, nil)
	return d, nil
}

// decideDeclineDraw: the responder declines or the offerer withdraws.
func decideDeclineDraw(s State, c CmdDeclineDraw) (Decision, error) {
	if s.Phase == PhaseEnded {
		return Decision{}, nil
	}
	if s.Phase != PhaseLive || s.DrawOfferBy == NoSeat {
		return Decision{Effect: EffectIgnored}, nil
	}
	if s.seatCount() != 2 {
		return Decision{}, ErrUnsupportedSeatCount
	}
	if int(c.By) >= s.seatCount() {
		return Decision{}, fmt.Errorf("decide: seat %d out of range", c.By)
	}
	d := Decision{Effect: EffectDrawDeclined}
	d.add(Event{Type: EvDrawDeclined, SeatAction: &SeatAction{SeatIdx: c.By}}, nil)
	return d, nil
}

func takebackAllowed(s State) error {
	if s.Rated || s.seatCount() != 2 {
		return ErrTakebackNotAllowed
	}
	return nil
}

// decideProposeTakeback is TAKEBACK (§5.2 step 1).
func decideProposeTakeback(s State, c CmdProposeTakeback) (Decision, error) {
	if s.Phase == PhaseEnded {
		return Decision{}, nil
	}
	if err := takebackAllowed(s); err != nil {
		return Decision{}, err
	}
	if s.Phase != PhaseLive {
		return Decision{}, ErrGameNotLive
	}
	if int(c.By) >= s.seatCount() {
		return Decision{}, fmt.Errorf("decide: seat %d out of range", c.By)
	}
	if p := s.PendingTakeback; p != nil {
		if p.By == c.By {
			return Decision{}, nil // a repeat of its own request
		}
		return Decision{Effect: EffectIgnored}, nil // [dev] the responder's TAKEBACK is not an accept
	}
	if s.Ply < 2 {
		return Decision{Effect: EffectIgnored}, nil
	}
	d := Decision{Effect: EffectTakebackProposed, AtPly: s.Ply}
	d.add(Event{Type: EvTakebackProposed, SeatAction: &SeatAction{SeatIdx: c.By}}, nil)
	return d, nil
}

// decideAcceptTakeback is TAKEBACK_ACCEPT (§5.2 step 2): only a matching
// request from the other seat is accepted; the flag check runs first.
func decideAcceptTakeback(ctx context.Context, s State, c CmdAcceptTakeback, nowMs uint64, opts FoldOptions, oracle LegalityOracle) (Decision, error) {
	p := s.PendingTakeback
	if s.Phase != PhaseLive || p == nil || p.By == c.By || p.AtPly != c.AtPly || s.HalfTerminal != nil {
		if s.Phase == PhaseEnded {
			return Decision{}, nil
		}
		return Decision{Effect: EffectIgnored}, nil
	}
	if err := takebackAllowed(s); err != nil {
		return Decision{}, err
	}
	if flagged, err := sideToMoveFlagged(s, nowMs, opts); err != nil {
		return Decision{}, err
	} else if flagged {
		return flagDecision(ctx, s, s.TurnIdx, oracle) // no refund for a flagged player
	}
	toPly, err := TakebackToPly(s, *p)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Effect: EffectTakebackRewound}
	d.add(Event{Type: EvTakebackRewound, TakebackRewound: &TakebackRewound{SeatIdx: c.By, ToPly: toPly, ServerTS: nowMs}}, nil)
	return d, nil
}

// decideDeclineTakeback is TAKEBACK_DECLINE: decline by the responder or
// cancel by the proposer (§5.2 step 3).
func decideDeclineTakeback(s State, c CmdDeclineTakeback) (Decision, error) {
	p := s.PendingTakeback
	if s.Phase != PhaseLive || p == nil || p.AtPly != c.AtPly {
		if s.Phase == PhaseEnded {
			return Decision{}, nil
		}
		return Decision{Effect: EffectIgnored}, nil
	}
	if int(c.By) >= s.seatCount() {
		return Decision{}, fmt.Errorf("decide: seat %d out of range", c.By)
	}
	d := Decision{Effect: EffectTakebackDeclined}
	d.add(Event{Type: EvTakebackDeclined, SeatAction: &SeatAction{SeatIdx: c.By}}, nil)
	return d, nil
}

// decideFinishHalfTerminal closes a stream that is terminal in substance but
// has no GameEnded: a ClockFlagged (HalfTerminal), or a last move whose engine
// verdict was terminal. Needs a state folded WITH the engine.
func decideFinishHalfTerminal(ctx context.Context, s State, oracle LegalityOracle) (Decision, error) {
	if s.Phase != PhaseLive {
		return Decision{}, nil
	}
	d := Decision{Effect: EffectEnded}
	switch {
	case s.HalfTerminal != nil:
		term, err := FlagTermination(ctx, oracle, s.CurFEN, *s.HalfTerminal, s.seatCount())
		if err != nil {
			return Decision{}, err
		}
		d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: FlagResultVector(term, *s.HalfTerminal, s.seatCount()), Termination: term}}, nil)
	case s.LastMoveTerminal() != nil:
		mover := s.History[len(s.History)-1].Seat
		term := *s.LastMoveTerminal()
		d.add(Event{Type: EvGameEnded, GameEnded: &GameEnded{ResultVector: WinnerFromMover(mover, term, s.seatCount()), Termination: term}}, nil)
	default:
		return Decision{}, nil
	}
	return d, nil
}

// WinnerFromMover is the result of a move-produced terminal: a mate wins for
// the mover; every other terminal is a draw for all seats.
func WinnerFromMover(mover uint8, term Termination, seatCount int) []Result {
	rv := make([]Result, seatCount)
	for i := range rv {
		rv[i] = ResultDraw
	}
	if term == TermMate {
		for i := range rv {
			rv[i] = ResultLoss
		}
		rv[mover] = ResultWin
	}
	return rv
}

// FlagResultVector: the flagged seat loses on FLAG; TIMEOUT_VS_INSUFFICIENT is
// a draw for all seats.
func FlagResultVector(term Termination, flaggedSeat uint8, seatCount int) []Result {
	rv := make([]Result, seatCount)
	for i := range rv {
		rv[i] = ResultDraw
	}
	if term != TermTimeoutVsInsufficient {
		for i := range rv {
			rv[i] = ResultWin
		}
		rv[flaggedSeat] = ResultLoss
	}
	return rv
}

// StreamIDFor builds the stream id for a game (SPEC-S2-PROTOCOL §4A.8).
func StreamIDFor(gameID string) string { return "game/" + gameID }
