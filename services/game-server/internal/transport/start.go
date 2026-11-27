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

// gameRole says why a session is being started.
type gameRole int

const (
	// rolePair is a game just created by the matchmaker. Its join timer is armed
	// by deliverMatched, after the seats bind.
	rolePair gameRole = iota
	// roleResync is a game a reconnecting user is seated in.
	roleResync
	// roleAdopt is a game found in the live-game index at start.
	roleAdopt
)

// startGame builds (or finds) the game's session from its durable stream, fills
// the per-game seat-to-user map from the stream, and arms the flag ticker once.
// The join timer is NOT armed here: its "joined" signal is process-local, so
// arming it for a rebuilt game would abort a live game whose players are
// briefly disconnected. A rebuilt game below two plies is bounded by the
// first-move timeout instead (GS-7 section 3.5).
func (s *Server) startGame(ctx context.Context, gameID string, role gameRole) (*session.Session, error) {
	events, err := s.reg.loadStream(ctx, gameID)
	if err != nil {
		return nil, err
	}
	var tcID string
	players := map[uint8]string{}
	for _, ev := range events {
		switch {
		case ev.GameCreated != nil:
			tcID = ev.GameCreated.TimeControl
		case ev.PlayerJoined != nil:
			players[ev.PlayerJoined.SeatIdx] = ev.PlayerJoined.UserID
		}
	}
	if tcID == "" {
		return nil, fmt.Errorf("%w: %s", errNoStream, gameID)
	}
	sess, err := s.reg.GetOrCreate(gameID, tcID)
	if err != nil {
		return nil, err
	}
	sh := s.gt.shard(gameID)
	sh.mu.Lock()
	e := sh.entryLocked(gameID)
	for seat, uid := range players {
		e.users[seat] = uid
	}
	arm := !e.ticker
	e.ticker = true
	sh.mu.Unlock()
	if arm {
		s.armFlagTicker(gameID)
	}
	return sess, nil
}

// AdoptLive starts every game in the live-game index (GS-8 section 4.4), oldest
// first, with bounded concurrency. It needs SetIndex. A game that fails to start
// is logged and left for its players' reconnect (AUTHENTICATE starts it then). A
// store outage is retried with capped backoff until ctx ends, so a node that
// starts while the store is down serves connections and adopts when it can.
func (s *Server) AdoptLive(ctx context.Context, concurrency int) (adopted, failed int, err error) {
	if s.idxCfg == nil {
		return 0, 0, errors.New("transport: AdoptLive needs the live-game index (SetIndex)")
	}
	st, ok := s.reg.store.(game.IndexedStore)
	if !ok {
		return 0, 0, errors.New("transport: the store has no live-game index")
	}
	if concurrency < 1 {
		concurrency = 32
	}
	var ids []string
	for shard := 0; shard < s.idxCfg.Shards; shard++ {
		rows, err := listLive(ctx, st, shard)
		if err != nil {
			return 0, 0, err
		}
		for _, r := range rows {
			ids = append(ids, r.GameID)
		}
	}
	sort.Strings(ids) // UUIDv7: oldest first
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, concurrency)
	)
	for _, id := range ids {
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer func() { <-sem; wg.Done() }()
			_, err := s.startGame(ctx, id, roleAdopt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				log.Printf("adopt: game %s not started: %v", id, err)
				return
			}
			adopted++
		}(id)
	}
	wg.Wait()
	return adopted, failed, ctx.Err()
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

var errNoStream = errors.New("transport: game has no stream")
