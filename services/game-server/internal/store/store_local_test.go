package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Integration test against a REAL DynamoDB engine (DynamoDB Local), gated on
// DYNAMODB_LOCAL_ENDPOINT so it runs in CI (where the container is up) and skips
// locally. The in-process fake (store_test.go) proves the store's Go logic
// everywhere; THIS proves the same contract against actual DynamoDB
// conditional-put + TransactWriteItems semantics — the §8 "real store instance"
// requirement (Q-GS4D). The two together are the full gate.
//
// CI brings up amazon/dynamodb-local, sets DYNAMODB_LOCAL_ENDPOINT, and this
// test creates the game_events table itself (so it is self-contained).
func dynamoLocal(t *testing.T) *dynamodb.Client {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		t.Skip("DYNAMODB_LOCAL_ENDPOINT not set — skipping real-DynamoDB integration (runs in CI)")
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		// DynamoDB Local accepts any credentials.
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(
			func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "local", SecretAccessKey: "local"}, nil
			})),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

// ensureTable creates game_events (idempotently) so the integration test is
// self-contained — mirrors the IaC table definition (ADR-16): stream_id (S)
// partition key, seq (N) sort key, on-demand billing.
func ensureTable(t *testing.T, db *dynamodb.Client) {
	t.Helper()
	ctx := context.Background()
	_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(tableName),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("stream_id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("seq"), AttributeType: ddbtypes.ScalarAttributeTypeN},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("stream_id"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("seq"), KeyType: ddbtypes.KeyTypeRange},
		},
	})
	if err != nil {
		var inUse *ddbtypes.ResourceInUseException
		if !errors.As(err, &inUse) {
			t.Fatalf("create table: %v", err)
		}
	}
}

func TestIntegration_RealDynamo(t *testing.T) {
	db := dynamoLocal(t)
	ensureTable(t, db)
	ctx := context.Background()
	s, err := New(ctx, db)
	if err != nil {
		t.Fatalf("New against DynamoDB Local: %v", err)
	}
	sid := "game/integ-" + t.Name()

	// Round-trip + durability: append, build a fresh Store (new handle),
	// Load — the stream survives (NFR-2).
	seed := []game.Event{gameCreated(), moveEv(1, 0, "e2e4"), moveEv(2, 1, "e7e5")}
	for i, ev := range seed {
		if err := s.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	s2, err := New(ctx, db) // fresh handle = the "reconnect" case
	if err != nil {
		t.Fatalf("reconnect New: %v", err)
	}
	got, err := s2.Load(ctx, sid)
	if err != nil {
		t.Fatalf("load after reconnect: %v", err)
	}
	if len(got) != len(seed) {
		t.Fatalf("loaded %d, want %d", len(got), len(seed))
	}

	// REAL conditional-put conflict: a second append at seq 0 must be rejected by
	// DynamoDB's own attribute_not_exists condition, mapped to ErrSeqConflict.
	if err := s.Append(ctx, sid, 0, moveEv(9, 0, "a2a3")); !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("real conditional-put conflict: want ErrSeqConflict, got %v", err)
	}

	// REAL atomic terminal: TransactWriteItems commits both ending events or
	// neither. First, a conflicting batch (seq 0 occupied) commits nothing.
	end := []game.Event{
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, Termination: game.TermFlag,
		}},
	}
	before, _ := s.Load(ctx, sid)
	if err := s.AppendBatch(ctx, sid, 0, end); !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("real transact conflict: want ErrSeqConflict, got %v", err)
	}
	after, _ := s.Load(ctx, sid)
	if len(after) != len(before) {
		t.Fatalf("cancelled transaction mutated the stream: %d -> %d", len(before), len(after))
	}
	// Then the ending lands cleanly at the true next seq.
	if err := s.AppendBatch(ctx, sid, uint32(len(seed)), end); err != nil {
		t.Fatalf("ending batch: %v", err)
	}
	fin, _ := s.Load(ctx, sid)
	if len(fin) != len(seed)+2 {
		t.Fatalf("after ending len %d, want %d", len(fin), len(seed)+2)
	}
	if fin[len(fin)-1].Type != game.EvGameEnded {
		t.Fatalf("last event type %d, want GameEnded", fin[len(fin)-1].Type)
	}
}
