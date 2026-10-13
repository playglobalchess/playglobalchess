package game

import (
	"context"
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

// This file models the PURE decision logic of the move pipeline (SPEC-S2-GAME-
// SERVER §3.6) against the LegalityOracle + EventStore interfaces. It has NO
// network, NO real engine, NO real store — those are later chunks. It encodes
// the ordering the spec requires: authorize -> validate (engine) -> persist
// (durable, before ack) -> project. The actual WS framing/codec/ack emission is
// out of scope (§3 of the task); this returns a decision the transport layer
// will act on.

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

// MoveIntent is a decoded C->MOVE intent (SPEC-S2-PROTOCOL §4A.3), already
// authenticated: SeatIdx is the SESSION's bound seat (never client-supplied —
// §3.6 step 2, T-1/T-2), not a field the client set in the frame.
type MoveIntent struct {
	GameID      string
	SeatIdx     uint8  // the authenticated session's seat
	PlyExpected uint32 // the client's race guard
	UCI         string
	ElapsedMs   int64 // server-measured think time for this move
	ServerTS    uint64
}

// MoveOutcome is the pipeline's decision. Exactly one of Accepted / Rejected /
// Flagged is true. On Accepted, AppliedSeq/Ply/ClockAfterMs describe the
// committed move and Terminal (if non-nil) is the game-ending condition the
// engine detected.
type MoveOutcome struct {
	Accepted bool
	Rejected bool
	Reason   RejectReason // valid when Rejected
	// Flagged is set when the mover ran out of time ON this move (the move
	// arrived after the clock expired). Not an ack and not a reject — the flag
	// path. FlaggedSeat is the seat that flagged. The transport drives
	// ClockFlagged -> GameEnded (FLAG vs TIMEOUT_VS_INSUFFICIENT via
	// FlagTermination); no MovePlayed is appended.
	Flagged     bool
	FlaggedSeat uint8
	// PlyExpected echoes the server's current ply on a STALE_PLY reject (§5).
	PlyExpected uint32

	AppliedSeq   uint32
	Ply          uint32
	ClockAfterMs uint32
	Terminal     *Termination // engine-detected mate/stalemate/draw, or nil
}

// Pipeline wires the two seams for one game-server node. No global mutable state:
// a Pipeline is an explicit value holding its oracle + store dependencies.
type Pipeline struct {
	Oracle LegalityOracle
	Store  EventStore
	Opts   FoldOptions // time-control expansion for the clock projection
}

// SubmitMove runs the §3.6 pipeline for one move against the current stream and
// returns the decision. It appends a MovePlayed event ONLY after the engine
// accepts the move; it returns an Accepted outcome ONLY after Store.Append
// returns (persist-before-ack, NFR-7). A persistence failure yields a non-nil
// error and NO accepted outcome — the transport must not ack (§5).
//
// Ordering (fixed, §3.6):
//  1. load + fold the stream to the authoritative current state;
//  2. authorize: game known, LIVE, correct seat, sequential ply (idempotent
//     re-ack on an already-persisted ply);
//  3. validate legality via the engine (the only authority, NFR-5);
//  4. persist MovePlayed durably, waiting for the commit;
//  5. return Accepted with the projected ply/clock/terminal.
func (p *Pipeline) SubmitMove(ctx context.Context, in MoveIntent) (MoveOutcome, error) {
	streamID := streamIDFor(in.GameID)
	events, err := p.Store.Load(ctx, streamID)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("pipeline: load: %w", err)
	}
	if len(events) == 0 {
		return reject(RejectUnknownGame, 0), nil
	}

	// Project the current state WITH the engine so CurFEN is authoritative for
	// legality validation (§3.6 step 3 needs the position).
	st, err := FoldWithEngine(ctx, events, p.Opts, p.Oracle)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("pipeline: fold: %w", err)
	}

	// (2) authorize.
	if st.Phase != PhaseLive {
		return reject(RejectGameOver, st.Ply), nil
	}
	if int(in.SeatIdx) >= st.seatCount() {
		return reject(RejectNotYourSeat, st.Ply), nil
	}
	// Idempotent retry: a MOVE whose ply is already persisted re-acks the
	// existing move, no second append (§5 duplicate-MOVE).
	if in.PlyExpected <= st.Ply && in.PlyExpected > 0 {
		if mv := findMove(events, in.PlyExpected); mv != nil {
			if mv.SeatIdx != in.SeatIdx {
				return reject(RejectNotYourSeat, st.Ply), nil
			}
			return MoveOutcome{
				Accepted:     true,
				Ply:          mv.Ply,
				ClockAfterMs: mv.ClockAfterMs,
			}, nil
		}
	}
	if in.SeatIdx != st.TurnIdx {
		return reject(RejectNotYourTurn, st.Ply), nil
	}
	if in.PlyExpected != st.Ply+1 {
		return reject(RejectStalePly, st.Ply), nil
	}

	// (3) validate legality — engine is the only authority (NFR-5).
	verdict, err := p.Oracle.Legality(ctx, st.CurFEN, in.UCI)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("pipeline: legality: %w", err)
	}
	if !verdict.Legal {
		return reject(RejectIllegal, st.Ply), nil
	}

	// Project the clock for the mover to fill clock_after_ms on the event.
	cl, clkErr := st.Clock.Apply(p.Opts.TimeControl, int(in.SeatIdx), in.ElapsedMs)
	if clkErr != nil {
		if errors.Is(clkErr, clock.ErrFlagged) {
			// The mover is out of time: the move arrived too late. This is not a
			// MOVE_REJECTED (there is no FLAG reject reason) and not an ACK — it
			// is the flag path. GS-1 surfaces it as a distinct outcome so the
			// transport/live-session layer drives ClockFlagged -> GameEnded
			// (with the FLAG vs TIMEOUT_VS_INSUFFICIENT split from
			// FlagTermination). No MovePlayed is appended.
			return MoveOutcome{Flagged: true, FlaggedSeat: in.SeatIdx, Ply: st.Ply}, nil
		}
		return MoveOutcome{}, fmt.Errorf("pipeline: clock: %w", clkErr)
	}
	clockAfter := uint32(cl.RemainingMs[in.SeatIdx])

	// (4) persist durably BEFORE any ack (NFR-7).
	ply := st.Ply + 1
	ev := Event{
		Type: EvMovePlayed,
		MovePlayed: &MovePlayed{
			Ply:          ply,
			SeatIdx:      in.SeatIdx,
			UCI:          in.UCI,
			ClockAfterMs: clockAfter,
			ServerTS:     in.ServerTS,
		},
	}
	seq, err := p.Store.Append(ctx, streamID, ev)
	if err != nil {
		// Persist failed after validation: do NOT ack (§5). Surface the error so
		// the transport emits ERROR{INTERNAL}; the move did not happen.
		return MoveOutcome{}, fmt.Errorf("pipeline: persist: %w", err)
	}

	// (5) accepted — only now, after the durable commit.
	return MoveOutcome{
		Accepted:     true,
		AppliedSeq:   seq,
		Ply:          ply,
		ClockAfterMs: clockAfter,
		Terminal:     verdict.Terminal,
	}, nil
}

func reject(r RejectReason, serverPly uint32) MoveOutcome {
	return MoveOutcome{Rejected: true, Reason: r, PlyExpected: serverPly}
}

// findMove returns the MovePlayed body with the given ply, or nil.
func findMove(events []Event, ply uint32) *MovePlayed {
	for i := range events {
		if events[i].Type == EvMovePlayed && events[i].MovePlayed != nil && events[i].MovePlayed.Ply == ply {
			return events[i].MovePlayed
		}
	}
	return nil
}

// streamIDFor builds the stream id for a game (SPEC-S2-PROTOCOL §4A.8:
// "game/{uuid}").
func streamIDFor(gameID string) string { return "game/" + gameID }
