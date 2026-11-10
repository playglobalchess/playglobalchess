package transport

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// Ended games after their live session is dropped (#102, GS6 §3.4).
//
// GAME_END goes only to connections bound when the game ends, and then the live
// session is dropped. Two paths keep the result reachable:
//
//   - RESYNC{game_id} of an ended game the user was seated in returns a STATE
//     with status ENDED, the result and the termination, rebuilt from the
//     durable stream (Registry.EndedView). No retention window: the stream is
//     the record.
//   - A seat that did NOT get GAME_END live (offline, or its write refused) is
//     noted here. Its next AUTHENTICATE on this node pushes that ended STATE
//     once, within missedEndTTL. A seat that saw GAME_END live is never pushed
//     the ending again unasked (it may already be seeking a new game).
//
// The missed-ending record is bounded: entries expire after missedEndTTL, and
// at most maxMissedEnds are kept (the oldest is dropped first). Expiry runs in
// insertion order off a FIFO, so it costs O(1) amortised per game end and never
// scans the whole map.

// missedEndTTL is how long a missed ending is pushed on reconnect. Long enough
// for a closed laptop or a dead phone battery; after it, RESYNC (or the game
// archive) still has the result.
const missedEndTTL = 24 * time.Hour

// maxMissedEnds caps the record so a burst of abandoned games cannot grow it
// without bound inside the TTL. A var so a test can lower it.
var maxMissedEnds = 100_000

type missedEnd struct {
	seat uint8
	at   time.Time
}

type missedKey struct {
	uid, gameID string
	at          time.Time
}

// recordMissedEnd notes that user uid (at seat) did not get gameID's GAME_END.
func (s *Server) recordMissedEnd(uid, gameID string, seat uint8) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneMissedLocked(now)
	games := s.missedEnds[uid]
	if games == nil {
		games = make(map[string]missedEnd)
		s.missedEnds[uid] = games
	}
	games[gameID] = missedEnd{seat: seat, at: now}
	s.missedOrder = append(s.missedOrder, missedKey{uid: uid, gameID: gameID, at: now})
	for len(s.missedOrder) > maxMissedEnds {
		s.dropOldestMissedLocked()
	}
}

// takeMissedEnds removes and returns uid's unexpired missed endings
// (game id -> seat).
func (s *Server) takeMissedEnds(uid string) map[string]uint8 {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneMissedLocked(now)
	games := s.missedEnds[uid]
	if len(games) == 0 {
		return nil
	}
	out := make(map[string]uint8, len(games))
	for gid, m := range games {
		out[gid] = m.seat
	}
	delete(s.missedEnds, uid)
	return out
}

// clearMissedEnd forgets one missed ending (the user has now seen it).
func (s *Server) clearMissedEnd(uid, gameID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if games := s.missedEnds[uid]; games != nil {
		delete(games, gameID)
		if len(games) == 0 {
			delete(s.missedEnds, uid)
		}
	}
}

// pruneMissedLocked drops expired entries from the front of the FIFO. s.mu held.
func (s *Server) pruneMissedLocked(now time.Time) {
	for len(s.missedOrder) > 0 && !now.Before(s.missedOrder[0].at.Add(missedEndTTL)) {
		s.dropOldestMissedLocked()
	}
}

// dropOldestMissedLocked pops the FIFO head and deletes its map entry, unless
// that entry was since taken, cleared or re-recorded (a newer `at`). s.mu held.
func (s *Server) dropOldestMissedLocked() {
	k := s.missedOrder[0]
	s.missedOrder[0] = missedKey{} // release the strings
	s.missedOrder = s.missedOrder[1:]
	if games := s.missedEnds[k.uid]; games != nil {
		if m, ok := games[k.gameID]; ok && m.at.Equal(k.at) {
			delete(games, k.gameID)
			if len(games) == 0 {
				delete(s.missedEnds, k.uid)
			}
		}
	}
}

// endedStateMsg wraps an ended snapshot as the STATE frame for youSeat.
func endedStateMsg(gameID string, snap session.Snapshot, youSeat uint8) codec.ServerMsg {
	return codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
		GameID: gameID, State: snapshotToStateBody(snap, youSeat),
	}}
}

// pushMissedEnds sends each of uid's missed endings once (on AUTHENTICATE). An
// ending whose push fails (a store error, or a write the connection refused)
// is recorded again, so the next AUTHENTICATE retries it rather than losing it.
func (s *Server) pushMissedEnds(ctx context.Context, c Conn, uid string) {
	for gid, seat := range s.takeMissedEnds(uid) {
		snap, _, seated, err := s.reg.EndedView(ctx, gid, uid)
		switch {
		case errors.Is(err, errNotEnded) || (err == nil && !seated):
			continue // cannot happen for a recorded ending; nothing to retry
		case err != nil:
			log.Printf("transport: missed-ending push of %s for %s: %v", gid, uid, err)
			s.recordMissedEnd(uid, gid, seat)
		case !s.send(ctx, c, endedStateMsg(gid, snap, seat)):
			s.recordMissedEnd(uid, gid, seat)
		}
	}
}

// resyncEnded answers a RESYNC for a game that is not live on this node: an
// ENDED STATE if the stream says the game ended and uid was seated in it, else
// ERROR{UNKNOWN_GAME} with the same message handleResync uses for a live game
// the caller is not seated in, so a RESYNC cannot tell other players' games
// (live, ended or unknown) apart.
func (s *Server) resyncEnded(ctx context.Context, c Conn, uid, gameID string) {
	snap, seat, seated, err := s.reg.EndedView(ctx, gameID, uid)
	switch {
	case err != nil && !errors.Is(err, errNotEnded):
		// A store/fold failure is retryable, not "no such game": say INTERNAL and
		// log it, so neither the player nor on-call mistakes an outage for a
		// missing game. Checked first: a failed stream read also reports !seated.
		log.Printf("transport: RESYNC ended view of %s: %v", gameID, err)
		s.send(ctx, c, errorMsg(codec.ECInternal, "could not read game state"))
	case !seated || errors.Is(err, errNotEnded):
		s.send(ctx, c, errorMsg(codec.ECUnknownGame, errNoSuchGame))
	default:
		s.send(ctx, c, endedStateMsg(gameID, snap, seat))
		s.clearMissedEnd(uid, gameID)
	}
}

// errNoSuchGame is the one UNKNOWN_GAME message for every RESYNC the caller may
// not see (not seated, not ended here, or no such game).
const errNoSuchGame = "no such game"
