package game

import "context"

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
	// Terminal, when set, is the game-ending condition the move produced
	// (mate/stalemate/repetition/fifty-move/insufficient). Nil means the game
	// continues. The engine — not Go — detects these (NFR-5).
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
	Legality(ctx context.Context, fen, uci string) (LegalityVerdict, error)

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
	// Append durably commits one event to the stream and returns only after the
	// commit (NFR-7 persist-before-ack). It assigns and returns the event's
	// 0-based seq (SPEC-S2-PROTOCOL §4A.8: strictly increasing, no gaps). A
	// non-nil error means the event is NOT committed and the caller MUST NOT ack
	// (SPEC-S2-GAME-SERVER §5 "persist fails after validation").
	Append(ctx context.Context, streamID string, ev Event) (seq uint32, err error)

	// Load returns the full ordered event slice for a stream (seq 0..n), from
	// which the game State is folded. Reconnection and node-failover resume read
	// through this (SPEC-S2-GAME-SERVER §3.5/§6.2). An unknown stream returns an
	// empty slice and no error.
	Load(ctx context.Context, streamID string) ([]Event, error)
}
