package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Snapshot is the §3.5 reconnection resync payload: the live projection a
// reconnecting client needs to rebuild its board. It equals a fresh
// FoldWithEngine of the durable stream (asserted in the tests), so any node can
// produce it — reconnection has no original-node affinity (NFR-2, §6.3). The WS
// STATE frame (transport chunk) is built from this.
type Snapshot struct {
	FEN         string
	Ply         uint32
	Turn        uint8
	Phase       game.Phase
	RemainingMs []int64 // per-seat clock remaining
	Ended       bool
	Terminal    game.Termination // meaningful only when Ended
}

// --- SubmitMove -------------------------------------------------------------

// SubmitMove drives the GS-1 pipeline for one move against this session's OWNED
// position + accumulated Zobrist history, then applies the outcome to the live
// projection. The session measures elapsed think time and hands it to the
// pipeline as data; it does NOT charge the clock itself (the pipeline does that
// once — charging here too would double-deduct). On an accepted move it advances
// CurFEN, appends the resulting Zobrist key (so repetition is seen next move —
// Q-GS5), and on a terminal/flag outcome appends the ending events to the
// stream.
func (s *Session) SubmitMove(ctx context.Context, seat uint8, plyExpected uint32, uci string, serverTS uint64) (game.MoveOutcome, error) {
	c := &submitCmd{seat: seat, plyExpected: plyExpected, uci: uci, serverTS: serverTS, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return game.MoveOutcome{}, err
	}
	return c.out, c.err
}

type submitCmd struct {
	seat        uint8
	plyExpected uint32
	uci         string
	serverTS    uint64
	out         game.MoveOutcome
	err         error
	done        chan struct{}
}

func (c *submitCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		c.out = game.MoveOutcome{Rejected: true, Reason: game.RejectGameOver, PlyExpected: s.ply}
		return
	}
	// Server-authoritative timing: stamp BOTH the think-time measurement and the
	// persisted MovePlayed.ServerTS from the session's own clock, captured once.
	// This keeps them consistent so a later reconnect/failover rebuild recovers
	// the exact same idle baseline from the stored ServerTS (finding: resync
	// clock baseline). The caller-supplied serverTS is not trusted for timing
	// (never trust a client clock, T-1/T-2).
	moveAt := s.now()
	elapsedMs := moveAt.Sub(s.lastMoveAt).Milliseconds()
	if elapsedMs < 0 {
		elapsedMs = 0
	}
	intent := game.MoveIntent{
		GameID:      s.gameID,
		SeatIdx:     c.seat,
		PlyExpected: c.plyExpected,
		UCI:         c.uci,
		CurFEN:      s.curFEN,
		ElapsedMs:   elapsedMs,
		ServerTS:    uint64(moveAt.UnixMilli()),
		PriorKeys:   s.priorKeys,
	}
	out, err := s.pipe.SubmitMove(ctx, intent)
	if err != nil {
		// Persist/engine I/O failure: do NOT advance any projection (NFR-7 — the
		// move did not happen). Surface the error for the transport.
		c.err = err
		return
	}
	c.out = out

	switch {
	case out.Accepted:
		// Distinguish a genuinely NEW move from the pipeline's idempotent re-ack
		// of an already-persisted ply (§5 duplicate-MOVE: a client resend after a
		// lost ack). A new move advances the ply by exactly one; a re-ack returns
		// the EXISTING move's ply (<= our current ply) with an empty NewFEN /
		// zero NewZobrist / nil Terminal. Advancing the projection on a re-ack
		// would overwrite curFEN with "", append a bogus 0 key, and flip turn —
		// corrupting the game. So we only advance on a new move; a re-ack is
		// returned as-is for the transport to re-emit MOVE_ACCEPTED, with no
		// projection change.
		if out.Ply != s.ply+1 {
			// Idempotent re-ack (or any non-advancing accept): do not touch state.
			break
		}
		s.curFEN = out.NewFEN
		s.priorKeys = append(s.priorKeys, out.NewZobrist)
		s.ply = out.Ply
		s.turn = uint8((int(s.turn) + 1) % s.seatCount)
		// Advance the owned clock incrementally from the pipeline's authoritative
		// post-move remaining (out.ClockAfterMs, which already includes delay +
		// increment). Only the mover's clock changes; CloneWith leaves the others
		// untouched — the same projection the fold does from a stored MovePlayed.
		// This keeps the clock projection O(1) per move (no re-fold) and in sync
		// with the stream, so StateSnapshot and TickFlagCheck see the real
		// remaining time (§3.5 resync + idle-flag correctness).
		s.clocks = s.clocks.CloneWith(int(c.seat), int64(out.ClockAfterMs))
		s.lastMoveAt = moveAt
		if out.Terminal != nil {
			// Engine-detected terminal (mate / stalemate / insufficient /
			// automatic draw). Append GameEnded; the game is over.
			if err := s.appendGameEnded(ctx, *out.Terminal, winnerFromMover(c.seat, *out.Terminal, s.seatCount)); err != nil {
				c.err = err
				return
			}
			s.ended = true
			s.terminal = *out.Terminal
		}
	case out.Flagged:
		// The mover ran out of time on this move. Append ClockFlagged then
		// GameEnded{FLAG|TIMEOUT_VS_INSUFFICIENT}; the material split is the
		// engine's call (NFR-5).
		if err := s.flag(ctx, out.FlaggedSeat); err != nil {
			c.err = err
		}
	default:
		// Rejected — no projection change.
	}
}

// --- TickFlagCheck ----------------------------------------------------------

// TickFlagCheck is the IDLE-timeout path (§3.4): with no move arriving, has the
// side to move used all its time? The transport/scheduler calls this on a timer
// (Q-GS3C — the session does not own a ticker). It charges elapsed-since-last-
// move against the side to move; if that flags, it ends the game. It never
// moves, so there is no double-charge with SubmitMove's per-move pipeline
// charge. Returns whether the game ended on this tick.
func (s *Session) TickFlagCheck(ctx context.Context) (ended bool, err error) {
	c := &tickCmd{done: make(chan struct{})}
	if e := s.dispatch(ctx, c, c.done); e != nil {
		return false, e
	}
	return c.ended, c.err
}

type tickCmd struct {
	ended bool
	err   error
	done  chan struct{}
}

func (c *tickCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.ended {
		return
	}
	elapsedMs := s.now().Sub(s.lastMoveAt).Milliseconds()
	if elapsedMs < 0 {
		elapsedMs = 0
	}
	// Charge the side to move; a flag here ends the game. We use clock.Apply on a
	// COPY to test the boundary without mutating unless it flags.
	next, applyErr := s.clocks.Apply(s.opts.TimeControl, int(s.turn), elapsedMs)
	if applyErr != nil {
		// Match the pipeline's flag-detection convention exactly (errors.Is on
		// clock.ErrFlagged) so the two callers cannot drift on what counts as a
		// flag if clock.Apply's error contract evolves.
		if errors.Is(applyErr, clock.ErrFlagged) {
			s.clocks = next
			if e := s.flag(ctx, uint8(next.FlaggedSeat)); e != nil {
				c.err = e
				return
			}
			c.ended = true
			return
		}
		c.err = applyErr
		return
	}
	// Not flagged: do NOT commit the charge (the real charge happens on the next
	// move via the pipeline); this was only a boundary check.
}

// --- StateSnapshot ----------------------------------------------------------

// StateSnapshot returns the live projection for a §3.5 reconnection resync.
func (s *Session) StateSnapshot(ctx context.Context) (Snapshot, error) {
	c := &snapCmd{done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return Snapshot{}, err
	}
	return c.snap, nil
}

type snapCmd struct {
	snap Snapshot
	done chan struct{}
}

func (c *snapCmd) apply(_ context.Context, s *Session) {
	defer close(c.done)
	rem := make([]int64, len(s.clocks.RemainingMs))
	copy(rem, s.clocks.RemainingMs)
	c.snap = Snapshot{
		FEN:         s.curFEN,
		Ply:         s.ply,
		Turn:        s.turn,
		Phase:       s.phase,
		RemainingMs: rem,
		Ended:       s.ended,
		Terminal:    s.terminal,
	}
}

// --- helpers (run on the owner goroutine) -----------------------------------

// flag appends ClockFlagged + GameEnded for a flagged seat, deciding FLAG vs
// TIMEOUT_VS_INSUFFICIENT via the engine (GS-1 FlagTermination over GS-2
// HasSufficientMaterial).
func (s *Session) flag(ctx context.Context, flaggedSeat uint8) error {
	if _, err := s.store.Append(ctx, s.stream, game.Event{
		Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: flaggedSeat},
	}); err != nil {
		return fmt.Errorf("session: append ClockFlagged: %w", err)
	}
	term, err := game.FlagTermination(ctx, s.oracle, s.curFEN, flaggedSeat, s.seatCount)
	if err != nil {
		return fmt.Errorf("session: flag termination: %w", err)
	}
	// The flagged seat loses on FLAG; on TIMEOUT_VS_INSUFFICIENT it is a draw.
	rv := make([]game.Result, s.seatCount)
	if term == game.TermTimeoutVsInsufficient {
		for i := range rv {
			rv[i] = game.ResultDraw
		}
	} else {
		for i := range rv {
			rv[i] = game.ResultWin
		}
		rv[flaggedSeat] = game.ResultLoss
	}
	if err := s.appendGameEnded(ctx, term, rv); err != nil {
		return err
	}
	s.ended = true
	s.terminal = term
	return nil
}

func (s *Session) appendGameEnded(ctx context.Context, term game.Termination, rv []game.Result) error {
	if _, err := s.store.Append(ctx, s.stream, game.Event{
		Type:      game.EvGameEnded,
		GameEnded: &game.GameEnded{ResultVector: rv, Termination: term},
	}); err != nil {
		return fmt.Errorf("session: append GameEnded: %w", err)
	}
	return nil
}

// --- Takeback (F-01.4, unrated) ---------------------------------------------

// Takeback rolls the live projection back by `plies` half-moves (SPEC
// §3.2 / F-01.4, unrated only). It appends the TakebackReq+TakebackAccepted
// events (keeping the stream append-only) and then re-derives the full
// projection — CurFEN, clocks, and the Zobrist history — by folding the
// retained prefix WITH the engine (FoldWithEngine), the same path construction
// uses. Rated games are refused.
func (s *Session) Takeback(ctx context.Context, by uint8, plies uint32) error {
	c := &takebackCmd{by: by, plies: plies, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return err
	}
	return c.err
}

type takebackCmd struct {
	by    uint8
	plies uint32
	err   error
	done  chan struct{}
}

func (c *takebackCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	if s.rated {
		c.err = fmt.Errorf("session: takeback not allowed in a rated game (F-01.4)")
		return
	}
	if s.ended {
		c.err = fmt.Errorf("session: takeback after game end")
		return
	}
	// Append the takeback request + acceptance (append-only; the OTHER seat
	// accepts — the fold enforces that a seat cannot accept its own request).
	other := uint8((int(c.by) + 1) % s.seatCount)
	for _, ev := range []game.Event{
		{Type: game.EvTakebackReq, SeatAction: &game.SeatAction{SeatIdx: c.by}},
		{Type: game.EvTakebackAccepted, SeatAction: &game.SeatAction{SeatIdx: other}},
	} {
		if _, err := s.store.Append(ctx, s.stream, ev); err != nil {
			c.err = fmt.Errorf("session: append takeback: %w", err)
			return
		}
	}
	// Re-derive the rolled-back projection. A takeback is a re-fold of the
	// retained move prefix (SPEC §3.2); because the stream is append-only the
	// undone moves stay in it. RefoldTakebackWithEngine does the cut AND the
	// engine projection in ONE place (the same cut the pure RefoldTakeback uses),
	// so there is no second cut algorithm that could pick a different prefix —
	// important because a prior takeback leaves undone MovePlayed events with
	// recurring ply numbers in the stream. The trailing TakebackReq/Accepted we
	// just appended are not MovePlayed, so they do not affect the cut.
	events, err := s.store.Load(ctx, s.stream)
	if err != nil {
		c.err = fmt.Errorf("session: takeback reload: %w", err)
		return
	}
	eng, err := game.RefoldTakebackWithEngine(ctx, events, s.opts, c.plies, s.oracle)
	if err != nil {
		c.err = fmt.Errorf("session: takeback refold: %w", err)
		return
	}
	s.curFEN = eng.CurFEN
	s.priorKeys = eng.PriorKeys
	s.clocks = eng.Clock
	s.ply = eng.Ply
	s.turn = eng.TurnIdx
	s.phase = eng.Phase
	s.lastMoveAt = s.now()
}

// winnerFromMover builds the result vector for a move-produced terminal. A mate
// is a win for the mover; stalemate / insufficient / the draw terminals are a
// draw for all seats.
func winnerFromMover(mover uint8, term game.Termination, seatCount int) []game.Result {
	rv := make([]game.Result, seatCount)
	if term == game.TermMate {
		for i := range rv {
			rv[i] = game.ResultLoss
		}
		rv[mover] = game.ResultWin
		return rv
	}
	for i := range rv {
		rv[i] = game.ResultDraw
	}
	return rv
}
