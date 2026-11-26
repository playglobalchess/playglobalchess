package store

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// cancelWith makes every transaction fail with the given per-item reasons, the
// way DynamoDB reports a cancellation.
type cancelWith struct {
	DynamoAPI
	codes []string
}

func (c cancelWith) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	rs := make([]ddbtypes.CancellationReason, len(c.codes))
	for i, code := range c.codes {
		rs[i] = ddbtypes.CancellationReason{Code: aws.String(code)}
	}
	return nil, &ddbtypes.TransactionCanceledException{CancellationReasons: rs}
}

func updateOp() game.IndexOp {
	return game.IndexOp{Table: game.IdxLive, Kind: game.IndexUpdate, Key: "3", GameID: "g1", OwnerNode: "n", OwnerIncarnation: "i"}
}

// Cancellation reasons map exactly: items in order are guard, one event, one
// Update. Only ConditionalCheckFailed maps, a seq failure wins over a missing
// Update, and throttling or conflicts are never reported as either sentinel.
func TestCancellationReasonMapping(t *testing.T) {
	cases := []struct {
		name  string
		codes []string
		want  error // nil means: neither sentinel
	}{
		{"guard failed", []string{"ConditionalCheckFailed", "None", "None"}, game.ErrSeqConflict},
		{"event failed", []string{"None", "ConditionalCheckFailed", "None"}, game.ErrSeqConflict},
		{"update missing", []string{"None", "None", "ConditionalCheckFailed"}, game.ErrIndexMissing},
		{"seq wins over missing update", []string{"None", "ConditionalCheckFailed", "ConditionalCheckFailed"}, game.ErrSeqConflict},
		{"throttled", []string{"None", "ThrottlingError", "None"}, nil},
		{"transaction conflict", []string{"TransactionConflict", "None", "None"}, nil},
		{"validation", []string{"None", "None", "ValidationError"}, nil},
	}
	for _, c := range cases {
		s, f := newTestStore(t)
		s.db = cancelWith{DynamoAPI: f, codes: c.codes}
		err := s.AppendIndexed(context.Background(), "game/g1", 1, []game.Event{moveEv(1, 0, "e2e4")}, []game.IndexOp{updateOp()})
		if c.want == nil {
			if errors.Is(err, game.ErrSeqConflict) || errors.Is(err, game.ErrIndexMissing) || err == nil {
				t.Errorf("%s: got %v, want a plain transact error", c.name, err)
			}
			continue
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// The Update must carry its existence condition: real DynamoDB Update upserts a
// missing item without it, which would turn ErrIndexMissing into a silent insert.
func TestIndexUpdateCarriesItsExistenceCondition(t *testing.T) {
	s, f := newTestStore(t)
	err := s.AppendIndexed(context.Background(), "game/g1", 0, []game.Event{gameCreated()}, []game.IndexOp{updateOp()})
	if !errors.Is(err, game.ErrIndexMissing) {
		t.Fatalf("want ErrIndexMissing, got %v", err)
	}
	if len(f.idxPart("live_games", "3")) != 0 {
		t.Fatal("a missing entry was upserted")
	}
}

// A malformed hint row is skipped, never turned into a plausible entry.
func TestMalformedIndexRowsAreSkipped(t *testing.T) {
	s, f := newTestStore(t)
	bad := map[string]ddbtypes.AttributeValue{
		"user_id": &ddbtypes.AttributeValueMemberS{Value: "alice"},
		"game_id": &ddbtypes.AttributeValueMemberS{Value: "gbad"}, // no seat
	}
	f.idxPart("user_live_games", "alice")["gbad"] = bad
	good := game.IndexOp{Table: game.IdxUserLive, Kind: game.IndexPut, Key: "alice", GameID: "ggood", Seat: 1}
	if err := s.AppendIndexed(context.Background(), "game/g1", 0, []game.Event{gameCreated()}, []game.IndexOp{good}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserLiveGames(context.Background(), "alice", 8)
	if len(got) != 1 || got[0].GameID != "ggood" {
		t.Fatalf("got %+v", got)
	}
	f.idxPart("live_games", "1")["x"] = map[string]ddbtypes.AttributeValue{
		"shard": &ddbtypes.AttributeValueMemberN{Value: "1"}, "game_id": &ddbtypes.AttributeValueMemberS{Value: "x"},
	}
	if live, _ := s.LiveGames(context.Background(), 1); len(live) != 0 {
		t.Fatalf("a row with no owner was returned: %+v", live)
	}
}
