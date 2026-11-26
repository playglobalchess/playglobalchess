package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// The index contract (GS-8 §4.3) is run against BOTH stores, so MemStore and the
// DynamoDB store cannot drift: atomicity with the events, conflict mapping,
// contiguity, ordering and TTL filtering.

type storeMaker func(t *testing.T) game.IndexedStore

func memMaker(t *testing.T) game.IndexedStore { return game.NewMemStore() }

func dynMaker(t *testing.T) game.IndexedStore {
	s, _ := newTestStore(t)
	s.now = time.Now // TTL filtering compares against the real clock
	return s
}

func openingOps(gid, white, black string) []game.IndexOp {
	return []game.IndexOp{
		{Table: game.IdxUserLive, Kind: game.IndexPut, Key: white, GameID: gid, Seat: 0},
		{Table: game.IdxUserLive, Kind: game.IndexPut, Key: black, GameID: gid, Seat: 1},
		{Table: game.IdxLive, Kind: game.IndexPut, Key: game.ShardKey(3), GameID: gid, OwnerNode: "n1", OwnerIncarnation: "i1"},
	}
}

func endingOps(gid, white, black string, exp time.Time) []game.IndexOp {
	return []game.IndexOp{
		{Table: game.IdxUserLive, Kind: game.IndexDelete, Key: white, GameID: gid},
		{Table: game.IdxUserLive, Kind: game.IndexDelete, Key: black, GameID: gid},
		{Table: game.IdxLive, Kind: game.IndexDelete, Key: game.ShardKey(3), GameID: gid},
		{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: white, GameID: gid, Seat: 0, EndedAt: exp.Add(-time.Hour), ExpiresAt: exp},
		{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: black, GameID: gid, Seat: 1, EndedAt: exp.Add(-time.Hour), ExpiresAt: exp},
	}
}

func endBatch() []game.Event {
	return []game.Event{
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, Termination: game.TermFlag}},
	}
}

func runIndexContract(t *testing.T, mk storeMaker) {
	ctx := context.Background()
	t.Run("opening writes events and index together", func(t *testing.T) {
		s := mk(t)
		if err := s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "alice", "bob")); err != nil {
			t.Fatal(err)
		}
		if evs, _ := s.Load(ctx, "game/g1"); len(evs) != 1 {
			t.Fatalf("events = %d", len(evs))
		}
		live, _ := s.LiveGames(ctx, 3)
		if len(live) != 1 || live[0].GameID != "g1" || live[0].OwnerNode != "n1" || live[0].OwnerIncarnation != "i1" {
			t.Fatalf("live = %+v", live)
		}
		ug, _ := s.UserLiveGames(ctx, "bob", 8)
		if len(ug) != 1 || ug[0].GameID != "g1" || ug[0].Seat != 1 {
			t.Fatalf("bob = %+v", ug)
		}
	})
	t.Run("seq conflict commits no index", func(t *testing.T) {
		s := mk(t)
		_ = s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "alice", "bob"))
		err := s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g2", "alice", "carol"))
		if !errors.Is(err, game.ErrSeqConflict) {
			t.Fatalf("want ErrSeqConflict, got %v", err)
		}
		if ug, _ := s.UserLiveGames(ctx, "carol", 8); len(ug) != 0 {
			t.Fatalf("a rejected write left index rows: %+v", ug)
		}
	})
	t.Run("a write ahead of the head is rejected (contiguity)", func(t *testing.T) {
		s := mk(t)
		_ = s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, nil)
		if err := s.AppendBatch(ctx, "game/g1", 5, endBatch()); !errors.Is(err, game.ErrSeqConflict) {
			t.Fatalf("AppendBatch gap: want ErrSeqConflict, got %v", err)
		}
		if err := s.AppendIndexed(ctx, "game/g1", 5, endBatch(), nil); !errors.Is(err, game.ErrSeqConflict) {
			t.Fatalf("AppendIndexed gap: want ErrSeqConflict, got %v", err)
		}
		if evs, _ := s.Load(ctx, "game/g1"); len(evs) != 1 {
			t.Fatalf("a gap write committed: %d events", len(evs))
		}
	})
	t.Run("ending moves a game from live to recent atomically", func(t *testing.T) {
		s := mk(t)
		_ = s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "alice", "bob"))
		exp := time.Now().Add(24 * time.Hour)
		if err := s.AppendIndexed(ctx, "game/g1", 1, endBatch(), endingOps("g1", "alice", "bob", exp)); err != nil {
			t.Fatal(err)
		}
		if live, _ := s.LiveGames(ctx, 3); len(live) != 0 {
			t.Fatalf("live = %+v", live)
		}
		if ug, _ := s.UserLiveGames(ctx, "alice", 8); len(ug) != 0 {
			t.Fatalf("alice live = %+v", ug)
		}
		rec, _ := s.UserRecentGames(ctx, "alice", 8)
		if len(rec) != 1 || rec[0].GameID != "g1" || rec[0].EndedAt.IsZero() {
			t.Fatalf("alice recent = %+v", rec)
		}
	})
	t.Run("expired recent entries are hidden", func(t *testing.T) {
		s := mk(t)
		_ = s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "alice", "bob"))
		past := time.Now().Add(-time.Minute)
		_ = s.AppendIndexed(ctx, "game/g1", 1, endBatch(), endingOps("g1", "alice", "bob", past))
		if rec, _ := s.UserRecentGames(ctx, "alice", 8); len(rec) != 0 {
			t.Fatalf("expired entry returned: %+v", rec)
		}
	})
	t.Run("update changes the owner, a missing entry commits nothing", func(t *testing.T) {
		s := mk(t)
		_ = s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "alice", "bob"))
		up := game.IndexOp{Table: game.IdxLive, Kind: game.IndexUpdate, Key: game.ShardKey(3), GameID: "g1", OwnerNode: "n2", OwnerIncarnation: "i9"}
		if err := s.AppendIndexed(ctx, "game/g1", 1, []game.Event{moveEv(1, 0, "e2e4")}, []game.IndexOp{up}); err != nil {
			t.Fatal(err)
		}
		live, _ := s.LiveGames(ctx, 3)
		if live[0].OwnerNode != "n2" || live[0].OwnerIncarnation != "i9" {
			t.Fatalf("owner not updated: %+v", live)
		}
		up.GameID = "ghost"
		err := s.AppendIndexed(ctx, "game/g1", 2, []game.Event{moveEv(2, 1, "e7e5")}, []game.IndexOp{up})
		if !errors.Is(err, game.ErrIndexMissing) {
			t.Fatalf("want ErrIndexMissing, got %v", err)
		}
		if evs, _ := s.Load(ctx, "game/g1"); len(evs) != 2 {
			t.Fatalf("events committed despite a failed update: %d", len(evs))
		}
	})
	t.Run("user lists are newest first and limited, all pages read", func(t *testing.T) {
		s := mk(t)
		for i := 1; i <= 5; i++ {
			gid := fmt.Sprintf("g%d", i)
			op := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "alice", GameID: gid}
			if err := s.AppendIndexed(ctx, "game/"+gid, 0, []game.Event{gameCreated()}, []game.IndexOp{op}); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := s.UserLiveGames(ctx, "alice", 0)
		if len(all) != 5 || all[0].GameID != "g5" || all[4].GameID != "g1" {
			t.Fatalf("all = %+v", all)
		}
		if two, _ := s.UserLiveGames(ctx, "alice", 2); len(two) != 2 || two[0].GameID != "g5" {
			t.Fatalf("limited = %+v", two)
		}
	})
}

func TestIndexContractMemStore(t *testing.T)    { runIndexContract(t, memMaker) }
func TestIndexContractDynamoStore(t *testing.T) { runIndexContract(t, dynMaker) }

func TestNewFailsWhenAnIndexTableIsMissing(t *testing.T) {
	for _, name := range []string{"user_live_games", "live_games", "user_recent_games"} {
		f := newFakeDynamo()
		f.missingTable = name
		if _, err := New(context.Background(), f); err == nil {
			t.Fatalf("New succeeded without table %s", name)
		}
	}
}

func TestIndexReadsAreStronglyConsistent(t *testing.T) {
	s, f := newTestStore(t)
	ctx := context.Background()
	_, _ = s.LiveGames(ctx, 1)
	_, _ = s.UserLiveGames(ctx, "alice", 8)
	_, _ = s.UserRecentGames(ctx, "alice", 8)
	if f.inconsistentQueries != 0 {
		t.Fatalf("%d index queries were not strongly consistent", f.inconsistentQueries)
	}
}

func TestIndexedWriteIsOneTransaction(t *testing.T) {
	s, f := newTestStore(t)
	if err := s.AppendIndexed(context.Background(), "game/g1", 0, []game.Event{gameCreated()}, openingOps("g1", "a", "b")); err != nil {
		t.Fatal(err)
	}
	if f.transactions != 1 {
		t.Fatalf("transactions = %d, want 1", f.transactions)
	}
}

func TestIndexedWriteOverTheItemLimitIsRefused(t *testing.T) {
	s, _ := newTestStore(t)
	ops := make([]game.IndexOp, 100)
	for i := range ops {
		ops[i] = game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "a", GameID: fmt.Sprint(i)}
	}
	if err := s.AppendIndexed(context.Background(), "game/g1", 0, []game.Event{gameCreated()}, ops); err == nil {
		t.Fatal("a 101-item transaction was not refused")
	}
}
