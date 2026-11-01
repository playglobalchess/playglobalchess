package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/matchmaking"
)

// --- fake Conn: a socket-free connection for dispatch tests -----------------

// fakeConn feeds inbound frames from a channel and records outbound frames. It
// lets the whole message flow be driven without a real WebSocket (Q-GS6a: the
// Conn interface exists for exactly this).
type fakeConn struct {
	in     chan []byte
	mu     sync.Mutex
	out    [][]byte
	seen   int // how many out frames have been consumed by waitForServer
	closed bool
	addr   string
}

func newFakeConn() *fakeConn { return &fakeConn{in: make(chan []byte, 16)} }

func (f *fakeConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case b, ok := <-f.in:
		if !ok {
			return nil, context.Canceled
		}
		return b, nil
	}
}

func (f *fakeConn) Write(_ context.Context, frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(frame))
	copy(cp, frame)
	f.out = append(f.out, cp)
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.in)
	}
	return nil
}

func (f *fakeConn) RemoteAddr() string { return f.addr }

// feed queues a decoded client message as an encoded frame onto the conn's input.
func (f *fakeConn) feedClient(t *testing.T, m codec.ClientMsg) {
	t.Helper()
	frame, err := codec.EncodeClient(m)
	if err != nil {
		t.Fatalf("encode client: %v", err)
	}
	f.in <- frame
}

// sent returns a copy of all server messages written to this conn, decoded.
func (f *fakeConn) sent(t *testing.T) []codec.ServerMsg {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := make([]codec.ServerMsg, 0, len(f.out))
	for _, frame := range f.out {
		m, err := codec.DecodeServer(frame)
		if err != nil {
			t.Fatalf("decode server frame: %v", err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func (f *fakeConn) waitForServer(t *testing.T, want codec.ServerMsgType, timeout time.Duration) codec.ServerMsg {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Consume NEW frames (past the seen offset) and return the first match.
		// Tracking the offset is essential: a move hot path sends many
		// MoveAccepted frames, and a test must wait for the NEXT one, not re-match
		// an earlier already-consumed accept (that would let a mover outrun the
		// opponent's reply and desync the game).
		f.mu.Lock()
		for f.seen < len(f.out) {
			frame := f.out[f.seen]
			f.seen++
			f.mu.Unlock()
			m, err := codec.DecodeServer(frame)
			if err != nil {
				t.Fatalf("decode server frame: %v", err)
			}
			if m.Type == want {
				return m
			}
			f.mu.Lock()
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for server msg type %d", want)
	return codec.ServerMsg{}
}

// --- a minimal in-memory SeekPool: pairs the first two compatible seekers ---
//
// Deterministic and tiny — it is NOT the GS-5 pairing core (that is unit-tested
// in the matchmaking package); it just lets the transport pair two seekers so
// the end-to-end MATCHED/MOVE/ending flow can be exercised. TryClaim returns the
// single other waiting seeker in the same pool.
type fakePool struct {
	mu      sync.Mutex
	waiting map[string]matchmaking.Seeker
	claimed map[string]bool
}

func newFakePool() *fakePool {
	return &fakePool{waiting: map[string]matchmaking.Seeker{}, claimed: map[string]bool{}}
}

func (p *fakePool) Enqueue(_ context.Context, s matchmaking.Seeker) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting[s.SeekID] = s
	return nil
}

func (p *fakePool) TryClaim(_ context.Context, newSeeker matchmaking.Seeker, _ matchmaking.Window, _ time.Time) (matchmaking.Seeker, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.waiting {
		if id == newSeeker.SeekID || p.claimed[id] {
			continue
		}
		if s.PoolID != newSeeker.PoolID {
			continue
		}
		p.claimed[id] = true
		p.claimed[newSeeker.SeekID] = true
		return s, true, nil
	}
	return matchmaking.Seeker{}, false, nil
}

func (p *fakePool) Release(_ context.Context, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.claimed, id)
	}
	return nil
}

func (p *fakePool) Remove(_ context.Context, _ string, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.waiting, id)
		delete(p.claimed, id)
	}
	return nil
}

func (p *fakePool) Cancel(_ context.Context, _, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.waiting, id)
	delete(p.claimed, id)
	return nil
}

// --- test harness -----------------------------------------------------------

type harness struct {
	srv    *Server
	store  game.EventStore
	cancel context.CancelFunc
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithStore(t, game.NewMemStore())
}

// newHarnessWithStore is newHarness over a caller-supplied store (fault
// injection tests wrap a MemStore).
func newHarnessWithStore(t *testing.T, store game.EventStore) *harness {
	t.Helper()
	o, err := engine.New()
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	mm := matchmaking.New(newFakePool(), store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reg := NewRegistry(ctx, store, o, ConventionResolver{})
	srv := NewServer(DevAuthenticator{}, mm, reg)
	return &harness{srv: srv, store: store, cancel: cancel}
}

func (h *harness) handle(ctx context.Context, c *fakeConn) {
	go h.srv.Handle(ctx, c)
}

// authAndSeek authenticates a conn as userID and sends a SEEK into pool.
func authAndSeek(t *testing.T, c *fakeConn, userID, pool string) {
	t.Helper()
	c.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: userID}})
	c.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: pool, TimeControl: "blitz-5+3", Rated: false}})
}

// TestSeekMatchedBothSeats: two seekers in the same pool pair, and BOTH connections
// receive MATCHED with their own seat.
func TestSeekMatchedBothSeats(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()

	a := newFakeConn()
	b := newFakeConn()
	h.handle(ctx, a)
	h.handle(ctx, b)

	authAndSeek(t, a, "alice", "blitz")
	// Give A time to enqueue before B seeks, so B is the one that claims A.
	time.Sleep(20 * time.Millisecond)
	authAndSeek(t, b, "bob", "blitz")

	ma := a.waitForServer(t, codec.SMMatched, 2*time.Second)
	mb := b.waitForServer(t, codec.SMMatched, 2*time.Second)
	if ma.Matched.GameID != mb.Matched.GameID {
		t.Fatalf("different game ids: %s vs %s", ma.Matched.GameID, mb.Matched.GameID)
	}
	if ma.Matched.SeatIdx == mb.Matched.SeatIdx {
		t.Fatalf("both got the same seat %d", ma.Matched.SeatIdx)
	}
	// Opponent names are set and crossed.
	if ma.Matched.Opponent != "bob" || mb.Matched.Opponent != "alice" {
		t.Fatalf("opponents wrong: a sees %q, b sees %q", ma.Matched.Opponent, mb.Matched.Opponent)
	}
}
