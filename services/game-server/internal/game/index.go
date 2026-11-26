package game

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"time"
)

// This file defines the live-game index (SPEC-S2-GAME-SERVER-GS8 §4.3, ADR-16
// amendment 2026-11-27). The index is HINTS about the event stream, never truth:
// every reader folds the stream before acting. Entries are written in the same
// atomic write as the game's opening and ending events, so they cannot disagree
// with the stream on the normal path.

// IndexTable names one of the three index tables.
type IndexTable string

const (
	// IdxUserLive: partition user_id, sort game_id. A user's live games.
	IdxUserLive IndexTable = "user_live_games"
	// IdxLive: partition shard, sort game_id. Every live game, for adoption.
	IdxLive IndexTable = "live_games"
	// IdxUserRecent: partition user_id, sort game_id, TTL. A user's recently
	// ended games whose result has not been delivered yet (replaces missedEnds).
	IdxUserRecent IndexTable = "user_recent_games"
)

// IndexOpKind is what an IndexOp does.
type IndexOpKind uint8

const (
	// IndexPut writes (or replaces) an entry. Idempotent.
	IndexPut IndexOpKind = iota + 1
	// IndexDelete removes an entry; deleting a missing entry is not an error.
	IndexDelete
	// IndexUpdate changes the owner fields of an EXISTING live_games entry; a
	// missing entry fails the whole write with ErrIndexMissing (stage 2 claims).
	IndexUpdate
)

// ErrIndexMissing is returned when an IndexUpdate names an entry that does not
// exist. Nothing is committed, events included.
var ErrIndexMissing = errors.New("event store: index entry missing")

// IndexOp is one index change inside an atomic write.
type IndexOp struct {
	Table IndexTable
	Kind  IndexOpKind
	// Key is the partition key: the user id for the user tables, the shard
	// number in decimal for live_games (use IndexShard to compute it).
	Key    string
	GameID string
	// Seat is the user's seat (user tables, Put only).
	Seat uint8
	// OwnerNode and OwnerIncarnation are the live_games owner fields (Put and
	// Update). The pairing process writes itself at the opening.
	OwnerNode        string
	OwnerIncarnation string
	// EndedAt and ExpiresAt apply to user_recent_games Put: when the game
	// ended, and when the entry may be dropped (DynamoDB TTL).
	EndedAt   time.Time
	ExpiresAt time.Time
}

// LiveEntry is one row of live_games.
type LiveEntry struct {
	GameID           string
	OwnerNode        string
	OwnerIncarnation string
}

// UserEntry is one row of a user table.
type UserEntry struct {
	GameID  string
	Seat    uint8
	EndedAt time.Time // user_recent_games only
}

// IndexedStore is implemented by stores that keep the live-game index. It is a
// separate interface from EventStore so existing callers and test doubles are
// unchanged until the session adopts it (GS-8 plan step 2).
type IndexedStore interface {
	EventStore
	// AppendIndexed commits evs at consecutive seqs from expectedSeq AND applies
	// ops, all in one atomic write: either everything commits or nothing does. A
	// seq conflict returns ErrSeqConflict; a failing IndexUpdate returns
	// ErrIndexMissing. With no ops it behaves as AppendBatch. At least one event
	// is required.
	AppendIndexed(ctx context.Context, streamID string, expectedSeq uint32, evs []Event, ops []IndexOp) error
	// LiveGames returns every live_games entry of one shard (all pages).
	LiveGames(ctx context.Context, shard int) ([]LiveEntry, error)
	// UserLiveGames returns up to limit of the user's live games, newest first
	// (game ids are time-ordered UUIDv7).
	UserLiveGames(ctx context.Context, userID string, limit int) ([]UserEntry, error)
	// UserRecentGames returns the user's recently ended games that have not
	// expired, newest first.
	UserRecentGames(ctx context.Context, userID string, limit int) ([]UserEntry, error)
}

// IndexShard maps a game id to its live_games shard. shards must be positive.
func IndexShard(gameID string, shards int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(gameID))
	return int(h.Sum32() % uint32(shards))
}

// ShardKey is the live_games partition key for a shard number.
func ShardKey(shard int) string { return strconv.Itoa(shard) }
