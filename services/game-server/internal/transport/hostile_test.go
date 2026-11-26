package transport

import (
	"context"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Hostile-client tests and the dispatch fuzz target (#204).
//
// Every case runs against a live game between two seats. The attacker is the
// BLACK seat (or an outsider); the victim is WHITE. The invariant is the same
// for each case: after the hostile input the server still serves the victim
// (white's move is accepted and both seats hear it), the durable stream holds
// exactly what the honest play produced, every frame the victim received
// decodes, and no goroutine is left behind.

// feedRaw queues raw bytes as one client frame. It never panics if the
// connection was closed under it.
func (f *fakeConn) feedRaw(b []byte) {
	select {
	case f.in <- b:
	case <-f.gone:
	}
}

// feedRawAsync queues frames without blocking the test on the 16-slot input.
func (f *fakeConn) feedRawAsync(frames ...[]byte) {
	go func() {
		for _, b := range frames {
			f.feedRaw(b)
		}
	}()
}

func mustEncode(t *testing.T, m codec.ClientMsg) []byte {
	t.Helper()
	b, err := codec.EncodeClient(m)
	if err != nil {
		t.Fatalf("encode client: %v", err)
	}
	return b
}

type hostileEnv struct {
	t        *testing.T
	h        *harness
	ctx      context.Context
	white    *fakeConn // the victim: seat 0
	black    *fakeConn // the attacker: seat 1
	gameID   string
	extra    []*fakeConn
	baseline int
}

// newHostileEnv pairs two users into a live game. It polls the pool instead of
// sleeping so the fuzz target stays fast. tune runs before any connection is
// handled (to shrink the write queue and deadline).
func newHostileEnv(t *testing.T, tune func(*Server)) *hostileEnv {
	t.Helper()
	base := runtime.NumGoroutine()
	h := newHarness(t)
	if tune != nil {
		tune(h.srv)
	}
	ctx := context.Background()
	ca, cb := newFakeConn(), newFakeConn()
	h.handle(ctx, ca)
	h.handle(ctx, cb)
	authAndSeek(t, ca, "alice", "blitz")
	deadline := time.Now().Add(2 * time.Second)
	for h.pool.waitingCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	authAndSeek(t, cb, "bob", "blitz")
	ma := ca.waitForServer(t, codec.SMMatched, 2*time.Second)
	mb := cb.waitForServer(t, codec.SMMatched, 2*time.Second)
	e := &hostileEnv{t: t, h: h, ctx: ctx, gameID: ma.Matched.GameID, baseline: base}
	e.white, e.black = whiteBlack(ca, cb, ma.Matched.SeatIdx, mb.Matched.SeatIdx)
	return e
}

// outsider connects and authenticates a user who is not in the game.
func (e *hostileEnv) outsider(user string) *fakeConn {
	c := newFakeConn()
	e.h.handle(e.ctx, c)
	c.feedClient(e.t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: user}})
	e.extra = append(e.extra, c)
	return c
}

func (e *hostileEnv) moveFrame(ply uint32, uci string) []byte {
	return mustEncode(e.t, codec.ClientMsg{Type: codec.CMMove, Move: &codec.MovePayload{GameID: e.gameID, PlyExpected: ply, UCI: uci}})
}

func (e *hostileEnv) gameFrame(ty codec.ClientMsgType) []byte {
	return mustEncode(e.t, codec.ClientMsg{Type: ty, GameID: e.gameID})
}

// stream returns the game's durable events.
func (e *hostileEnv) stream() []game.Event {
	evs, err := e.h.store.Load(e.ctx, game.StreamIDFor(e.gameID))
	if err != nil {
		e.t.Fatalf("load stream: %v", err)
	}
	return evs
}

// moves returns the UCIs of the stream's MovePlayed events in order.
func moves(evs []game.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type == game.EvMovePlayed {
			out = append(out, ev.MovePlayed.UCI)
		}
	}
	return out
}

// victimStillServed is the oracle for every non-ending case: white's e2e4 is
// accepted, both seats hear it, the stream holds exactly that move and no
// ending, and everything white received decodes.
func (e *hostileEnv) victimStillServed() {
	e.t.Helper()
	move(e.t, e.white, e.gameID, 1, "e2e4")
	e.white.waitForServer(e.t, codec.SMMoveAccepted, 3*time.Second)
	for _, ev := range e.stream() {
		switch ev.Type {
		case game.EvGameCreated, game.EvPlayerJoined, game.EvClockStarted, game.EvMovePlayed, game.EvDrawOffered:
		default:
			e.t.Fatalf("hostile input produced a %#x event in the stream", uint8(ev.Type))
		}
	}
	if got := moves(e.stream()); len(got) != 1 || got[0] != "e2e4" {
		e.t.Fatalf("stream moves = %v, want [e2e4]", got)
	}
	_ = e.white.sent(e.t) // fails the test on an undecodable frame
	if n := count(e.t, e.white, codec.SMGameEnd); n != 0 {
		e.t.Fatalf("victim got %d GAME_END frames, want 0", n)
	}
}

// finish tears the game down and checks no goroutine outlives it.
func (e *hostileEnv) finish() {
	e.t.Helper()
	e.h.cancel()
	e.white.Close()
	e.black.Close()
	for _, c := range e.extra {
		c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > e.baseline+5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > e.baseline+5 {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		e.t.Fatalf("goroutines: %d after teardown, baseline %d\n%s", n, e.baseline, buf)
	}
}

// Frames that must never reach the game: each is sent by the black seat while
// it is white's turn (or by an outsider) and must change nothing.
func TestHostileClientsLeaveTheGameUntouched(t *testing.T) {
	validMove := func(e *hostileEnv, ply uint32, uci string) []byte { return e.moveFrame(ply, uci) }
	cases := []struct {
		name   string
		attack func(e *hostileEnv)
	}{
		{"every truncation of a valid MOVE frame", func(e *hostileEnv) {
			full := validMove(e, 1, "e7e5")
			for n := 0; n < len(full); n++ {
				e.black.feedRaw(append([]byte(nil), full[:n]...))
			}
		}},
		{"empty frame", func(e *hostileEnv) { e.black.feedRaw(nil); e.black.feedRaw([]byte{}) }},
		{"1 MiB of zeros", func(e *hostileEnv) { e.black.feedRaw(make([]byte, 1<<20)) }},
		{"1 MiB of 0xFF", func(e *hostileEnv) { e.black.feedRaw(bytesOf(0xFF, 1<<20)) }},
		{"unknown message codes", func(e *hostileEnv) {
			for _, code := range []byte{13, 14, 64, 127, 128, 200, 254, 255} {
				e.black.feedRaw([]byte{code})
				e.black.feedRaw([]byte{code, 0, 0, 0, 0})
			}
		}},
		{"MOVE by the side not to move", func(e *hostileEnv) {
			for _, ply := range []uint32{0, 1, 2} {
				e.black.feedRaw(validMove(e, ply, "e7e5"))
			}
		}},
		{"wrong ply_expected", func(e *hostileEnv) {
			for _, ply := range []uint32{0, 2, 99, math.MaxUint32} {
				e.black.feedRaw(validMove(e, ply, "e7e5"))
			}
		}},
		{"garbage and oversize UCI strings", func(e *hostileEnv) {
			for _, uci := range []string{"", "zz", "e2e5", "e7e5q", "\x00\x00\x00\x00", strings.Repeat("e", 4_000)} {
				e.black.feedRaw(validMove(e, 1, uci))
			}
		}},
		{"MOVE in another user's game, by an outsider", func(e *hostileEnv) {
			m := e.outsider("mallory")
			m.feedRaw(validMove(e, 1, "e2e4"))
			m.feedRaw(e.gameFrame(codec.CMResign))
			m.feedRaw(e.gameFrame(codec.CMAbort))
			m.feedRaw(e.gameFrame(codec.CMOfferDraw))
			m.feedRaw(e.gameFrame(codec.CMResync))
		}},
		{"commands for a game that does not exist", func(e *hostileEnv) {
			for _, ty := range []codec.ClientMsgType{codec.CMMove, codec.CMResign, codec.CMAbort, codec.CMOfferDraw, codec.CMTakeback, codec.CMResync, codec.CMDeclineDraw} {
				e.black.feedRaw(mustEncode(t, codec.ClientMsg{Type: ty, GameID: "no-such-game",
					Move: &codec.MovePayload{GameID: "no-such-game", PlyExpected: 1, UCI: "e2e4"}}))
			}
		}},
		{"re-AUTHENTICATE on the attacker's connection as the victim", func(e *hostileEnv) {
			e.black.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "alice"}})
			e.black.feedClient(t, codec.ClientMsg{Type: codec.CMAuthenticate, Authenticate: &codec.AuthenticatePayload{Token: "bob"}})
		}},
		{"SEEK and RESYNC while seated", func(e *hostileEnv) {
			e.black.feedClient(t, codec.ClientMsg{Type: codec.CMSeek, Seek: &codec.SeekPayload{PoolID: "blitz", TimeControl: "blitz-5+3"}})
			e.black.feedRaw(e.gameFrame(codec.CMResync))
		}},
		{"draw offers and takebacks before ply 2", func(e *hostileEnv) {
			for _, ty := range []codec.ClientMsgType{codec.CMOfferDraw, codec.CMTakeback, codec.CMDeclineDraw} {
				e.black.feedRaw(e.gameFrame(ty))
			}
			e.black.feedRaw(mustEncode(t, codec.ClientMsg{Type: codec.CMTakebackAccept, TakebackReply: &codec.TakebackReplyPayload{GameID: e.gameID, AtPly: 1}}))
		}},
		{"flood of RESYNC", func(e *hostileEnv) {
			f := e.gameFrame(codec.CMResync)
			frames := make([][]byte, 3000)
			for i := range frames {
				frames[i] = f
			}
			e.black.feedRawAsync(frames...)
		}},
		{"flood of OFFER_DRAW", func(e *hostileEnv) {
			f := e.gameFrame(codec.CMOfferDraw)
			frames := make([][]byte, 3000)
			for i := range frames {
				frames[i] = f
			}
			e.black.feedRawAsync(frames...)
		}},
		{"frame cut off mid-way, then the connection drops", func(e *hostileEnv) {
			full := validMove(e, 1, "e7e5")
			e.black.feedRaw(full[:len(full)/2])
			e.black.Close()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHostileEnv(t, nil)
			tc.attack(e)
			time.Sleep(50 * time.Millisecond) // let the server chew on the input
			e.victimStillServed()
			e.finish()
		})
	}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// Garbage must be answered with the defined error, and the connection must
// survive it (a hard close per bad frame is a cheap DoS lever, GS-6 §3.1).
func TestMalformedFrameGetsProtocolViolation(t *testing.T) {
	e := newHostileEnv(t, nil)
	defer e.finish()
	e.black.feedRaw([]byte{200, 1, 2, 3})
	m := e.black.waitForServer(t, codec.SMError, 2*time.Second)
	if m.Error.Code != codec.ECProtocolViolation {
		t.Fatalf("error code = 0x%02x, want PROTOCOL_VIOLATION", m.Error.Code)
	}
	e.black.feedRaw(e.gameFrame(codec.CMResync)) // still usable afterwards
	e.black.waitForServer(t, codec.SMState, 2*time.Second)
	e.victimStillServed()
}

// A frame before AUTHENTICATE is refused and the connection is closed.
func TestMoveBeforeAuthenticateIsRefused(t *testing.T) {
	e := newHostileEnv(t, nil)
	defer e.finish()
	c := newFakeConn()
	e.h.handle(e.ctx, c)
	c.feedRaw(e.moveFrame(1, "e2e4"))
	m := c.waitForServer(t, codec.SMError, 2*time.Second)
	if m.Error.Code != codec.ECUnauthenticated {
		t.Fatalf("error code = 0x%02x, want UNAUTHENTICATED", m.Error.Code)
	}
	e.extra = append(e.extra, c)
	e.victimStillServed()
}

// A client that stops reading is disconnected by the write pump; the victim
// and the game carry on (#91).
func TestSlowReaderUnderFloodDoesNotAffectTheOpponent(t *testing.T) {
	e := newHostileEnv(t, func(s *Server) {
		s.outboundQueue = 4
		s.writeDeadline = 100 * time.Millisecond
	})
	e.black.stall.Store(true)
	f := e.gameFrame(codec.CMResync)
	frames := make([][]byte, 500)
	for i := range frames {
		frames[i] = f
	}
	e.black.feedRawAsync(frames...)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.black.mu.Lock()
		closed := e.black.closed
		e.black.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.black.mu.Lock()
	closed := e.black.closed
	e.black.mu.Unlock()
	if !closed {
		t.Fatal("a client that stopped reading was not disconnected")
	}
	e.victimStillServed()
	e.finish()
}

// A replayed or duplicated frame never plays a move twice.
func TestReplayedFramesPlayEachMoveOnce(t *testing.T) {
	e := newHostileEnv(t, nil)
	defer e.finish()
	m1, m2 := e.moveFrame(1, "e2e4"), e.moveFrame(2, "e7e5")
	e.white.feedRaw(m1)
	e.white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	e.white.feedRaw(m1) // exact replay
	e.white.feedRaw(m1)
	e.black.feedRaw(m2)
	e.black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	e.black.feedRaw(m2)
	e.white.feedRaw(m1)
	e.black.feedRaw(m2)
	time.Sleep(100 * time.Millisecond)
	if got := moves(e.stream()); len(got) != 2 || got[0] != "e2e4" || got[1] != "e7e5" {
		t.Fatalf("stream moves = %v, want [e2e4 e7e5]", got)
	}
	_ = e.white.sent(t)
	_ = e.black.sent(t)
}

// After the game ends, every further command is refused, and nobody is told
// the game ended twice.
func TestCommandsAfterGameEndChangeNothing(t *testing.T) {
	e := newHostileEnv(t, nil)
	defer e.finish()
	e.black.feedRaw(e.gameFrame(codec.CMResign)) // black may resign: that ends the game
	e.white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	before := len(e.stream())
	for _, c := range []*fakeConn{e.black, e.white} {
		c.feedRaw(e.moveFrame(1, "e2e4"))
		c.feedRaw(e.moveFrame(2, "e7e5"))
		for _, ty := range []codec.ClientMsgType{codec.CMResign, codec.CMAbort, codec.CMOfferDraw, codec.CMTakeback, codec.CMDeclineDraw} {
			c.feedRaw(e.gameFrame(ty))
		}
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(e.stream()); got != before {
		t.Fatalf("stream grew from %d to %d events after the game ended", before, got)
	}
	for name, c := range map[string]*fakeConn{"white": e.white, "black": e.black} {
		if n := count(t, c, codec.SMGameEnd); n != 1 {
			t.Fatalf("%s got %d GAME_END frames, want exactly 1", name, n)
		}
	}
}

// --- fuzz ---------------------------------------------------------------------

// FuzzDispatch drives an authenticated connection in a live game with arbitrary
// input and checks the server neither panics nor stops serving the opponent.
// sel picks the shape: raw bytes, or a structurally valid message of any type
// aimed at the REAL game with a fuzzed ply and UCI (so the handlers' state
// logic is reached, not just the decoder). The seed corpus is committed under
// testdata/fuzz; per commit CI runs it plus a short mutation run, nightly runs
// longer.
func FuzzDispatch(f *testing.F) {
	f.Add([]byte{}, uint8(0), uint32(0), "")
	f.Add([]byte{200, 1, 2}, uint8(0), uint32(0), "")
	f.Add([]byte{2, 0xFF, 0xFF, 0xFF, 0xFF}, uint8(0), uint32(0), "")
	for sel := uint8(1); sel <= 14; sel++ {
		f.Add([]byte(nil), sel, uint32(1), "e7e5")
	}
	f.Add([]byte(nil), uint8(3), uint32(math.MaxUint32), "e2e4")
	f.Add([]byte(nil), uint8(3), uint32(2), strings.Repeat("a", 5000))
	f.Fuzz(func(t *testing.T, data []byte, sel uint8, ply uint32, uci string) {
		e := newHostileEnv(t, nil)
		if sel%15 == 0 {
			e.black.feedRaw(data)
		} else {
			ty := codec.ClientMsgType(sel % 15)
			msg := codec.ClientMsg{Type: ty, GameID: e.gameID}
			switch ty {
			case codec.CMAuthenticate:
				msg.Authenticate = &codec.AuthenticatePayload{Token: uci}
			case codec.CMSeek:
				msg.Seek = &codec.SeekPayload{PoolID: uci, TimeControl: uci, Rated: len(data)%2 == 1}
			case codec.CMMove:
				msg.Move = &codec.MovePayload{GameID: e.gameID, PlyExpected: ply, UCI: uci}
			case codec.CMPing:
				msg.Ping = &codec.PingPayload{T: uint64(ply)}
			case codec.CMTakebackAccept, codec.CMTakebackDecline:
				msg.TakebackReply = &codec.TakebackReplyPayload{GameID: e.gameID, AtPly: ply}
			}
			if frame, err := codec.EncodeClient(msg); err == nil {
				e.black.feedRaw(frame)
			}
			e.black.feedRaw(data) // and the raw bytes after it
		}
		// The victim must still be served: a RESYNC always gets a STATE back,
		// whether or not the fuzzed input legitimately ended the game.
		e.white.feedRaw(e.gameFrame(codec.CMResync))
		e.white.waitForServer(t, codec.SMState, 3*time.Second)
		_ = e.white.sent(t)
		_ = e.black.sent(t)
		e.h.cancel()
		e.white.Close()
		e.black.Close()
	})
}
