package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// #93: the server, not the client, decides a game's clock.

// Every interim catalogue pool resolves to a valid clock.
func TestDefaultPoolsResolve(t *testing.T) {
	for pool, tc := range DefaultPools() {
		if _, err := (ConventionResolver{}).Resolve(tc); err != nil {
			t.Errorf("pool %q time control %q: %v", pool, tc, err)
		}
	}
}

// Out-of-range, overflowing and no-thinking-time controls are refused.
func TestResolverRejectsOutOfRange(t *testing.T) {
	for _, id := range []string{
		"daily-9223372036854775807",
		"daily-15",
		"custom-9223372036854775807+0",
		"custom-0+9223372036854775807",
		"custom-0+0",
		"custom-10800001+0",
		"custom-1000+180001",
		"blitz-9223372036854775807+0",
		"blitz-5+9223372036854775807",
		"blitz-181+0",
		"blitz-5+181",
		"blitz-0+0",
	} {
		if tc, err := (ConventionResolver{}).Resolve(id); err == nil {
			t.Errorf("Resolve(%q) = %+v, want an error", id, tc)
		}
	}
	// The bounds themselves are allowed.
	for _, id := range []string{"daily-14", "custom-10800000+180000", "classical-180+180", "custom-80+0"} {
		if _, err := (ConventionResolver{}).Resolve(id); err != nil {
			t.Errorf("Resolve(%q): %v", id, err)
		}
	}
}

func (p *fakePool) waitingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiting)
}

// SEEK into an unknown pool, or with a time control other than the pool's, gets
// an ERROR and is never enqueued.
func TestSeekRejectsUnknownPoolAndMismatch(t *testing.T) {
	for _, tc := range []struct{ name, pool, timeControl string }{
		{"unknown pool", "nope", "blitz-5+3"},
		{"mismatched time control", "blitz", "custom-1+0"},
		{"empty time control", "blitz", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			defer h.cancel()
			c := newFakeConn()
			h.handle(context.Background(), c)
			c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "mallory"}})
			c.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: tc.pool, TimeControl: tc.timeControl}})
			e := c.waitForServer(t, codec.SMError, 2*time.Second)
			if e.Error.Code != codec.ECProtocolViolation {
				t.Fatalf("error code = 0x%02x, want PROTOCOL_VIOLATION", e.Error.Code)
			}
			if n := h.pool.waitingCount(); n != 0 {
				t.Fatalf("%d seekers enqueued, want 0", n)
			}
		})
	}
}

// The #93 attack: a waiting player in "blitz" is never paired into a game with
// a clock an attacker chose; the attacker's SEEK is refused and the victim keeps
// waiting, then pairs normally on the pool's own clock.
func TestSeekCannotImposeAClock(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	victim, attacker, honest := newFakeConn(), newFakeConn(), newFakeConn()
	h.handle(ctx, victim)
	h.handle(ctx, attacker)
	h.handle(ctx, honest)
	authAndSeek(t, victim, "victim", "blitz")
	time.Sleep(20 * time.Millisecond)
	attacker.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "attacker"}})
	attacker.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: "blitz", TimeControl: "custom-1+0"}})
	attacker.waitForServer(t, codec.SMError, 2*time.Second)

	authAndSeek(t, honest, "honest", "blitz")
	m := victim.waitForServer(t, codec.SMMatched, 2*time.Second)
	honest.waitForServer(t, codec.SMMatched, 2*time.Second)
	evs, err := h.store.Load(ctx, game.StreamIDFor(m.Matched.GameID))
	if err != nil || len(evs) == 0 || evs[0].GameCreated == nil {
		t.Fatalf("read opening: %v (%d events)", err, len(evs))
	}
	if got := evs[0].GameCreated.TimeControl; got != "blitz-5+3" {
		t.Fatalf("game time control = %q, want the pool's blitz-5+3", got)
	}
}

// A catalogue entry the resolver rejects is a server misconfiguration: the
// client sent the pool's own control, so it gets INTERNAL (not
// PROTOCOL_VIOLATION) and nothing is enqueued.
func TestSeekIntoMisconfiguredPoolIsInternal(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.pools = PoolCatalog{"broken": "custom-0+0"}
	c := newFakeConn()
	h.handle(context.Background(), c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
	c.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: "broken", TimeControl: "custom-0+0"}})
	e := c.waitForServer(t, codec.SMError, 2*time.Second)
	if e.Error.Code != codec.ECInternal {
		t.Fatalf("error code = 0x%02x, want INTERNAL", e.Error.Code)
	}
	if n := h.pool.waitingCount(); n != 0 {
		t.Fatalf("%d seekers enqueued, want 0", n)
	}
}
