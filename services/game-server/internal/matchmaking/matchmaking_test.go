package matchmaking

import (
	"testing"
	"time"
)

func sk(id string, rating int, ageSec int, rated bool) Seeker {
	return Seeker{
		SeekID: id, UserID: "u-" + id, PoolID: "blitz-5+3", TimeControl: "blitz-5+3",
		Rating: rating, Rated: rated, EnqueuedAt: time.Unix(1000, 0).Add(-time.Duration(ageSec) * time.Second),
	}
}

var now = time.Unix(1000, 0)

func TestBestMatch_ClosestRatingWins(t *testing.T) {
	newS := sk("new", 1500, 0, true)
	waiting := []Seeker{sk("a", 1400, 0, true), sk("b", 1480, 0, true), sk("c", 1600, 0, true)}
	got, ok := bestMatch(newS, waiting, DefaultWindow, now)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.SeekID != "b" { // 1480 is closest to 1500 (delta 20)
		t.Fatalf("matched %s (rating %d), want b (1480, closest)", got.SeekID, got.Rating)
	}
}

func TestBestMatch_NeverSelfPair(t *testing.T) {
	newS := sk("new", 1500, 0, true)
	// Only candidate is the seeker itself (same SeekID) → no match.
	if _, ok := bestMatch(newS, []Seeker{newS}, DefaultWindow, now); ok {
		t.Fatal("self-paired — must never happen")
	}
}

func TestBestMatch_OutsideWindowNoMatch(t *testing.T) {
	newS := sk("new", 1500, 0, true)
	// Candidate 900 away, waiting 0s → window is Base=100, far outside.
	if _, ok := bestMatch(newS, []Seeker{sk("far", 600, 0, true)}, DefaultWindow, now); ok {
		t.Fatal("matched outside the window")
	}
}

func TestBestMatch_WindowWidensWithAge(t *testing.T) {
	newS := sk("new", 1500, 0, true)
	far := sk("far", 1100, 0, true) // delta 400
	// At age 0: window 100 → no match.
	if _, ok := bestMatch(newS, []Seeker{far}, DefaultWindow, now); ok {
		t.Fatal("matched a 400-delta seeker at age 0 (window 100)")
	}
	// At age 20s: window = 100 + 20*20 = 500 ≥ 400 → match.
	farOld := sk("far", 1100, 20, true)
	if _, ok := bestMatch(newS, []Seeker{farOld}, DefaultWindow, now); !ok {
		t.Fatal("did NOT match a 400-delta seeker after 20s (window should be 500)")
	}
}

func TestBestMatch_RatedInvariant(t *testing.T) {
	newS := sk("new", 1500, 0, true) // rated
	// An unrated candidate in (hypothetically) the same slice must not pair.
	unrated := sk("u", 1500, 0, false)
	if _, ok := bestMatch(newS, []Seeker{unrated}, DefaultWindow, now); ok {
		t.Fatal("rated seeker paired with an unrated one")
	}
}

func TestBestMatch_TieBreakOldestThenID(t *testing.T) {
	newS := sk("new", 1500, 0, true)
	// Two candidates, equal rating (equal delta). Older enqueue wins.
	younger := sk("younger", 1480, 2, true) // waited 2s
	older := sk("older", 1480, 50, true)    // waited 50s
	got, ok := bestMatch(newS, []Seeker{younger, older}, DefaultWindow, now)
	if !ok || got.SeekID != "older" {
		t.Fatalf("tie-break: matched %q, want older", got.SeekID)
	}
	// Exactly equal rating AND age → lowest SeekID wins (deterministic).
	eqA := Seeker{SeekID: "a", PoolID: "blitz-5+3", Rating: 1480, Rated: true, EnqueuedAt: now.Add(-5 * time.Second)}
	eqB := Seeker{SeekID: "b", PoolID: "blitz-5+3", Rating: 1480, Rated: true, EnqueuedAt: now.Add(-5 * time.Second)}
	got2, _ := bestMatch(newS, []Seeker{eqB, eqA}, DefaultWindow, now)
	if got2.SeekID != "a" {
		t.Fatalf("full-tie: matched %q, want a (lowest SeekID)", got2.SeekID)
	}
}

func TestSeedRatingSource(t *testing.T) {
	var rs RatingSource = SeedRatingSource{}
	r, err := rs.RatingFor(nil, "u", "p")
	if err != nil || r != 1500 {
		t.Fatalf("seed rating = %d, %v; want 1500, nil", r, err)
	}
}
