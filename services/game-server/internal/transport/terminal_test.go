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

// #89 and #122: the move that ends the game, resent.

// Fool's mate: Black mates on ply 4.
var foolsMate = []string{"f2f3", "e7e5", "g2g4", "d8h4"}

// Sam Loyd's 10-move stalemate: White's 19th ply stalemates Black.
var loydStalemate = []string{
	"e2e3", "a7a5", "d1h5", "a8a6", "h5a5", "h7h5", "h2h4", "a6h6", "a5c7", "f7f6",
	"c7d7", "e8f7", "d7b7", "d8d3", "b7b8", "d3h7", "b8c8", "f7g6", "c8e6",
}

// playAllButLast plays every move except the last, alternating white/black, and
// returns the conns of the mover of the last move and of the other seat.
func playAllButLast(t *testing.T, white, black *fakeConn, gameID string, line []string) (lastMover, other *fakeConn) {
	t.Helper()
	for i, uci := range line[:len(line)-1] {
		mover, opp := white, black
		if i%2 == 1 {
			mover, opp = black, white
		}
		moveAndAwaitBoth(t, mover, opp, gameID, uint32(i+1), uci)
	}
	if (len(line)-1)%2 == 0 {
		return white, black
	}
	return black, white
}

// ambiguousStore commits the first batch that ends the game but reports an
// error, like a store write that timed out after it succeeded (#89, #95). With
// failReread, the session's re-read right after that error fails once too, so
// the session cannot confirm the commit at the time.
type ambiguousStore struct {
	game.EventStore
	failReread bool

	mu        sync.Mutex
	fired     bool
	loadFails int
}

func (a *ambiguousStore) AppendBatch(ctx context.Context, stream string, seq uint32, evs []game.Event) error {
	if err := a.EventStore.AppendBatch(ctx, stream, seq, evs); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ev := range evs {
		if ev.Type == game.EvGameEnded && !a.fired {
			a.fired = true
			if a.failReread {
				a.loadFails = 1
			}
			return errors.New("store: timeout (the write did commit)")
		}
	}
	return nil
}

func (a *ambiguousStore) Load(ctx context.Context, stream string) ([]game.Event, error) {
	a.mu.Lock()
	fail := a.loadFails > 0
	if fail {
		a.loadFails--
	}
	a.mu.Unlock()
	if fail {
		return nil, errors.New("store: unavailable")
	}
	return a.EventStore.Load(ctx, stream)
}

func countEvents(t *testing.T, h *harness, gameID string, typ game.EventType) int {
	t.Helper()
	evs, err := h.store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// wantOneEnd checks both seats got exactly one GAME_END with term and rv (for
// white, black), and that the game was dropped with one GameEnded and no flag.
func wantOneEnd(t *testing.T, h *harness, gameID string, white, black *fakeConn, term game.Termination, rv []uint8) {
	t.Helper()
	for _, c := range []*fakeConn{white, black} {
		ge := c.waitForServer(t, codec.SMGameEnd, 2*time.Second)
		if game.Termination(ge.GameEnd.Termination) != term {
			t.Fatalf("termination = 0x%02x, want 0x%02x", ge.GameEnd.Termination, uint8(term))
		}
		if len(ge.GameEnd.ResultVector) != 2 || ge.GameEnd.ResultVector[0] != rv[0] || ge.GameEnd.ResultVector[1] != rv[1] {
			t.Fatalf("result = %v, want %v", ge.GameEnd.ResultVector, rv)
		}
	}
	waitUntil(t, time.Second, "the game to be dropped", func() bool {
		_, live := h.srv.reg.Get(gameID)
		return !live
	})
	h.clk.add(time.Hour) // time passes: nothing flags the ended game
	time.Sleep(50 * time.Millisecond)
	if n := countEvents(t, h, gameID, game.EvGameEnded); n != 1 {
		t.Fatalf("%d GameEnded events, want 1", n)
	}
	if n := countEvents(t, h, gameID, game.EvClockFlagged); n != 0 {
		t.Fatalf("%d ClockFlagged events after the end, want 0", n)
	}
	white.noServerMsg(t, codec.SMGameEnd, 50*time.Millisecond)
	black.noServerMsg(t, codec.SMGameEnd, 0)
}

var (
	blackWins = []uint8{uint8(game.ResultLoss), uint8(game.ResultWin)}
	drawn     = []uint8{uint8(game.ResultDraw), uint8(game.ResultDraw)}
)

// #89: the ending batch commits but the store reports an error. The session
// re-reads the stream, sees its events, and the ending is announced at once,
// with no client resend (PROTOCOL forbids one): MOVE_ACCEPTED and one GAME_END
// to both seats with the engine's termination, then the game is dropped.
func TestAmbiguousEndingCommitIsConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		line []string // nil: White resigns at the start
		term game.Termination
		rv   []uint8
	}{
		{"mate", foolsMate, game.TermMate, blackWins},
		{"stalemate", loydStalemate, game.TermStalemate, drawn},
		{"resign", nil, game.TermResign, blackWins},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			defer h.cancel()
			amb := &ambiguousStore{EventStore: h.store}
			h.srv.reg.store = amb
			ctx := context.Background()
			ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
			white, black := whiteBlack(ca, cb, seatA, seatB)
			if tc.line == nil {
				white.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
			} else {
				mover, other := playAllButLast(t, white, black, gameID, tc.line)
				ply, last := uint32(len(tc.line)), tc.line[len(tc.line)-1]
				move(t, mover, gameID, ply, last)
				for _, c := range []*fakeConn{mover, other} {
					if acc := c.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.Ply != ply {
						t.Fatalf("MOVE_ACCEPTED ply = %d, want %d", acc.MoveAccepted.Ply, ply)
					}
				}
			}
			wantOneEnd(t, h, gameID, white, black, tc.term, tc.rv)
			if !amb.fired {
				t.Fatal("the store wrapper never saw the ending batch")
			}
			white.noServerMsg(t, codec.SMError, 0)
			black.noServerMsg(t, codec.SMError, 0)
		})
	}
}

// #89, when the session cannot confirm the commit at the time (the re-read
// fails too): the mover gets INTERNAL and the opponent hears nothing. A later
// resend of that move (from a client that does resend) is re-acked, and then
// the opponent gets the move and both get one GAME_END.
func TestAmbiguousEndingUnconfirmedThenResend(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	amb := &ambiguousStore{EventStore: h.store, failReread: true}
	h.srv.reg.store = amb
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	_, _ = playAllButLast(t, white, black, gameID, foolsMate)

	move(t, black, gameID, 4, "d8h4")
	if e := black.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECInternal {
		t.Fatalf("first attempt: error 0x%02x, want INTERNAL", e.Error.Code)
	}
	white.noServerMsg(t, codec.SMMoveAccepted, 50*time.Millisecond) // the bug: White never hears
	white.noServerMsg(t, codec.SMGameEnd, 0)

	move(t, black, gameID, 4, "d8h4")
	if acc := black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.UCI != "d8h4" {
		t.Fatalf("re-ack = %+v", acc.MoveAccepted)
	}
	if acc := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.Ply != 4 {
		t.Fatalf("opponent MOVE_ACCEPTED ply = %d, want 4", acc.MoveAccepted.Ply)
	}
	wantOneEnd(t, h, gameID, white, black, game.TermMate, blackWins)
}

// #122: after the game ended and its session was dropped, a resend of the
// mating move is re-acked with the result (to the sender only); other plies
// follow the duplicate-MOVE rule; a new move is GAME_OVER; strangers are
// NOT_YOUR_SEAT as before.
func TestMoveAfterEnd_DuplicateRule(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	_, _ = playAllButLast(t, white, black, gameID, foolsMate)
	moveAndAwaitBoth(t, black, white, gameID, 4, "d8h4")
	black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	waitUntil(t, time.Second, "the game to be dropped", func() bool {
		_, live := h.srv.reg.Get(gameID)
		return !live
	})

	// The mating move, resent: MOVE_ACCEPTED + GAME_END{MATE}, sender only.
	move(t, black, gameID, 4, "d8h4")
	if acc := black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.Ply != 4 || acc.MoveAccepted.UCI != "d8h4" {
		t.Fatalf("re-ack = %+v, want ply 4 d8h4", acc.MoveAccepted)
	}
	if ge := black.waitForServer(t, codec.SMGameEnd, 2*time.Second); game.Termination(ge.GameEnd.Termination) != game.TermMate {
		t.Fatalf("termination = 0x%02x, want MATE", ge.GameEnd.Termination)
	}
	white.noServerMsg(t, codec.SMMoveAccepted, 50*time.Millisecond)

	// An earlier ply, resent: re-acked, with no GAME_END (it did not end the game).
	move(t, white, gameID, 3, "g2g4")
	if acc := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second); acc.MoveAccepted.Ply != 3 {
		t.Fatalf("re-ack ply = %d, want 3", acc.MoveAccepted.Ply)
	}
	white.noServerMsg(t, codec.SMGameEnd, 50*time.Millisecond)

	// Same ply, different move: STALE_PLY. A new move: GAME_OVER.
	for _, c := range []struct {
		conn *fakeConn
		ply  uint32
		uci  string
		want uint8
	}{
		{black, 4, "d8g5", codec.ReasonStalePly},
		{white, 5, "e2e4", codec.ReasonGameOver},
	} {
		move(t, c.conn, gameID, c.ply, c.uci)
		r := c.conn.waitForServer(t, codec.SMMoveRejected, 2*time.Second)
		if r.MoveRejected.Reason != c.want {
			t.Fatalf("MOVE ply %d %s: reason 0x%02x, want 0x%02x", c.ply, c.uci, r.MoveRejected.Reason, c.want)
		}
	}

	// The other seat resending the mating ply: NOT_YOUR_SEAT (it is Black's move).
	move(t, white, gameID, 4, "d8h4")
	if r := white.waitForServer(t, codec.SMMoveRejected, 2*time.Second); r.MoveRejected.Reason != codec.ReasonNotYourSeat {
		t.Fatalf("other seat: reason 0x%02x, want NOT_YOUR_SEAT", r.MoveRejected.Reason)
	}

	// A reconnected sender (no seat binding) is served the same way.
	disconnect(black)
	blackUser := "bob"
	if seatA == 1 {
		blackUser = "alice"
	}
	b2 := reconnect(t, h, ctx, blackUser)
	move(t, b2, gameID, 4, "d8h4")
	b2.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	b2.waitForServer(t, codec.SMGameEnd, 2*time.Second)

	// Someone who did not play it: NOT_YOUR_SEAT, as before.
	eve := reconnect(t, h, ctx, "eve")
	move(t, eve, gameID, 4, "d8h4")
	if r := eve.waitForServer(t, codec.SMMoveRejected, 2*time.Second); r.MoveRejected.Reason != codec.ReasonNotYourSeat {
		t.Fatalf("stranger: reason 0x%02x, want NOT_YOUR_SEAT", r.MoveRejected.Reason)
	}
}

// A store failure while answering a MOVE on an ended game is a retryable
// INTERNAL, not "not your seat".
func TestMoveAfterEnd_StoreFailureIsInternal(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, _, _ := pair(t, h, ctx, "alice", "bob")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gameID})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	waitUntil(t, time.Second, "the game to be dropped", func() bool {
		_, live := h.srv.reg.Get(gameID)
		return !live
	})
	h.srv.reg.store = failingLoadStore{h.store}
	move(t, cb, gameID, 1, "e2e4")
	if e := cb.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECInternal {
		t.Fatalf("error code = 0x%02x, want INTERNAL", e.Error.Code)
	}
}
