package transport

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// start.go: startGame is the only way a live session comes into being on this
// node (GS-8 section 4.2). Every route (a fresh pairing, a reconnect, adoption
// at start) goes through it, so every live game has its deadline ticker armed.
// A game rebuilt after a restart used to have none (#98, #182).

// startGame builds (or finds) the game's session from its durable stream, fills
// the per-game seat-to-user map from the stream, and makes sure the game's flag
// ticker is running (armFlagTicker keeps at most one per game). A game whose
// stream already holds its ending is not started (errGameEnded).
//
// tcID and players may be passed by a caller that already has them (a fresh
// pairing), which saves a stream read; otherwise they come from the stream.
//
// The join timer is NOT armed here: its "joined" signal is process-local, so
// arming it for a rebuilt game would abort a live game whose players are
// briefly disconnected. A rebuilt game below two plies is bounded by the
// first-move timeout instead (GS-7 section 3.5).
//
// The pairing and adoption routes use it now; the reconnect route moves onto
// it in step 2c-2.
func (s *Server) startGame(ctx context.Context, gameID, tcID string, players map[uint8]string) (*session.Session, error) {
	if tcID == "" {
		events, err := s.reg.loadStream(ctx, gameID)
		if err != nil {
			return nil, err
		}
		if game.HasEnding(events) {
			return nil, fmt.Errorf("%w: %s", errGameEnded, gameID)
		}
		players = map[uint8]string{}
		for _, ev := range events {
			switch {
			case ev.GameCreated != nil:
				if tcID == "" { // first wins, as in the fold
					tcID = ev.GameCreated.TimeControl
				}
			case ev.PlayerJoined != nil:
				players[ev.PlayerJoined.SeatIdx] = ev.PlayerJoined.UserID
			}
		}
		if tcID == "" {
			return nil, fmt.Errorf("%w: %s", errNoStream, gameID)
		}
	}
	sess, err := s.reg.GetOrCreate(gameID, tcID)
	if err != nil {
		return nil, err
	}
	// Fill the seat map only while this session is still the registered one.
	// dropGame unregisters before it deletes the entry under the shard lock, so
	// checking here means a concurrent drop either sees our fill and deletes
	// it, or has already unregistered and we write nothing (no orphan entry).
	sh := s.gt.shard(gameID)
	sh.mu.Lock()
	if cur, ok := s.reg.Get(gameID); ok && cur == sess {
		e := sh.entryLocked(gameID)
		for seat, uid := range players {
			e.users[seat] = uid
		}
	}
	sh.mu.Unlock()
	s.armFlagTicker(gameID)
	return sess, nil
}

// AdoptLive starts every game in the live-game index (GS-8 section 4.4), oldest
// first (UUIDv7 ids sort by time), with bounded concurrency. It needs SetIndex.
// A game whose stream has already ended is skipped and counted in skipped (its
// stale row is left: the index rows are hints and readers fold the stream). A
// game that fails to start is retried once after the first pass, then logged;
// its players' reconnect starts it (2c-2). A store outage on a shard read is
// retried with capped backoff until ctx ends, so a node that starts while the
// store is down serves connections and adopts when it can.
func (s *Server) AdoptLive(ctx context.Context, concurrency int) (adopted, skipped, failed int, err error) {
	if s.idxCfg == nil {
		return 0, 0, 0, errors.New("transport: AdoptLive needs the live-game index (SetIndex)")
	}
	st, ok := s.reg.store.(game.IndexedStore)
	if !ok {
		return 0, 0, 0, errors.New("transport: the store has no live-game index")
	}
	if concurrency < 1 {
		concurrency = 32
	}
	var ids []string
	for shard := 0; shard < s.idxCfg.Shards; shard++ {
		rows, err := listLive(ctx, st, shard)
		if err != nil {
			return 0, 0, 0, err
		}
		for _, r := range rows {
			ids = append(ids, r.GameID)
		}
	}
	sort.Strings(ids)
	total := len(ids)
	for pass := 0; pass < 2 && len(ids) > 0; pass++ {
		var (
			mu    sync.Mutex
			wg    sync.WaitGroup
			sem   = make(chan struct{}, concurrency)
			retry []string
		)
	launch:
		for _, id := range ids {
			select {
			case <-ctx.Done():
				break launch
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(id string) {
				defer func() { <-sem; wg.Done() }()
				_, err := s.startGame(ctx, id, "", nil)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					adopted++
				case errors.Is(err, errGameEnded):
					skipped++
				default:
					log.Printf("adopt: game %s not started (pass %d): %v", id, pass+1, err)
					retry = append(retry, id)
				}
			}(id)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return adopted, skipped, total - adopted - skipped, ctx.Err()
		}
		sort.Strings(retry)
		ids = retry
	}
	return adopted, skipped, len(ids), nil
}

// listLive reads one shard, retrying a failing store with capped backoff until
// ctx ends.
func listLive(ctx context.Context, st game.IndexedStore, shard int) ([]game.LiveEntry, error) {
	for wait := 200 * time.Millisecond; ; {
		rows, err := st.LiveGames(ctx, shard)
		if err == nil {
			return rows, nil
		}
		log.Printf("adopt: reading live shard %d: %v (retrying)", shard, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		if wait *= 2; wait > 10*time.Second {
			wait = 10 * time.Second
		}
	}
}

var (
	errNoStream  = errors.New("transport: game has no stream")
	errGameEnded = errors.New("transport: game has already ended")
)
