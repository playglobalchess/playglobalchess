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
//
// CurFEN is the authoritative current position, supplied BY THE SERVER (the
// live-session layer, a later chunk, holds the game's position in memory and
// advances it with each accepted move's verdict.NewFEN). It is NOT
// client-supplied — it is server truth (NFR-5). The pipeline passes it straight
// to the engine to validate this one move; it does NOT reconstruct the position
// from the stream on the move path (see SubmitMove). A fresh in-memory session
// seeds CurFEN once from FoldWithEngine (reconnection/node-failover resume,
// §3.5/§6.2) and then advances it incrementally — so the hot path costs exactly
// one engine round-trip per move, not O(ply).
type MoveIntent struct {
	GameID      string
	SeatIdx     uint8  // the authenticated session's seat
	PlyExpected uint32 // the client's race guard
	UCI         string
	CurFEN      string // server-authoritative current position (NOT from the client)
	ElapsedMs   int64  // server-measured think time for this move
	ServerTS    uint64
	// PriorKeys is the folded Zobrist-key history BEFORE the resulting position,
	// supplied by the live session (GS-3) so the engine can detect repetition
	// (Q-GS5). Nil for callers without history (the move path still detects every
	// position-derived terminal; only repetition needs this).
	PriorKeys []uint64
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
	// UCI is the move just applied (§4A v1.2, ADR-18). Set on BOTH accept paths
	// from the authoritative source: the fresh-accept path copies the request's
	// in.UCI (the move just validated+persisted); the idempotent re-ack path
	// copies the PERSISTED event's uci (mv.UCI from findMove), NEVER the retry's
	// request — so a retry carrying a different uci for an already-persisted ply
	// re-acks the persisted move. Empty unless Accepted. The transport sends this
	// in MOVE_ACCEPTED to both seats; it must read out.UCI, not the request.
	UCI string
	// NewFEN is the position AFTER the accepted move (the engine's verdict). The
	// live-session caches it as the next move's CurFEN, which is what keeps the
	// move path at one engine round-trip (no stream replay). Empty unless
	// Accepted.
	NewFEN string
	// NewZobrist is the Zobrist key of the position after the accepted move
	// (engine-computed). The live session appends it to its folded history so
	// the next move's PriorKeys can carry it — the repetition path (Q-GS5).
	// Zero unless Accepted.
	NewZobrist uint64
	Terminal   *Termination // engine-detected mate/stalemate/draw, or nil
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
// Position handling (NFR-5 + hot-path cost): the move path does NOT reconstruct
// the board from the stream. It folds the stream with the PURE Fold (phase/ply/
// turn/clock only — no chess logic, no engine calls) for authorization, and
// takes the authoritative current position from in.CurFEN (held by the live-
// session, seeded once via FoldWithEngine on resume and advanced from each
// accepted move's NewFEN). So one move costs exactly ONE engine round-trip —
// the legality check for this move — not O(ply) replays. Reconnection resync,
// which genuinely must rebuild FEN from the stream, is the one place that uses
// FoldWithEngine (§3.5), and it runs once per resume, not per move.
//
// Ordering (fixed, §3.6):
//  1. load the stream; fold it (pure) to the authoritative phase/ply/turn/clock;
//  2. authorize: game known, LIVE, correct seat, sequential ply (idempotent
//     re-ack on an already-persisted ply);
//  3. validate legality via the engine against in.CurFEN (the only authority,
//     NFR-5);
//  4. persist MovePlayed durably, waiting for the commit;
//  5. return Accepted with the projected ply/clock/terminal and the new FEN.
func (p *Pipeline) SubmitMove(ctx context.Context, in MoveIntent) (MoveOutcome, error) {
	streamID := StreamIDFor(in.GameID)
	events, err := p.Store.Load(ctx, streamID)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("pipeline: load: %w", err)
	}
	if len(events) == 0 {
		return reject(RejectUnknownGame, 0), nil
	}

	// Fold the stream with the PURE fold: it gives phase/ply/turn/clock without
	// touching the engine (no stream replay — NFR-5 keeps chess logic out of Go,
	// and the hot path out of O(ply) oracle calls). The current board position
	// is in.CurFEN (server truth, owned by the live-session), used only to
	// validate this one move at step 3.
	st, err := Fold(events, p.Opts)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("pipeline: fold: %w", err)
	}

	// (2) authorize.
	if int(in.SeatIdx) >= st.seatCount() {
		return reject(RejectNotYourSeat, st.Ply), nil
	}
	// Idempotent retry: a MOVE whose ply is already persisted re-acks the
	// existing move, no second append (§5 duplicate-MOVE).
	if in.PlyExpected <= st.Ply && in.PlyExpected > 0 {
		if mv, mvSeq := findMove(events, in.PlyExpected); mv != nil {
			if mv.SeatIdx != in.SeatIdx {
				return reject(RejectNotYourSeat, st.Ply), nil
			}
			// On a game that is no longer live, re-ack ONLY the final move (the
			// one whose ack may have been lost when the game ended with it) and
			// only for the same uci; any other ply or a different uci is GAME_OVER.
			// A live game keeps the §4A v1.2 rule: re-ack the persisted move, even
			// if the retry carries a different uci.
			if st.Phase != PhaseLive && (in.PlyExpected != st.Ply || in.UCI != mv.UCI) {
				return reject(RejectGameOver, st.Ply), nil
			}
			return MoveOutcome{
				Accepted:     true,
				AppliedSeq:   mvSeq, // the move's REAL seq, not 0
				Ply:          mv.Ply,
				UCI:          mv.UCI, // the PERSISTED move's uci, not the retry's (§4A v1.2)
				ClockAfterMs: mv.ClockAfterMs,
			}, nil
		}
	}
	// Not a re-ack of a persisted ply: a game that is no longer live accepts no
	// new move. (The idempotent re-ack above runs FIRST so a resend of the final
	// already-persisted move, e.g. a mating move whose GameEnded committed with
	// it in one batch, is re-acked rather than rejected GAME_OVER.)
	if st.Phase != PhaseLive {
		return reject(RejectGameOver, st.Ply), nil
	}
	if in.SeatIdx != st.TurnIdx {
		return reject(RejectNotYourTurn, st.Ply), nil
	}
	if in.PlyExpected != st.Ply+1 {
		return reject(RejectStalePly, st.Ply), nil
	}

	// (3) validate legality — engine is the only authority (NFR-5). The position
	// is in.CurFEN (server-held current position), not reconstructed here.
	verdict, err := p.Oracle.Legality(ctx, in.CurFEN, in.UCI, in.PriorKeys)
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
	// The expected seq is the current event count — we already Loaded the full
	// stream above (len(events)), so the hot path passes it with NO extra read
	// (Q-GS4E). A concurrent (cross-node) writer that took this seq yields
	// ErrSeqConflict; the store CANNOT tell a true duplicate from a valid move
	// that lost the race, so recovery lives HERE (the caller that owns game
	// truth): on conflict we reload, check whether OUR move already landed
	// (idempotent — a resend/duplicate, re-ack it) or genuinely lost the seq to a
	// DIFFERENT event (re-sequence at the new head and retry), so a valid move is
	// never dropped (NFR-2, §3.2). Single-writer-per-game makes a conflict rare
	// today; the contract is in place for when multi-node writers land.
	seq := uint32(len(events))
	// A move that ends the game (mate / stalemate / insufficient / automatic
	// draw) commits MovePlayed + GameEnded in ONE AppendBatch, so a crash or store
	// outage cannot leave a half-terminal stream (issue #67; same atomicity as the
	// flag path). A plain move keeps the single-event Append hot path.
	var terminalBatch []Event
	if verdict.Terminal != nil {
		terminalBatch = []Event{ev, {Type: EvGameEnded, GameEnded: &GameEnded{
			ResultVector: MoverResultVector(in.SeatIdx, *verdict.Terminal, st.seatCount()),
			Termination:  *verdict.Terminal,
		}}}
	}
	const maxSeqRetries = 8
	for attempt := 0; ; attempt++ {
		var err error
		if terminalBatch != nil {
			err = p.Store.AppendBatch(ctx, streamID, seq, terminalBatch)
		} else {
			err = p.Store.Append(ctx, streamID, seq, ev)
		}
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSeqConflict) {
			// A real persist failure after validation: do NOT ack (§5).
			return MoveOutcome{}, fmt.Errorf("pipeline: persist: %w: %w", ErrPersist, err)
		}
		if attempt >= maxSeqRetries {
			return MoveOutcome{}, fmt.Errorf("pipeline: persist: seq contention unresolved after %d retries: %w: %w", maxSeqRetries, ErrPersist, err)
		}
		// Seq conflict — reconcile against the current head.
		events, err = p.Store.Load(ctx, streamID)
		if err != nil {
			return MoveOutcome{}, fmt.Errorf("pipeline: reload on conflict: %w", err)
		}
		if mv, mvSeq := findMove(events, ply); mv != nil {
			// OUR move (this ply) is already persisted — the conflict was our own
			// duplicate/resend. Idempotent re-ack: return the existing move, no
			// second append, reporting its REAL seq (where it sits).
			return MoveOutcome{
				Accepted:     true,
				AppliedSeq:   mvSeq,
				Ply:          mv.Ply,
				UCI:          mv.UCI, // persisted move's uci (§4A v1.2)
				ClockAfterMs: mv.ClockAfterMs,
			}, nil
		}
		// A DIFFERENT event took the seq. Re-fold to confirm it is still our turn
		// at this ply (a genuinely conflicting move from the other seat would
		// change the turn/ply and must be rejected, not forced in), then retry at
		// the new head.
		rst, ferr := Fold(events, p.Opts)
		if ferr != nil {
			return MoveOutcome{}, fmt.Errorf("pipeline: refold on conflict: %w", ferr)
		}
		if rst.Phase != PhaseLive || in.PlyExpected != rst.Ply+1 || in.SeatIdx != rst.TurnIdx {
			// The stream advanced past us (another move landed first): this is no
			// longer a valid move at this ply — a stale-ply reject, not a drop.
			return reject(RejectStalePly, rst.Ply), nil
		}
		seq = uint32(len(events)) // re-sequence the valid loser at the new head
	}

	// (5) accepted — only now, after the durable commit.
	return MoveOutcome{
		Accepted:     true,
		AppliedSeq:   seq,
		Ply:          ply,
		UCI:          in.UCI, // the move just validated + persisted (§4A v1.2)
		ClockAfterMs: clockAfter,
		NewFEN:       verdict.NewFEN,
		NewZobrist:   verdict.NewZobrist,
		Terminal:     verdict.Terminal,
	}, nil
}

// MoverResultVector builds the result vector for a move-produced terminal. A
// mate is a win for the mover; stalemate / insufficient / the automatic-draw
// terminals are a draw for all seats.
func MoverResultVector(mover uint8, term Termination, seatCount int) []Result {
	rv := make([]Result, seatCount)
	if term == TermMate {
		for i := range rv {
			rv[i] = ResultLoss
		}
		rv[mover] = ResultWin
		return rv
	}
	for i := range rv {
		rv[i] = ResultDraw
	}
	return rv
}

func reject(r RejectReason, serverPly uint32) MoveOutcome {
	return MoveOutcome{Rejected: true, Reason: r, PlyExpected: serverPly}
}

// findMove returns the MovePlayed body with the given ply and its 0-based seq
// (its index in the stream), or (nil, 0) if absent. The seq lets a re-ack report
// the move's REAL AppliedSeq (where it sits), not the next free seq.
func findMove(events []Event, ply uint32) (*MovePlayed, uint32) {
	for i := range events {
		if events[i].Type == EvMovePlayed && events[i].MovePlayed != nil && events[i].MovePlayed.Ply == ply {
			return events[i].MovePlayed, uint32(i)
		}
	}
	return nil, 0
}

// StreamIDFor builds the stream id for a game (SPEC-S2-PROTOCOL §4A.8:
// "game/{uuid}"). Exported because the live session (GS-3) seeds and reads the
// same stream the pipeline writes, and both must agree on the id.
func StreamIDFor(gameID string) string { return "game/" + gameID }
