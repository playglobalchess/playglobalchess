package game

import (
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

// GS-7 (SPEC-S2-GAME-SERVER-GS7 §3): ONE evolve. Evolve is the only state
// transition in the game server. Fold, FoldWithEngine, the live session and
// every stream consumer go through it, so a live session can never disagree with
// a rebuild from the store, and no event the server writes can be unfoldable
// (the session evolves before it appends).

// Phase is a game lifecycle state (SPEC-S2-GAME-SERVER §3.2).
type Phase uint8

const (
	// PhaseNone is the zero value: no game yet (before GameCreated).
	PhaseNone Phase = iota
	// PhaseSeeking is modelled for §3.2 completeness; matchmaking does not use it.
	PhaseSeeking
	// PhaseMatched: game created and seats assigned, not all players joined.
	PhaseMatched
	// PhaseLive: all seats joined; moves and in-game actions allowed.
	PhaseLive
	// PhaseEnded: terminal (GameEnded applied). Only RatingApplied may follow.
	PhaseEnded
)

func (p Phase) String() string {
	switch p {
	case PhaseNone:
		return "None"
	case PhaseSeeking:
		return "Seeking"
	case PhaseMatched:
		return "Matched"
	case PhaseLive:
		return "Live"
	case PhaseEnded:
		return "Ended"
	default:
		return "Phase(unknown)"
	}
}

// NoSeat marks "no seat" in the int-valued seat fields of State.
const NoSeat = -1

var (
	// ErrLegacyTakeback: a stream holds the pre-GS-7 TakebackReq/TakebackAccepted
	// events. They stay decodable but are never folded (GS-7 §5.1, Q-V3: no
	// production data exists).
	ErrLegacyTakeback = errors.New("game: legacy TakebackReq/TakebackAccepted events are not supported (GS-7 §5.1)")
	// ErrUnsupportedSeatCount: takeback and draw are 2-seat only (GS-7 §1).
	ErrUnsupportedSeatCount = errors.New("game: takeback and draw are 2-seat only")
)

// PlyEntry is one LIVE (not undone) ply. FEN, Zobrist and Terminal are
// engine-derived and are filled only when Evolve is given a verdict (§3.2).
type PlyEntry struct {
	Seat         uint8
	UCI          string
	ClockAfterMs uint32
	ServerTS     uint64
	Seq          uint32 // the MovePlayed's stream seq (re-ack reports it)
	FEN          string
	Zobrist      uint64
	Terminal     *Termination
}

// PendingTakeback is an open takeback request: proposed by By when the game
// was at AtPly.
type PendingTakeback struct {
	By    uint8
	AtPly uint32
}

// Verdict is the engine's result for one MovePlayed, supplied to Evolve.
type Verdict struct {
	FEN      string
	Zobrist  uint64
	Terminal *Termination
}

// State is the projection folded from a game's event stream. It is a value:
// Evolve returns a new State and never mutates its input's backing arrays.
type State struct {
	Phase Phase

	// Game identity and config (GameCreated).
	PoolID      string
	Rated       bool
	Seats       []Seat
	TimeControl string
	StartFEN    string
	BaseMs      int64 // each seat's starting time (needed by a rewind to ply 0)

	Joined []bool // indexed by seat_idx

	// Live play.
	Ply     uint32
	TurnIdx uint8
	Clock   clock.Clock
	// CurFEN is the current position. Only an engine verdict can advance it
	// (NFR-5), so after a pure Fold it stays at StartFEN; FoldWithEngine and the
	// live session supply verdicts and keep it exact.
	CurFEN  string
	History []PlyEntry // one entry per live ply; History[i] is ply i+1
	NextSeq uint32     // stream seq of the next append

	// Idle-clock baseline (§3.1), epoch ms, read ONLY from event bodies.
	LastChangeTS    uint64
	ClockStartedSet bool

	// Pending prompts.
	DrawOfferBy      int     // seat with a pending draw offer, or NoSeat
	LastDrawOfferPly []int64 // per seat: ply of its last offer, or -1 (cooldown)
	PendingTakeback  *PendingTakeback
	HalfTerminal     *uint8 // a ClockFlagged not yet followed by GameEnded

	// Terminal projection (GameEnded).
	Termination   Termination
	ResultVector  []Result
	RatingApplied bool
}

// seatCount is the number of seats in the game (2 for v1 Standard).
func (s State) seatCount() int { return len(s.Seats) }

// SeatCount is the number of seats in the game.
func (s State) SeatCount() int { return len(s.Seats) }

// PriorKeys is the Zobrist history of the live plies (one key per ply), the
// repetition input the engine needs (Q-GS5). Empty without verdicts.
func (s State) PriorKeys() []uint64 {
	keys := make([]uint64, 0, len(s.History))
	for _, e := range s.History {
		keys = append(keys, e.Zobrist)
	}
	return keys
}

// LastMoveUCI is the last live move, empty at ply 0.
func (s State) LastMoveUCI() string {
	if len(s.History) == 0 {
		return ""
	}
	return s.History[len(s.History)-1].UCI
}

// LastMoveTerminal is the terminal the last live move produced (engine verdict),
// or nil. Used to finish a half-terminal stream on rebuild.
func (s State) LastMoveTerminal() *Termination {
	if len(s.History) == 0 {
		return nil
	}
	return s.History[len(s.History)-1].Terminal
}

// RunningSeat is the seat whose clock is running, or NoSeat: none before the
// clock has started, after a flag, and once the game has ended.
func (s State) RunningSeat() int {
	if s.Phase != PhaseLive || !s.ClockStartedSet || s.HalfTerminal != nil || s.Clock.Flagged() {
		return NoSeat
	}
	return int(s.TurnIdx)
}

// ElapsedMs is the side to move's think time at nowMs: integer epoch ms since
// LastChangeTS, never negative; 0 before the clock has started (§3.5).
func (s State) ElapsedMs(nowMs uint64) int64 {
	if !s.ClockStartedSet || nowMs <= s.LastChangeTS {
		return 0
	}
	return int64(nowMs - s.LastChangeTS)
}

// ClocksAt returns every seat's remaining ms charged up to nowMs: the running
// seat is charged its elapsed think (after the per-move delay), floored at 0;
// increment is not granted (that happens only on a completed move). STATE
// carries these values with server_t = nowMs (§7.4, #123).
func (s State) ClocksAt(nowMs uint64, tc clock.TimeControl) []int64 {
	out := append([]int64(nil), s.Clock.RemainingMs...)
	if seat := s.RunningSeat(); seat != NoSeat && seat < len(out) {
		charged := s.ElapsedMs(nowMs) - tc.PerMoveMs
		if charged > 0 {
			out[seat] -= charged
		}
		if out[seat] < 0 {
			out[seat] = 0
		}
	}
	return out
}

// FoldOptions carries the time-control expansion the clock projection needs
// (the stream carries only the TimeControlId string).
type FoldOptions struct {
	TimeControl clock.TimeControl
}

// initialState is the state before any event.
func initialState() State {
	return State{Phase: PhaseNone, DrawOfferBy: NoSeat}
}

// Fold replays a stream with no engine (NFR-5): ply, turn, clocks, offers,
// History shape and NextSeq are exact; CurFEN and the History engine fields are
// left empty. It is the same Evolve the live session uses.
func Fold(events []Event, opts FoldOptions) (State, error) {
	s := initialState()
	for i := range events {
		next, err := Evolve(s, events[i], nil, opts)
		if err != nil {
			return State{}, fmt.Errorf("fold: event %d (%s): %w", i, events[i].Type, err)
		}
		s = next
	}
	return s, nil
}

// Evolve applies one event. v is the engine verdict for a MovePlayed (nil
// otherwise, or nil throughout a pure Fold). Every value it writes comes from
// the event body, the verdict, or the prior state; no wall clock and no store
// header is read (§3.2, §3.9).
func Evolve(s State, ev Event, v *Verdict, opts FoldOptions) (State, error) {
	var (
		next State
		err  error
	)
	switch ev.Type {
	case EvGameCreated:
		next, err = applyGameCreated(s, ev, opts.TimeControl)
	case EvPlayerJoined:
		next, err = applyPlayerJoined(s, ev)
	case EvClockStarted:
		next, err = applyClockStarted(s, ev)
	case EvMovePlayed:
		next, err = applyMovePlayed(s, ev, v)
	case EvDrawOffered:
		next, err = applyDrawOffered(s, ev)
	case EvDrawAccepted:
		next, err = applyDrawAccepted(s, ev)
	case EvDrawDeclined:
		next, err = applyDrawDeclined(s, ev)
	case EvResigned:
		next, err = applyResigned(s, ev)
	case EvAborted:
		next, err = applyAborted(s, ev)
	case EvTakebackProposed:
		next, err = applyTakebackProposed(s, ev)
	case EvTakebackDeclined:
		next, err = applyTakebackDeclined(s, ev)
	case EvTakebackRewound:
		next, err = applyTakebackRewound(s, ev)
	case EvTakebackReq, EvTakebackAccepted:
		return s, ErrLegacyTakeback
	case EvClockFlagged:
		next, err = applyClockFlagged(s, ev)
	case EvGameEnded:
		next, err = applyGameEnded(s, ev)
	case EvRatingApplied:
		next, err = applyRatingApplied(s, ev)
	default:
		return s, fmt.Errorf("unknown event type %d", ev.Type)
	}
	if err != nil {
		return s, err
	}
	next.NextSeq = s.NextSeq + 1
	return next, nil
}

func applyGameCreated(s State, ev Event, tc clock.TimeControl) (State, error) {
	if s.Phase != PhaseNone {
		return s, fmt.Errorf("GameCreated in phase %s (must be first event)", s.Phase)
	}
	b := ev.GameCreated
	if b == nil {
		return s, fmt.Errorf("GameCreated: nil body")
	}
	if len(b.Seats) == 0 {
		return s, fmt.Errorf("GameCreated: no seats")
	}
	seen := make([]bool, len(b.Seats))
	for _, seat := range b.Seats {
		if int(seat.SeatIdx) >= len(b.Seats) || seen[seat.SeatIdx] {
			return s, fmt.Errorf("GameCreated: seat_idx %d out of range or duplicated", seat.SeatIdx)
		}
		seen[seat.SeatIdx] = true
	}
	cl, err := clock.New(tc, len(b.Seats))
	if err != nil {
		return s, fmt.Errorf("GameCreated: clock init: %w", err)
	}
	s.Phase = PhaseMatched
	s.PoolID = b.PoolID
	s.Rated = b.Rated
	s.Seats = append([]Seat(nil), b.Seats...)
	s.TimeControl = b.TimeControl
	s.StartFEN = b.StartFEN
	s.CurFEN = b.StartFEN
	s.BaseMs = tc.BaseMs
	s.Joined = make([]bool, len(b.Seats))
	s.Ply = 0
	s.TurnIdx = 0
	s.Clock = cl
	s.DrawOfferBy = NoSeat
	s.LastDrawOfferPly = make([]int64, len(b.Seats))
	for i := range s.LastDrawOfferPly {
		s.LastDrawOfferPly[i] = -1
	}
	return s, nil
}

func applyPlayerJoined(s State, ev Event) (State, error) {
	if s.Phase != PhaseMatched {
		return s, fmt.Errorf("PlayerJoined in phase %s (must be Matched)", s.Phase)
	}
	b := ev.PlayerJoined
	if b == nil {
		return s, fmt.Errorf("PlayerJoined: nil body")
	}
	if int(b.SeatIdx) >= s.seatCount() {
		return s, fmt.Errorf("PlayerJoined: seat_idx %d out of range", b.SeatIdx)
	}
	if s.Joined[b.SeatIdx] {
		return s, fmt.Errorf("PlayerJoined: seat %d already joined", b.SeatIdx)
	}
	s.Joined = append([]bool(nil), s.Joined...)
	s.Joined[b.SeatIdx] = true
	all := true
	for _, j := range s.Joined {
		if !j {
			all = false
			break
		}
	}
	if all {
		s.Phase = PhaseLive
	}
	return s, nil
}

// applyClockStarted records the durable first-clock baseline (§3.5).
func applyClockStarted(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("ClockStarted in phase %s (must be Live)", s.Phase)
	}
	if ev.ClockStarted == nil {
		return s, fmt.Errorf("ClockStarted: nil body")
	}
	if s.ClockStartedSet {
		return s, fmt.Errorf("ClockStarted: clock already started")
	}
	s.LastChangeTS = ev.ClockStarted.ServerTS
	s.ClockStartedSet = true
	return s, nil
}

func applyMovePlayed(s State, ev Event, v *Verdict) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("MovePlayed in phase %s (must be Live)", s.Phase)
	}
	b := ev.MovePlayed
	if b == nil {
		return s, fmt.Errorf("MovePlayed: nil body")
	}
	if int(b.SeatIdx) >= s.seatCount() {
		return s, fmt.Errorf("MovePlayed: seat_idx %d out of range", b.SeatIdx)
	}
	if b.SeatIdx != s.TurnIdx {
		return s, fmt.Errorf("MovePlayed: seat %d moved out of turn (turn=%d)", b.SeatIdx, s.TurnIdx)
	}
	if b.Ply != s.Ply+1 {
		return s, fmt.Errorf("MovePlayed: ply %d not sequential (expected %d)", b.Ply, s.Ply+1)
	}
	if s.HalfTerminal != nil {
		return s, fmt.Errorf("MovePlayed after ClockFlagged")
	}
	entry := PlyEntry{Seat: b.SeatIdx, UCI: b.UCI, ClockAfterMs: b.ClockAfterMs, ServerTS: b.ServerTS, Seq: s.NextSeq}
	if v != nil {
		entry.FEN, entry.Zobrist, entry.Terminal = v.FEN, v.Zobrist, v.Terminal
		s.CurFEN = v.FEN
	}
	s.History = append(append([]PlyEntry(nil), s.History...), entry)
	s.Clock = s.Clock.CloneWith(int(b.SeatIdx), int64(b.ClockAfterMs))
	s.Ply = b.Ply
	s.TurnIdx = uint8((int(s.TurnIdx) + 1) % s.seatCount())
	s.LastChangeTS = b.ServerTS
	// A move means the clock is running. Without this, a stream with moves but no
	// ClockStarted would get one appended on rebuild, resetting the baseline.
	s.ClockStartedSet = true
	// Only the responder's move declines (§6.2, §5.2.4); the offerer's or
	// proposer's own move leaves its prompt standing.
	if s.DrawOfferBy != NoSeat && int(b.SeatIdx) != s.DrawOfferBy {
		s.DrawOfferBy = NoSeat
	}
	if s.PendingTakeback != nil && b.SeatIdx != s.PendingTakeback.By {
		s.PendingTakeback = nil
	}
	return s, nil
}

func applyDrawOffered(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "DrawOffered"); err != nil {
		return s, err
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("DrawOffered: %w", err)
	}
	switch s.DrawOfferBy {
	case int(seat):
		return s, nil // a repeat while its own offer is pending: idempotent (§5.1)
	case NoSeat:
	default:
		return s, fmt.Errorf("DrawOffered: seat %d offered while seat %d's offer is pending (that is an accept)", seat, s.DrawOfferBy)
	}
	s.DrawOfferBy = int(seat)
	s.LastDrawOfferPly = append([]int64(nil), s.LastDrawOfferPly...)
	s.LastDrawOfferPly[seat] = int64(s.Ply)
	return s, nil
}

func applyDrawAccepted(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "DrawAccepted"); err != nil {
		return s, err
	}
	if s.DrawOfferBy == NoSeat {
		return s, fmt.Errorf("DrawAccepted: no open draw offer")
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("DrawAccepted: %w", err)
	}
	if int(seat) == s.DrawOfferBy {
		return s, fmt.Errorf("DrawAccepted: seat %d cannot accept its own offer", seat)
	}
	// GameEnded (appended in the same batch) makes the game terminal.
	s.DrawOfferBy = NoSeat
	return s, nil
}

func applyDrawDeclined(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "DrawDeclined"); err != nil {
		return s, err
	}
	if _, err := seatOf(s, ev); err != nil {
		return s, fmt.Errorf("DrawDeclined: %w", err)
	}
	if s.DrawOfferBy == NoSeat {
		return s, fmt.Errorf("DrawDeclined: no open draw offer")
	}
	s.DrawOfferBy = NoSeat
	return s, nil
}

func applyResigned(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("Resigned in phase %s (must be Live)", s.Phase)
	}
	if _, err := seatOf(s, ev); err != nil {
		return s, fmt.Errorf("Resigned: %w", err)
	}
	return s, nil
}

func applyAborted(s State, ev Event) (State, error) {
	if s.Phase != PhaseMatched && s.Phase != PhaseLive {
		return s, fmt.Errorf("Aborted in phase %s (must be Matched or Live)", s.Phase)
	}
	b := ev.Aborted
	if b == nil {
		return s, fmt.Errorf("Aborted: nil body")
	}
	if b.By != SystemActor && int(b.By) >= s.seatCount() {
		return s, fmt.Errorf("Aborted: by %d out of range", b.By)
	}
	return s, nil
}

func applyTakebackProposed(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "TakebackProposed"); err != nil {
		return s, err
	}
	if s.Rated {
		return s, fmt.Errorf("TakebackProposed: not allowed in a rated game (F-01.4)")
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("TakebackProposed: %w", err)
	}
	if s.Ply < 2 {
		return s, fmt.Errorf("TakebackProposed: needs ply >= 2, at %d", s.Ply)
	}
	if s.PendingTakeback != nil {
		return s, fmt.Errorf("TakebackProposed: a request by seat %d is already pending", s.PendingTakeback.By)
	}
	s.PendingTakeback = &PendingTakeback{By: seat, AtPly: s.Ply}
	return s, nil
}

func applyTakebackDeclined(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "TakebackDeclined"); err != nil {
		return s, err
	}
	if _, err := seatOf(s, ev); err != nil {
		return s, fmt.Errorf("TakebackDeclined: %w", err)
	}
	if s.PendingTakeback == nil {
		return s, fmt.Errorf("TakebackDeclined: no pending request")
	}
	s.PendingTakeback = nil
	return s, nil
}

// TakebackToPly is the §5.3 rewind target for request p at the state's
// History: k = 1 if the proposer made the ply it asked about (the accepter was
// to move), else 2.
func TakebackToPly(s State, p PendingTakeback) (uint32, error) {
	if p.AtPly < 2 || int(p.AtPly) > len(s.History) {
		return 0, fmt.Errorf("takeback: at_ply %d outside the live history (%d plies)", p.AtPly, len(s.History))
	}
	k := uint32(2)
	if s.History[p.AtPly-1].Seat == p.By {
		k = 1
	}
	return p.AtPly - k, nil
}

func applyTakebackRewound(s State, ev Event) (State, error) {
	if err := liveTwoSeat(s, "TakebackRewound"); err != nil {
		return s, err
	}
	if s.Rated {
		return s, fmt.Errorf("TakebackRewound: not allowed in a rated game (F-01.4)")
	}
	b := ev.TakebackRewound
	if b == nil {
		return s, fmt.Errorf("TakebackRewound: nil body")
	}
	if int(b.SeatIdx) >= s.seatCount() {
		return s, fmt.Errorf("TakebackRewound: seat_idx %d out of range", b.SeatIdx)
	}
	p := s.PendingTakeback
	if p == nil {
		return s, fmt.Errorf("TakebackRewound: no pending request")
	}
	if b.SeatIdx == p.By {
		return s, fmt.Errorf("TakebackRewound: seat %d cannot accept its own request", b.SeatIdx)
	}
	want, err := TakebackToPly(s, *p)
	if err != nil {
		return s, fmt.Errorf("TakebackRewound: %w", err)
	}
	if b.ToPly != want {
		return s, fmt.Errorf("TakebackRewound: to_ply %d, the §5.3 rule gives %d", b.ToPly, want)
	}
	before := s.History
	kept := append([]PlyEntry(nil), before[:b.ToPly]...)
	// §5.4 clocks: the side to move after the rewind keeps the clock of its own
	// (now undone) move; the other seat keeps its last retained move's clock, or
	// the base time if it has none.
	toMove := before[b.ToPly].Seat
	cl := s.Clock.CloneWith(int(toMove), int64(before[b.ToPly].ClockAfterMs))
	for seat := 0; seat < s.seatCount(); seat++ {
		if uint8(seat) == toMove {
			continue
		}
		rem := s.BaseMs
		for i := len(kept) - 1; i >= 0; i-- {
			if int(kept[i].Seat) == seat {
				rem = int64(kept[i].ClockAfterMs)
				break
			}
		}
		cl = cl.CloneWith(seat, rem)
	}
	s.History = kept
	s.Clock = cl
	s.Ply = b.ToPly
	s.TurnIdx = toMove
	s.CurFEN = s.StartFEN
	if len(kept) > 0 && kept[len(kept)-1].FEN != "" {
		s.CurFEN = kept[len(kept)-1].FEN
	}
	s.LastChangeTS = b.ServerTS
	s.PendingTakeback = nil
	s.DrawOfferBy = NoSeat // [dev] §5.5: the offer was about a position that is gone
	s.LastDrawOfferPly = append([]int64(nil), s.LastDrawOfferPly...)
	for i, l := range s.LastDrawOfferPly {
		if l > int64(b.ToPly) {
			s.LastDrawOfferPly[i] = -1
		}
	}
	return s, nil
}

func applyClockFlagged(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("ClockFlagged in phase %s (must be Live)", s.Phase)
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("ClockFlagged: %w", err)
	}
	if s.HalfTerminal != nil {
		return s, fmt.Errorf("ClockFlagged: seat %d already flagged", *s.HalfTerminal)
	}
	// #157: the flag zeroes the flagged clock in the fold, as it does live.
	s.Clock = s.Clock.CloneWith(int(seat), 0)
	s.Clock.FlaggedSeat = int(seat)
	s.HalfTerminal = &seat
	return s, nil
}

func applyGameEnded(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive && s.Phase != PhaseMatched {
		return s, fmt.Errorf("GameEnded in phase %s (must be Live or Matched)", s.Phase)
	}
	b := ev.GameEnded
	if b == nil {
		return s, fmt.Errorf("GameEnded: nil body")
	}
	if len(b.ResultVector) != s.seatCount() {
		return s, fmt.Errorf("GameEnded: result_vector len %d != seat count %d", len(b.ResultVector), s.seatCount())
	}
	if !knownTermination(b.Termination) {
		return s, fmt.Errorf("GameEnded: unknown termination %d", b.Termination)
	}
	s.Phase = PhaseEnded
	s.Termination = b.Termination
	s.ResultVector = append([]Result(nil), b.ResultVector...)
	s.DrawOfferBy = NoSeat
	s.PendingTakeback = nil
	s.HalfTerminal = nil
	return s, nil
}

func applyRatingApplied(s State, ev Event) (State, error) {
	if s.Phase != PhaseEnded {
		return s, fmt.Errorf("RatingApplied in phase %s (must be Ended)", s.Phase)
	}
	if !s.Rated {
		return s, fmt.Errorf("RatingApplied: game is unrated")
	}
	if s.RatingApplied {
		return s, nil // a compensating correction; rating math is X-4's
	}
	if ev.RatingApplied == nil {
		return s, fmt.Errorf("RatingApplied: nil body")
	}
	s.RatingApplied = true
	return s, nil
}

// liveTwoSeat guards the takeback and draw events: Live phase, 2 seats.
func liveTwoSeat(s State, name string) error {
	if s.Phase != PhaseLive {
		return fmt.Errorf("%s in phase %s (must be Live)", name, s.Phase)
	}
	if s.seatCount() != 2 {
		return fmt.Errorf("%s: %w", name, ErrUnsupportedSeatCount)
	}
	return nil
}

// seatOf extracts and range-checks the seat_idx of a SeatAction event.
func seatOf(s State, ev Event) (uint8, error) {
	if ev.SeatAction == nil {
		return 0, fmt.Errorf("nil seat body")
	}
	if int(ev.SeatAction.SeatIdx) >= s.seatCount() {
		return 0, fmt.Errorf("seat_idx %d out of range", ev.SeatAction.SeatIdx)
	}
	return ev.SeatAction.SeatIdx, nil
}

// knownTermination reports whether t is one of the frozen §4A.5 codes.
func knownTermination(t Termination) bool {
	switch t {
	case TermMate, TermResign, TermFlag, TermAbort, TermDrawAgree,
		TermStalemate, TermRepetition, TermFiftyMove, TermInsufficient,
		TermTimeoutVsInsufficient:
		return true
	default:
		return false
	}
}
