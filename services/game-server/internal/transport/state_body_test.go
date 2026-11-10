package transport

import (
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
	anchor := time.UnixMilli(1728000000500)
	fen := "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1"
	cases := map[string]session.Snapshot{
		"matched": {FEN: fen, Phase: game.PhaseMatched, RemainingMs: []int64{300000, 300000},
			DrawOfferBy: -1, ClockAnchor: anchor},
		"live at ply 0": {FEN: fen, Phase: game.PhaseLive, RemainingMs: []int64{300000, 300000},
			DrawOfferBy: -1, ClockAnchor: anchor},
		"live with draw offer": {FEN: fen, Ply: 7, Turn: 1, Phase: game.PhaseLive,
			RemainingMs: []int64{250000, -5}, LastMoveUCI: "g1f3", DrawOfferBy: 0, ClockAnchor: anchor},
		"ended": {FEN: fen, Ply: 42, Turn: 1, Phase: game.PhaseEnded, RemainingMs: []int64{120000, 0},
			Ended: true, Terminal: game.TermResign, LastMoveUCI: "d1h5",
			ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, DrawOfferBy: 1, ClockAnchor: anchor},
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
	// No clock runs before the first move: the ply-0 anchor is session
	// construction, not a real clock start.
	if z := snapshotToStateBody(cases["live at ply 0"], 0); z.Status != codec.StatusLive || z.RunningSeat != codec.NoSeat {
		t.Fatalf("ply-0 LIVE must report no running clock: %+v", z)
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
