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
	t.Run("invalid writes are refused and commit nothing", func(t *testing.T) {
		exp := time.Now().Add(time.Hour)
		put := func(tb game.IndexTable, key string) game.IndexOp {
			return game.IndexOp{Table: tb, Kind: game.IndexPut, Key: key, GameID: "g1", ExpiresAt: exp}
		}
		cases := map[string][]game.IndexOp{
			"two ops on one item (a user paired with themselves)": {put(game.IdxUserLive, "alice"), put(game.IdxUserLive, "alice")},
			"put then delete of one item":                         {put(game.IdxLive, "3"), {Table: game.IdxLive, Kind: game.IndexDelete, Key: "3", GameID: "g1"}},
			"non-numeric shard":                                   {put(game.IdxLive, "three")},
			"update outside live_games":                           {{Table: game.IdxUserLive, Kind: game.IndexUpdate, Key: "alice", GameID: "g1"}},
			"recent put without expiry":                           {{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: "alice", GameID: "g1"}},
			"empty key":                                           {put(game.IdxUserLive, "")},
			"unknown table":                                       {put("nope", "alice")},
			"unknown kind":                                        {{Table: game.IdxUserLive, Kind: 99, Key: "alice", GameID: "g1"}},
		}
		for name, ops := range cases {
			s := mk(t)
			err := s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, ops)
			if !errors.Is(err, game.ErrBadIndexOp) {
				t.Errorf("%s: want ErrBadIndexOp, got %v", name, err)
			}
			if evs, _ := s.Load(ctx, "game/g1"); len(evs) != 0 {
				t.Errorf("%s: events committed", name)
			}
		}
	})
	t.Run("the transaction item cap is exact", func(t *testing.T) {
		ops := func(n int) []game.IndexOp {
			out := make([]game.IndexOp, n)
			for i := range out {
				out[i] = game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "alice", GameID: fmt.Sprintf("g%03d", i)}
			}
			return out
		}
		s := mk(t)
		if err := s.AppendIndexed(ctx, "game/a", 0, []game.Event{gameCreated()}, ops(99)); err != nil {
			t.Fatalf("1 event + 99 ops at seq 0 is 100 items: %v", err)
		}
		if err := s.AppendIndexed(ctx, "game/b", 0, []game.Event{gameCreated()}, ops(100)); !errors.Is(err, game.ErrBadIndexOp) {
			t.Fatalf("101 items: want ErrBadIndexOp, got %v", err)
		}
		// Above seq 0 the head guard takes one item.
		if err := s.AppendIndexed(ctx, "game/a", 1, []game.Event{moveEv(1, 0, "e2e4")}, ops(98)); err != nil {
			t.Fatalf("guard + 1 event + 98 ops is 100 items: %v", err)
		}
		if err := s.AppendIndexed(ctx, "game/a", 2, []game.Event{moveEv(2, 1, "e7e5")}, ops(99)); !errors.Is(err, game.ErrBadIndexOp) {
			t.Fatalf("guard + 1 + 99 is 101 items: want ErrBadIndexOp, got %v", err)
		}
	})
	t.Run("delete of a missing entry is fine and a put replaces", func(t *testing.T) {
		s := mk(t)
		del := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexDelete, Key: "alice", GameID: "nope"}
		if err := s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, []game.IndexOp{del}); err != nil {
			t.Fatal(err)
		}
		a := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "alice", GameID: "g1", Seat: 0}
		b := a
		b.Seat = 1
		_ = s.AppendIndexed(ctx, "game/g1", 1, []game.Event{moveEv(1, 0, "e2e4")}, []game.IndexOp{a})
		_ = s.AppendIndexed(ctx, "game/g1", 2, []game.Event{moveEv(2, 1, "e7e5")}, []game.IndexOp{b})
		if ug, _ := s.UserLiveGames(ctx, "alice", 8); len(ug) != 1 || ug[0].Seat != 1 {
			t.Fatalf("put did not replace: %+v", ug)
		}
	})
	t.Run("the limit applies after expired rows are dropped", func(t *testing.T) {
		s := mk(t)
		now := time.Now()
		rec := func(gid string, exp time.Time) game.IndexOp {
			return game.IndexOp{Table: game.IdxUserRecent, Kind: game.IndexPut, Key: "alice", GameID: gid, EndedAt: now, ExpiresAt: exp}
		}
		ops := []game.IndexOp{rec("g1", now.Add(time.Hour)), rec("g2", now.Add(-time.Hour)), rec("g3", now.Add(-time.Hour))}
		if err := s.AppendIndexed(ctx, "game/g1", 0, []game.Event{gameCreated()}, ops); err != nil {
			t.Fatal(err)
		}
		got, _ := s.UserRecentGames(ctx, "alice", 1)
		if len(got) != 1 || got[0].GameID != "g1" {
			t.Fatalf("expired newer rows hid the valid older one: %+v", got)
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
