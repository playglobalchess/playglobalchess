package transport

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
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
// committing, until restore is called. It trips only once. With writesOnly,
// reads keep working, so a failed check returns at once (the session's
// re-read sees the write did not land) and the check rate is set by the
// ticker's backoff alone.
type outageStore struct {
	game.EventStore
	writesOnly bool

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
	if !o.writesOnly && o.refuse(nil) {
		return nil, errOutage
	}
	return o.EventStore.Load(ctx, stream)
}

// failingGames is the node's count of games whose flag checks are failing.
func failingGames(h *harness) int {
	h.srv.tickLog.mu.Lock()
	defer h.srv.tickLog.mu.Unlock()
	return h.srv.tickLog.failing
}

// pairTiny pairs alice and bob on the 80 ms "tiny" pool and returns their
// connections and the game id.
func pairTiny(t *testing.T, h *harness) (*fakeConn, *fakeConn, string) {
	t.Helper()
	ctx := context.Background()
	ca, cb := newFakeConn(), newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	m := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	return ca, cb, m.Matched.GameID
}

// TestFlagBackoffLimitsRetries: the backoff is really applied in the loop.
// Reads work, so each failed check returns at once; without backoff the
// ticker would retry on every 10 ms tick (about 100 checks in a second). With
// a 200 ms cap it makes far fewer.
func TestFlagBackoffLimitsRetries(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 10 * time.Millisecond
	h.srv.flagBackoffMax = 200 * time.Millisecond
	h.useRealClock()
	out := &outageStore{EventStore: h.store, writesOnly: true}
	h.srv.reg.store = out
	w := watchTickers(h)
	ca, cb, gameID := pairTiny(t, h)

	waitUntil(t, 5*time.Second, "the first refused flag", func() bool { return out.refusedFlags() >= 1 })
	start := out.refusedFlags()
	time.Sleep(time.Second)
	if n := out.refusedFlags() - start; n > 30 {
		t.Fatalf("%d flag checks in 1 s of outage at a 10 ms tick and 200 ms cap: the backoff is not applied", n)
	}
	out.restore()
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	wantReclaimed(t, h, w, gameID)
	if n := failingGames(h); n != 0 {
		t.Fatalf("node still counts %d failing game(s) after recovery", n)
	}
}

// TestFlagTickerExitsWhenDroppedDuringOutage: a game dropped by another path
// while its flag checks are failing stops its ticker within a tick (the gone
// check runs before the backoff skip), and the node stops counting it.
func TestFlagTickerExitsWhenDroppedDuringOutage(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 10 * time.Millisecond
	h.srv.flagBackoffMax = 2 * time.Second // a long backoff the drop must not wait out
	h.useRealClock()
	out := &outageStore{EventStore: h.store, writesOnly: true}
	h.srv.reg.store = out
	w := watchTickers(h)
	_, _, gameID := pairTiny(t, h)

	// After 8 failures the next skip is at least 64 ticks (640 ms), far longer
	// than the 200 ms the drop is allowed to take below.
	waitUntil(t, 5*time.Second, "the ticker to back off", func() bool { return out.refusedFlags() >= 8 })
	h.srv.dropGame(gameID)
	waitUntil(t, 200*time.Millisecond, "the flag ticker to exit after the drop", func() bool { return w.gone(gameID) })
	if n := failingGames(h); n != 0 {
		t.Fatalf("node still counts %d failing game(s) after the ticker exited", n)
	}
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
	if n := failingGames(h); n != 0 {
		t.Fatalf("node still counts %d failing game(s) after shutdown", n)
	}
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

// TestTickFailLogRateLimitsAFlappingStore: one game failing and recovering
// over and over logs one "failing" line and one closing line per
// tickFailLogEvery, not two lines per cycle.
func TestTickFailLogRateLimitsAFlappingStore(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(flags) }()

	var l tickFailLog
	err := errors.New("down")
	for i := 0; i < 50; i++ { // all well inside one tickFailLogEvery window
		l.fail(true, err)
		l.clear(true)
	}
	if got := strings.Count(buf.String(), "\n"); got != 2 {
		t.Fatalf("%d log lines for 50 fail/recover cycles in one window, want 2:\n%s", got, buf.String())
	}
	// A game whose ticker exits while failing also closes the summary.
	buf.Reset()
	l.last = time.Time{} // the next window
	l.fail(true, err)
	l.clear(false)
	if out := buf.String(); !strings.Contains(out, "failing for 1 game(s)") || !strings.Contains(out, "no flag checks failing") {
		t.Fatalf("exit while failing: log = %q, want a failing line and a closing line", out)
	}
}
