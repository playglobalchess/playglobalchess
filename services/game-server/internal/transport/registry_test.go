package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// gateStore wraps a store so Load of one game's stream blocks until the gate
// opens or the caller's context ends (#174). It counts in-flight loads.
type gateStore struct {
	game.EventStore
	stream   string
	gate     chan struct{}
	entered  chan struct{}
	inflight atomic.Int32
	maxIn    atomic.Int32
}

func newGateStore(inner game.EventStore, gameID string) *gateStore {
	return &gateStore{
		EventStore: inner,
		stream:     game.StreamIDFor(gameID),
		gate:       make(chan struct{}),
		entered:    make(chan struct{}, 64),
	}
}

func (g *gateStore) Load(ctx context.Context, id string) ([]game.Event, error) {
	if id == g.stream {
		n := g.inflight.Add(1)
		for {
			m := g.maxIn.Load()
			if n <= m || g.maxIn.CompareAndSwap(m, n) {
				break
			}
		}
		defer g.inflight.Add(-1)
		select {
		case g.entered <- struct{}{}:
		default:
		}
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.EventStore.Load(ctx, id)
}

// twoGames starts two live games on a harness and returns a second registry over
// the same durable store, gated on the first game's stream.
func twoGames(t *testing.T) (r *Registry, gate *gateStore, gameA, gameB string, cleanup func()) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	_, _, gameA, _, _ = pair(t, h, ctx, "alice", "bob")
	_, _, gameB, _, _ = pair(t, h, ctx, "carol", "dave")
	gate = newGateStore(h.store, gameA)
	rctx, cancel := context.WithCancel(context.Background())
	r = NewRegistry(rctx, gate, h.srv.reg.oracle, ConventionResolver{})
	r.now = h.clk.now
	return r, gate, gameA, gameB, func() { cancel(); h.cancel() }
}

func waitEntered(t *testing.T, g *gateStore) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the gated store load never started")
	}
}

func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked behind a session build (registry lock held across the store read)", what)
	}
}

// #174: a slow build of one game never blocks Get, Drop or another game's build.
func TestSessionBuildDoesNotHoldTheRegistryLock(t *testing.T) {
	r, gate, gameA, gameB, cleanup := twoGames(t)
	defer cleanup()
	res := make(chan error, 1)
	go func() { _, err := r.GetOrCreate(gameA, "blitz-5+3"); res <- err }()
	waitEntered(t, gate)

	within(t, "Get of another game", func() { r.Get(gameB) })
	within(t, "Drop of another game", func() { r.Drop("00000000-0000-4000-8000-000000000000") })
	within(t, "the build of another game", func() {
		if _, err := r.GetOrCreate(gameB, "blitz-5+3"); err != nil {
			t.Errorf("GetOrCreate(B): %v", err)
		}
	})
	if _, ok := r.Get(gameA); ok {
		t.Fatal("game A is visible before its build finished")
	}
	close(gate.gate)
	if err := <-res; err != nil {
		t.Fatalf("GetOrCreate(A): %v", err)
	}
	if _, ok := r.Get(gameA); !ok {
		t.Fatal("game A not registered after its build")
	}
}

// Concurrent callers for one game share ONE build and one Session.
func TestConcurrentGetOrCreateSharesOneBuild(t *testing.T) {
	r, gate, gameA, _, cleanup := twoGames(t)
	defer cleanup()
	const n = 8
	got := make([]interface{}, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := r.GetOrCreate(gameA, "blitz-5+3")
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
			got[i] = s
		}(i)
	}
	waitEntered(t, gate)
	time.Sleep(50 * time.Millisecond) // let the other callers queue on the build
	close(gate.gate)
	wg.Wait()
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("caller %d got a different Session", i)
		}
	}
	if m := gate.maxIn.Load(); m != 1 {
		t.Fatalf("%d builds of one game ran at once, want 1", m)
	}
}

// The build is bounded: a store that never answers fails the build, and the next
// call builds afresh.
func TestSessionBuildIsBounded(t *testing.T) {
	r, gate, gameA, _, cleanup := twoGames(t)
	defer cleanup()
	r.buildTimeout = 50 * time.Millisecond
	res := make(chan error, 1)
	go func() { _, err := r.GetOrCreate(gameA, "blitz-5+3"); res <- err }()
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("a build against a hung store succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the build was not bounded by its deadline")
	}
	close(gate.gate)
	if _, err := r.GetOrCreate(gameA, "blitz-5+3"); err != nil {
		t.Fatalf("retry after a failed build: %v", err)
	}
}

// A game dropped, or a registry closed, while its session is being built does
// not come back as a live session.
func TestDropAndCloseAllDiscardAnInFlightBuild(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(r *Registry, gameID string)
	}{
		{"drop", func(r *Registry, id string) { r.Drop(id) }},
		{"closeall", func(r *Registry, _ string) { r.CloseAll() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, gate, gameA, _, cleanup := twoGames(t)
			defer cleanup()
			res := make(chan error, 1)
			go func() { _, err := r.GetOrCreate(gameA, "blitz-5+3"); res <- err }()
			waitEntered(t, gate)
			tc.stop(r, gameA)
			close(gate.gate)
			if err := <-res; !errors.Is(err, errBuildDiscarded) {
				t.Fatalf("GetOrCreate = %v, want errBuildDiscarded", err)
			}
			if _, ok := r.Get(gameA); ok {
				t.Fatal("a discarded build left a live session")
			}
		})
	}
}
