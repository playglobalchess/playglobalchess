package game

import (
	"context"
	"errors"
)

// ErrSeqConflict is returned by EventStore.Append/AppendBatch when the target
// (streamID, seq) is already occupied — a concurrent or duplicate append lost
// the race for that seq (SPEC-GS4 §3.2, Q-GS4E). It is a DISTINGUISHABLE sentinel
// (callers branch on errors.Is) because the store cannot itself tell a true
// duplicate from a valid move that lost a cross-node race — identical bytes at
// the store layer. Recovery therefore lives in the caller that owns game truth
// (the GS-3 session actor): on ErrSeqConflict it reloads the head and either
// idempotently re-acks its already-present event or re-sequences a genuinely new
// loser at the next free seq, so a valid move is never dropped (NFR-2). The
// concrete store (DynamoDB conditional-put / TransactWriteItems) maps its
// condition-check failure to this one sentinel.
var ErrSeqConflict = errors.New("event store: seq already occupied")

// This file defines the two external seams GS-1 depends on but does NOT
// implement (SPEC-S2-GAME-SERVER §2c). Both are Go interfaces here; their real
// implementations are later chunks. The move-pipeline decision logic (pipeline.go)
// is written against these interfaces so it can be unit-tested with doubles.

// LegalityVerdict is the engine's answer about a proposed move. It mirrors what
// the S1 engine returns over the (later) engine-cgo seam (ADR-15, Q-GS4). GS-1
// treats it as opaque game-truth: it never second-guesses or recomputes any of
// these fields (NFR-5).
type LegalityVerdict struct {
	// Legal reports whether the move is legal from the given position. An
	// illegal move produces MOVE_REJECTED{ILLEGAL} and appends no event
	// (SPEC-S2-GAME-SERVER §5).
	Legal bool
	// NewFEN is the position AFTER the move (only meaningful when Legal).
	NewFEN string
	// NewZobrist is the Zobrist key of the position AFTER the move (only
	// meaningful when Legal). The live session appends it to its folded history
	// so the NEXT move's legality call can pass it as a prior key — this is how
	// repetition (threefold/fivefold) is detected on the live path (Q-GS5,
	// SPEC-S2-GAME-SERVER §10 / GS-3). The engine computes it; Go never hashes.
	NewZobrist uint64
	// Terminal, when set, is the game-ending condition the move produced
	// (mate/stalemate/repetition/fifty-move/insufficient). Nil means the game
	// continues. The engine — not Go — detects these (NFR-5). REPETITION is only
	// ever returned when a non-empty priorKeys history was supplied to Legality.
	Terminal *Termination
}

// LegalityOracle is the single legality authority (NFR-5): the S1 engine, bound
// over cgo in a later chunk (ADR-15). GS-1 ships only this interface and a test
// double. NO chess logic in Go.
//
// It also answers the insufficient-material question that decides whether a flag
// ends as FLAG or TIMEOUT_VS_INSUFFICIENT (SPEC-S2-GAME-SERVER §3.3) — because
// that is a chess-material judgement, it belongs to the engine, never the clock
// or the game layer.
type LegalityOracle interface {
	// Legality validates a UCI move against a FEN position. context.Context is
	// on the method because the real implementation crosses the cgo seam and may
	// be cancelled/timed-out on the hot path (SPEC-S2-GAME-SERVER §6.1).
	//
	// priorKeys is the Zobrist key of every earlier position of the game BEFORE
	// the resulting position (status_with_history's convention: it excludes the
	// position the move produces): the START position's key first (from
	// PositionKey), then the key after each live ply. State.PriorKeys builds it;
	// leaving out the start key undercounts every return to the start by one
	// (#100). Callers with no history (single-move validation, or the GS-1 stub
	// path) pass nil — the engine then detects every position-derived terminal
	// but never repetition.
	Legality(ctx context.Context, fen, uci string, priorKeys []uint64) (LegalityVerdict, error)

	// PositionKey is the engine's Zobrist key of a FEN. FoldWithEngine calls it
	// once per game for StartFEN, the first entry of the repetition history; no
	// Legality call reports it, because a verdict carries only the key of the
	// position a move produces. Go never hashes (NFR-5).
	PositionKey(ctx context.Context, fen string) (uint64, error)

	// HasSufficientMaterial reports whether the given seat has enough material to
	// mate. Used ONLY at a flag: if the opponent of the flagged seat lacks
	// sufficient material, the termination is TIMEOUT_VS_INSUFFICIENT rather than
	// FLAG (SPEC-S2-GAME-SERVER §3.3). GS-1 does not compute material itself.
	HasSufficientMaterial(ctx context.Context, fen string, seatIdx uint8) (bool, error)
}

// EventStore is the append-only game-truth store (ADR-10). The concrete store
// product is unchosen (SPEC-S2-GAME-SERVER §2c, ADR-10); GS-1 ships this
// interface and an in-memory double (store_stub.go).
//
// The interface encodes the persist-before-ack ORDERING contract (NFR-7): Append
// returns only AFTER the event is durably committed. A caller that waits for
// Append before acking a move therefore cannot ack before persistence — the
// contract is in the type, not left to discipline. The stub commits
// synchronously, but a real store must not return until the durable commit, so
// the pipeline logic written against this interface is correct for both.
type EventStore interface {
	// Append durably commits one event to the stream at the caller-supplied
	// expectedSeq and returns only after the commit (NFR-7 persist-before-ack).
	// The caller passes the 0-based seq it expects this event to occupy — it
	// already holds the stream head (it Loaded + folded the stream), so no
	// server-side max(seq) read is needed on the hot path (Q-GS4E, SPEC-GS4
	// §3.2). The store writes conditionally on that seq being unoccupied, so a
	// concurrent/duplicate append at an occupied seq is REJECTED with the typed
	// ErrSeqConflict (see store package) rather than silently reordered — the
	// (streamID, seq) pair is unique and strictly increasing, no gaps
	// (SPEC-S2-PROTOCOL §4A.8). A non-nil error means the event is NOT committed
	// and the caller MUST NOT ack (SPEC-S2-GAME-SERVER §5); on ErrSeqConflict the
	// caller reconciles (idempotent re-ack vs re-sequence a valid loser, §3.2).
	Append(ctx context.Context, streamID string, expectedSeq uint32, ev Event) error

	// AppendBatch atomically commits several events at consecutive seqs starting
	// at expectedSeq (expectedSeq, expectedSeq+1, …) — all-or-nothing. It is how
	// a terminal ending (ClockFlagged+GameEnded, or MovePlayed+GameEnded) is
	// written without a half-terminal window (SPEC-GS4 §3.4): a crash cannot
	// interleave a partial ending. Each event's seq is conditioned unoccupied, so
	// the whole batch surfaces ErrSeqConflict if any slot is taken. An empty slice
	// is a no-op (nil error).
	AppendBatch(ctx context.Context, streamID string, expectedSeq uint32, evs []Event) error

	// Load returns the full ordered event slice for a stream (seq 0..n), from
	// which the game State is folded. Reconnection and node-failover resume read
	// through this (SPEC-S2-GAME-SERVER §3.5/§6.2). An unknown stream returns an
	// empty slice and no error.
	Load(ctx context.Context, streamID string) ([]Event, error)
}
