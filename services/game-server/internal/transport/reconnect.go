package transport

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// reconnect.go: AUTHENTICATE with the live-game index on (GS-8 section 4.4).
// The index, not this node's memory, says which games a user is in, so a
// reconnect after a restart finds its game, and an ending the user did not see
// is pushed once from user_recent_games, which replaces the in-memory
// missedEnds while the index is on.

// userLiveLimit is how many of a user's live games AUTHENTICATE restores.
const userLiveLimit = 8

// userRecentLimit bounds the missed endings one login pushes (newest first);
// older ones are still pushed at a later login, until their TTL.
const userRecentLimit = 32

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
// succeed, so a login never silently loses a live game or a missed ending.
// (Nothing stops a logged-in user from SEEKing while in a live game today; a
// one-live-game guard is a separate product decision, #105.)
func (s *Server) lookupUser(ctx context.Context, st game.IndexedStore, uid string) (userIndex, error) {
	live, err := st.UserLiveGames(ctx, uid, userLiveLimit)
	if err != nil {
		return userIndex{}, err
	}
	recent, err := st.UserRecentGames(ctx, uid, userRecentLimit)
	if err != nil {
		return userIndex{}, err
	}
	return userIndex{live: live, recent: recent}, nil
}

// consumeRecent deletes the user_recent_games (and any stale user_live_games)
// rows of users who have now seen
// the game's ending, so it is pushed once ("consume once", as missedEnds was).
// A failure is logged: the worst case is one more ended STATE on a later login.
func (s *Server) consumeRecent(ctx context.Context, gameID string, uids []string) {
	st, ok := s.indexStore()
	if !ok || len(uids) == 0 {
		return
	}
	ops := make([]game.IndexOp, 0, len(uids))
	seen := map[string]bool{}
	for _, uid := range uids {
		if seen[uid] {
			continue // one user in both seats: one row, and a repeated item is refused
		}
		seen[uid] = true
		ops = append(ops,
			game.IndexOp{Table: game.IdxUserRecent, Kind: game.IndexDelete, Key: uid, GameID: gameID},
			// An ending written without its rows (persist's fallback) leaves a
			// stale live row, which would replay the ending at the next login.
			game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexDelete, Key: uid, GameID: gameID})
	}
	if err := st.RepairIndex(ctx, ops); err != nil {
		log.Printf("index: consuming the recent rows of %s: %v", gameID, err)
	}
}

// resyncFromIndex restores the user's games from the index after AUTHENTICATE.
// The seat comes from the stream (startGame fills it), never from the row.
//
// Pass 1 resolves every live row and sends nothing:
//   - a running or startable game is kept for pass 2;
//   - a game whose stream has ended (a stale row) becomes a missed ending;
//   - a row that is wrong for good (no stream, a stream that does not seat the
//     user, a time control that does not resolve, a stream the engine cannot
//     fold) is logged and its row deleted: such a game cannot be played, so it
//     must not lock the user out of every login;
//   - a game the stream seats uid in but which left this node during the
//     login (it ended, or the node is shutting down) is settled as a missed
//     ending; if it turns out still live, the login is refused;
//   - any other error (the store, a timeout, a cancelled build) may pass, so
//     the login is refused (the caller sends a retryable INTERNAL) and no row
//     is deleted: a login never goes through with a live game it lost.
//
// Pass 2 binds and sends STATE for the live games, then pushes each missed
// ending once. A row is deleted only after its ending was handed to the
// connection, so a refused login or a refused send keeps it. "Sent" means
// queued on the connection: a connection that dies right after loses that
// one push, the same as the in-memory missedEnds did.
func (s *Server) resyncFromIndex(ctx context.Context, c Conn, st *connState, ix userIndex) error {
	uid := st.userID
	type liveGame struct {
		gameID string
		sess   *session.Session
		seat   uint8
	}
	var live []liveGame
	var staleEnded []string // live rows to settle through the ended view
	// mustEnd holds seated games that left this node during the login (they
	// ended, or the node is shutting down). Their ended view must find an
	// ending; a game that is still live refuses the login instead.
	mustEnd := map[string]bool{}
	for _, row := range ix.live {
		sess, err := s.startGame(ctx, row.GameID, "", nil)
		if errors.Is(err, errBuildDiscarded) {
			sess, err = s.startGame(ctx, row.GameID, "", nil) // a racing drop: look again
		}
		switch {
		case errors.Is(err, errGameEnded):
			staleEnded = append(staleEnded, row.GameID)
			continue
		case permanentRowErr(ctx, err):
			log.Printf("transport: AUTHENTICATE of %s: live game %s cannot be played, dropping its row: %v", uid, row.GameID, err)
			s.dropUserLiveRow(ctx, row.GameID, uid)
			continue
		case err != nil:
			return fmt.Errorf("starting live game %s: %w", row.GameID, err)
		}
		seat, ok := s.seatOfUser(row.GameID, uid)
		if !ok {
			// The seat map is in memory: it is empty both when the stream does
			// not seat uid and when the game was dropped since startGame. Only
			// the stream can tell them apart.
			seated, err := s.streamSeats(ctx, row.GameID, uid)
			switch {
			case err != nil:
				return fmt.Errorf("reading live game %s: %w", row.GameID, err)
			case !seated:
				s.dropUserLiveRow(ctx, row.GameID, uid) // a row the stream does not back
			default:
				staleEnded = append(staleEnded, row.GameID)
				mustEnd[row.GameID] = true
			}
			continue
		}
		live = append(live, liveGame{gameID: row.GameID, sess: sess, seat: seat})
	}
	snaps := make([]session.Snapshot, len(live))
	for i, g := range live {
		snap, err := g.sess.StateSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("reading live game %s: %w", g.gameID, err)
		}
		snaps[i] = snap
	}

	pushed := map[string]bool{}
	for i, g := range live {
		if s.beforeBind != nil {
			s.beforeBind(g.gameID)
		}
		if snaps[i].Ended || !s.bindSeat(c, st, g.gameID, g.seat) {
			// The game ended (or was dropped) between its start and this bind,
			// so no GAME_END reaches this connection: settle it as a missed
			// ending, which shows it once and consumes its rows.
			staleEnded = append(staleEnded, g.gameID)
			mustEnd[g.gameID] = true
			continue
		}
		s.send(ctx, c, codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
			GameID: g.gameID, State: snapshotToStateBody(snaps[i], g.seat),
		}})
		pushed[g.gameID] = true
	}
	fromLive := map[string]bool{}
	for _, gid := range staleEnded {
		if !fromLive[gid] {
			fromLive[gid] = true
			ix.recent = append(ix.recent, game.UserEntry{GameID: gid})
		}
	}
	for _, row := range ix.recent {
		if pushed[row.GameID] {
			continue
		}
		pushed[row.GameID] = true
		snap, seat, seated, err := s.reg.EndedView(ctx, row.GameID, uid)
		switch {
		case errors.Is(err, errNotEnded):
			if mustEnd[row.GameID] {
				return fmt.Errorf("live game %s left this node during the login", row.GameID)
			}
			continue // a recent row for a live game cannot be written; ignore it
		case permanentRowErr(ctx, err):
			// An ended game that can never be shown: drop its row, or every
			// login would fold it again until its TTL.
			log.Printf("transport: AUTHENTICATE of %s: ended game %s cannot be shown, dropping its rows: %v", uid, row.GameID, err)
			s.consumeRecent(ctx, row.GameID, []string{uid})
		case err != nil:
			if mustEnd[row.GameID] {
				return fmt.Errorf("reading game %s: %w", row.GameID, err)
			}
			// An ended game cannot pair the user anywhere, so this does not
			// refuse the login; the row stays and a later login retries.
			log.Printf("transport: AUTHENTICATE of %s: ended view of %s: %v", uid, row.GameID, err)
			continue
		case !seated:
			if ctx.Err() == nil {
				s.consumeRecent(ctx, row.GameID, []string{uid}) // a bad row: drop it
			}
		case s.send(ctx, c, endedStateMsg(row.GameID, snap, seat)):
			s.consumeRecent(ctx, row.GameID, []string{uid})
		default:
			continue // not sent: keep the rows for a later login
		}
		if fromLive[row.GameID] {
			s.dropStaleLiveRows(ctx, row.GameID, "") // the shared live_games row
		}
	}
	return nil
}

// permanentRowErr reports whether err says a game can never be played or
// shown (so its row may be deleted): no stream, a time control that does not
// resolve, or a stream the engine cannot fold. Never true while ctx is done or
// for a context error, which says nothing about the game.
func permanentRowErr(ctx context.Context, err error) bool {
	if err == nil || session.IsTransient(ctx, err) {
		return false
	}
	return errors.Is(err, errNoStream) || errors.Is(err, errBadTimeControl) || errors.Is(err, session.ErrFold)
}

// streamSeats reports whether gameID's durable stream seats uid.
func (s *Server) streamSeats(ctx context.Context, gameID, uid string) (bool, error) {
	events, err := s.reg.loadStream(ctx, gameID)
	if err != nil {
		return false, err
	}
	for _, ev := range events {
		if ev.PlayerJoined != nil && ev.PlayerJoined.UserID == uid {
			return true, nil
		}
	}
	return false, nil
}

// dropUserLiveRow deletes one user_live_games row that is wrong for good.
func (s *Server) dropUserLiveRow(ctx context.Context, gameID, uid string) {
	st, ok := s.indexStore()
	if !ok {
		return
	}
	op := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexDelete, Key: uid, GameID: gameID}
	if err := st.RepairIndex(ctx, []game.IndexOp{op}); err != nil {
		log.Printf("index: dropping a bad live row of %s for %s: %v", gameID, uid, err)
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
