package transport

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// TestSnapshotToStateBodyEncodes: every snapshot shape the session produces must
// map to a v1.3 STATE body the codec accepts. send() drops a frame the codec
// refuses, so a body that broke a §4A.6 invariant would vanish silently; this
// pins the mapping directly, for both recipients.
func TestSnapshotToStateBodyEncodes(t *testing.T) {
	serverT := time.UnixMilli(1728000000500)
	fen := "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	cases := map[string]session.Snapshot{
		"matched": {FEN: fen, Phase: game.PhaseMatched, RemainingMs: []int64{300000, 300000},
			DrawOfferBy: -1, TakebackBy: -1, RunningSeat: -1, ServerT: serverT},
		"live at ply 0": {FEN: fen, Phase: game.PhaseLive, RemainingMs: []int64{300000, 300000},
			DrawOfferBy: -1, TakebackBy: -1, RunningSeat: 0, ServerT: serverT, ClockStartedSet: true},
		"live with draw offer": {FEN: fen, Ply: 7, Turn: 1, Phase: game.PhaseLive,
			RemainingMs: []int64{250000, -5}, LastMoveUCI: "g1f3", DrawOfferBy: 0, TakebackBy: -1,
			RunningSeat: 1, ServerT: serverT, ClockStartedSet: true},
		"live with takeback request": {FEN: fen, Ply: 7, Turn: 1, Phase: game.PhaseLive,
			RemainingMs: []int64{250000, 1000}, LastMoveUCI: "g1f3", DrawOfferBy: -1, TakebackBy: 1,
			TakebackAtPly: 6, RunningSeat: 1, ServerT: serverT, ClockStartedSet: true},
		"ended": {FEN: fen, Ply: 42, Turn: 1, Phase: game.PhaseEnded, RemainingMs: []int64{120000, 0},
			Ended: true, Terminal: game.TermResign, LastMoveUCI: "d1h5",
			ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, DrawOfferBy: -1, TakebackBy: -1,
			RunningSeat: -1, ServerT: serverT, ClockStartedSet: true},
	}
	for name, snap := range cases {
		for _, seat := range []uint8{0, 1} {
			body := snapshotToStateBody(snap, seat)
			msg := codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{GameID: "g", State: body}}
			frame, err := codec.EncodeServer(msg)
			if err != nil {
				t.Fatalf("%s seat %d: codec refused STATE: %v (body=%+v)", name, seat, err, body)
			}
			back, err := codec.DecodeServer(frame)
			if err != nil {
				t.Fatalf("%s seat %d: decode: %v", name, seat, err)
			}
			got := back.State.State
			if got.YouSeat != seat {
				t.Fatalf("%s: you_seat = %d, want %d", name, got.YouSeat, seat)
			}
			if got.ServerT != 1728000000500 {
				t.Fatalf("%s: server_t = %d", name, got.ServerT)
			}
		}
	}
	// GS-7: a LIVE game's clock runs from ClockStarted, so the side to move is
	// the running seat even at ply 0 (a MATCHED game has none).
	if z := snapshotToStateBody(cases["live at ply 0"], 0); z.Status != codec.StatusLive || z.RunningSeat != 0 {
		t.Fatalf("ply-0 LIVE must report seat 0 running: %+v", z)
	}
	if m := snapshotToStateBody(cases["matched"], 0); m.Status != codec.StatusMatched || m.RunningSeat != codec.NoSeat {
		t.Fatalf("MATCHED must report no running clock: %+v", m)
	}
	if tb := snapshotToStateBody(cases["live with takeback request"], 0); tb.TakebackBy != 1 || tb.TakebackAtPly != 6 {
		t.Fatalf("takeback mapping wrong: %+v", tb)
	}
	live := snapshotToStateBody(cases["live with draw offer"], 0)
	if live.Status != codec.StatusLive || live.RunningSeat != 1 || live.PendingDrawBy != 0 || live.Clocks[1] != 0 {
		t.Fatalf("live mapping wrong: %+v", live)
	}
	ended := snapshotToStateBody(cases["ended"], 0)
	if ended.Status != codec.StatusEnded || ended.RunningSeat != codec.NoSeat || ended.PendingDrawBy != codec.NoSeat ||
		ended.Termination != uint8(game.TermResign) || len(ended.ResultVector) != 2 {
		t.Fatalf("ended mapping wrong: %+v", ended)
	}
}

// TestStateChargesRunningClockToServerT (#123): the running seat's clock is
// charged up to server_t, so a client can recompute from the pair. One shared
// fake clock drives the session; only the running seat's clock decreases.
func TestStateChargesRunningClockToServerT(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	ctx := context.Background()
	ca, cb, gameID, seatA, seatB := pair(t, h, ctx, "alice", "bob")
	white, _ := whiteBlack(ca, cb, seatA, seatB)

	resync := func() codec.StateBody {
		before := count(t, white, codec.SMState)
		white.feedClient(t, codec.ClientMsg{Type: codec.CMResync, GameID: gameID})
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if count(t, white, codec.SMState) > before {
				return lastState(t, white)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("no STATE after RESYNC")
		return codec.StateBody{}
	}

	t0 := resync()
	if t0.RunningSeat != 0 {
		t.Fatalf("running_seat at ply 0 = %d, want 0 (white, clock started)", t0.RunningSeat)
	}
	h.clk.add(7 * time.Second)
	t1 := resync()
	if t1.ServerT != t0.ServerT+7000 {
		t.Fatalf("server_t advanced %d, want 7000", int64(t1.ServerT)-int64(t0.ServerT))
	}
	if t0.Clocks[0]-t1.Clocks[0] != 7000 {
		t.Fatalf("running seat clock fell %d ms, want 7000 (t0=%v t1=%v)", int64(t0.Clocks[0])-int64(t1.Clocks[0]), t0.Clocks, t1.Clocks)
	}
	if t1.Clocks[1] != t0.Clocks[1] {
		t.Fatalf("idle seat clock changed %d -> %d, want unchanged", t0.Clocks[1], t1.Clocks[1])
	}
	// After white moves, the running seat flips to black and white's clock stops.
	moveAndAwaitBoth(t, white, cb2(ca, cb, white), gameID, 1, "e2e4")
	h.clk.add(3 * time.Second)
	t2 := resync()
	if t2.RunningSeat != 1 {
		t.Fatalf("running_seat after white's move = %d, want 1", t2.RunningSeat)
	}
	if t2.Clocks[1] != t0.Clocks[1]-3000 {
		t.Fatalf("black clock = %d, want %d", t2.Clocks[1], int64(t0.Clocks[1])-3000)
	}
}

// cb2 returns whichever of ca/cb is not `white`.
func cb2(ca, cb, white *fakeConn) *fakeConn {
	if ca == white {
		return cb
	}
	return ca
}
