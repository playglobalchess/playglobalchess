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

// ambiguousStore fails ONE write that `match` selects with an error, like a
// store timeout. With commit (the default for the ending tests) the write did
// land first: an ambiguous commit (#89, #95). Without it, nothing was written.
// After the failed write, the next loadFails Loads fail too (an outage often
// hits both).
type ambiguousStore struct {
	game.EventStore
	match     func([]game.Event) bool // nil: a batch with GameEnded
	noCommit  bool
	loadFails int

	mu      sync.Mutex
	fired   bool
	pending int // Loads still to fail
}

func (a *ambiguousStore) fail(evs []game.Event) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fired {
		return false
	}
	m := a.match
	if m == nil {
		m = func(evs []game.Event) bool {
			for _, ev := range evs {
				if ev.Type == game.EvGameEnded {
					return true
				}
			}
			return false
		}
	}
	if !m(evs) {
		return false
	}
	a.fired, a.pending = true, a.loadFails
	return true
}

var errAmbiguous = errors.New("store: timeout (the write may have committed)")

func (a *ambiguousStore) AppendBatch(ctx context.Context, stream string, seq uint32, evs []game.Event) error {
	if !a.fail(evs) {
		return a.EventStore.AppendBatch(ctx, stream, seq, evs)
	}
	if !a.noCommit {
		if err := a.EventStore.AppendBatch(ctx, stream, seq, evs); err != nil {
			return err
		}
	}
	return errAmbiguous
}

func (a *ambiguousStore) Append(ctx context.Context, stream string, seq uint32, ev game.Event) error {
	if !a.fail([]game.Event{ev}) {
		return a.EventStore.Append(ctx, stream, seq, ev)
	}
	if !a.noCommit {
		if err := a.EventStore.Append(ctx, stream, seq, ev); err != nil {
			return err
		}
	}
	return errAmbiguous
}

func (a *ambiguousStore) Load(ctx context.Context, stream string) ([]game.Event, error) {
	a.mu.Lock()
	fail := a.pending > 0
	if fail {
		a.pending--
	}
	a.mu.Unlock()
	if fail {
		return nil, errors.New("store: unavailable")
	}
	return a.EventStore.Load(ctx, stream)
}

// movePlayedAt selects the single MovePlayed write for ply.
func movePlayedAt(ply uint32) func([]game.Event) bool {
	return func(evs []game.Event) bool {
		return len(evs) == 1 && evs[0].MovePlayed != nil && evs[0].MovePlayed.Ply == ply
	}
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

// #89, when the session cannot confirm the commit at the time (every re-read
// fails too): the mover gets INTERNAL and the opponent hears nothing. A later
// resend of that move (from a client that does resend) is re-acked, and then
// the opponent gets the move and both get one GAME_END.
func TestAmbiguousEndingUnconfirmedThenResend(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	// Every re-read the session tries fails, so it cannot confirm the commit.
	amb := &ambiguousStore{EventStore: h.store, loadFails: 3}
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

// The hot path: an ordinary (single-event) move whose Append commits but
// reports an error is confirmed by the re-read and acked to both seats, with no
// error to the mover (#95's ambiguous case, on the path every move takes).
func TestAmbiguousOrdinaryMoveIsConfirmed(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	amb := &ambiguousStore{EventStore: h.store, match: movePlayedAt(1)}
	h.srv.reg.store = amb
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")
	if !amb.fired {
		t.Fatal("the store wrapper never saw the move")
	}
	white.noServerMsg(t, codec.SMError, 0)
	// The game carries on from the confirmed move.
	moveAndAwaitBoth(t, black, white, gameID, 2, "e7e5")
	if n := countEvents(t, h, gameID, game.EvMovePlayed); n != 2 {
		t.Fatalf("%d MovePlayed events, want 2", n)
	}
}

// The re-read is retried: an outage that also fails the first two re-reads
// still confirms the commit on the third.
func TestAmbiguousCommitConfirmedAfterRereadRetries(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	amb := &ambiguousStore{EventStore: h.store, match: movePlayedAt(1), loadFails: 2}
	h.srv.reg.store = amb
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")
	white.noServerMsg(t, codec.SMError, 0)
}

// A write that errored and did NOT commit: the re-read finds nothing, so the
// mover gets INTERNAL ("not confirmed"), the opponent hears nothing, and the
// session is not left inconsistent: the same move can be played again.
func TestFailedMoveNotCommittedLeavesGamePlayable(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	amb := &ambiguousStore{EventStore: h.store, match: movePlayedAt(1), noCommit: true}
	h.srv.reg.store = amb
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	move(t, white, gameID, 1, "e2e4")
	if e := white.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECInternal {
		t.Fatalf("error 0x%02x, want INTERNAL", e.Error.Code)
	}
	black.noServerMsg(t, codec.SMMoveAccepted, 50*time.Millisecond)
	if n := countEvents(t, h, gameID, game.EvMovePlayed); n != 0 {
		t.Fatalf("%d MovePlayed events, want 0", n)
	}
	moveAndAwaitBoth(t, white, black, gameID, 1, "e2e4")
	moveAndAwaitBoth(t, black, white, gameID, 2, "e7e5")
}

// A terminal re-ack while the end claim is already held (by a broadcast that
// failed after claiming, #88): the sender still gets GAME_END.
func TestTerminalReackWithClaimHeldStillSendsGameEnd(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	amb := &ambiguousStore{EventStore: h.store, loadFails: 3}
	h.srv.reg.store = amb
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black := whiteBlack(ca, cb, seatA, seatB)
	_, _ = playAllButLast(t, white, black, gameID, foolsMate)
	move(t, black, gameID, 4, "d8h4")
	black.waitForServer(t, codec.SMError, 2*time.Second) // unconfirmed

	h.srv.claimGameEnd(gameID) // another path took the claim and never sent
	move(t, black, gameID, 4, "d8h4")
	black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if ge := black.waitForServer(t, codec.SMGameEnd, 2*time.Second); game.Termination(ge.GameEnd.Termination) != game.TermMate {
		t.Fatalf("termination = 0x%02x, want MATE", ge.GameEnd.Termination)
	}
}

// Lookups of games not live here are budgeted per connection: past the budget
// the reply is RATE_LIMITED with no store read, and the budget refills.
func TestEndedLookupBudget(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	eve := reconnect(t, h, ctx, "eve")
	const unknown = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	for i := 0; i < lookupBudget; i++ {
		resync(t, eve, unknown)
		if e := eve.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECUnknownGame {
			t.Fatalf("lookup %d: error 0x%02x, want UNKNOWN_GAME", i, e.Error.Code)
		}
	}
	h.srv.reg.store = failingLoadStore{h.store} // a store read would now be INTERNAL
	move(t, eve, unknown, 1, "e2e4")
	if e := eve.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECRateLimited {
		t.Fatalf("over budget: error 0x%02x, want RATE_LIMITED", e.Error.Code)
	}
	h.srv.reg.store = h.store
	h.clk.add(lookupWindow)
	resync(t, eve, unknown)
	if e := eve.waitForServer(t, codec.SMError, 2*time.Second); e.Error.Code != codec.ECUnknownGame {
		t.Fatalf("after the window: error 0x%02x, want UNKNOWN_GAME", e.Error.Code)
	}
}
