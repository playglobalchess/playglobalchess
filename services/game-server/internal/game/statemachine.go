package game

import (
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

// Phase is a game lifecycle state (SPEC-S2-GAME-SERVER §3.2).
type Phase uint8

const (
	// PhaseNone is the zero value: no game yet (before GameCreated).
	PhaseNone Phase = iota
	// PhaseSeeking — a seek is open (GameCreated applied, awaiting players).
	// Modeled for completeness of §3.2; matchmaking (which drives SEEKING) is a
	// later chunk, so GS-1 reaches SEEKING via GameCreated and leaves it on the
	// first PlayerJoined wiring in MATCHED.
	PhaseSeeking
	// PhaseMatched — game created and seats assigned, not all players joined.
	PhaseMatched
	// PhaseLive — all seats joined; moves and in-game actions allowed.
	PhaseLive
	// PhaseEnded — terminal (GameEnded applied). Only RatingApplied may follow.
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

// State is the projection folded from a game's event stream (SPEC-S2-GAME-SERVER
// §3.2). It is a VALUE produced by Fold; it is never mutated in place by callers
// and holds no I/O handles. Board truth beyond what GS-1 needs for routing/authz
// (the current FEN, whose turn, the ply) is tracked; full board state is the
// engine's (NFR-5).
type State struct {
	Phase Phase

	// Game identity / config (from GameCreated).
	PoolID      string
	Rated       bool
	Seats       []Seat
	TimeControl string
	StartFEN    string

	// Which seats have joined (indexed by seat_idx).
	Joined []bool

	// Live play projection.
	Ply uint32 // number of half-moves played (0 before the first move)
	// CurFEN is the current position. The PURE Fold cannot derive a position
	// from a UCI move (that is chess logic — NFR-5), so after Fold CurFEN equals
	// StartFEN regardless of moves played; use FoldWithEngine to project the live
	// position by replaying moves through the engine. Phase/Ply/TurnIdx/Clock are
	// always accurate from either fold.
	CurFEN  string
	TurnIdx uint8 // seat_idx to move (derived from Ply over the seat order)
	Clock   clock.Clock

	// PriorKeys is the folded Zobrist-key history of the positions reached by
	// the moves played so far (one key per MovePlayed, in order). Populated only
	// by FoldWithEngine (the pure Fold leaves it nil — it has no engine to hash
	// with). The live session (GS-3) seeds its running history from this and
	// threads it back into Legality so repetition is detected on the live path
	// (Q-GS5). It follows status_with_history's convention indirectly: to ask
	// about the position after move N, pass PriorKeys[:N].
	PriorKeys []uint64

	// Pending offers/requests (reset appropriately on resolution).
	DrawOfferBy   int // seat_idx with an open draw offer, or -1
	TakebackReqBy int // seat_idx with an open takeback request, or -1

	// Terminal projection (set at GameEnded).
	Termination   Termination
	ResultVector  []Result
	RatingApplied bool
}

// seatCount is the number of seats in the game (2 for v1 Standard).
func (s State) seatCount() int { return len(s.Seats) }

// FoldOptions carries the time-control expansion the clock projection needs. The
// event stream carries only the TimeControlId string (SPEC-S2-PROTOCOL §3.3);
// the expanded {base,increment,per_move} is resolved elsewhere (GET /v1/pools)
// and supplied to the fold. A takeback re-fold reuses the same expansion.
type FoldOptions struct {
	// TimeControl is the expanded form of the GameCreated TimeControlId. The
	// fold uses it only to initialize the per-seat clock at GameCreated (every
	// seat starts at BaseMs). The per-move remaining time is NOT recomputed by
	// the fold — it is read from each MovePlayed's authoritative ClockAfterMs
	// (the server computed it with clock.Apply and persisted it, SPEC-S2-PROTOCOL
	// §4A.8). This keeps the fold self-contained from the stream and makes it
	// trivially deterministic: the same events always project the same clock, no
	// external timing input to vary.
	TimeControl clock.TimeControl
}

// Fold replays an ordered event slice into the resulting State (SPEC-S2-GAME-
// SERVER §3.2: "the game state is a FOLD over an ordered event slice"). It is
// deterministic: the same events + options always produce the same State, which
// is what makes reconnection resync and takeback re-fold correct.
//
// It validates each transition against the §3.2 state machine and returns an
// error on an illegal/rejected transition (e.g. a MovePlayed before LIVE, a
// second GameEnded) — the stream is the authority, so an impossible stream is a
// bug upstream and the fold refuses to project a nonsensical state rather than
// silently accepting it.
func Fold(events []Event, opts FoldOptions) (State, error) {
	s := State{Phase: PhaseNone, DrawOfferBy: -1, TakebackReqBy: -1}
	for i := range events {
		next, err := apply(s, events[i], opts.TimeControl)
		if err != nil {
			return State{}, fmt.Errorf("fold: event %d (%s): %w", i, events[i].Type, err)
		}
		s = next
	}
	return s, nil
}

// apply is the single transition function: (state, event) -> state' or error. It
// is the §3.2 state machine. Keeping every legal transition — and the rejection
// of every illegal one — in this one switch makes the machine auditable.
func apply(s State, ev Event, tc clock.TimeControl) (State, error) {
	switch ev.Type {
	case EvGameCreated:
		return applyGameCreated(s, ev, tc)
	case EvPlayerJoined:
		return applyPlayerJoined(s, ev)
	case EvMovePlayed:
		return applyMovePlayed(s, ev)
	case EvDrawOffered:
		return applyDrawOffered(s, ev)
	case EvDrawAccepted:
		return applyDrawAccepted(s, ev)
	case EvResigned:
		return applyResigned(s, ev)
	case EvAborted:
		return applyAborted(s, ev)
	case EvTakebackReq:
		return applyTakebackReq(s, ev)
	case EvTakebackAccepted:
		return applyTakebackAccepted(s, ev)
	case EvClockFlagged:
		return applyClockFlagged(s, ev)
	case EvGameEnded:
		return applyGameEnded(s, ev)
	case EvRatingApplied:
		return applyRatingApplied(s, ev)
	default:
		return s, fmt.Errorf("unknown event type %d", ev.Type)
	}
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
	// seat_idx must be the contiguous 0-based set {0..n-1} (SPEC-S2-PROTOCOL
	// §3.3). Reject gaps/dups so downstream seat indexing is safe.
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
	s.Joined = make([]bool, len(b.Seats))
	s.Ply = 0
	s.TurnIdx = 0
	s.Clock = cl
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
	// Transition to LIVE once every seat has joined.
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

func applyMovePlayed(s State, ev Event) (State, error) {
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
	// Project the clock from the move's authoritative ClockAfterMs: the server
	// computed the mover's remaining time with clock.Apply and persisted it on
	// the event (SPEC-S2-PROTOCOL §4A.8). The fold trusts the stored value
	// rather than recomputing — the event is the truth, and a MovePlayed is only
	// ever appended for a non-flagging move (a flag is ClockFlagged, not a
	// MovePlayed), so a stored ClockAfterMs is always a valid remaining time.
	// Only the mover's clock changes; the opponent's is unchanged (§4A.3:
	// MOVE_ACCEPTED carries only the mover's clock_after_ms).
	cl := s.Clock.CloneWith(int(b.SeatIdx), int64(b.ClockAfterMs))
	s.Clock = cl
	s.Ply = b.Ply
	// CurFEN is engine-authoritative and is NOT derived here (the pure fold has
	// no chess logic — NFR-5); FoldWithEngine projects it by replaying moves
	// through the engine. The pure fold advances only ply/turn/clock.
	s.TurnIdx = uint8((int(s.TurnIdx) + 1) % s.seatCount())
	// A new move clears a pending draw offer (standard: a move declines a
	// standing offer). Takeback requests persist until resolved.
	s.DrawOfferBy = -1
	return s, nil
}

func applyDrawOffered(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("DrawOffered in phase %s (must be Live)", s.Phase)
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("DrawOffered: %w", err)
	}
	s.DrawOfferBy = int(seat)
	return s, nil
}

func applyDrawAccepted(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("DrawAccepted in phase %s (must be Live)", s.Phase)
	}
	if s.DrawOfferBy < 0 {
		return s, fmt.Errorf("DrawAccepted: no open draw offer")
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("DrawAccepted: %w", err)
	}
	if int(seat) == s.DrawOfferBy {
		return s, fmt.Errorf("DrawAccepted: seat %d cannot accept its own offer", seat)
	}
	// Note: the actual ENDED transition is driven by the GameEnded event that
	// the pipeline appends after a DrawAccepted (SPEC-S2-GAME-SERVER §3.2:
	// "DrawAccepted ... -> ENDED (GameEnded + term=DRAW_AGREE)"). DrawAccepted
	// alone clears the offer; GameEnded makes it terminal. This keeps every
	// terminal transition flowing through exactly one event (GameEnded).
	s.DrawOfferBy = -1
	return s, nil
}

func applyResigned(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("Resigned in phase %s (must be Live)", s.Phase)
	}
	if _, err := seatOf(s, ev); err != nil {
		return s, fmt.Errorf("Resigned: %w", err)
	}
	// Terminal transition is the following GameEnded{RESIGN}. Resigned records
	// intent; GameEnded ends the game (single terminal event, as above).
	return s, nil
}

func applyAborted(s State, ev Event) (State, error) {
	// Abort is allowed from MATCHED or early LIVE (SPEC-S2-GAME-SERVER §3.2:
	// "MATCHED/LIVE(early) --Aborted--> ENDED"). "early" (pre-first-move policy
	// window) is a pipeline/policy concern; the fold accepts an abort from
	// MATCHED or LIVE and leaves the terminal transition to GameEnded{ABORT}.
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

func applyTakebackReq(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("TakebackReq in phase %s (must be Live)", s.Phase)
	}
	if s.Rated {
		return s, fmt.Errorf("TakebackReq: not allowed in a rated game (F-01.4 unrated only)")
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("TakebackReq: %w", err)
	}
	if s.Ply == 0 {
		return s, fmt.Errorf("TakebackReq: no move to take back")
	}
	s.TakebackReqBy = int(seat)
	return s, nil
}

func applyTakebackAccepted(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("TakebackAccepted in phase %s (must be Live)", s.Phase)
	}
	if s.Rated {
		return s, fmt.Errorf("TakebackAccepted: not allowed in a rated game (F-01.4 unrated only)")
	}
	if s.TakebackReqBy < 0 {
		return s, fmt.Errorf("TakebackAccepted: no open takeback request")
	}
	seat, err := seatOf(s, ev)
	if err != nil {
		return s, fmt.Errorf("TakebackAccepted: %w", err)
	}
	if int(seat) == s.TakebackReqBy {
		return s, fmt.Errorf("TakebackAccepted: seat %d cannot accept its own request", seat)
	}
	// The projection roll-back to ply-n is performed by re-folding the stream up
	// to the pre-move point (SPEC-S2-GAME-SERVER §3.2: "re-folding to ply-n").
	// Because the stream is append-only, the mechanism lives in RefoldTakeback
	// (statemachine_takeback.go), not here: a TakebackAccepted event in a replay
	// just clears the pending request; the authoritative truncated projection is
	// produced by re-folding the retained prefix. See RefoldTakeback.
	s.TakebackReqBy = -1
	return s, nil
}

func applyClockFlagged(s State, ev Event) (State, error) {
	if s.Phase != PhaseLive {
		return s, fmt.Errorf("ClockFlagged in phase %s (must be Live)", s.Phase)
	}
	if _, err := seatOf(s, ev); err != nil {
		return s, fmt.Errorf("ClockFlagged: %w", err)
	}
	// Terminal transition is the following GameEnded{FLAG|TIMEOUT_VS_INSUFFICIENT}.
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
	s.DrawOfferBy = -1
	s.TakebackReqBy = -1
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
		// A compensating RatingApplied is a legitimate later correction
		// (SPEC-S2-PROTOCOL §3.2 immutability). The flag is already set; folding
		// another is allowed (the latest wins at the projection level). We keep
		// it idempotent-at-the-flag and leave rating math to X-4.
		return s, nil
	}
	if ev.RatingApplied == nil {
		return s, fmt.Errorf("RatingApplied: nil body")
	}
	s.RatingApplied = true
	return s, nil
}

// seatOf extracts and range-checks the seat_idx of a single-seat event
// (SeatAction body). Returns an error for a nil body or an out-of-range seat.
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
