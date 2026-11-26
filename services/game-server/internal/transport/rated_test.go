package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
)

func (p *fakePool) ratedWaiting() (rated, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.waiting {
		total++
		if s.Rated {
			rated++
		}
	}
	return rated, total
}

func seekMsg(pool, tc string, rated bool) codec.ClientMsg {
	return codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: pool, TimeControl: tc, Rated: rated}}
}

// #179: with the switch off (the default) a rated SEEK is refused with
// PROTOCOL_VIOLATION and nothing is enqueued. The connection stays usable: a
// following unrated SEEK is enqueued.
func TestRatedSeekRefusedByDefault(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	c := newFakeConn()
	h.handle(context.Background(), c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "mallory"}})
	c.feedClient(t, seekMsg("blitz", "blitz-5+3", true))
	e := c.waitForServer(t, codec.SMError, 2*time.Second)
	if e.Error.Code != codec.ECProtocolViolation {
		t.Fatalf("error code = 0x%02x, want PROTOCOL_VIOLATION", e.Error.Code)
	}
	if rated, total := h.pool.ratedWaiting(); total != 0 {
		t.Fatalf("%d seekers enqueued (%d rated), want 0", total, rated)
	}
	c.feedClient(t, seekMsg("blitz", "blitz-5+3", false))
	waitUntil(t, 2*time.Second, "unrated seek enqueued", func() bool { _, total := h.pool.ratedWaiting(); return total == 1 })
	if rated, _ := h.pool.ratedWaiting(); rated != 0 {
		t.Fatalf("the unrated SEEK was stored as rated")
	}
}

// Two clients that both ask for rated are refused and never paired into a game.
func TestRatedSeekersNeverPairWhenDisabled(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	a, b := newFakeConn(), newFakeConn()
	h.handle(context.Background(), a)
	h.handle(context.Background(), b)
	for i, c := range []*fakeConn{a, b} {
		tok := []string{"alice", "bob"}[i]
		c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: tok}})
		c.feedClient(t, seekMsg("blitz", "blitz-5+3", true))
		c.waitForServer(t, codec.SMError, 2*time.Second)
	}
	if _, total := h.pool.ratedWaiting(); total != 0 {
		t.Fatalf("%d seekers enqueued, want 0", total)
	}
}

// With the switch on, a rated SEEK is enqueued as rated (the future path; the
// switch exists so the policy lives on the server).
func TestRatedSeekAllowedWhenEnabled(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.SetRatedEnabled(true)
	c := newFakeConn()
	h.handle(context.Background(), c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
	c.feedClient(t, seekMsg("blitz", "blitz-5+3", true))
	waitUntil(t, 2*time.Second, "rated seek enqueued", func() bool { r, _ := h.pool.ratedWaiting(); return r == 1 })
}
