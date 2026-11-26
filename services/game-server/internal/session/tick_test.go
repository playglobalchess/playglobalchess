package session

import (
	"context"
	"testing"
	"time"
)

// TestNextTickPublishedAfterEveryCommand (#182): the session publishes its
// flag deadline after each command and signals TickChanged when it moves, so
// the flag timer never needs to poll.
func TestNextTickPublishedAfterEveryCommand(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	drain := func() bool {
		select {
		case <-s.TickChanged():
			return true
		default:
			return false
		}
	}
	first := s.NextTick()
	if !first.Due || first.Ended {
		t.Fatalf("a fresh live game has no first-move deadline: %+v", first)
	}
	drain()
	for i, uci := range []string{"e2e4", "e7e5"} {
		fc.add(time.Second)
		if o, err := s.SubmitMove(ctx, uint8(i), uint32(i+1), uci, 0); err != nil || !o.Accepted {
			t.Fatalf("%s: %+v %v", uci, o, err)
		}
		if !drain() {
			t.Fatalf("no TickChanged after %s", uci)
		}
	}
	running := s.NextTick()
	// White's 5-minute clock now runs from the second move (blitz 5+3).
	if want := uint64(fc.now().UnixMilli()) + 300_000 + 1; !running.Due || running.AtMs != want {
		t.Fatalf("deadline after both first moves = %+v, want AtMs %d", running, want)
	}
	if _, err := s.StateSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if drain() {
		t.Fatal("TickChanged signalled for a command that changed nothing")
	}
	if err := s.Resign(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if tk := s.NextTick(); !tk.Ended || tk.Due || !drain() {
		t.Fatalf("after resign: %+v, want Ended, no deadline, and a signal", tk)
	}
}

// TestStaleSessionPublishesDueNow (#182 review): while the projection is behind
// the store (here: after a panicked command) a deadline derived from it could
// be minutes late, so the session publishes a 1 s recheck and the timer's next check
// reloads, as the old poll did.
func TestStaleSessionPublishesDueNow(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	c := &panicCmd{done: make(chan struct{})}
	if err := s.dispatch(context.Background(), c, c.done); err != nil {
		t.Fatal(err)
	}
	if tk := s.NextTick(); !tk.Due || tk.AtMs != uint64(fc.now().Add(staleRecheck).UnixMilli()) {
		t.Fatalf("after a panic the tick must be staleRecheck away, got %+v", tk)
	}
	if _, err := s.TickFlagCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tk := s.NextTick(); tk.AtMs <= uint64(fc.now().UnixMilli())+1000 {
		t.Fatalf("the check must reload and republish a real deadline, got %+v", tk)
	}
}
