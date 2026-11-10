package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
)

// Tests for the per-connection write pump (pump.go, #91).

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeConn) outLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.out)
}

// waitUntil polls cond until it holds or the timeout passes.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Frames reach the socket in the order they were written.
func TestPump_WritesInOrder(t *testing.T) {
	f := newFakeConn()
	p := newPumpedConn(f, 64, time.Second)
	defer p.Close()
	for i := 0; i < 20; i++ {
		if err := p.Write(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	waitUntil(t, time.Second, "20 frames", func() bool { return f.outLen() == 20 })
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.out {
		if b[0] != byte(i) {
			t.Fatalf("frame %d = %d, want %d", i, b[0], i)
		}
	}
}

// A peer that stops reading fills its queue: the next Write returns at once
// with errSlowConsumer and the connection is closed. Write never blocks.
func TestPump_QueueOverflowClosesWithoutBlocking(t *testing.T) {
	f := newFakeConn()
	f.stall.Store(true)
	p := newPumpedConn(f, 2, time.Hour) // the deadline is not what closes it here
	start := time.Now()
	var err error
	for i := 0; i < 10 && err == nil; i++ {
		err = p.Write(context.Background(), []byte{byte(i)})
	}
	if !errors.Is(err, errSlowConsumer) {
		t.Fatalf("err = %v, want errSlowConsumer", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("writes blocked for %v", d)
	}
	waitUntil(t, time.Second, "close on overflow", f.isClosed)
	if err := p.Write(context.Background(), []byte{0}); !errors.Is(err, errConnClosed) {
		t.Fatalf("write after close = %v, want errConnClosed", err)
	}
}

// One write that passes the deadline closes the connection.
func TestPump_WriteDeadlineCloses(t *testing.T) {
	f := newFakeConn()
	f.stall.Store(true)
	p := newPumpedConn(f, 8, 50*time.Millisecond)
	if err := p.Write(context.Background(), []byte{1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitUntil(t, time.Second, "close after the write deadline", f.isClosed)
	if err := p.Write(context.Background(), []byte{2}); !errors.Is(err, errConnClosed) {
		t.Fatalf("write after close = %v, want errConnClosed", err)
	}
}

// CloseAfterFlush writes what is already queued, then closes; later writes are
// refused.
func TestPump_CloseAfterFlushDeliversQueued(t *testing.T) {
	f := newFakeConn()
	p := newPumpedConn(f, 8, time.Second)
	for i := 0; i < 3; i++ {
		_ = p.Write(context.Background(), []byte{byte(i)})
	}
	p.CloseAfterFlush()
	if err := p.Write(context.Background(), []byte{9}); !errors.Is(err, errConnClosed) {
		t.Fatalf("write after CloseAfterFlush = %v, want errConnClosed", err)
	}
	waitUntil(t, time.Second, "close after flush", f.isClosed)
	if n := f.outLen(); n != 3 {
		t.Fatalf("flushed %d frames, want 3", n)
	}
}

// slowConn writes each frame after a fixed delay (a slow but live peer),
// honouring the write's ctx.
type slowConn struct {
	*fakeConn
	delay time.Duration
}

func (s *slowConn) Write(ctx context.Context, frame []byte) error {
	select {
	case <-time.After(s.delay):
		return s.fakeConn.Write(ctx, frame)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The flush after CloseAfterFlush shares ONE deadline: a slow peer whose every
// frame fits the per-frame deadline is still cut off once the flush deadline
// passes, instead of being given a deadline per queued frame.
func TestPump_CloseAfterFlushSharesOneDeadline(t *testing.T) {
	f := &slowConn{fakeConn: newFakeConn(), delay: 30 * time.Millisecond}
	p := newPumpedConn(f, 16, 50*time.Millisecond) // each frame fits; 16 do not
	for i := 0; i < 16; i++ {
		_ = p.Write(context.Background(), []byte{byte(i)})
	}
	p.CloseAfterFlush()
	start := time.Now()
	waitUntil(t, 2*time.Second, "close after the flush deadline", f.isClosed)
	// Per-frame deadlines would write all 16 (~480 ms); one shared deadline
	// stops it after the in-flight frame plus about one more.
	if n := f.outLen(); n >= 16 {
		t.Fatalf("flushed all %d frames in %v: the flush is not bounded by one deadline", n, time.Since(start))
	}
}

// blockingCloseConn's Close blocks, like coder/websocket's closing handshake
// with a peer that does not answer.
type blockingCloseConn struct {
	*fakeConn
	release chan struct{}
}

func (b *blockingCloseConn) Close() error {
	<-b.release
	return b.fakeConn.Close()
}

// The overflow path runs on the WRITER's caller (the opponent's handler or the
// flag ticker), so it must not wait for the socket close.
func TestPump_OverflowDoesNotWaitForSocketClose(t *testing.T) {
	f := &blockingCloseConn{fakeConn: newFakeConn(), release: make(chan struct{})}
	defer close(f.release)
	f.stall.Store(true)
	p := newPumpedConn(f, 2, time.Hour)
	done := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < 10 && err == nil; i++ {
			err = p.Write(context.Background(), []byte{byte(i)})
		}
		// A second Close (e.g. Handle teardown) must not wait either.
		_ = p.Close()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errSlowConsumer) {
			t.Fatalf("err = %v, want errSlowConsumer", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write blocked on the socket close")
	}
}

// Handle's final frame (ERROR before an auth-failure close) still reaches the
// client: the deferred close flushes the queue first.
func TestHandle_AuthFailureErrorIsDelivered(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	c := newFakeConn()
	h.handle(context.Background(), c)
	c.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: "g"}) // not AUTHENTICATE
	c.waitForServer(t, codec.SMError, 2*time.Second)
	waitUntil(t, time.Second, "close after the auth failure", c.isClosed)
}

// #91: a seat whose peer has stopped reading must not stall the other seat. The
// MOVE_ACCEPTED fan-out to the stalled seat used to block the mover's handler,
// so its RESIGN was never processed. Now the mover is served, and the stalled
// seat is disconnected after the write deadline.
func TestStalledPeerDoesNotBlockOpponent(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.writeDeadline = 100 * time.Millisecond
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)

	black.stall.Store(true)
	move(t, white, gameID, 1, "e2e4")
	white.waitForServer(t, codec.SMMoveAccepted, time.Second)
	white.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ge := white.waitForServer(t, codec.SMGameEnd, time.Second)
	if ge.GameEnd.Termination != 0x02 { // RESIGN
		t.Fatalf("termination = 0x%02x, want RESIGN(0x02)", ge.GameEnd.Termination)
	}
	waitUntil(t, 2*time.Second, "the stalled seat to be disconnected", black.isClosed)
}

// #91: the flag ticker fans GAME_END out to both seats. With one seat stalled it
// must still end the game for the other and drop the session (it used to block
// on the stalled write, so dropGame never ran).
func TestFlagWithStalledPeerStillEndsGame(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	h.srv.flagInterval = 20 * time.Millisecond
	h.srv.writeDeadline = 100 * time.Millisecond
	h.useRealClock()
	ctx := context.Background()
	ca := newFakeConn()
	cb := newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	seekTC(t, ca, "alice", "tiny")
	time.Sleep(20 * time.Millisecond)
	seekTC(t, cb, "bob", "tiny")
	ma := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	cb.stall.Store(true)

	ge := ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	if ge.GameEnd.GameID != ma.Matched.GameID {
		t.Fatalf("GAME_END for %q, want %q", ge.GameEnd.GameID, ma.Matched.GameID)
	}
	waitUntil(t, 2*time.Second, "the session to be dropped", func() bool {
		_, ok := h.srv.reg.Get(ma.Matched.GameID)
		return !ok
	})
}
