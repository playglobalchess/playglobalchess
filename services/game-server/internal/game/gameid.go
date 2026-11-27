package game

import (
	"fmt"

	"github.com/google/uuid"
)

// NewGameID returns a time-ordered UUIDv7 game id (GS-8 §4.3). The 48-bit
// millisecond prefix makes ids sort oldest first, so an index keyed by game_id
// needs no started_at, and GameCreatedMs recovers the creation time that
// GameCreated does not carry (GS-9 §5).
func NewGameID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("game: new game id: %w", err)
	}
	return id.String(), nil
}

// GameCreatedMs returns the creation time, in epoch ms, encoded in a UUIDv7
// game id. A UUIDv4 id (a game created before GS-8 step 2) and a malformed id
// return 0: GS-9 §5 treats such a game as having lived through every outage
// gap, which is the safe direction.
func GameCreatedMs(gameID string) uint64 {
	id, err := uuid.Parse(gameID)
	if err != nil || id.Version() != 7 {
		return 0
	}
	// The first 48 bits are the unix millisecond timestamp (RFC 9562 §5.7).
	return uint64(id[0])<<40 | uint64(id[1])<<32 | uint64(id[2])<<24 |
		uint64(id[3])<<16 | uint64(id[4])<<8 | uint64(id[5])
}
