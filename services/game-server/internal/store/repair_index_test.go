package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// runRepairContract: RepairIndex applies an ended game's EndingOps with no
// events, so a stale live row goes and each player gets a recent row; it leaves
// the stream alone, is idempotent, and refuses Update and an empty write.
func runRepairContract(t *testing.T, mk storeMaker) {
	ctx := context.Background()
	cfg := game.IndexConfig{Shards: 4, NodeID: "n1", Incarnation: "i1", RecentTTL: time.Hour}
	gid := "018f0000-0000-7000-8000-00000000bbbb"
	sid := game.StreamIDFor(gid)
	players := []string{"alice", "bob"}
	s := mk(t)
	openOps, err := cfg.OpeningOps(gid, players)
	if err != nil {
		t.Fatal(err)
	}
	open := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{PoolID: "p", Seats: []game.Seat{{SeatIdx: 0}, {SeatIdx: 1, Color: 1}}, StartFEN: "x", TimeControl: "blitz-5+3"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "alice"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "bob"}},
	}
	if err := s.AppendIndexed(ctx, sid, 0, open, openOps); err != nil {
		t.Fatalf("opening: %v", err)
	}
	// The ending written WITHOUT its rows, as persist's fallback does.
	if err := s.AppendBatch(ctx, sid, 3, endBatch()); err != nil {
		t.Fatalf("plain ending: %v", err)
	}
	before := len(rd(t, s).load(ctx, sid))
	if before == 0 {
		t.Fatal("no events read back")
	}
	ops, err := cfg.EndingOps(gid, players, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second run is a no-op
		if err := s.RepairIndex(ctx, ops); err != nil {
			t.Fatalf("repair %d: %v", i+1, err)
		}
	}
	if l := rd(t, s).live(ctx, game.IndexShard(gid, cfg.Shards)); len(l) != 0 {
		t.Fatalf("live_games after the repair: %+v", l)
	}
	for seat, u := range players {
		if r := rd(t, s).userLive(ctx, u, 5); len(r) != 0 {
			t.Fatalf("%s live rows after the repair: %+v", u, r)
		}
		if r := rd(t, s).userRecent(ctx, u, 5); len(r) != 1 || r[0].GameID != gid || int(r[0].Seat) != seat {
			t.Fatalf("%s recent rows after the repair: %+v", u, r)
		}
	}
	if after := len(rd(t, s).load(ctx, sid)); after != before {
		t.Fatalf("the repair changed the stream: %d events, was %d", after, before)
	}
	if err := s.RepairIndex(ctx, nil); !errors.Is(err, game.ErrBadIndexOp) {
		t.Fatalf("empty repair: %v, want ErrBadIndexOp", err)
	}
	upd := game.IndexOp{Table: game.IdxLive, Kind: game.IndexUpdate, Key: "0", GameID: gid, OwnerNode: "x", OwnerIncarnation: "y"}
	if err := s.RepairIndex(ctx, []game.IndexOp{upd}); !errors.Is(err, game.ErrBadIndexOp) {
		t.Fatalf("Update in a repair: %v, want ErrBadIndexOp", err)
	}
}

func TestRepairContractMemStore(t *testing.T)    { runRepairContract(t, memMaker) }
func TestRepairContractDynamoStore(t *testing.T) { runRepairContract(t, dynMaker) }
