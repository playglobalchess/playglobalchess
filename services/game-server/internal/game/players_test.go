package game

import "testing"

func TestFoldRecordsPlayersBySeat(t *testing.T) {
	events := []Event{evCreated(false, "blitz-5+3", "startpos"), evJoined(0)}
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Players) != 2 || st.Players[0] != "user-0" || st.Players[1] != "" {
		t.Fatalf("after seat 0 joined, Players = %q", st.Players)
	}
	st, err = Fold(append(events, evJoined(1)), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Players) != 2 || st.Players[0] != "user-0" || st.Players[1] != "user-1" {
		t.Fatalf("after both joined, Players = %q", st.Players)
	}
}

// Evolve must not share the Players slice with the state it came from, or a
// state kept by one reader would change under it (Evolve is copy-on-write).
func TestEvolveDoesNotAliasPlayers(t *testing.T) {
	st, err := Fold([]Event{evCreated(false, "blitz-5+3", "startpos"), evJoined(0)}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	next, err := Evolve(st, evJoined(1), nil, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if st.Players[1] != "" {
		t.Fatalf("the earlier state changed: Players = %q", st.Players)
	}
	if next.Players[1] != "user-1" {
		t.Fatalf("the new state lacks seat 1: Players = %q", next.Players)
	}
}

func TestPlayersSurviveTheGame(t *testing.T) {
	events := []Event{
		evCreated(false, "blitz-5+3", "startpos"), evJoined(0), evJoined(1),
		evMove(1, 0, "e2e4"), evSeat(EvResigned, 1),
		evEnded(TermResign, []Result{ResultWin, ResultLoss}),
	}
	st, err := Fold(events, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if st.Players[0] != "user-0" || st.Players[1] != "user-1" {
		t.Fatalf("Players lost by the end of the game: %q", st.Players)
	}
}
