package game

import (
	"context"
	"testing"
)

// TestNextTickMsIsExact (#182): NextTickMs is the first instant a tick ends
// the game. One ms before it a tick is a no-op; at it the tick ends the game.
// The flag timer relies on both: it sleeps until NextTickMs and does nothing
// in between.
func TestNextTickMsIsExact(t *testing.T) {
	tickAt := func(m *sim, atMs uint64) Decision {
		t.Helper()
		m.o.material[m.s.CurFEN+"#0"] = true
		m.o.material[m.s.CurFEN+"#1"] = true
		d, err := Decide(context.Background(), m.s, CmdTickFlag{}, atMs, m.opts, m.o)
		if err != nil {
			t.Fatalf("tick at %d: %v", atMs, err)
		}
		return d
	}
	exact := func(t *testing.T, m *sim) {
		t.Helper()
		at, ok := NextTickMs(m.s, m.opts)
		if !ok {
			t.Fatal("no deadline for a live game")
		}
		if d := tickAt(m, at-1); len(d.Events) != 0 {
			t.Fatalf("a tick 1 ms before the deadline acted: %+v", d)
		}
		if d := tickAt(m, at); d.Effect != EffectEnded {
			t.Fatalf("a tick at the deadline did not end the game: %+v", d)
		}
	}

	t.Run("first-move timeout before any move", func(t *testing.T) { exact(t, newSim(t, false)) })
	t.Run("first-move timeout after one move", func(t *testing.T) {
		m := newSim(t, false)
		m.move(3_000)
		exact(t, m)
	})
	t.Run("flag for the side to move", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(5)
		exact(t, m)
	})
	t.Run("flag with a per-move delay", func(t *testing.T) {
		m := newSim(t, false)
		m.opts.TimeControl.PerMoveMs = 2_000
		m.playTo(4)
		exact(t, m)
	})
	t.Run("the deadline follows the running seat", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.move(290_000) // white is down to about 13 s
		m.move(1_000)   // black hands the clock back to white
		before, _ := NextTickMs(m.s, m.opts)
		exact(t, m)
		m.move(1_000) // white moves: black, with far more time, runs
		after, _ := NextTickMs(m.s, m.opts)
		if after <= before {
			t.Fatalf("deadline did not move with the running seat: %d -> %d", before, after)
		}
		exact(t, m)
	})
	t.Run("no deadline once ended", func(t *testing.T) {
		m := newSim(t, false)
		m.playTo(2)
		m.do(CmdResign{By: 0}, 1_000)
		if _, ok := NextTickMs(m.s, m.opts); ok {
			t.Fatal("an ended game has a deadline")
		}
	})
}
