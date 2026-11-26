package store

import (
	"errors"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// The index half of fakeDynamo: the three live-game tables as maps keyed by
// table, partition value and game_id. Models only what the store emits.

func attrKey(v ddbtypes.AttributeValue) string {
	switch x := v.(type) {
	case *ddbtypes.AttributeValueMemberS:
		return x.Value
	case *ddbtypes.AttributeValueMemberN:
		return x.Value
	}
	return ""
}

func partName(table string) string {
	if table == "live_games" {
		return "shard"
	}
	return "user_id"
}

func (f *fakeDynamo) idxPart(table, pk string) map[string]map[string]ddbtypes.AttributeValue {
	if f.idx == nil {
		f.idx = map[string]map[string]map[string]map[string]ddbtypes.AttributeValue{}
	}
	if f.idx[table] == nil {
		f.idx[table] = map[string]map[string]map[string]ddbtypes.AttributeValue{}
	}
	if f.idx[table][pk] == nil {
		f.idx[table][pk] = map[string]map[string]ddbtypes.AttributeValue{}
	}
	return f.idx[table][pk]
}

// stageIndex checks one index write and queues its application. It reports
// false when a condition fails.
func (f *fakeDynamo) stageIndex(ti ddbtypes.TransactWriteItem, apply *[]func()) bool {
	switch {
	case ti.Put != nil:
		t := *ti.Put.TableName
		pk, gid := attrKey(ti.Put.Item[partName(t)]), attrKey(ti.Put.Item["game_id"])
		it := ti.Put.Item
		*apply = append(*apply, func() { f.idxPart(t, pk)[gid] = it })
	case ti.Delete != nil:
		t := *ti.Delete.TableName
		pk, gid := attrKey(ti.Delete.Key[partName(t)]), attrKey(ti.Delete.Key["game_id"])
		*apply = append(*apply, func() { delete(f.idxPart(t, pk), gid) })
	case ti.Update != nil:
		u := ti.Update
		t := *u.TableName
		pk, gid := attrKey(u.Key[partName(t)]), attrKey(u.Key["game_id"])
		_, exists := f.idxPart(t, pk)[gid]
		// Real DynamoDB Update UPSERTS a missing item unless a condition forbids
		// it, so the fake only fails when the store sent the condition.
		if !exists && u.ConditionExpression != nil && *u.ConditionExpression == "attribute_exists(game_id)" {
			return false
		}
		n, i := u.ExpressionAttributeValues[":n"], u.ExpressionAttributeValues[":i"]
		*apply = append(*apply, func() {
			row := f.idxPart(t, pk)[gid]
			if row == nil {
				row = map[string]ddbtypes.AttributeValue{partName(t): u.Key[partName(t)], "game_id": u.Key["game_id"]}
				f.idxPart(t, pk)[gid] = row
			}
			row["owner_node"], row["owner_incarnation"] = n, i
		})
	}
	return true
}

// queryIndexLocked answers a Query on an index table: one partition, ordered by
// game_id, paged two rows at a time so the store's pagination loop is exercised.
func (f *fakeDynamo) queryIndexLocked(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
	if in.ConsistentRead == nil || !*in.ConsistentRead {
		f.inconsistentQueries++
	}
	t := *in.TableName
	// Real DynamoDB rejects reserved words used unaliased in an expression, and
	// SHARD is one of them: the store must alias the partition key.
	if in.KeyConditionExpression != nil && strings.Contains(strings.ToLower(*in.KeyConditionExpression), "shard") {
		return nil, errors.New("ValidationException: reserved keyword SHARD in KeyConditionExpression")
	}
	pk := attrKey(in.ExpressionAttributeValues[":k"])
	var ids []string
	for gid := range f.idxPart(t, pk) {
		ids = append(ids, gid)
	}
	sort.Strings(ids)
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	start := 0
	if in.ExclusiveStartKey != nil {
		after := attrKey(in.ExclusiveStartKey["game_id"])
		for i, id := range ids {
			if id == after {
				start = i + 1
			}
		}
	}
	end := start + 2
	var last map[string]ddbtypes.AttributeValue
	if end < len(ids) {
		last = map[string]ddbtypes.AttributeValue{"game_id": f.idxPart(t, pk)[ids[end-1]]["game_id"]}
	} else {
		end = len(ids)
	}
	var out []map[string]ddbtypes.AttributeValue
	for _, id := range ids[start:end] {
		out = append(out, f.idxPart(t, pk)[id])
	}
	return &dynamodb.QueryOutput{Items: out, LastEvaluatedKey: last}, nil
}

// badIndexKey reports an index write whose key attribute has the wrong type,
// which real DynamoDB rejects with a ValidationException (live_games.shard is N).
func badIndexKey(ti ddbtypes.TransactWriteItem) bool {
	var t string
	var key map[string]ddbtypes.AttributeValue
	switch {
	case ti.Put != nil:
		t, key = *ti.Put.TableName, ti.Put.Item
	case ti.Delete != nil:
		t, key = *ti.Delete.TableName, ti.Delete.Key
	case ti.Update != nil:
		t, key = *ti.Update.TableName, ti.Update.Key
	default:
		return false
	}
	switch v := key[partName(t)].(type) {
	case *ddbtypes.AttributeValueMemberN:
		return t != "live_games"
	case *ddbtypes.AttributeValueMemberS:
		return t == "live_games" || v.Value == ""
	}
	return true
}
