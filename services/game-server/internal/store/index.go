package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// The live-game index tables (SPEC-S2-GAME-SERVER-GS8 §4.3, ADR-16 amendment).
// Hints only; the event stream stays the truth. Tables are created out of band
// (there is no IaC in this repo yet); store.New refuses to start without them.
//
//	user_live_games   user_id S (partition) / game_id S (sort)   seat N
//	live_games        shard N   (partition) / game_id S (sort)   owner_node S, owner_incarnation S
//	user_recent_games user_id S (partition) / game_id S (sort)   seat N, ended_at S, expires_at N (TTL)
var indexTables = []game.IndexTable{game.IdxUserLive, game.IdxLive, game.IdxUserRecent}

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
			// Round up: a sub-second expiry must not expire early.
			it["expires_at"] = num((op.ExpiresAt.UnixMilli() + 999) / 1000)
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
	if err := game.ValidateIndexedWrite(expectedSeq, len(evs), ops); err != nil {
		return err
	}
	guard := 0
	if expectedSeq > 0 {
		guard = 1
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

// RepairIndex applies Put and Delete ops in one TransactWriteItems with no
// events (see game.IndexedStore).
func (s *Store) RepairIndex(ctx context.Context, ops []game.IndexOp) error {
	if err := game.ValidateRepair(ops); err != nil {
		return err
	}
	writes := make([]ddbtypes.TransactWriteItem, 0, len(ops))
	for _, op := range ops {
		w, err := indexWrite(op)
		if err != nil {
			return err
		}
		writes = append(writes, w)
	}
	if _, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
		return fmt.Errorf("store: repair index: %w", err)
	}
	return nil
}

// queryIndex reads every page of one partition (strongly consistent: adoption
// and AUTHENTICATE decide on what they read) and returns the raw items.
func (s *Store) queryIndex(ctx context.Context, t game.IndexTable, key string, newestFirst bool) ([]map[string]ddbtypes.AttributeValue, error) {
	var out []map[string]ddbtypes.AttributeValue
	var start map[string]ddbtypes.AttributeValue
	for {
		q, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(string(t)),
			// "shard" is a DynamoDB reserved word, so the key is always aliased.
			KeyConditionExpression:    aws.String("#pk = :k"),
			ExpressionAttributeNames:  map[string]string{"#pk": pkName(t)},
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

func strAttr(it map[string]ddbtypes.AttributeValue, name string) (string, bool) {
	v, ok := it[name].(*ddbtypes.AttributeValueMemberS)
	if !ok || v.Value == "" {
		return "", false
	}
	return v.Value, true
}

func numAttr(it map[string]ddbtypes.AttributeValue, name string) (int64, bool) {
	v, ok := it[name].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v.Value, 10, 64)
	return n, err == nil
}

// LiveGames returns every live_games entry of one shard. A malformed row is a
// hint nobody can trust, so it is skipped and logged rather than guessed at.
func (s *Store) LiveGames(ctx context.Context, shard int) ([]game.LiveEntry, error) {
	items, err := s.queryIndex(ctx, game.IdxLive, game.ShardKey(shard), false)
	if err != nil {
		return nil, err
	}
	out := make([]game.LiveEntry, 0, len(items))
	for _, it := range items {
		gid, ok1 := strAttr(it, "game_id")
		node, ok2 := strAttr(it, "owner_node")
		inc, ok3 := strAttr(it, "owner_incarnation")
		if !ok1 || !ok2 || !ok3 {
			log.Printf("store: skipping malformed live_games row in shard %d: %v", shard, it)
			continue
		}
		out = append(out, game.LiveEntry{GameID: gid, OwnerNode: node, OwnerIncarnation: inc})
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
		gid, ok1 := strAttr(it, "game_id")
		seat, ok2 := numAttr(it, "seat")
		if !ok1 || !ok2 || seat < 0 || seat > 255 {
			log.Printf("store: skipping malformed %s row for user %s: %v", t, userID, it)
			continue
		}
		e := game.UserEntry{GameID: gid, Seat: uint8(seat)}
		if t == game.IdxUserRecent {
			exp, ok3 := numAttr(it, "expires_at")
			at, ok4 := strAttr(it, "ended_at")
			ended, perr := time.Parse(time.RFC3339Nano, at)
			if !ok3 || !ok4 || perr != nil {
				log.Printf("store: skipping malformed %s row for user %s: %v", t, userID, it)
				continue
			}
			// DynamoDB TTL deletion lags by up to 48 hours, so readers filter.
			if exp <= now.Unix() {
				continue
			}
			e.EndedAt = ended
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
