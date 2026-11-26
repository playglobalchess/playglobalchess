package store

import (
	"sort"

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
		if _, ok := f.idxPart(t, pk)[gid]; !ok {
			return false
		}
		n, i := u.ExpressionAttributeValues[":n"], u.ExpressionAttributeValues[":i"]
		*apply = append(*apply, func() {
			row := f.idxPart(t, pk)[gid]
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
