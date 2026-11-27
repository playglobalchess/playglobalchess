package transport

import "sync"

// gamestate.go holds the per-game transport state, sharded by game id (#174).
//
// Every per-move and per-ending path touches ONE game, so it takes ONE shard's
// lock and does O(seats) work. Nothing on those paths iterates a map sized by
// the number of connections or games on the node. User-keyed state (the user's
// current connection, the games they are seated in, missed endings) lives under
// Server.userMu and is touched at AUTHENTICATE, MATCHED, disconnect and game end,
// never by MOVE.
//
// Lock order: Server.userMu, then a shard's mu. A path that holds a shard lock
// never takes userMu (dropGame releases the shard before it takes userMu).

const gameShardCount = 64

// gameEntry is one game's transport state.
type gameEntry struct {
	conns map[uint8]Conn   // seat -> the connection seated there
	users map[uint8]string // seat -> user id, bound at MATCHED, kept until dropGame
	// joined is closed the FIRST time any seat binds a connection, so the join
	// timeout can tell "a seat joined" (even if it later disconnected) from
	// "nobody ever joined" (the GS-5 ghost-claim case, reviewer C1).
	joined chan struct{}
	// endClaimed latches that this game's GAME_END has been claimed, so
	// concurrent end paths do not each broadcast it.
	endClaimed bool
	// ticker latches that this game's flag ticker is running (one per live game).
	ticker bool
}

type gameShard struct {
	mu    sync.Mutex
	games map[string]*gameEntry
}

// gameTable is the sharded game-id -> gameEntry index. The zero value is ready.
type gameTable [gameShardCount]gameShard

func (t *gameTable) shard(gameID string) *gameShard {
	h := uint32(2166136261) // FNV-1a, inlined so a lookup does not allocate
	for i := 0; i < len(gameID); i++ {
		h = (h ^ uint32(gameID[i])) * 16777619
	}
	return &t[h%gameShardCount]
}

// entryLocked returns the game's entry, creating it. sh.mu held.
func (sh *gameShard) entryLocked(gameID string) *gameEntry {
	e := sh.games[gameID]
	if e == nil {
		if sh.games == nil {
			sh.games = make(map[string]*gameEntry)
		}
		e = &gameEntry{conns: make(map[uint8]Conn, 2), users: make(map[uint8]string, 2)}
		sh.games[gameID] = e
	}
	return e
}

// tracked reports whether any transport state exists for gameID (tests).
func (t *gameTable) tracked(gameID string) bool {
	sh := t.shard(gameID)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	_, ok := sh.games[gameID]
	return ok
}
