// Package matchmaking implements GS-5 (SPEC-S2-GAME-SERVER §3.4): the seek pool
// that pairs two compatible seekers and creates the opening game stream.
//
// Structure (so the correctness-critical bits are pure and testable without
// Redis): the PAIRING DECISION is a pure function over the current waiting set
// (this file); the Redis-backed waiting set lives behind the SeekPool interface
// (redis.go); game creation on a pair writes GameCreated+PlayerJoined to the
// GS-4 EventStore (create.go). No chess logic (NFR-5) — the matchmaker assigns
// seats/colors and carries the Standard start FEN as an opaque string.
package matchmaking

import (
	"context"
	"time"
)

// StandardStartFEN is the opening position carried opaquely into GameCreated.
// The matchmaker does NOT parse or reason about it (NFR-5); the engine owns FEN
// semantics. It is the canonical Standard start position.
const StandardStartFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

// Seeker is one open seek. pool_id already encodes rating-band × format ×
// time-control (parent spec §3.3), so a Seeker's PoolID selects the waiting set
// and Rating orders it WITHIN that set.
type Seeker struct {
	SeekID      string
	UserID      string
	PoolID      string
	TimeControl string // the TimeControlId string carried into GameCreated
	Rating      int    // from RatingSource; seed until X-4 (Q-MM2)
	Rated       bool
	EnqueuedAt  time.Time
}

// RatingSource yields a seeker's rating for pool ordering. GS-5 ships a seed
// implementation (SeedRatingSource, 1500) because the rating service (X-4) is
// not built yet (Q-MM2); X-4 later provides the real implementation and the
// matchmaker does not change — the dependency is on this abstraction, not a
// number.
type RatingSource interface {
	RatingFor(ctx context.Context, userID, poolID string) (int, error)
}

// SeedRatingSource returns a fixed seed rating for every user until X-4 lands.
// Early pairing is therefore effectively random (there is no rating signal to be
// fair about yet) — correct for F-01 bring-up (Q-MM2).
type SeedRatingSource struct{ Seed int }

func (s SeedRatingSource) RatingFor(ctx context.Context, _, _ string) (int, error) {
	if s.Seed == 0 {
		return 1500, nil
	}
	return s.Seed, nil
}

// Window is the rating tolerance for a pair: a seeker pairs only with a waiting
// seeker whose rating is within W of its own. It WIDENS with the age of the
// oldest waiting candidate so a lonely seeker eventually matches anyone
// (Q-MM/§3.2). The numbers are a placeholder the rating service (X-4) will tune;
// they are config, not a frozen constant.
type Window struct {
	Base  int // initial tolerance (rating points)
	Slope int // added tolerance per second of wait
	Max   int // cap
}

// DefaultWindow is the F-01 placeholder schedule.
var DefaultWindow = Window{Base: 100, Slope: 20, Max: 1000}

// widthFor returns the rating tolerance for a candidate that has waited `age`.
func (w Window) widthFor(age time.Duration) int {
	width := w.Base + w.Slope*int(age.Seconds())
	if width > w.Max {
		width = w.Max
	}
	return width
}

// bestMatch is the PURE pairing core: given a new seeker and the current waiting
// set (NOT including the new seeker), it returns the best compatible waiting
// seeker to pair with, or (Seeker{}, false) if none qualifies right now.
//
// Compatibility (all required):
//   - different seek (never self-pair);
//   - same rated-ness (the pool already separates rated/unrated, so this is an
//     invariant check, not a filter — a violation means a pool-routing bug);
//   - same pool and same time control (same reasoning; #93);
//   - rating within the candidate's current widening window (uses the candidate's
//     wait age, since the candidate is the one who has been waiting).
//
// Tie-break (deterministic, so the pure core is testable against fixed inputs):
// closest rating, then oldest EnqueuedAt, then lowest SeekID.
func bestMatch(newSeeker Seeker, waiting []Seeker, w Window, now time.Time) (Seeker, bool) {
	var best Seeker
	found := false
	bestDelta := 0
	for _, c := range waiting {
		if c.SeekID == newSeeker.SeekID {
			continue // never self-pair
		}
		if c.PoolID != newSeeker.PoolID || c.Rated != newSeeker.Rated || c.TimeControl != newSeeker.TimeControl {
			// Pool invariant (same pool_id ⇒ same rated-ness and time control);
			// the time-control check is a second line of defence behind the
			// transport's pool catalogue (#93), so a mismatch never pairs.
			continue
		}
		delta := newSeeker.Rating - c.Rating
		if delta < 0 {
			delta = -delta
		}
		if delta > w.widthFor(now.Sub(c.EnqueuedAt)) {
			continue // outside the candidate's current window
		}
		if !found || better(delta, c, bestDelta, best) {
			best, bestDelta, found = c, delta, true
		}
	}
	return best, found
}

// better reports whether candidate (delta,c) beats the current best
// (bestDelta,best): closer rating wins; tie → older enqueue; tie → lower SeekID.
func better(delta int, c Seeker, bestDelta int, best Seeker) bool {
	if delta != bestDelta {
		return delta < bestDelta
	}
	if !c.EnqueuedAt.Equal(best.EnqueuedAt) {
		return c.EnqueuedAt.Before(best.EnqueuedAt)
	}
	return c.SeekID < best.SeekID
}
