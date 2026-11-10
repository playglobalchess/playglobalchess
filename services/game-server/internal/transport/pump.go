package transport

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// The per-connection write pump (GS6 §3.1, §5; #91).
//
// Every connection Handle serves is wrapped in a pumpedConn. Handlers never
// write to a socket: Write only ENQUEUES the frame on a bounded queue and
// returns at once, and ONE writer goroutine per connection drains the queue in
// order, each write under a deadline. So a peer that stops reading (a
// backgrounded tab, a hostile client) can never block another seat's handler or
// the flag ticker that fans a frame out to it.
//
// A slow consumer is disconnected, not waited on: if its queue is full, or one
// write exceeds the deadline, the connection is closed. The game keeps running
// under the clock, and the player can reconnect (AUTHENTICATE pushes STATE).

const (
	// defaultOutboundQueue bounds the frames waiting for one connection. A game
	// sends a few frames per move, so 64 is many moves of headroom for a
	// briefly slow client while still bounding memory per connection.
	defaultOutboundQueue = 64
	// defaultWriteDeadline bounds one frame's write.
	defaultWriteDeadline = 5 * time.Second
)

var (
	errConnClosed   = errors.New("transport: connection closed")
	errSlowConsumer = errors.New("transport: outbound queue full (slow consumer), connection closed")
)

type pumpedConn struct {
	Conn // the underlying connection: Read, RemoteAddr pass through

	queue     chan []byte
	done      chan struct{} // closed when the connection is closed
	drain     chan struct{} // closed by CloseAfterFlush
	once      sync.Once
	drainOnce sync.Once
	deadline  time.Duration
}

// newPumpedConn wraps c and starts its writer goroutine. The writer stops when
// the connection is closed (by Handle on return, or by the pump itself).
func newPumpedConn(c Conn, queueSize int, deadline time.Duration) *pumpedConn {
	p := &pumpedConn{
		Conn:     c,
		queue:    make(chan []byte, queueSize),
		done:     make(chan struct{}),
		drain:    make(chan struct{}),
		deadline: deadline,
	}
	go p.writer()
	return p
}

// Write enqueues one frame without blocking. The caller's ctx is not used for
// the socket write (that runs on the pump's own deadline), so a handler's or
// the ticker's context can never stall on a peer.
func (p *pumpedConn) Write(_ context.Context, frame []byte) error {
	select {
	case <-p.done:
		return errConnClosed
	case <-p.drain:
		return errConnClosed
	default:
	}
	select {
	case p.queue <- frame:
		return nil
	case <-p.done:
		return errConnClosed
	default:
		log.Printf("transport: closing %s: %v", p.RemoteAddr(), errSlowConsumer)
		_ = p.Close()
		return errSlowConsumer
	}
}

// Close marks the connection closed at once (later Writes are refused, the
// writer stops) and closes the underlying socket on its own goroutine. The
// socket close can block for seconds (coder/websocket's closing handshake waits
// for the write lock and the peer's close frame), and Close is reached from
// Write's overflow path, i.e. on the OPPONENT's handler or the flag ticker, so
// it must not wait for it. Idempotent; it never blocks.
func (p *pumpedConn) Close() error {
	p.once.Do(func() {
		close(p.done)
		go func() { _ = p.Conn.Close() }()
	})
	return nil
}

// CloseAfterFlush closes the connection once the frames already queued are
// written. Handle uses it on return, so a final frame (the ERROR before an
// auth-failure close) still reaches the client. Frames written after it are
// refused. Bounded: the frame in flight when it is called keeps its own
// deadline, and the rest of the flush shares ONE deadline, so teardown of a
// stalled peer takes at most two deadlines. Idempotent; it does not block.
func (p *pumpedConn) CloseAfterFlush() {
	p.drainOnce.Do(func() { close(p.drain) })
}

func (p *pumpedConn) writer() {
	for {
		// Check drain FIRST: once CloseAfterFlush is called, no further frame may
		// be taken on the per-frame path (a select over both ready channels picks
		// at random, which would leave the flush unbounded).
		select {
		case <-p.done:
			return
		case <-p.drain:
			p.flush()
			return
		default:
		}
		select {
		case <-p.done:
			return
		case <-p.drain:
			p.flush()
			return
		case frame := <-p.queue:
			ctx, cancel := context.WithTimeout(context.Background(), p.deadline)
			ok := p.write(ctx, frame)
			cancel()
			if !ok {
				return
			}
		}
	}
}

// flush writes the frames still queued under ONE shared deadline, then closes.
func (p *pumpedConn) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), p.deadline)
	defer cancel()
	for {
		select {
		case frame := <-p.queue:
			if !p.write(ctx, frame) {
				return
			}
		default:
			_ = p.Close()
			return
		}
	}
}

// write sends one frame under ctx's deadline. On failure (the peer is gone, or
// not reading) it closes the connection rather than keep frames for it, and
// reports false.
func (p *pumpedConn) write(ctx context.Context, frame []byte) bool {
	err := p.Conn.Write(ctx, frame)
	if err == nil {
		return true
	}
	select {
	case <-p.done:
	default:
		log.Printf("transport: closing %s after a failed write: %v", p.RemoteAddr(), err)
	}
	_ = p.Close()
	return false
}
