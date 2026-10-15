package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// GS-3b — the resign / abort / draw-offer game-ending commands
// (SPEC-S2-GAME-SERVER-GS3B). Each is an actor command on the single-writer
// Session loop (the Takeback pattern): compute the result vector (no I/O) first,
// then one ATOMIC appendBatchAtHead, then endGame — identical persist-before-ack
// discipline to flag(), so a crash cannot leave a half-terminal stream (one
// atomic batch) and a resend after the game ended is a safe no-op.
//
// No chess logic here (NFR-5): resign is a forfeit, abort is a ply-count rule,
// draw is agreement — all server-authoritative. v1 is 2-seat (Q-EG4).

// ErrAbortTooLate is returned by Abort once the game has truly started
// (ply >= abortBeforePly): quitting then is a resign, not an abort (Q-EG1). The
// caller/UI decides whether to offer "resign instead"; the session does NOT
// silently convert.
var ErrAbortTooLate = errors.New("session: too late to abort (game started) — resign instead")

// ErrNoDrawToAccept is returned by AcceptDraw when there is no pending offer from
// the OTHER seat (none outstanding, or the only offer is the accepter's own —
// you cannot accept your own offer).
var ErrNoDrawToAccept = errors.New("session: no pending draw offer to accept")

// ErrGameNotLive is returned by Resign / OfferDraw / AcceptDraw when the game is
// not in the LIVE phase (e.g. still MATCHED — created but not all players joined).
// Those actions append events the fold permits ONLY in PhaseLive (Resigned /
// DrawOffered / DrawAccepted), so persisting one into a non-live stream would
// succeed on write but render the stream permanently UNFOLDABLE on the next
// rebuild (§3.5 reconnect/failover). The session rejects them here rather than
// trusting the caller (GS-6) to phase-gate — the single-writer owns stream
// foldability. Abort is deliberately NOT guarded this way: Aborted is valid from
// MATCHED (the correct pre-start exit) and the fold allows it there.
var ErrGameNotLive = errors.New("session: action requires a live game")

// --- Resign -----------------------------------------------------------------

// Resign ends the game as a loss for seat `by` (RESIGN). Always legal while the
// game is live, for either seat, on any turn. Idempotent: a resend after the
// game already ended is a no-op success.
func (s *Session) Resign(ctx context.Context, by uint8) error {
	c := &resignCmd{by: by, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return err
	}
	return c.err
}

type resignCmd struct {
	by   uint8
	err  error
	done chan struct{}
}

func (c *resignCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		return // idempotent: already over.
	}
	if s.phase != game.PhaseLive {
		c.err = ErrGameNotLive // Resigned is a Live-only event (see ErrGameNotLive).
		return
	}
	rv, err := loserResultVector(c.by, s.seatCount)
	if err != nil {
		c.err = err
		return
	}
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvResigned, SeatAction: &game.SeatAction{SeatIdx: c.by}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: game.TermResign}},
	}); err != nil {
		c.err = fmt.Errorf("session: append resign ending: %w", err)
		return
	}
	s.endGame(game.TermResign)
}

// --- Abort ------------------------------------------------------------------

// Abort ends an unstarted game with no result (ABORT). Allowed only while
// ply < abortBeforePly (Q-EG1); past that it returns ErrAbortTooLate (the quit
// is a resign, not an abort — the caller decides, we do not convert). Idempotent
// after end.
func (s *Session) Abort(ctx context.Context, by uint8) error {
	c := &abortCmd{by: by, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return err
	}
	return c.err
}

type abortCmd struct {
	by   uint8
	err  error
	done chan struct{}
}

func (c *abortCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		return // idempotent.
	}
	if s.ply >= s.abortBeforePly {
		c.err = ErrAbortTooLate
		return
	}
	rv, err := allDrawResultVector(s.seatCount)
	if err != nil {
		c.err = err
		return
	}
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvAborted, Aborted: &game.Aborted{By: c.by}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: game.TermAbort}},
	}); err != nil {
		c.err = fmt.Errorf("session: append abort ending: %w", err)
		return
	}
	s.endGame(game.TermAbort)
}

// --- Draw offer / accept ----------------------------------------------------

// OfferDraw records a pending draw offer from seat `by` and appends a visible
// DrawOffered event (§3.3). A repeat offer by the same seat is a no-op; an offer
// by the OTHER seat while one is pending is a mutual agreement that ends the game
// (DRAW_AGREE), with DrawOffered{other} appended BEFORE DrawAccepted{other} so
// every accept pairs to a preceding offer (Q-EG5). Idempotent after end.
func (s *Session) OfferDraw(ctx context.Context, by uint8) error {
	c := &offerDrawCmd{by: by, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return err
	}
	return c.err
}

type offerDrawCmd struct {
	by   uint8
	err  error
	done chan struct{}
}

func (c *offerDrawCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		return // idempotent.
	}
	if s.phase != game.PhaseLive {
		c.err = ErrGameNotLive // DrawOffered/DrawAccepted are Live-only events.
		return
	}
	// A pending offer from the OTHER seat → mutual agreement (DRAW_AGREE).
	if s.drawOfferBy != nil && *s.drawOfferBy != c.by {
		rv, err := allDrawResultVector(s.seatCount)
		if err != nil {
			c.err = err
			return
		}
		if err := s.appendBatchAtHead(ctx, []game.Event{
			{Type: game.EvDrawOffered, SeatAction: &game.SeatAction{SeatIdx: c.by}},
			{Type: game.EvDrawAccepted, SeatAction: &game.SeatAction{SeatIdx: c.by}},
			{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: game.TermDrawAgree}},
		}); err != nil {
			c.err = fmt.Errorf("session: append mutual-draw ending: %w", err)
			return
		}
		s.drawOfferBy = nil
		s.endGame(game.TermDrawAgree)
		return
	}
	// A repeat offer by the same seat is a no-op (offer already pending).
	if s.drawOfferBy != nil && *s.drawOfferBy == c.by {
		return
	}
	// First offer: append the visible event and record the ephemeral pending state.
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvDrawOffered, SeatAction: &game.SeatAction{SeatIdx: c.by}},
	}); err != nil {
		c.err = fmt.Errorf("session: append draw offer: %w", err)
		return
	}
	by := c.by
	s.drawOfferBy = &by
}

// AcceptDraw accepts a pending draw offer from the OTHER seat, ending the game as
// DRAW_AGREE. Returns ErrNoDrawToAccept if there is no pending offer or the only
// pending offer is the accepter's own. Idempotent after end.
func (s *Session) AcceptDraw(ctx context.Context, by uint8) error {
	c := &acceptDrawCmd{by: by, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return err
	}
	return c.err
}

type acceptDrawCmd struct {
	by   uint8
	err  error
	done chan struct{}
}

func (c *acceptDrawCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		return // idempotent.
	}
	if s.phase != game.PhaseLive {
		c.err = ErrGameNotLive // DrawAccepted is a Live-only event.
		return
	}
	// Must be a pending offer from the OTHER seat (you cannot accept your own).
	if s.drawOfferBy == nil || *s.drawOfferBy == c.by {
		c.err = ErrNoDrawToAccept
		return
	}
	rv, err := allDrawResultVector(s.seatCount)
	if err != nil {
		c.err = err
		return
	}
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvDrawAccepted, SeatAction: &game.SeatAction{SeatIdx: c.by}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: game.TermDrawAgree}},
	}); err != nil {
		c.err = fmt.Errorf("session: append draw accept ending: %w", err)
		return
	}
	s.drawOfferBy = nil
	s.endGame(game.TermDrawAgree)
}

// --- result-vector helpers (2-seat, Q-EG4) ----------------------------------

// ErrUnsupportedSeatCount is returned when an ending command runs on a game whose
// seat count is not 2. v1 is 2-seat (Q-EG4); an N-seat resign/draw result rule is
// a variant concern (ADR-3 / F-07). This fails the ONE command rather than
// panicking the actor (which would crash the whole process and every other live
// game on it) — a bad GameCreated must not take down unrelated sessions.
var ErrUnsupportedSeatCount = errors.New("session: GS-3b endgame results are 2-seat only (Q-EG4)")

// loserResultVector: seat `loser` loses, every other seat wins. v1 is 2-seat; an
// N-seat resign rule is out of scope (Q-EG4), so a non-2-seat game returns
// ErrUnsupportedSeatCount rather than mis-scoring.
func loserResultVector(loser uint8, seatCount int) ([]game.Result, error) {
	if seatCount != 2 {
		return nil, ErrUnsupportedSeatCount
	}
	rv := make([]game.Result, seatCount)
	for i := range rv {
		rv[i] = game.ResultWin
	}
	rv[loser] = game.ResultLoss
	return rv, nil
}

// allDrawResultVector: every seat draws (abort / draw-agreement).
func allDrawResultVector(seatCount int) ([]game.Result, error) {
	if seatCount != 2 {
		return nil, ErrUnsupportedSeatCount
	}
	rv := make([]game.Result, seatCount)
	for i := range rv {
		rv[i] = game.ResultDraw
	}
	return rv, nil
}
