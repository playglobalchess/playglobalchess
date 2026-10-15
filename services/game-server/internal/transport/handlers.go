package transport

import (
	"context"
	"errors"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/matchmaking"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// defaultJoinTTL is the window a MATCHED seat has to send its first frame before
// the game is aborted (closes the GS-5 ghost-claim residual: a game created from
// a just-expired seeker whose connection never joins). Config via Server.joinTTL.
const defaultJoinTTL = 30 * time.Second

// defaultFlagInterval is how often the per-game flag ticker charges idle time
// against the side to move (§3.4). 1s is well under any real time control's
// resolution for forfeit purposes while being cheap (one actor command/game/sec).
const defaultFlagInterval = 1 * time.Second

// --- SEEK -> matchmaking -> MATCHED ----------------------------------------

func (s *Server) handleSeek(ctx context.Context, c Conn, st *connState, p *codec.SeekPayload) bool {
	if p == nil {
		s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "empty SEEK"))
		return true
	}
	seeker := matchmaking.Seeker{
		UserID:      st.userID,
		PoolID:      p.PoolID,
		TimeControl: p.TimeControl,
		Rated:       p.Rated,
	}
	match, paired, err := s.mm.Seek(ctx, seeker)
	if err != nil {
		s.send(ctx, c, errorMsg(codec.ECInternal, "seek failed"))
		return true
	}
	if !paired {
		// Enqueued, waiting for a partner. No reply frame — the client waits for
		// MATCHED (sent when a counter-seek pairs it; see below).
		return true
	}
	// Paired: this connection is one seat; the other seat's connection (if its
	// owner is connected to THIS node) gets MATCHED too. Deliver to both.
	s.deliverMatched(ctx, c, st, match)
	return true
}

// deliverMatched builds the live session, binds each seat to its player's
// connection, sends MATCHED to each connected seat, and arms the join-timeout.
// It is called from the handler of whichever seeker WON the pairing; the OTHER
// seeker's handler already returned (enqueued), so this reaches that waiting
// connection via userConns (by its user id from match.Players) rather than
// through its own handler.
func (s *Server) deliverMatched(ctx context.Context, _ Conn, _ *connState, match matchmaking.Match) {
	// Build (or find) the live session from the just-written opening stream.
	sess, err := s.reg.GetOrCreate(match.GameID, match.TimeControl)
	if err != nil {
		// S1 fix: the game exists durably but we could not stand up a session on
		// this node (bad time-control id, fold failure). Fail LOUDLY to each
		// connected player rather than leaving both hanging with no MATCHED.
		for _, uid := range match.Players {
			if cc, present := s.userConn(uid); present {
				s.send(ctx, cc, errorMsg(codec.ECInternal, "could not start game"))
			}
		}
		return
	}
	snap, err := sess.StateSnapshot(ctx)
	if err != nil {
		for _, uid := range match.Players {
			if cc, present := s.userConn(uid); present {
				s.send(ctx, cc, errorMsg(codec.ECInternal, "could not read game state"))
			}
		}
		return
	}
	// Register the join signal BEFORE binding, so the binds below (which close it)
	// are observed by the timer armed afterward (C1 fix).
	s.registerJoin(match.GameID)
	// Deliver MATCHED to each seat whose player's connection is on this node.
	for seat, uid := range match.Players {
		cc, present := s.userConn(uid)
		if !present {
			continue // that player is not connected to this node (will STATE-resync on reconnect).
		}
		// Bind the seat to that player's connection; this also closes the join
		// signal (markJoinedLocked), so the timer knows a seat really joined.
		s.bindSeatForUser(cc, uid, match.GameID, seat)
		s.send(ctx, cc, codec.ServerMsg{Type: codec.SMMatched, Matched: &codec.MatchedPayload{
			GameID: match.GameID, SeatIdx: seat, Opponent: opponentUser(match, seat),
			StartState: snapshotToStateBody(snap),
		}})
	}
	s.armJoinTimeout(match.GameID)
	s.armFlagTicker(match.GameID)
}

// armFlagTicker starts the per-game idle/disconnect FORFEIT driver (§3.4). GS-3's
// session does not own a ticker — "the transport/scheduler calls TickFlagCheck on
// a timer" — so the transport must drive it, or a disconnected/stalled player's
// clock runs forever and the game hangs (and its session/goroutines leak). Every
// flagInterval it charges elapsed time against the side to move; if that flags,
// the session ends the game (FLAG / TIMEOUT_VS_INSUFFICIENT) and we broadcast
// GAME_END + drop. The ticker also exits when the game is gone (ended by any
// other path: mate/resign/draw/abort), so there is exactly one driver per live
// game and it stops with the game.
func (s *Server) armFlagTicker(gameID string) {
	iv := s.flagInterval
	if iv == 0 {
		iv = defaultFlagInterval
	}
	go func() {
		t := time.NewTicker(iv)
		defer t.Stop()
		consecutiveErrs := 0
		const maxConsecutiveErrs = 10
		for range t.C {
			sess, ok := s.reg.Get(gameID)
			if !ok {
				return // game ended/dropped by some path — stop the ticker.
			}
			ended, err := sess.TickFlagCheck(context.Background())
			if err != nil {
				// A persistent store/engine outage makes this recur every tick.
				// Count consecutive failures and give up after a bound rather than
				// spinning invisibly forever (reviewer finding): the game is
				// recovered on the next reconnect/rebuild (NewSession folds from the
				// durable stream), so a stopped ticker is safe. The failure COUNT /
				// a bare retry each belongs in an X-14 metric/log when observability
				// is wired (same deferral as GS-4 F2) — tracked, not inlined here.
				consecutiveErrs++
				if consecutiveErrs >= maxConsecutiveErrs {
					return
				}
				continue
			}
			consecutiveErrs = 0
			if ended {
				s.broadcastGameEnd(context.Background(), gameID)
				return
			}
		}
	}()
}

// registerJoin creates the game's joined signal channel if absent (open =
// not-yet-joined). Idempotent.
func (s *Server) registerJoin(gameID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.joined[gameID]; !ok {
		s.joined[gameID] = make(chan struct{})
	}
}

// userConn returns the user's current connection, if any.
func (s *Server) userConn(uid string) (Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.userConns[uid]
	return c, ok
}

// bindSeatForUser records (gameID,seat)->conn and the userID->game index for a
// seat, independent of a specific connState (used by MATCHED delivery, which may
// bind the OTHER player's connection whose handler is elsewhere). The owning
// connection's own teardown reconciles s.conns by identity.
func (s *Server) bindSeatForUser(c Conn, uid, gameID string, seat uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[gameSeAT{gameID: gameID, seat: seat}] = c
	if s.userGames[uid] == nil {
		s.userGames[uid] = make(map[string]uint8)
	}
	s.userGames[uid][gameID] = seat
	s.markJoinedLocked(gameID)
}

// markJoinedLocked closes the game's joined signal the first time any seat binds.
// Caller MUST hold s.mu. Safe to call repeatedly (closes once).
func (s *Server) markJoinedLocked(gameID string) {
	ch, ok := s.joined[gameID]
	if !ok {
		// No timer armed for this game (e.g. a direct test bind) — nothing to signal.
		return
	}
	select {
	case <-ch: // already closed
	default:
		close(ch)
	}
}

// armJoinTimeout aborts a game ONLY if no seat ever joined within the window —
// the true GS-5 ghost-claim case (a game created from a just-expired seeker
// whose connection never arrives). It waits on the join signal vs the TTL: if a
// seat joins (binds a connection) the signal closes and the timer exits without
// touching the game, so a LATER disconnect falls under the clock/forfeit path,
// NOT abort (C1 fix — the old version aborted a live game on a transient
// double-disconnect). On a true timeout it aborts, but only if the game has not
// progressed (ply 0, so Abort is valid) and only drops the session if the abort
// actually succeeded.
func (s *Server) armJoinTimeout(gameID string) {
	ttl := s.joinTTL
	if ttl == 0 {
		ttl = defaultJoinTTL
	}
	s.mu.Lock()
	joined := s.joined[gameID]
	s.mu.Unlock()
	if joined == nil {
		return // no signal registered (shouldn't happen; deliverMatched registers first).
	}
	go func() {
		t := time.NewTimer(ttl)
		defer t.Stop()
		select {
		case <-joined:
			return // a seat joined — normal play/forfeit rules apply, not abort.
		case <-t.C:
		}
		// Timed out with no seat ever joined: a genuine ghost game. Abort only if
		// the game never progressed (Abort requires ply < abortBeforePly); if the
		// abort succeeds, drop the session. If it did NOT abort (ErrAbortTooLate —
		// a move was somehow played by a since-gone connection), leave the game
		// for the clock/forfeit path rather than dropping a progressed game.
		sess, ok := s.reg.Get(gameID)
		if !ok {
			s.clearJoin(gameID)
			return
		}
		if err := sess.Abort(context.Background(), 0); err == nil {
			s.dropGame(gameID)
		} else {
			s.clearJoin(gameID)
		}
	}()
}

// clearJoin removes a game's join signal (cleanup when the timer exits without
// dropping the game). Idempotent.
func (s *Server) clearJoin(gameID string) {
	s.mu.Lock()
	delete(s.joined, gameID)
	s.mu.Unlock()
}

// --- MOVE -> session -> MOVE_ACCEPTED / MOVE_REJECTED / GAME_END ------------

func (s *Server) handleMove(ctx context.Context, c Conn, st *connState, p *codec.MovePayload) bool {
	if p == nil {
		s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "empty MOVE"))
		return true
	}
	seat, seated := s.seatOf(c, p.GameID)
	if !seated {
		s.send(ctx, c, rejectMove(p.GameID, p.PlyExpected, codec.ReasonNotYourSeat))
		return true
	}
	sess, ok := s.reg.Get(p.GameID)
	if !ok {
		s.send(ctx, c, rejectMove(p.GameID, p.PlyExpected, codec.ReasonUnknownGame))
		return true
	}
	// The session does the durable append BEFORE returning (persist-before-ack,
	// NFR-7); we send the authoritative outcome only after it returns.
	out, err := sess.SubmitMove(ctx, seat, p.PlyExpected, p.UCI, uint64(s.now().UnixMilli()))
	if err != nil {
		// Persist/engine failure — the move did NOT happen (NFR-7); no ack.
		s.send(ctx, c, errorMsg(codec.ECInternal, "move not persisted"))
		return true
	}
	switch {
	case out.Rejected:
		s.send(ctx, c, rejectMove(p.GameID, out.PlyExpected, uint8(out.Reason)))
	case out.Flagged:
		// The move arrived after the clock expired: the session appended the
		// ClockFlagged+GameEnded ending. Broadcast GAME_END to both seats.
		s.broadcastGameEnd(ctx, p.GameID)
	case out.Accepted:
		s.send(ctx, c, codec.ServerMsg{Type: codec.SMMoveAccepted, MoveAccepted: &codec.MoveAcceptedPayload{
			GameID: p.GameID, Ply: out.Ply, ClockAfterMs: out.ClockAfterMs,
		}})
		// An engine-detected terminal (mate/stalemate/draw) ended the game.
		if out.Terminal != nil {
			s.broadcastGameEnd(ctx, p.GameID)
		}
	}
	return true
}

// --- ending / draw / takeback commands -------------------------------------

type endingKind int

const (
	endingResign endingKind = iota
	endingAbort
	endingOfferDraw
	endingTakeback
	// NOTE: DRAW_ACCEPT shares the OFFER_DRAW wire code path in this protocol —
	// the §4A client set has OFFER_DRAW (0x04) only; an accept is a second
	// OFFER_DRAW by the other seat (the session's mutual-offer path turns it into
	// a DrawAccepted). So there is no separate accept message to route.
)

func (s *Server) handleEnding(ctx context.Context, c Conn, gameID string, kind endingKind) bool {
	seat, seated := s.seatOf(c, gameID)
	if !seated {
		s.send(ctx, c, errorMsg(codec.ECUnknownGame, "not seated in this game"))
		return true
	}
	sess, ok := s.reg.Get(gameID)
	if !ok {
		s.send(ctx, c, errorMsg(codec.ECUnknownGame, "no such live game"))
		return true
	}
	var err error
	// offeredNew is set only for a genuine first draw offer (not a no-op repeat),
	// so the transport notifies the opponent exactly once per real offer —
	// honoring the session's idempotency at the notification layer.
	offeredNew := false
	switch kind {
	case endingResign:
		err = sess.Resign(ctx, seat)
	case endingAbort:
		err = sess.Abort(ctx, seat)
	case endingOfferDraw:
		offeredNew, err = sess.OfferDrawResult(ctx, seat)
	case endingTakeback:
		err = sess.Takeback(ctx, seat, 1)
	}
	if err != nil {
		s.send(ctx, c, mapSessionErr(gameID, err))
		return true
	}
	// Did this command end the game? If so, broadcast GAME_END to both seats.
	if snap, serr := sess.StateSnapshot(ctx); serr == nil {
		if snap.Ended {
			s.broadcastGameEnd(ctx, gameID)
			return true
		}
		// Non-ending outcomes that the opponent must still learn about:
		switch kind {
		case endingTakeback:
			// A takeback rewinds the board but §4A has no takeback frame — push
			// STATE to both seats so neither client's board silently diverges (S2).
			s.broadcastState(ctx, gameID, snap)
		case endingOfferDraw:
			// Push DRAW_OFFERED{game_id, by} to the OPPONENT only when a NEW offer
			// was recorded (§4A v1.1, ADR-17). A no-op repeat by the same seat
			// (offeredNew == false) sends nothing, so a double-click / resent
			// OFFER_DRAW does not spam the opponent. The opponent accepts by
			// sending their own OFFER_DRAW (the mutual path ends via GAME_END).
			if !offeredNew {
				break
			}
			other := uint8(1) - seat
			if cc, present := s.connFor(gameID, other); present {
				s.send(ctx, cc, codec.ServerMsg{Type: codec.SMDrawOffered, DrawOffered: &codec.DrawOfferedPayload{
					GameID: gameID, By: seat,
				}})
			}
		}
	}
	return true
}

// handleResync serves an on-demand STATE resync (§4A v1.1 RESYNC, ADR-17): if the
// connection is seated in the game, reply with the authoritative STATE folded
// from the durable stream (same path as the auth-time server push); else
// ERROR{UNKNOWN_GAME}. The complement to the Option-A auto-push.
func (s *Server) handleResync(ctx context.Context, c Conn, gameID string) bool {
	if _, seated := s.seatOf(c, gameID); !seated {
		s.send(ctx, c, errorMsg(codec.ECUnknownGame, "not seated in this game"))
		return true
	}
	sess, ok := s.reg.Get(gameID)
	if !ok {
		s.send(ctx, c, errorMsg(codec.ECUnknownGame, "no such live game"))
		return true
	}
	snap, err := sess.StateSnapshot(ctx)
	if err != nil {
		s.send(ctx, c, errorMsg(codec.ECInternal, "could not read game state"))
		return true
	}
	s.send(ctx, c, codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
		GameID: gameID, State: snapshotToStateBody(snap),
	}})
	return true
}

// broadcastState pushes a STATE frame to both seats' connections (used after a
// takeback, which has no dedicated §4A frame).
func (s *Server) broadcastState(ctx context.Context, gameID string, snap session.Snapshot) {
	msg := codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
		GameID: gameID, State: snapshotToStateBody(snap),
	}}
	for seat := uint8(0); seat < 2; seat++ {
		if cc, present := s.connFor(gameID, seat); present {
			s.send(ctx, cc, msg)
		}
	}
}

// mapSessionErr maps a session command sentinel to the authoritative wire reply.
func mapSessionErr(gameID string, err error) codec.ServerMsg {
	switch {
	case errors.Is(err, session.ErrAbortTooLate):
		return errorMsg(codec.ECProtocolViolation, "too late to abort — resign instead")
	case errors.Is(err, session.ErrNoDrawToAccept):
		return errorMsg(codec.ECProtocolViolation, "no draw offer to accept")
	case errors.Is(err, session.ErrGameNotLive):
		return errorMsg(codec.ECProtocolViolation, "game is not live")
	case errors.Is(err, session.ErrUnsupportedSeatCount):
		return errorMsg(codec.ECInternal, "unsupported game shape")
	default:
		return errorMsg(codec.ECInternal, "command failed")
	}
}

// broadcastGameEnd loads the ended stream's result + termination and sends
// GAME_END to both seats' connections. It CLAIMS the end-broadcast atomically
// (claimGameEnd) so that when a terminal move and the flag tick (or two ending
// paths) resolve near-simultaneously, exactly ONE goroutine broadcasts + drops —
// a client never receives two GAME_END frames for one game (reviewer finding).
func (s *Server) broadcastGameEnd(ctx context.Context, gameID string) {
	if !s.claimGameEnd(gameID) {
		return // another goroutine already broadcast this game's end.
	}
	snap, ok := s.reg.Get(gameID)
	if !ok {
		return
	}
	sp, err := snap.StateSnapshot(ctx)
	if err != nil || !sp.Ended {
		return
	}
	rv, term := s.endingResult(ctx, gameID, sp)
	msg := gameEndMsg(gameID, rv, term)
	for seat := uint8(0); seat < 2; seat++ {
		if cc, present := s.connFor(gameID, seat); present {
			s.send(ctx, cc, msg)
		}
	}
	// The game is over; drop the live session so the node reclaims it (the stream
	// persists in the store; a late reconnect rebuilds a read-only ended view).
	s.dropGame(gameID)
}

// claimGameEnd returns true to exactly ONE caller per game — the one that should
// broadcast GAME_END and drop. Subsequent callers (the racing end path) get
// false and no-op. Guarded by s.mu.
func (s *Server) claimGameEnd(gameID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endBroadcast[gameID] {
		return false
	}
	s.endBroadcast[gameID] = true
	return true
}

// dropGame removes a finished game's live session and ALL its transport state:
// the seat→conn bindings and the userID→game reconnection index (so a later
// AUTHENTICATE does not try to resync an ended game). Idempotent.
func (s *Server) dropGame(gameID string) {
	s.mu.Lock()
	for gs := range s.conns {
		if gs.gameID == gameID {
			delete(s.conns, gs)
		}
	}
	for uid, games := range s.userGames {
		if _, ok := games[gameID]; ok {
			delete(games, gameID)
			if len(games) == 0 {
				delete(s.userGames, uid)
			}
		}
	}
	delete(s.joined, gameID)
	delete(s.endBroadcast, gameID)
	s.mu.Unlock()
	s.reg.Drop(gameID)
}

// endingResult reads the authoritative result vector + termination from the
// stream's final GameEnded (the snapshot carries the termination; the result
// vector lives in the event).
func (s *Server) endingResult(ctx context.Context, gameID string, sp session.Snapshot) ([]game.Result, game.Termination) {
	events, err := s.reg.loadStream(ctx, gameID)
	if err == nil {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == game.EvGameEnded && events[i].GameEnded != nil {
				return events[i].GameEnded.ResultVector, events[i].GameEnded.Termination
			}
		}
	}
	// Fallback: the snapshot's terminal with an empty result vector.
	return nil, sp.Terminal
}

// --- small helpers on Match -------------------------------------------------

func opponentUser(match matchmaking.Match, seat uint8) string {
	for s2, uid := range match.Players {
		if s2 != seat {
			return uid
		}
	}
	return ""
}
