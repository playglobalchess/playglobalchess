// Package store is the GS-4 durable event store (ADR-10, ADR-16): a DynamoDB
// implementation of game.EventStore. It replaces the GS-1 in-memory MemStore so
// game truth survives process/node restart (NFR-2), moves persist before they
// ack (NFR-7), and every game is fully replayable (NFR-9).
//
// Table `game_events` (ADR-16), one item per event:
//
//	stream_id        S  (partition key)  -- "game/{uuid}"
//	seq              N  (sort key)       -- 0-based, strictly increasing per stream
//	event_type       N                   -- the §4A event type code
//	encoding_version N                   -- the codec version `payload` is encoded with (NFR-9)
//	payload          B                   -- the full canonical §4A event frame (Q-GS4a-2 / Q-GS4B)
//	committed_at     S                   -- RFC-3339 commit timestamp
//
// The (stream_id, seq) key is the ordering + idempotency guarantee: a write is a
// conditional PutItem (attribute_not_exists(seq)), so an append at an occupied
// seq is REJECTED with game.ErrSeqConflict, never silently reordered. Reads are
// a single Query on the partition in seq order.
//
// §4A encoding lives entirely in Rust via the codec seam (NFR-5): this package
// calls codec.EncodeEvent / codec.DecodeEvent and never hand-encodes a §4A byte.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// currentEncodingVersion is the codec version GS-4 writes on every item. A codec
// evolution adds a new value and a decode branch; existing items keep their own
// version and stay replayable (NFR-9). The §4A event frame is produced by the
// codec seam, so this tracks that seam's version, pinned at 1 for v1.
const currentEncodingVersion = 1

// tableName is the DynamoDB table (ADR-16). Created via IaC, not in-app.
const tableName = "game_events"

// DynamoAPI is the subset of the DynamoDB client this store uses. An interface
// so tests can run against DynamoDB Local (a real client pointed at the local
// endpoint) or a fake — the production path passes a real *dynamodb.Client.
type DynamoAPI interface {
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
}

// Store is a DynamoDB-backed game.EventStore.
type Store struct {
	db  DynamoAPI
	now func() time.Time // injectable for deterministic committed_at in tests
}

// Compile-time proof Store satisfies the ratified interface.
var _ game.EventStore = (*Store)(nil)

// New builds a Store over the given client and validates that the table exists,
// so the server fails fast rather than serving games it cannot persist
// (SPEC-GS4 §5 "DB unavailable at startup").
func New(ctx context.Context, db DynamoAPI) (*Store, error) {
	if db == nil {
		return nil, errors.New("store: nil DynamoDB client")
	}
	if _, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	}); err != nil {
		return nil, fmt.Errorf("store: table %q not reachable: %w", tableName, err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// item builds the DynamoDB attribute map for one event at seq, encoding the
// event to its canonical §4A frame via the codec seam (Rust owns §4A, NFR-5).
func (s *Store) item(streamID string, seq uint32, ev game.Event) (map[string]ddbtypes.AttributeValue, error) {
	// One wall-clock read so the frame's embedded TS and committed_at agree
	// (they describe the same commit instant — the durable record stays
	// self-consistent).
	now := s.now()
	frame, err := codec.EncodeEvent(codec.EventHeader{
		StreamID:  streamID,
		Seq:       seq,
		SchemaVer: currentEncodingVersion,
		TS:        uint64(now.UnixMilli()),
	}, ev)
	if err != nil {
		return nil, fmt.Errorf("store: encode event: %w", err)
	}
	return map[string]ddbtypes.AttributeValue{
		"stream_id":        &ddbtypes.AttributeValueMemberS{Value: streamID},
		"seq":              &ddbtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", seq)},
		"event_type":       &ddbtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", ev.Type)},
		"encoding_version": &ddbtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", currentEncodingVersion)},
		"payload":          &ddbtypes.AttributeValueMemberB{Value: frame},
		"committed_at":     &ddbtypes.AttributeValueMemberS{Value: now.UTC().Format(time.RFC3339Nano)},
	}, nil
}

// Append durably commits one event at expectedSeq via a conditional PutItem.
// The condition attribute_not_exists(seq) makes the (stream_id, seq) write
// succeed only if that seq is unoccupied; a conflict maps to game.ErrSeqConflict
// (NFR-7 persist-before-ack: returns only after the committed put, or an error
// and no write).
func (s *Store) Append(ctx context.Context, streamID string, expectedSeq uint32, ev game.Event) error {
	it, err := s.item(streamID, expectedSeq, ev)
	if err != nil {
		return err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(tableName),
		Item:                it,
		ConditionExpression: aws.String("attribute_not_exists(seq)"),
	})
	if err != nil {
		var cc *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &cc) {
			return fmt.Errorf("store: seq %d occupied on %s: %w", expectedSeq, streamID, game.ErrSeqConflict)
		}
		return fmt.Errorf("store: put: %w", err)
	}
	return nil
}

// AppendBatch atomically commits evs at consecutive seqs from expectedSeq via a
// single TransactWriteItems — all-or-nothing (SPEC-GS4 §3.4 atomic terminal). An
// empty slice is a no-op. Each item carries its own attribute_not_exists(seq)
// condition, so a cancelled transaction (any slot occupied) surfaces
// game.ErrSeqConflict and commits nothing.
func (s *Store) AppendBatch(ctx context.Context, streamID string, expectedSeq uint32, evs []game.Event) error {
	if len(evs) == 0 {
		return nil
	}
	if len(evs) > 100 {
		// TransactWriteItems hard-caps at 100 items; an ending is 2, so this is a
		// guardrail, not a limit we expect to hit.
		return fmt.Errorf("store: batch of %d exceeds the 100-item transaction limit", len(evs))
	}
	writes := make([]ddbtypes.TransactWriteItem, 0, len(evs))
	for i, ev := range evs {
		it, err := s.item(streamID, expectedSeq+uint32(i), ev)
		if err != nil {
			return err
		}
		writes = append(writes, ddbtypes.TransactWriteItem{
			Put: &ddbtypes.Put{
				TableName:           aws.String(tableName),
				Item:                it,
				ConditionExpression: aws.String("attribute_not_exists(seq)"),
			},
		})
	}
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: writes,
	})
	if err != nil {
		var tc *ddbtypes.TransactionCanceledException
		if errors.As(err, &tc) {
			// A conditional-check failure inside the transaction means some seq was
			// occupied — the all-or-nothing batch lost a race. Surface as the typed
			// conflict so the session reconciles.
			for _, r := range tc.CancellationReasons {
				if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
					return fmt.Errorf("store: batch seq from %d occupied on %s: %w", expectedSeq, streamID, game.ErrSeqConflict)
				}
			}
		}
		return fmt.Errorf("store: transact: %w", err)
	}
	return nil
}

// Load returns the full ordered event slice for a stream (seq 0..n), decoding
// each item's §4A payload back to a game.Event via the codec seam, dispatching
// on encoding_version (an unknown version is a hard error — NFR-9 fidelity over
// best-effort). An unknown stream returns an empty slice and nil error.
func (s *Store) Load(ctx context.Context, streamID string) ([]game.Event, error) {
	var out []game.Event
	var startKey map[string]ddbtypes.AttributeValue
	for {
		q, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(tableName),
			KeyConditionExpression: aws.String("stream_id = :sid"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":sid": &ddbtypes.AttributeValueMemberS{Value: streamID},
			},
			ScanIndexForward:  aws.Bool(true), // seq ascending
			ExclusiveStartKey: startKey,
			// Strongly consistent: every Load decides game state (rebuild, the
			// seq-conflict reload, and the re-read that confirms an ambiguous
			// commit, #89). An eventually consistent read can miss the write
			// that just landed and decide on a stale game.
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			return nil, fmt.Errorf("store: query %s: %w", streamID, err)
		}
		for _, raw := range q.Items {
			ev, err := decodeItem(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		if len(q.LastEvaluatedKey) == 0 {
			break
		}
		startKey = q.LastEvaluatedKey
	}
	return out, nil
}

// decodeItem turns one stored item back into a game.Event, enforcing the
// encoding_version dispatch (NFR-9) before decoding the §4A payload via the seam.
func decodeItem(raw map[string]ddbtypes.AttributeValue) (game.Event, error) {
	verAttr, ok := raw["encoding_version"].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return game.Event{}, errors.New("store: item missing encoding_version")
	}
	var ver int
	if _, err := fmt.Sscanf(verAttr.Value, "%d", &ver); err != nil {
		return game.Event{}, fmt.Errorf("store: unparseable encoding_version %q: %w", verAttr.Value, err)
	}
	if ver != currentEncodingVersion {
		// A future build adds a decode branch per known version; this build
		// refuses a version it cannot faithfully replay rather than misdecode.
		return game.Event{}, fmt.Errorf("store: unknown encoding_version %d (this build speaks %d) — refusing to misdecode (NFR-9)", ver, currentEncodingVersion)
	}
	payAttr, ok := raw["payload"].(*ddbtypes.AttributeValueMemberB)
	if !ok {
		return game.Event{}, errors.New("store: item missing payload bytes")
	}
	_, ev, err := codec.DecodeEvent(payAttr.Value)
	if err != nil {
		return game.Event{}, fmt.Errorf("store: decode event frame: %w", err)
	}
	return ev, nil
}
