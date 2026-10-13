// Package game is the deterministic in-memory core of a live chess game
// (SPEC-S2-GAME-SERVER §3.2): the lifecycle state machine, modeled as a FOLD
// over an append-only, ordered slice of events. Board state, PGN and the clock
// are PROJECTIONS of that stream and are never written directly (MASTER §VIII.3,
// ADR-10). The engine is the only legality authority (NFR-5): no chess rules
// live in this package.
//
// This file defines the canonical event set. It mirrors — and cites, never
// redefines — SPEC-S2-PROTOCOL §4A.8 (event body layout) and §4A.9 (event type
// enum). The on-the-wire/on-disk BYTES are owned by the codec (ADR-14) and are
// out of scope here; these Go types are the server-side domain representation of
// the same events, which GS-1 folds. Encoding them to codec bytes is the cgo
// seam, a later wiring step.
package game

// EventType enumerates the canonical v1 game events (SPEC-S2-PROTOCOL §4A.9;
// values match the frozen u8 codes so the Go domain and the wire stay aligned,
// though GS-1 does not itself serialize them).
type EventType uint8

const (
	EvGameCreated      EventType = 0x01
	EvPlayerJoined     EventType = 0x02
	EvMovePlayed       EventType = 0x03
	EvDrawOffered      EventType = 0x04
	EvDrawAccepted     EventType = 0x05
	EvResigned         EventType = 0x06
	EvAborted          EventType = 0x07
	EvTakebackReq      EventType = 0x08
	EvTakebackAccepted EventType = 0x09
	EvClockFlagged     EventType = 0x0A
	EvGameEnded        EventType = 0x0B
	EvRatingApplied    EventType = 0x0C
)

func (t EventType) String() string {
	switch t {
	case EvGameCreated:
		return "GameCreated"
	case EvPlayerJoined:
		return "PlayerJoined"
	case EvMovePlayed:
		return "MovePlayed"
	case EvDrawOffered:
		return "DrawOffered"
	case EvDrawAccepted:
		return "DrawAccepted"
	case EvResigned:
		return "Resigned"
	case EvAborted:
		return "Aborted"
	case EvTakebackReq:
		return "TakebackReq"
	case EvTakebackAccepted:
		return "TakebackAccepted"
	case EvClockFlagged:
		return "ClockFlagged"
	case EvGameEnded:
		return "GameEnded"
	case EvRatingApplied:
		return "RatingApplied"
	default:
		return "Event(unknown)"
	}
}

// Termination mirrors SPEC-S2-PROTOCOL §4A.5 (frozen u8 table). It is the
// closed domain of reasons a game ends.
type Termination uint8

const (
	TermMate                  Termination = 0x01
	TermResign                Termination = 0x02
	TermFlag                  Termination = 0x03
	TermAbort                 Termination = 0x04
	TermDrawAgree             Termination = 0x05
	TermStalemate             Termination = 0x06
	TermRepetition            Termination = 0x07
	TermFiftyMove             Termination = 0x08
	TermInsufficient          Termination = 0x09
	TermTimeoutVsInsufficient Termination = 0x0A
)

// Result is a per-seat outcome (SPEC-S2-PROTOCOL §4A.3: 0=loss, 1=draw, 2=win).
type Result uint8

const (
	ResultLoss Result = 0
	ResultDraw Result = 1
	ResultWin  Result = 2
)

// SystemActor is the sentinel seat value for an Aborted event raised by the
// system rather than a seat (SPEC-S2-PROTOCOL §4A.8: Aborted.by 0xFF = system).
const SystemActor uint8 = 0xFF

// Seat is one seat in the game (SPEC-S2-PROTOCOL §3.3 entity shape). seat_idx is
// 0-based; color 0=white, 1=black (§4A.8).
type Seat struct {
	SeatIdx uint8
	Color   uint8
}

// Event is one appended, immutable game event. Exactly one of the typed bodies
// below is set, selected by Type. The server-side domain form (not codec bytes);
// a fold over an ordered []Event produces the game State.
//
// A single struct with optional bodies (rather than a Go interface per event) is
// deliberate: the event set is frozen and closed (SPEC-S2-PROTOCOL §4A.9), the
// fold switches on Type exhaustively, and this keeps an event trivially
// value-comparable and copyable for deterministic re-folds (takeback).
type Event struct {
	Type EventType

	GameCreated   *GameCreated
	PlayerJoined  *PlayerJoined
	MovePlayed    *MovePlayed
	SeatAction    *SeatAction // the single-seat_idx events (§4A.8)
	Aborted       *Aborted
	GameEnded     *GameEnded
	RatingApplied *RatingApplied
}

// GameCreated — §4A.8. start_fen/time_control carried as given; GS-1 does not
// parse the FEN (no chess logic).
type GameCreated struct {
	PoolID      string
	Rated       bool
	Seats       []Seat
	StartFEN    string
	TimeControl string // the TimeControlId string (expanded by the clock layer)
}

// PlayerJoined — §4A.8.
type PlayerJoined struct {
	SeatIdx uint8
	UserID  string
}

// MovePlayed — §4A.8. uci is the engine-validated move string; clock_after_ms
// is the mover's remaining time after the move (the clock layer computes it);
// server_ts is the server persist time (fair-play signal, §3.7).
type MovePlayed struct {
	Ply          uint32
	SeatIdx      uint8
	UCI          string
	ClockAfterMs uint32
	ServerTS     uint64
}

// SeatAction is the shared body for every event whose §4A.8 schema is exactly
// {seat_idx:u8}: DrawOffered, DrawAccepted, Resigned, TakebackReq,
// TakebackAccepted, ClockFlagged. The owning Event.Type disambiguates.
type SeatAction struct {
	SeatIdx uint8
}

// Aborted — §4A.8 {by:u8}; By == SystemActor (0xFF) means a system abort.
type Aborted struct {
	By uint8
}

// GameEnded — §4A.8. ResultVector is indexed by seat_idx.
type GameEnded struct {
	ResultVector []Result
	Termination  Termination
}

// RatingApplied — §4A.8. Produced by the rating service (X-4), appended to the
// stream; GS-1 folds it only to mark the game terminally rated. GS-1 computes no
// rating math.
type RatingApplied struct {
	PoolID  string
	PerSeat []SeatRating
}

// SeatRating — §4A.8 RatingApplied.per_seat entry.
type SeatRating struct {
	SeatIdx uint8
	Before  int16
	After   int16
	RD      uint16
	Vol     float32
}
