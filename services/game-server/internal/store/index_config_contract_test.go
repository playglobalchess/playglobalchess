package store

import (
	"context"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// The ops that game.IndexConfig builds for a real opening and a real ending
// commit on both stores, including the ending that is a lone GameEnded (the
// half-terminal repair), which is a single event written behind a head guard.
func runIndexConfigContract(t *testing.T, mk storeMaker) {
	ctx := context.Background()
	cfg := game.IndexConfig{Shards: 4, NodeID: "n1", Incarnation: "i1", RecentTTL: time.Hour}
	players := []string{"alice", "bob"}
	open := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{PoolID: "p", Seats: []game.Seat{{SeatIdx: 0}, {SeatIdx: 1, Color: 1}}, StartFEN: "x", TimeControl: "blitz-5+3"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "alice"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "bob"}},
	}
	lone := []game.Event{{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
		ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, Termination: game.TermFlag}}}

	for name, ending := range map[string][]game.Event{"batch ending": endBatch(), "lone GameEnded": lone} {
		t.Run(name, func(t *testing.T) {
			s := mk(t)
			gid := "018f0000-0000-7000-8000-00000000aaaa"
			sid := game.StreamIDFor(gid)
			openOps, err := cfg.OpeningOps(gid, players)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AppendIndexed(ctx, sid, 0, open, openOps); err != nil {
				t.Fatalf("opening: %v", err)
			}
			shard := game.IndexShard(gid, cfg.Shards)
			if l := rd(t, s).live(ctx, shard); len(l) != 1 || l[0].OwnerNode != "n1" {
				t.Fatalf("live_games after the opening: %+v", l)
			}
			endOps, err := cfg.EndingOps(gid, players, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AppendIndexed(ctx, sid, 3, ending, endOps); err != nil {
				t.Fatalf("ending: %v", err)
			}
			if l := rd(t, s).live(ctx, shard); len(l) != 0 {
				t.Fatalf("live_games after the ending: %+v", l)
			}
			for seat, u := range players {
				if r := rd(t, s).userLive(ctx, u, 5); len(r) != 0 {
					t.Fatalf("%s live rows after the ending: %+v", u, r)
				}
				r := rd(t, s).userRecent(ctx, u, 5)
				if len(r) != 1 || r[0].GameID != gid || int(r[0].Seat) != seat {
					t.Fatalf("%s recent rows: %+v, want %s at seat %d", u, r, gid, seat)
				}
			}
			// An ending written twice (a retry after an ambiguous commit) is a
			// seq conflict, and changes nothing.
			if err := s.AppendIndexed(ctx, sid, 3, ending, endOps); err == nil {
				t.Fatal("a second ending at the same seq was accepted")
			}
		})
	}
}

func TestIndexConfigContractMemStore(t *testing.T)    { runIndexConfigContract(t, memMaker) }
func TestIndexConfigContractDynamoStore(t *testing.T) { runIndexConfigContract(t, dynMaker) }
