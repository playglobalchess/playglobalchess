package transport

import (
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
)

type panicResolver struct{}

func (panicResolver) Resolve(string) (clock.TimeControl, error) { panic("resolver boom") }

// A build that panics fails its waiters and leaves the game buildable again,
// instead of wedging every later caller on a done channel nobody closes.
func TestPanickingBuildDoesNotWedgeTheGame(t *testing.T) {
	r, _, gameA, _, cleanup := twoGames(t)
	defer cleanup()
	good := r.resolver
	r.resolver = panicResolver{}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.GetOrCreate(gameA, "blitz-5+3")
			errs <- err
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a caller is stuck after a panicking build")
	}
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("a panicking build reported success")
		}
	}
	r.resolver = good
	close(r.store.(*gateStore).gate)
	if _, err := r.GetOrCreate(gameA, "blitz-5+3"); err != nil {
		t.Fatalf("the game stayed wedged after the panic: %v", err)
	}
}
