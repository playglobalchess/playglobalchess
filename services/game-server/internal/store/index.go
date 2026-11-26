package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// The live-game index tables (SPEC-S2-GAME-SERVER-GS8 §4.3, ADR-16 amendment).
// Hints only; the event stream stays the truth. Created via IaC like game_events.
//
//	user_live_games   user_id S (partition) / game_id S (sort)   seat N
//	live_games        shard N   (partition) / game_id S (sort)   owner_node S, owner_incarnation S
//	user_recent_games user_id S (partition) / game_id S (sort)   seat N, ended_at S, expires_at N (TTL)
var indexTables = []game.IndexTable{game.IdxUserLive, game.IdxLive, game.IdxUserRecent}

// maxTransactItems is the DynamoDB TransactWriteItems cap.
const maxTransactItems = 100

var _ game.IndexedStore = (*Store)(nil)

func pkName(t game.IndexTable) string {
	if t == game.IdxLive {
		return "shard"
	}
	return "user_id"
}

// pkValue is the partition key attribute: a number for live_games, else a string.
func pkValue(t game.IndexTable, key string) ddbtypes.AttributeValue {
	if t == game.IdxLive {
		return &ddbtypes.AttributeValueMemberN{Value: key}
	}
	return &ddbtypes.AttributeValueMemberS{Value: key}
}

func num(v int64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func str(v string) ddbtypes.AttributeValue { return &ddbtypes.AttributeValueMemberS{Value: v} }

func indexKey(op game.IndexOp) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		pkName(op.Table): pkValue(op.Table, op.Key),
		"game_id":        str(op.GameID),
	}
}

// indexWrite turns one IndexOp into a transaction item.
func indexWrite(op game.IndexOp) (ddbtypes.TransactWriteItem, error) {
	tn := aws.String(string(op.Table))
	switch op.Kind {
	case game.IndexPut:
		it := indexKey(op)
		switch op.Table {
		case game.IdxUserLive:
			it["seat"] = num(int64(op.Seat))
		case game.IdxLive:
			it["owner_node"] = str(op.OwnerNode)
			it["owner_incarnation"] = str(op.OwnerIncarnation)
		case game.IdxUserRecent:
			it["seat"] = num(int64(op.Seat))
			it["ended_at"] = str(op.EndedAt.UTC().Format(time.RFC3339Nano))
			it["expires_at"] = num(op.ExpiresAt.Unix())
		default:
			return ddbtypes.TransactWriteItem{}, fmt.Errorf("store: unknown index table %q", op.Table)
		}
		return ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{TableName: tn, Item: it}}, nil
	case game.IndexDelete:
		return ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{TableName: tn, Key: indexKey(op)}}, nil
	case game.IndexUpdate:
		if op.Table != game.IdxLive {
			return ddbtypes.TransactWriteItem{}, fmt.Errorf("store: IndexUpdate only applies to %s", game.IdxLive)
		}
		return ddbtypes.TransactWriteItem{Update: &ddbtypes.Update{
			TableName:           tn,
			Key:                 indexKey(op),
			UpdateExpression:    aws.String("SET owner_node = :n, owner_incarnation = :i"),
			ConditionExpression: aws.String("attribute_exists(game_id)"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":n": str(op.OwnerNode), ":i": str(op.OwnerIncarnation),
			},
		}}, nil
	}
	return ddbtypes.TransactWriteItem{}, fmt.Errorf("store: unknown index op kind %d", op.Kind)
}

// appendTx writes evs (and ops) in ONE TransactWriteItems. When expectedSeq is
// above zero it also checks that seq-1 exists, so a write can never leave a gap
// behind the head (contiguity, SPEC-S2-PROTOCOL §4A.8): a caller that is ahead
// of the stream gets ErrSeqConflict, like one that is behind it.
func (s *Store) appendTx(ctx context.Context, streamID string, expectedSeq uint32, evs []game.Event, ops []game.IndexOp) error {
	guard := 0
	if expectedSeq > 0 {
		guard = 1
	}
	if guard+len(evs)+len(ops) > maxTransactItems {
		return fmt.Errorf("store: write of %d items exceeds the %d-item transaction limit", guard+len(evs)+len(ops), maxTransactItems)
	}
	writes := make([]ddbtypes.TransactWriteItem, 0, guard+len(evs)+len(ops))
	if guard == 1 {
		writes = append(writes, ddbtypes.TransactWriteItem{ConditionCheck: &ddbtypes.ConditionCheck{
			TableName: aws.String(tableName),
			Key: map[string]ddbtypes.AttributeValue{
				"stream_id": str(streamID),
				"seq":       num(int64(expectedSeq) - 1),
			},
			ConditionExpression: aws.String("attribute_exists(seq)"),
		}})
	}
	for i, ev := range evs {
		it, err := s.item(streamID, expectedSeq+uint32(i), ev)
		if err != nil {
			return err
		}
		writes = append(writes, ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{
			TableName:           aws.String(tableName),
			Item:                it,
			ConditionExpression: aws.String("attribute_not_exists(seq)"),
		}})
	}
	eventItems := len(writes)
	for _, op := range ops {
		w, err := indexWrite(op)
		if err != nil {
			return err
		}
		writes = append(writes, w)
	}
	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes})
	if err == nil {
		return nil
	}
	var tc *ddbtypes.TransactionCanceledException
	if errors.As(err, &tc) {
		// The seq guard and event puts come first, so a failed condition at an
		// index below eventItems is a lost seq race; at or above it, an
		// IndexUpdate whose entry is missing. Nothing was committed either way.
		for i, r := range tc.CancellationReasons {
			if r.Code == nil || *r.Code != "ConditionalCheckFailed" {
				continue
			}
			if i < eventItems {
				return fmt.Errorf("store: seq %d not at the head of %s: %w", expectedSeq, streamID, game.ErrSeqConflict)
			}
			return fmt.Errorf("store: index entry missing on %s: %w", streamID, game.ErrIndexMissing)
		}
	}
	return fmt.Errorf("store: transact: %w", err)
}

// AppendIndexed commits evs and the index ops atomically (GS-8 §4.3).
func (s *Store) AppendIndexed(ctx context.Context, streamID string, expectedSeq uint32, evs []game.Event, ops []game.IndexOp) error {
	if len(evs) == 0 {
		return errors.New("store: AppendIndexed needs at least one event")
	}
	return s.appendTx(ctx, streamID, expectedSeq, evs, ops)
}

// queryIndex reads every page of one partition (strongly consistent: adoption
// and AUTHENTICATE decide on what they read) and returns the raw items.
func (s *Store) queryIndex(ctx context.Context, t game.IndexTable, key string, newestFirst bool) ([]map[string]ddbtypes.AttributeValue, error) {
	var out []map[string]ddbtypes.AttributeValue
	var start map[string]ddbtypes.AttributeValue
	for {
		q, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(string(t)),
			KeyConditionExpression:    aws.String(pkName(t) + " = :k"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":k": pkValue(t, key)},
			ScanIndexForward:          aws.Bool(!newestFirst),
			ExclusiveStartKey:         start,
			ConsistentRead:            aws.Bool(true),
		})
		if err != nil {
			return nil, fmt.Errorf("store: query %s/%s: %w", t, key, err)
		}
		out = append(out, q.Items...)
		if len(q.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = q.LastEvaluatedKey
	}
}

func strAttr(it map[string]ddbtypes.AttributeValue, name string) string {
	if v, ok := it[name].(*ddbtypes.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func numAttr(it map[string]ddbtypes.AttributeValue, name string) int64 {
	if v, ok := it[name].(*ddbtypes.AttributeValueMemberN); ok {
		n, _ := strconv.ParseInt(v.Value, 10, 64)
		return n
	}
	return 0
}

// LiveGames returns every live_games entry of one shard.
func (s *Store) LiveGames(ctx context.Context, shard int) ([]game.LiveEntry, error) {
	items, err := s.queryIndex(ctx, game.IdxLive, game.ShardKey(shard), false)
	if err != nil {
		return nil, err
	}
	out := make([]game.LiveEntry, 0, len(items))
	for _, it := range items {
		out = append(out, game.LiveEntry{
			GameID:           strAttr(it, "game_id"),
			OwnerNode:        strAttr(it, "owner_node"),
			OwnerIncarnation: strAttr(it, "owner_incarnation"),
		})
	}
	return out, nil
}

func (s *Store) userEntries(ctx context.Context, t game.IndexTable, userID string, limit int) ([]game.UserEntry, error) {
	items, err := s.queryIndex(ctx, t, userID, true)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []game.UserEntry
	for _, it := range items {
		// DynamoDB TTL deletion lags by up to 48 hours, so readers filter.
		if exp := numAttr(it, "expires_at"); exp != 0 && exp <= now.Unix() {
			continue
		}
		e := game.UserEntry{GameID: strAttr(it, "game_id"), Seat: uint8(numAttr(it, "seat"))}
		if at := strAttr(it, "ended_at"); at != "" {
			e.EndedAt, _ = time.Parse(time.RFC3339Nano, at)
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// UserLiveGames returns up to limit of the user's live games, newest first.
func (s *Store) UserLiveGames(ctx context.Context, userID string, limit int) ([]game.UserEntry, error) {
	return s.userEntries(ctx, game.IdxUserLive, userID, limit)
}

// UserRecentGames returns the user's unexpired recently ended games, newest first.
func (s *Store) UserRecentGames(ctx context.Context, userID string, limit int) ([]game.UserEntry, error) {
	return s.userEntries(ctx, game.IdxUserRecent, userID, limit)
}

func indexTableNames() []string {
	out := make([]string, 0, len(indexTables))
	for _, t := range indexTables {
		out = append(out, string(t))
	}
	return out
}
