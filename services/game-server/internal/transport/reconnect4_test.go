package transport

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// S2: a game that ends and leaves the node between the seat lookup and the
// bind (its ticker ended and dropped it) never sends GAME_END to this
// connection. It is shown once as an ending and its recent row is consumed,
// so the next login does not show it again.
func TestGameDroppedBeforeBindIsShownOnce(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ab0000-0000-7000-8000-00000000b003"
	seedGame(t, st, gid, "blitz-5+3", "max", "ned")
	var once atomic.Bool
	h.srv.beforeBind = func(id string) {
		if !once.CompareAndSwap(false, true) {
			return
		}
		sess, ok := h.srv.reg.Get(id)
		if !ok {
			t.Error("no session before the bind")
			return
		}
		if err := sess.Resign(ctx, 1); err != nil {
			t.Errorf("resign: %v", err)
		}
		h.srv.dropGame(id)
	}
	c := reconnect(t, h, ctx, "max")
	if m := c.waitForServer(t, codec.SMState, 2*time.Second); m.State.GameID != gid || m.State.State.Status != codec.StatusEnded {
		t.Fatalf("STATE for a game that ended during the login: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "max's recent row to be consumed", func() bool {
		r, _ := st.UserRecentGames(ctx, "max", 5)
		return len(r) == 0
	})
	h.srv.beforeBind = nil
	reconnect(t, h, ctx, "max").noServerMsg(t, codec.SMState, 150*time.Millisecond)
}

// S2, the other order: a game already ended when its snapshot is read is not
// bound; it is shown once through the ended path and its row consumed.
func TestGameEndedBeforeSnapshotIsNotBound(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ab0000-0000-7000-8000-00000000b005"
	seedGame(t, st, gid, "blitz-5+3", "sam", "tia")
	var once atomic.Bool
	h.srv.afterBuild = func(id string) {
		if !once.CompareAndSwap(false, true) {
			return
		}
		if sess, ok := h.srv.reg.Get(id); ok {
			_ = sess.Resign(ctx, 1)
		}
	}
	c := reconnect(t, h, ctx, "sam")
	if m := c.waitForServer(t, codec.SMState, 2*time.Second); m.State.State.Status != codec.StatusEnded {
		t.Fatalf("STATE: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "sam's recent row to be consumed", func() bool {
		r, _ := st.UserRecentGames(ctx, "sam", 5)
		return len(r) == 0
	})
	// Not bound, so the ticker's GAME_END for it does not show it a second time.
	c.noServerMsg(t, codec.SMGameEnd, 300*time.Millisecond)
}

// S3: a RESYNC that shows an ended game consumes its recent row, so the next
// login does not show the same ending again.
func TestResyncOfEndedGameConsumesTheRecentRow(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gid, _, seatB := pair(t, h, ctx, "olga", "pete")
	ca.feedClient(t, codec.ClientMsg{Type: codec.CMResign, GameID: gid})
	ca.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	cb.waitForServer(t, codec.SMGameEnd, 2*time.Second)
	waitUntil(t, 2*time.Second, "pete's recent row to be consumed", func() bool {
		r, _ := st.UserRecentGames(ctx, "pete", 5)
		return len(r) == 0
	})
	// Pete missed it after all (say, the GAME_END frame was lost): his row is back.
	put := game.IndexOp{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: "pete", GameID: gid, Seat: seatB,
		EndedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.RepairIndex(ctx, []game.IndexOp{put}); err != nil {
		t.Fatal(err)
	}
	cb.feedClient(t, codec.ClientMsg{Type: codec.CMResync, GameID: gid})
	if m := cb.waitForServer(t, codec.SMState, 2*time.Second); m.State.State.Status != codec.StatusEnded {
		t.Fatalf("RESYNC of an ended game: %+v", m.State)
	}
	waitUntil(t, 2*time.Second, "the RESYNC to consume pete's recent row", func() bool {
		r, _ := st.UserRecentGames(ctx, "pete", 5)
		return len(r) == 0
	})
}

// S6: an ended game that can never be shown (its time control does not
// resolve) has its rows dropped instead of being folded again at every login.
func TestUnshowableEndedGameRowsAreDropped(t *testing.T) {
	h, st := indexedHarness(t)
	defer h.cancel()
	ctx := context.Background()
	gid := "01ab0000-0000-7000-8000-00000000b004"
	seedGame(t, st, gid, "no-such-tc", "quin", "rosa")
	endStreamPlainly(t, h, st, gid)
	reconnect(t, h, ctx, "quin").noServerMsg(t, codec.SMError, 150*time.Millisecond)
	waitUntil(t, 2*time.Second, "quin's rows to be dropped", func() bool {
		l, _ := st.UserLiveGames(ctx, "quin", 5)
		return len(l) == 0
	})
}
