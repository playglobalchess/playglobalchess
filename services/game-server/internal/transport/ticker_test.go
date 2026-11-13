package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// #97: a store outage at the flag must not kill the game's only forfeit
// driver. The ticker keeps retrying, with capped backoff, until the game ends
// or the server shuts down.

// outageStore goes down at the first write that contains a ClockFlagged (the
// flag) and stays down, failing every Append, AppendBatch and Load without
// committing, until restore is called. It trips only once.
type outageStore struct {
	game.EventStore

	mu       sync.Mutex
	tripped  bool // the outage happens once: the retried flag must then land
	down     bool
	failures int // writes and reads refused while down
	flags    int // flag writes refused: one per failed flag check
}

var errOutage = errors.New("store: unavailable")

func hasFlag(evs []game.Event) bool {
	for _, ev := range evs {
		if ev.Type == game.EvClockFlagged {
			return true
		}
	}
	return false
}

// refuse reports whether this call fails, tripping the outage on the flag.
func (o *outageStore) refuse(evs []game.Event) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.tripped && hasFlag(evs) {
		o.tripped, o.down = true, true
	}
	if o.down {
		o.failures++
		if hasFlag(evs) {
			o.flags++
		}
	}
	return o.down
}

func (o *outageStore) restore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.down = false
}

func (o *outageStore) refused() (bool, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.down, o.failures
}

func (o *outageStore) refusedFlags() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.flags
}

func (o *outageStore) Append(ctx context.Context, stream string, seq uint32, ev game.Event) error {
	if o.refuse([]game.Event{ev}) {
		return errOutage
	}
	return o.EventStore.Append(ctx, stream, seq, ev)
}

func (o *outageStore) AppendBatch(ctx context.Context, stream string, seq uint32, evs []game.Event) error {
	if o.refuse(evs) {
		return errOutage
	}
	return o.EventStore.AppendBatch(ctx, stream, seq, evs)
}

func (o *outageStore) Load(ctx context.Context, stream string) ([]game.Event, error) {
	if o.refuse(nil) {
		return nil, errOutage
	}
	return o.EventStore.Load(ctx, stream)
}

// TestFlagSurvivesStoreOutage: the flag write fails for well over the old
// give-up bound of 10 ticks; the ticker stays alive, and once the store is
// back the game ends on time and both seats get GAME_END.
func TestFlagSurvivesStoreOutage(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 10 * time.Millisecond
	h.srv.flagBackoffMax = 20 * time.Millisecond
	h.useRealClock() // the idle flag needs real elapsed time
	out := &outageStore{EventStore: h.store}
	h.srv.reg.store = out
	w := watchTickers(h)
	ctx := context.Background()

	ca, cb := newFakeConn(), newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	m := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	gameID := m.Matched.GameID

	// White's 80 ms clock runs out; the flag write trips the outage. Hold it
	// until the ticker has retried the flag well past the old give-up bound of
	// 10 failed checks.
	waitUntil(t, 10*time.Second, "the ticker to retry the flag through the outage", func() bool {
		return out.refusedFlags() >= 15
	})
	if w.gone(gameID) {
		t.Fatal("the flag ticker exited during the store outage (#97)")
	}
	if _, ok := h.srv.reg.Get(gameID); !ok {
		t.Fatal("the game was dropped during the outage")
	}

	out.restore()
	geA := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	geB := cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	okTerm := func(x uint8) bool { return x == 0x03 || x == 0x0A }
	if !okTerm(geA.GameEnd.Termination) || !okTerm(geB.GameEnd.Termination) {
		t.Fatalf("termination = 0x%02x/0x%02x, want FLAG or TIMEOUT_VS_INSUFFICIENT", geA.GameEnd.Termination, geB.GameEnd.Termination)
	}
	wantReclaimed(t, h, w, gameID)
	h.srv.tickLog.mu.Lock()
	failing := h.srv.tickLog.failing
	h.srv.tickLog.mu.Unlock()
	if failing != 0 {
		t.Fatalf("node still counts %d failing game(s) after recovery", failing)
	}
}

// TestFlagTickerStopsOnShutdown: with the store down for good, the ticker
// still exits when the server's root context is cancelled.
func TestFlagTickerStopsOnShutdown(t *testing.T) {
	h := newHarness(t)
	h.srv.flagInterval = 10 * time.Millisecond
	h.srv.flagBackoffMax = 20 * time.Millisecond
	h.useRealClock()
	out := &outageStore{EventStore: h.store}
	h.srv.reg.store = out
	w := watchTickers(h)
	ctx := context.Background()

	ca, cb := newFakeConn(), newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	m := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)

	waitUntil(t, 5*time.Second, "the outage to start", func() bool {
		down, n := out.refused()
		return down && n >= 3
	})
	h.cancel()
	waitUntil(t, 2*time.Second, "the flag ticker to exit on shutdown", func() bool { return w.gone(m.Matched.GameID) })
}

// TestTickBackoff pins the retry schedule: doubling from the tick interval,
// capped, never below the interval.
func TestTickBackoff(t *testing.T) {
	iv, max := 100*time.Millisecond, 800*time.Millisecond
	for _, c := range []struct {
		errs int
		want time.Duration
	}{{1, 100 * time.Millisecond}, {2, 200 * time.Millisecond}, {3, 400 * time.Millisecond}, {4, 800 * time.Millisecond}, {5, 800 * time.Millisecond}, {60, 800 * time.Millisecond}} {
		if got := tickBackoff(iv, max, c.errs); got != c.want {
			t.Errorf("tickBackoff(%d errors) = %v, want %v", c.errs, got, c.want)
		}
	}
	if got := tickBackoff(iv, iv/2, 3); got != iv {
		t.Errorf("a cap below the interval gives %v, want the interval %v", got, iv)
	}
}

// TestRetryTicks pins the effective schedule in ticks: one failure retries on
// the next tick (no extra interval), and later waits are jittered over the
// upper half of the backoff.
func TestRetryTicks(t *testing.T) {
	iv, max := time.Second, 5*time.Second
	lowest := func(int) int { return 0 }
	highest := func(n int) int { return n - 1 }
	for _, c := range []struct{ errs, lo, hi int }{
		{1, 0, 0}, // backoff 1 tick: retry on the next tick
		{2, 0, 1}, // backoff 2 ticks: wait 1 or 2
		{3, 1, 3}, // backoff 4 ticks: wait 2 to 4
		{4, 2, 4}, // capped at 5 ticks: wait 3 to 5
		{50, 2, 4},
	} {
		if got := retryTicks(iv, max, c.errs, lowest); got != c.lo {
			t.Errorf("errs %d: lowest skip %d, want %d", c.errs, got, c.lo)
		}
		if got := retryTicks(iv, max, c.errs, highest); got != c.hi {
			t.Errorf("errs %d: highest skip %d, want %d", c.errs, got, c.hi)
		}
	}
}

// TestTickFailLogCounts: the node summary counts each failing game once and
// returns to zero when they recover or their tickers exit.
func TestTickFailLogCounts(t *testing.T) {
	var l tickFailLog
	err := errors.New("down")
	l.fail(true, err)
	l.fail(false, err) // same game, second failure
	l.fail(true, err)  // another game
	if l.failing != 2 {
		t.Fatalf("failing = %d, want 2", l.failing)
	}
	l.clear(true)
	l.clear(false)
	if l.failing != 0 {
		t.Fatalf("failing = %d after both cleared, want 0", l.failing)
	}
}
