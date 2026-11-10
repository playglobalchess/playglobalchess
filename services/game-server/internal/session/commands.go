package session

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	// v1.3 STATE fields (§4A.6).
	LastMoveUCI  string        // the last live move; empty iff Ply == 0
	ResultVector []game.Result // set iff Ended
	DrawOfferBy  int           // seat with a pending draw offer, or -1
	// ClockAnchor is the instant RemainingMs is valid at: the side to move's
	// clock has been running since then (the last move, or session start).
	ClockAnchor time.Time
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
		switch {
		case out.Ply == s.ply+1:
			// Genuine new move — fall through to advance the projection below.
		case out.Ply <= s.ply:
			// Idempotent re-ack of an already-persisted ply (a client resend after
			// a lost ack): the pipeline returns Accepted with the EXISTING move's
			// ply, empty NewFEN, zero NewZobrist, nil Terminal. Advancing here
			// would overwrite curFEN with "", append a bogus 0 key, and flip turn.
			// So we return it as-is (transport re-emits MOVE_ACCEPTED) with NO
			// projection change. NOTE (GS-3b): this returns BEFORE the
			// drawOfferBy=nil clear below, deliberately — a re-ack appends no new
			// MovePlayed, so the stream (the truth) gained no move, and a pending
			// draw offer made AFTER the original move must NOT be killed by a mere
			// transport duplicate of that already-counted move. A genuine new move
			// (the case above) does clear it.
			return
		default:
			// out.Ply > s.ply+1: the pipeline accepted a move that skips a ply.
			// This is NOT a duplicate — it is a pipeline/stream desync, and
			// silently treating it as a re-ack would freeze the projection while
			// the stream advanced (every later move then validates a stale FEN).
			// Surface it as an error so the transport fails loudly rather than
			// corrupting the game.
			c.out = game.MoveOutcome{}
			c.err = fmt.Errorf("session: pipeline accepted ply %d but session is at %d (expected %d) — stream desync",
				out.Ply, s.ply, s.ply+1)
			return
		}
		s.curFEN = out.NewFEN
		s.priorKeys = append(s.priorKeys, out.NewZobrist)
		s.ply = out.Ply
		s.lastMoveUCI = c.uci
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
		// Any move clears a pending draw offer (GS-3b §3.3, Q-EG2): the offerer
		// moving withdraws it; the opponent moving implicitly declines it. Both
		// land here (c.seat is whoever moved), so one clear covers both.
		s.drawOfferBy = nil
		if out.Terminal != nil {
			// Engine-detected terminal (mate / stalemate / insufficient /
			// automatic draw). Append GameEnded; the game is over.
			rv := winnerFromMover(c.seat, *out.Terminal, s.seatCount)
			if err := s.appendGameEnded(ctx, *out.Terminal, rv); err != nil {
				c.err = err
				return
			}
			s.endGame(*out.Terminal, rv)
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
	drawBy := -1
	if s.drawOfferBy != nil {
		drawBy = int(*s.drawOfferBy)
	}
	c.snap = Snapshot{
		FEN:          s.curFEN,
		Ply:          s.ply,
		Turn:         s.turn,
		Phase:        s.phase,
		RemainingMs:  rem,
		Ended:        s.ended,
		Terminal:     s.terminal,
		LastMoveUCI:  s.lastMoveUCI,
		ResultVector: append([]game.Result(nil), s.resultVector...),
		DrawOfferBy:  drawBy,
		ClockAnchor:  s.lastMoveAt,
	}
}

// --- helpers (run on the owner goroutine) -----------------------------------

// flag appends ClockFlagged + GameEnded for a flagged seat, deciding FLAG vs
// TIMEOUT_VS_INSUFFICIENT via the engine (GS-1 FlagTermination over GS-2
// HasSufficientMaterial).
//
// Ordering: everything fallible that does NOT touch the store (the termination
// decision, the result vector) is computed FIRST, so the only step between the
// two appends is the second append itself — the smallest possible window for a
// half-terminal stream (ClockFlagged committed, GameEnded not). If the GameEnded
// append fails after ClockFlagged committed, the session returns the error and
// does NOT mark itself ended; NewSession's recovery (finishTrailingFlag) closes
// the half-terminal stream on the next rebuild so it never resumes as LIVE.
func (s *Session) flag(ctx context.Context, flaggedSeat uint8) error {
	// (1) Decide the termination + result vector BEFORE any append (fail here
	// leaves the stream untouched).
	term, err := game.FlagTermination(ctx, s.oracle, s.curFEN, flaggedSeat, s.seatCount)
	if err != nil {
		return fmt.Errorf("session: flag termination: %w", err)
	}
	rv := flagResultVector(term, flaggedSeat, s.seatCount)

	// (2) Append ClockFlagged + GameEnded ATOMICALLY (SPEC-GS4 §3.4): a single
	// AppendBatch commits both or neither, so a crash cannot leave a
	// half-terminal stream. appendBatchAtHead derives the seq from the head and
	// retries on a cross-node conflict (NFR-2).
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: flaggedSeat}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: term}},
	}); err != nil {
		return fmt.Errorf("session: append flag ending: %w", err)
	}
	s.endGame(term, rv)
	return nil
}

// flagResultVector: the flagged seat loses on FLAG; on TIMEOUT_VS_INSUFFICIENT
// the game is a draw for all seats.
func flagResultVector(term game.Termination, flaggedSeat uint8, seatCount int) []game.Result {
	rv := make([]game.Result, seatCount)
	if term == game.TermTimeoutVsInsufficient {
		for i := range rv {
			rv[i] = game.ResultDraw
		}
		return rv
	}
	for i := range rv {
		rv[i] = game.ResultWin
	}
	rv[flaggedSeat] = game.ResultLoss
	return rv
}

// endGame is the SINGLE place the three ending-state fields are written together,
// so no code path can set one and forget another (the stale-phase bug: ended=true
// with phase still Live produced a contradictory snapshot). Every terminal path
// — move-produced, flag, and construction-time recovery — calls this.
func (s *Session) endGame(term game.Termination, rv []game.Result) {
	s.ended = true
	s.terminal = term
	s.resultVector = append([]game.Result(nil), rv...)
	s.phase = game.PhaseEnded
}

func (s *Session) appendGameEnded(ctx context.Context, term game.Termination, rv []game.Result) error {
	// GameEnded is an ending append (not the per-move hot path). appendBatchAtHead
	// derives the seq from a fresh Load and retries on a cross-node conflict, so
	// it stays correct regardless of recurring-ply quirks on the move path (e.g.
	// after a takeback) that make a tracked counter fragile.
	if err := s.appendBatchAtHead(ctx, []game.Event{{
		Type:      game.EvGameEnded,
		GameEnded: &game.GameEnded{ResultVector: rv, Termination: term},
	}}); err != nil {
		return fmt.Errorf("session: append GameEnded: %w", err)
	}
	return nil
}

// streamLen returns the current stream length (= the next free seq). Used by the
// session's own batch/ending appends to set expectedSeq correctly; these are
// off the per-move hot path (endings, takebacks), so the extra Load is fine.
func (s *Session) streamLen(ctx context.Context) (uint32, error) {
	events, err := s.store.Load(ctx, s.stream)
	if err != nil {
		return 0, fmt.Errorf("session: load for seq: %w", err)
	}
	return uint32(len(events)), nil
}

// appendBatchAtHead commits evs atomically at the current stream head, with a
// bounded retry if a concurrent (cross-node) writer takes the seq first
// (ErrSeqConflict). The session is single-writer per game, so a conflict is the
// across-node case (§3.2/Q-GS4E); re-deriving the head and retrying re-sequences
// our events rather than dropping them (NFR-2). Used by the ending/takeback
// paths (not the per-move hot path).
func (s *Session) appendBatchAtHead(ctx context.Context, evs []game.Event) error {
	const maxRetries = 8
	for attempt := 0; ; attempt++ {
		seq, err := s.streamLen(ctx)
		if err != nil {
			return err
		}
		err = s.store.AppendBatch(ctx, s.stream, seq, evs)
		if err == nil {
			return nil
		}
		if !errors.Is(err, game.ErrSeqConflict) || attempt >= maxRetries {
			return err
		}
		// lost the seq to a concurrent writer — reload head and retry.
	}
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
	// accepts — the fold enforces that a seat cannot accept its own request). One
	// atomic batch at the head, with cross-node conflict retry.
	other := uint8((int(c.by) + 1) % s.seatCount)
	if err := s.appendBatchAtHead(ctx, []game.Event{
		{Type: game.EvTakebackReq, SeatAction: &game.SeatAction{SeatIdx: c.by}},
		{Type: game.EvTakebackAccepted, SeatAction: &game.SeatAction{SeatIdx: other}},
	}); err != nil {
		c.err = fmt.Errorf("session: append takeback: %w", err)
		return
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
	s.lastMoveUCI = eng.LastMoveUCI
	s.turn = eng.TurnIdx
	s.phase = eng.Phase
	s.lastMoveAt = s.now()
	// A takeback rewinds game state; a pending draw offer (GS-3b) is part of that
	// state and must not survive the rewind. Clearing it here prevents an
	// offer-then-takeback-then-AcceptDraw from ending the game on an offer the
	// takeback logically voided (the takebackPrefix cut may drop the DrawOffered
	// event, so the ephemeral field would otherwise outlive the stream's offer).
	s.drawOfferBy = nil
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
