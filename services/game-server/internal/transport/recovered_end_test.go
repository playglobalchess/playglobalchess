package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// ambiguousBatchStore commits the next MovePlayed+GameEnded batch and THEN
// reports an error (a store timeout after the write landed).
type ambiguousBatchStore struct {
	*game.MemStore
	armed atomic.Bool
}

func (s *ambiguousBatchStore) AppendBatch(ctx context.Context, id string, seq uint32, evs []game.Event) error {
	err := s.MemStore.AppendBatch(ctx, id, seq, evs)
	if err == nil && len(evs) == 2 && evs[0].Type == game.EvMovePlayed && evs[1].Type == game.EvGameEnded &&
		s.armed.CompareAndSwap(true, false) {
		return errors.New("injected: ambiguous store error after commit")
	}
	return err
}

// playToMatePrefix pairs two players and plays Scholar's mate up to (not
// including) the mating move. Returns white, black and the game id.
func playToMatePrefix(t *testing.T, h *harness, ctx context.Context) (white, black *fakeConn, gameID string) {
	t.Helper()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, black = whiteBlack(ca, cb, seatA, seatB)
	for i, uci := range []string{"e2e4", "e7e5", "f1c4", "b8c6", "d1h5", "g8f6"} {
		mover, opp := white, black
		if i%2 == 1 {
			mover, opp = black, white
		}
		moveAndAwaitBoth(t, mover, opp, gameID, uint32(i+1), uci)
	}
	return white, black, gameID
}

func countType(t *testing.T, c *fakeConn, typ codec.ServerMsgType) int {
	t.Helper()
	n := 0
	for _, m := range c.sent(t) {
		if m.Type == typ {
			n++
		}
	}
	return n
}

func assertOneGameEnd(t *testing.T, white, black *fakeConn) {
	t.Helper()
	white.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	black.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	time.Sleep(60 * time.Millisecond) // let any duplicate (wrongly) arrive
	if n := countType(t, white, codec.SMGameEnd); n != 1 {
		t.Fatalf("white received %d GAME_END, want exactly 1", n)
	}
	if n := countType(t, black, codec.SMGameEnd); n != 1 {
		t.Fatalf("black received %d GAME_END, want exactly 1", n)
	}
}

// The mating batch commits but the store errors: the mover gets an ERROR. The
// client resends the same ply; both seats must receive MOVE_ACCEPTED for it and
// GAME_END exactly once.
func TestAmbiguousMateCommitThenResendDeliversGameEndOnce(t *testing.T) {
	mem := game.NewMemStore()
	st := &ambiguousBatchStore{MemStore: mem}
	h := newHarnessWithStore(t, st)
	defer h.cancel()
	h.srv.flagInterval = time.Hour // only the resend may deliver the end
	ctx := context.Background()
	white, black, gameID := playToMatePrefix(t, h, ctx)

	st.armed.Store(true)
	move(t, white, gameID, 7, "h5f7")
	white.waitForServer(t, codec.SMError, 2*time.Second)

	move(t, white, gameID, 7, "h5f7")
	acc := white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	if acc.MoveAccepted.Ply != 7 || acc.MoveAccepted.UCI != "h5f7" {
		t.Fatalf("resend ack = %+v, want ply 7 h5f7", acc.MoveAccepted)
	}
	black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	assertOneGameEnd(t, white, black)
}

// Same ambiguous commit, but the client never resends: the flag ticker, after
// the session reconciles to Ended, must deliver GAME_END exactly once.
func TestAmbiguousMateCommitTickerDeliversGameEndOnce(t *testing.T) {
	mem := game.NewMemStore()
	st := &ambiguousBatchStore{MemStore: mem}
	h := newHarnessWithStore(t, st)
	defer h.cancel()
	h.srv.flagInterval = 15 * time.Millisecond
	ctx := context.Background()
	white, black, gameID := playToMatePrefix(t, h, ctx)

	st.armed.Store(true)
	move(t, white, gameID, 7, "h5f7")
	white.waitForServer(t, codec.SMError, 2*time.Second)
	assertOneGameEnd(t, white, black)
}

// Cross-node: another writer already committed the mating batch. The mover's
// MOVE is re-acked; both seats get MOVE_ACCEPTED and GAME_END exactly once.
func TestForeignMateReAckDeliversGameEndOnce(t *testing.T) {
	mem := game.NewMemStore()
	h := newHarnessWithStore(t, mem)
	defer h.cancel()
	h.srv.flagInterval = time.Hour
	ctx := context.Background()
	white, black, gameID := playToMatePrefix(t, h, ctx)

	sid := game.StreamIDFor(gameID)
	evs, err := mem.Load(ctx, sid)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := mem.AppendBatch(ctx, sid, uint32(len(evs)), []game.Event{
		{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: 7, SeatIdx: 0, UCI: "h5f7", ClockAfterMs: 290000, ServerTS: uint64(time.Now().UnixMilli())}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: game.MoverResultVector(0, game.TermMate, 2), Termination: game.TermMate}},
	}); err != nil {
		t.Fatalf("other writer: %v", err)
	}

	move(t, white, gameID, 7, "h5f7")
	white.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	black.waitForServer(t, codec.SMMoveAccepted, 2*time.Second)
	assertOneGameEnd(t, white, black)
}
