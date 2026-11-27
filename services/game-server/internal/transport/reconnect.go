package transport

import (
	"context"
	"errors"
	"log"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// reconnect.go: AUTHENTICATE with the live-game index on (GS-8 section 4.4).
// The index, not this node's memory, says which games a user is in, so a
// reconnect after a restart finds its game, and an ending the user did not see
// is pushed once from user_recent_games, which replaces the in-memory
// missedEnds while the index is on.

// userLiveLimit is how many of a user's live games AUTHENTICATE restores.
const userLiveLimit = 8

// indexStore returns the index store when the index is on.
func (s *Server) indexStore() (game.IndexedStore, bool) {
	if s.idxCfg == nil {
		return nil, false
	}
	st, ok := s.reg.store.(game.IndexedStore)
	return st, ok
}

// userIndex is what AUTHENTICATE read from the index for one user.
type userIndex struct {
	live, recent []game.UserEntry
}

// lookupUser reads the user's live and recently ended games. Both reads must
// succeed: a user whose live game cannot be found must not be allowed to SEEK
// into a second one.
func (s *Server) lookupUser(ctx context.Context, st game.IndexedStore, uid string) (userIndex, error) {
	live, err := st.UserLiveGames(ctx, uid, userLiveLimit)
	if err != nil {
		return userIndex{}, err
	}
	recent, err := st.UserRecentGames(ctx, uid, 0)
	if err != nil {
		return userIndex{}, err
	}
	return userIndex{live: live, recent: recent}, nil
}

// consumeRecent deletes the user_recent_games rows of users who have now seen
// the game's ending, so it is pushed once ("consume once", as missedEnds was).
// A failure is logged: the worst case is one more ended STATE on a later login.
func (s *Server) consumeRecent(ctx context.Context, gameID string, uids []string) {
	st, ok := s.indexStore()
	if !ok || len(uids) == 0 {
		return
	}
	ops := make([]game.IndexOp, 0, len(uids))
	for _, uid := range uids {
		ops = append(ops, game.IndexOp{Table: game.IdxUserRecent, Kind: game.IndexDelete, Key: uid, GameID: gameID})
	}
	if err := st.RepairIndex(ctx, ops); err != nil {
		log.Printf("index: consuming the recent rows of %s: %v", gameID, err)
	}
}

// resyncFromIndex restores the user's games from the index after AUTHENTICATE:
// each live game is started here if it is not running (startGame), the seat is
// rebound and STATE pushed; each recently ended game is pushed once as an ended
// STATE and its row consumed. The seat comes from the stream (startGame fills
// it), never from the row: the row is a hint.
func (s *Server) resyncFromIndex(ctx context.Context, c Conn, st *connState, ix userIndex) {
	uid := st.userID
	pushed := map[string]bool{}
	for _, row := range ix.live {
		sess, err := s.startGame(ctx, row.GameID, "", nil)
		switch {
		case errors.Is(err, errGameEnded):
			// The row was stale and startGame repaired it into a recent row;
			// push the ending below.
			ix.recent = append(ix.recent, game.UserEntry{GameID: row.GameID, Seat: row.Seat})
			continue
		case err != nil:
			log.Printf("transport: AUTHENTICATE of %s: starting live game %s: %v", uid, row.GameID, err)
			continue
		}
		seat, ok := s.seatOfUser(row.GameID, uid)
		if !ok {
			continue // the stream does not seat this user: a bad row
		}
		snap, err := sess.StateSnapshot(ctx)
		if err != nil {
			continue
		}
		s.bindSeat(c, st, row.GameID, seat)
		s.send(ctx, c, codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
			GameID: row.GameID, State: snapshotToStateBody(snap, seat),
		}})
		pushed[row.GameID] = true
	}
	for _, row := range ix.recent {
		if pushed[row.GameID] {
			continue
		}
		pushed[row.GameID] = true
		snap, seat, seated, err := s.reg.EndedView(ctx, row.GameID, uid)
		switch {
		case errors.Is(err, errNotEnded):
			continue // a recent row for a live game cannot be written; ignore it
		case err != nil:
			log.Printf("transport: AUTHENTICATE of %s: ended view of %s: %v", uid, row.GameID, err)
			continue // the row stays, so a later login retries
		case !seated:
			s.consumeRecent(ctx, row.GameID, []string{uid}) // a bad row: drop it
		case s.send(ctx, c, endedStateMsg(row.GameID, snap, seat)):
			s.consumeRecent(ctx, row.GameID, []string{uid})
		}
	}
}

// seatOfUser returns uid's seat in gameID from the per-game seat map.
func (s *Server) seatOfUser(gameID, uid string) (uint8, bool) {
	for seat, u := range s.seatUsers(gameID) {
		if u == uid {
			return seat, true
		}
	}
	return 0, false
}
