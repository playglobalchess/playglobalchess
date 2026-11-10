package matchmaking

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// SeekPool is the ephemeral waiting set (ADR-10 Redis; an in-memory fake backs
// the pure tests). It separates the ATOMIC CLAIM (reserve a pair so two nodes
// cannot both take the same seeker) from the actual removal, which the
// matchmaker defers until AFTER the opening stream is durably appended (Q-MM6).
type SeekPool interface {
	// Enqueue adds a seeker to its pool's waiting set (with a TTL so an abandoned
	// seek self-expires).
	Enqueue(ctx context.Context, s Seeker) error
	// TryClaim atomically reserves the best compatible waiting partner for the
	// new seeker (per the pure bestMatch over the pool's current members) WITHOUT
	// removing either — it flips both to a claimed state so a concurrent node
	// cannot claim the same partner. Returns the claimed partner and true, or
	// false if no partner qualifies now (the new seeker remains enqueued).
	TryClaim(ctx context.Context, newSeeker Seeker, w Window, now time.Time) (Seeker, bool, error)
	// Release undoes a claim (claim held but game creation failed) so both
	// seekers become pairable again.
	Release(ctx context.Context, seekIDs ...string) error
	// Remove finalizes a match: delete the seekers' pool membership, hash, and
	// claim. poolID is passed EXPLICITLY (the caller knows it) so removal never
	// depends on reading a possibly-expired seek hash to learn the pool — that
	// dependency orphaned ZSET members when the hash TTL'd first. Called only
	// AFTER the opening stream is durably appended (Q-MM6).
	Remove(ctx context.Context, poolID string, seekIDs ...string) error
	// Cancel withdraws a single unmatched seeker (disconnect / explicit).
	Cancel(ctx context.Context, poolID, seekID string) error
}

// Match is the result of a successful pairing: the created game and the opening
// facts the transport (GS-6) needs to emit MATCHED{start_state}. StartState is
// deliberately the opening fields (FEN + time control + seats), not a wire
// StateBody — GS-6 owns assembling the §4A.6 frame from these.
type Match struct {
	GameID      string
	StreamID    string
	StartFEN    string
	TimeControl string
	Rated       bool
	Seats       []game.Seat      // seat_idx → color (0=white,1=black)
	Players     map[uint8]string // seat_idx → user_id
}

// Matchmaker turns a SEEK into a created game. It owns no chess logic (NFR-5): it
// assigns seats/colors and carries the Standard start FEN as an opaque string.
type Matchmaker struct {
	pool    SeekPool
	store   game.EventStore
	ratings RatingSource
	window  Window
	now     func() time.Time
	// coin flips white/black for the two seekers (Q-MM3). Injectable so tests are
	// deterministic. Returns true if the NEW seeker takes white (seat 0).
	coin func() bool
}

// New builds a Matchmaker with the default window and a time-seeded coin for
// seat/color assignment. Tests override m.coin (and m.now) after construction
// for determinism — there is no rng parameter.
func New(pool SeekPool, store game.EventStore, ratings RatingSource) *Matchmaker {
	if ratings == nil {
		ratings = SeedRatingSource{}
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &Matchmaker{
		pool:    pool,
		store:   store,
		ratings: ratings,
		window:  DefaultWindow,
		now:     time.Now,
		coin:    func() bool { return r.Intn(2) == 0 },
	}
}

// Seek enters a seeker into its pool and attempts an immediate pair. On a pair
// it creates the game and returns (match, true, nil). With no partner available
// now it leaves the seeker enqueued and returns (Match{}, false, nil) — NOT an
// error.
//
// Ordering matters twice:
//   - ENQUEUE BEFORE CLAIM (closes the mutual-miss race): if two compatible
//     seekers A and B arrive concurrently and we claimed before enqueuing, each
//     could read an empty pool, miss the other, and both sit idle until a third
//     seek or the TTL. Enqueuing FIRST means a concurrent partner is already
//     visible; the atomic claim then resolves the "both try to claim each other"
//     case — exactly one wins the (A,B) claim and creates the game, the loser's
//     claim fails and it stays enqueued (and is removed by the winner), so a
//     ready couple always pairs on the second of the two seeks.
//   - CLAIM → APPEND → REMOVE (Q-MM6, the durability-safe path): createGame
//     AppendBatches the opening stream (durable truth) BEFORE Remove finalizes
//     the claim. A crash before the append loses only ephemeral claims (they TTL
//     out and the seekers re-seek); a crash after it leaves a created, reachable
//     game.
func (m *Matchmaker) Seek(ctx context.Context, s Seeker) (Match, bool, error) {
	if s.SeekID == "" {
		s.SeekID = uuid.NewString()
	}
	if s.EnqueuedAt.IsZero() {
		s.EnqueuedAt = m.now()
	}
	// Fill the rating from the source (seed until X-4, Q-MM2) if unset.
	if s.Rating == 0 {
		r, err := m.ratings.RatingFor(ctx, s.UserID, s.PoolID)
		if err != nil {
			return Match{}, false, fmt.Errorf("matchmaking: rating: %w", err)
		}
		s.Rating = r
	}

	// Enqueue FIRST so a concurrently-arriving compatible seeker can see us (the
	// mutual-miss fix). TryClaim excludes our own SeekID, so being in the pool
	// does not let us claim ourselves.
	if err := m.pool.Enqueue(ctx, s); err != nil {
		return Match{}, false, fmt.Errorf("matchmaking: enqueue: %w", err)
	}

	partner, claimed, err := m.pool.TryClaim(ctx, s, m.window, m.now())
	if err != nil {
		return Match{}, false, fmt.Errorf("matchmaking: claim: %w", err)
	}
	if !claimed {
		// No partner now — remain enqueued; a counter-seek (or a concurrent
		// arrival that won its own claim) will pair us.
		return Match{}, false, nil
	}

	// Claimed a partner — create the game (append FIRST, Q-MM6).
	match, err := m.createGame(ctx, s, partner)
	if err != nil {
		// Creation failed: release the claims so both become pairable again.
		_ = m.pool.Release(ctx, s.SeekID, partner.SeekID)
		return Match{}, false, err
	}
	// Game is durable truth now — finalize by removing both from the pool.
	if err := m.pool.Remove(ctx, s.PoolID, s.SeekID, partner.SeekID); err != nil {
		// The game EXISTS and is reachable; a failed Remove leaves stale claims
		// that TTL out. Do NOT fail the match for this — log-worthy (X-14), but
		// the safe direction (Q-MM6). Return the match.
		return match, true, nil
	}
	return match, true, nil
}

// createGame allocates the game, assigns seats/colors, and commits the opening
// stream atomically (GameCreated + two PlayerJoined at seqs 0,1,2 in one
// AppendBatch — Q-MM4). seat 0 = white, seat 1 = black; the coin decides which
// seeker takes white (Q-MM3).
func (m *Matchmaker) createGame(ctx context.Context, a, b Seeker) (Match, error) {
	gameID := uuid.NewString()
	streamID := game.StreamIDFor(gameID)

	// Coin flip: whiteSeeker takes seat 0 (white), the other seat 1 (black).
	whiteSeeker, blackSeeker := a, b
	if !m.coin() {
		whiteSeeker, blackSeeker = b, a
	}
	seats := []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}}
	players := map[uint8]string{0: whiteSeeker.UserID, 1: blackSeeker.UserID}

	// bestMatch pairs only identical pool_id/rated/time_control (#93), so the
	// new seeker's (a) values are both seekers' values.
	opening := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: a.PoolID, Rated: a.Rated, Seats: seats,
			StartFEN: StandardStartFEN, TimeControl: a.TimeControl,
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: whiteSeeker.UserID}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: blackSeeker.UserID}},
	}

	// Append the opening ATOMICALLY at seq 0 (Q-MM4). A fresh UUID cannot collide,
	// but a seq conflict here means a game_id reuse — abandon rather than
	// overwrite (never clobber an existing stream).
	if err := m.store.AppendBatch(ctx, streamID, 0, opening); err != nil {
		if errors.Is(err, game.ErrSeqConflict) {
			return Match{}, fmt.Errorf("matchmaking: game_id %s already has a stream (abandoned, not overwritten): %w", gameID, err)
		}
		return Match{}, fmt.Errorf("matchmaking: append opening: %w", err)
	}

	return Match{
		GameID: gameID, StreamID: streamID,
		StartFEN: StandardStartFEN, TimeControl: a.TimeControl, Rated: a.Rated,
		Seats: seats, Players: players,
	}, nil
}
