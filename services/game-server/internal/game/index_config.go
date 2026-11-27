package game

import (
	"errors"
	"fmt"
	"time"
)

// IndexConfig turns on the live-game index writes (GS-8 §4.3). Without it a
// session and the matchmaker write events only, exactly as before step 2.
type IndexConfig struct {
	// Shards is the number of live_games partitions. It is hard to change once
	// games exist, so it is chosen with headroom (DefaultIndexShards).
	Shards int
	// NodeID and Incarnation name the pairing process in live_games. Stage 2's
	// takeover sweep finds games that never failed over from them. GS-9 makes
	// the incarnation a persisted counter; until then it is per process.
	NodeID      string
	Incarnation string
	// RecentTTL is how long an ended game stays in user_recent_games, the
	// retention of the in-memory missed-ending record it replaces.
	RecentTTL time.Duration
}

const (
	// DefaultIndexShards is the default live_games shard count.
	DefaultIndexShards = 64
	// DefaultRecentTTL matches the 24 h missed-ending retention it replaces.
	DefaultRecentTTL = 24 * time.Hour
)

// DefaultIndexConfig is the index config for a node.
func DefaultIndexConfig(nodeID, incarnation string) IndexConfig {
	return IndexConfig{Shards: DefaultIndexShards, NodeID: nodeID, Incarnation: incarnation, RecentTTL: DefaultRecentTTL}
}

// ErrBadIndexConfig is returned for an IndexConfig that cannot be used.
var ErrBadIndexConfig = errors.New("game: invalid index config")

// Validate rejects a config that would write unusable index rows.
func (c IndexConfig) Validate() error {
	switch {
	case c.Shards <= 0:
		return fmt.Errorf("%w: shards %d must be positive", ErrBadIndexConfig, c.Shards)
	case c.NodeID == "":
		return fmt.Errorf("%w: empty node id", ErrBadIndexConfig)
	case c.RecentTTL <= 0:
		return fmt.Errorf("%w: recent TTL %s must be positive", ErrBadIndexConfig, c.RecentTTL)
	}
	return nil
}

// OpeningOps are the index puts that commit with a game's opening events: one
// user_live_games row per player and one live_games row. players is indexed by
// seat; an empty or repeated user is an error, because the write would be
// refused (one item twice) or would name no one.
func (c IndexConfig) OpeningOps(gameID string, players []string) ([]IndexOp, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ops := make([]IndexOp, 0, len(players)+1)
	for seat, uid := range players {
		if uid == "" || seen[uid] {
			return nil, fmt.Errorf("%w: seat %d has an empty or repeated user %q", ErrBadIndexOp, seat, uid)
		}
		seen[uid] = true
		ops = append(ops, IndexOp{Table: IdxUserLive, Kind: IndexPut, Key: uid, GameID: gameID, Seat: uint8(seat)})
	}
	ops = append(ops, IndexOp{
		Table: IdxLive, Kind: IndexPut, Key: ShardKey(IndexShard(gameID, c.Shards)), GameID: gameID,
		OwnerNode: c.NodeID, OwnerIncarnation: c.Incarnation,
	})
	return ops, nil
}

// EndingOps are the index changes that commit with a game's GameEnded: the live
// rows go, and each player gets a user_recent_games row so a result they did not
// see is still delivered after a restart. A seat with no user (a game created
// before the index, or a malformed stream) is skipped rather than failing the
// ending: a game must always be able to end.
func (c IndexConfig) EndingOps(gameID string, players []string, endedAt time.Time) ([]IndexOp, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ops []IndexOp
	for seat, uid := range players {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		ops = append(ops,
			IndexOp{Table: IdxUserLive, Kind: IndexDelete, Key: uid, GameID: gameID},
			IndexOp{Table: IdxUserRecent, Kind: IndexPut, Key: uid, GameID: gameID, Seat: uint8(seat),
				EndedAt: endedAt, ExpiresAt: endedAt.Add(c.RecentTTL)},
		)
	}
	ops = append(ops, IndexOp{Table: IdxLive, Kind: IndexDelete, Key: ShardKey(IndexShard(gameID, c.Shards)), GameID: gameID})
	return ops, nil
}

// HasEnding reports whether any of evs is a GameEnded.
func HasEnding(evs []Event) bool {
	for i := range evs {
		if evs[i].Type == EvGameEnded {
			return true
		}
	}
	return false
}
