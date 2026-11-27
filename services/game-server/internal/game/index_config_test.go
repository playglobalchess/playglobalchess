package game

import (
	"errors"
	"testing"
	"time"
)

var testIdx = IndexConfig{Shards: 8, NodeID: "node-a", Incarnation: "inc-1", RecentTTL: time.Hour}

func TestIndexConfigValidate(t *testing.T) {
	if err := testIdx.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := DefaultIndexConfig("n", "i").Validate(); err != nil {
		t.Fatalf("default config: %v", err)
	}
	for name, c := range map[string]IndexConfig{
		"no shards": {NodeID: "n", RecentTTL: time.Hour},
		"no node":   {Shards: 4, RecentTTL: time.Hour},
		"no ttl":    {Shards: 4, NodeID: "n"},
	} {
		if err := c.Validate(); !errors.Is(err, ErrBadIndexConfig) {
			t.Errorf("%s: %v, want ErrBadIndexConfig", name, err)
		}
	}
}

func TestOpeningOps(t *testing.T) {
	ops, err := testIdx.OpeningOps("g1", []string{"alice", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("%d ops, want 2 user rows and 1 live row: %+v", len(ops), ops)
	}
	for seat, u := range []string{"alice", "bob"} {
		op := ops[seat]
		if op.Table != IdxUserLive || op.Kind != IndexPut || op.Key != u || op.GameID != "g1" || int(op.Seat) != seat {
			t.Errorf("user row %d = %+v", seat, op)
		}
	}
	live := ops[2]
	if live.Table != IdxLive || live.Kind != IndexPut || live.Key != ShardKey(IndexShard("g1", 8)) ||
		live.OwnerNode != "node-a" || live.OwnerIncarnation != "inc-1" {
		t.Errorf("live row = %+v", live)
	}
	if err := ValidateIndexedWrite(0, 3, ops); err != nil {
		t.Errorf("the ops the opening writes fail the store's own validation: %v", err)
	}
}

func TestOpeningOpsRefuseEmptyAndRepeatedUsers(t *testing.T) {
	for name, players := range map[string][]string{
		"empty seat": {"alice", ""},
		"same user":  {"alice", "alice"},
	} {
		if _, err := testIdx.OpeningOps("g1", players); !errors.Is(err, ErrBadIndexOp) {
			t.Errorf("%s: %v, want ErrBadIndexOp", name, err)
		}
	}
}

func TestEndingOps(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	ops, err := testIdx.EndingOps("g1", []string{"alice", "bob"}, at)
	if err != nil {
		t.Fatal(err)
	}
	var del, put, live int
	for _, op := range ops {
		switch {
		case op.Table == IdxUserLive && op.Kind == IndexDelete:
			del++
		case op.Table == IdxUserRecent && op.Kind == IndexPut:
			put++
			if !op.EndedAt.Equal(at) || !op.ExpiresAt.Equal(at.Add(time.Hour)) {
				t.Errorf("recent row times = %v / %v", op.EndedAt, op.ExpiresAt)
			}
		case op.Table == IdxLive && op.Kind == IndexDelete && op.Key == ShardKey(IndexShard("g1", 8)):
			live++
		default:
			t.Errorf("unexpected op %+v", op)
		}
	}
	if del != 2 || put != 2 || live != 1 {
		t.Fatalf("deletes=%d recent puts=%d live deletes=%d, want 2/2/1", del, put, live)
	}
	if err := ValidateIndexedWrite(5, 1, ops); err != nil {
		t.Errorf("the ending's ops fail the store's own validation: %v", err)
	}
}

// A game must always be able to end: a seat with no user (a game from before the
// index, or a malformed stream) is skipped, never an error.
func TestEndingOpsSkipEmptySeatsAndRepeats(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	ops, err := testIdx.EndingOps("g1", []string{"alice", ""}, at)
	if err != nil {
		t.Fatalf("an empty seat failed the ending: %v", err)
	}
	if len(ops) != 3 { // alice's delete and recent put, and the live delete
		t.Fatalf("%d ops, want 3: %+v", len(ops), ops)
	}
	ops, err = testIdx.EndingOps("g1", []string{"alice", "alice"}, at)
	if err != nil || len(ops) != 3 {
		t.Fatalf("a repeated user: %d ops, err %v; want one set of rows", len(ops), err)
	}
	if err := ValidateIndexedWrite(5, 1, ops); err != nil {
		t.Errorf("repeated-user ending ops are refused by the store: %v", err)
	}
}

func TestHasEnding(t *testing.T) {
	if HasEnding([]Event{{Type: EvMovePlayed}}) {
		t.Error("a move is not an ending")
	}
	if !HasEnding([]Event{{Type: EvMovePlayed}, {Type: EvGameEnded}}) {
		t.Error("a batch with GameEnded is an ending")
	}
	if HasEnding(nil) {
		t.Error("no events is not an ending")
	}
}
