package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// fakeDynamo is an in-process DynamoAPI that faithfully models the ONLY DynamoDB
// semantics this store relies on: a (stream_id, seq) primary key, a conditional
// PutItem that honors attribute_not_exists(seq), an ordered Query on the
// partition, and an all-or-nothing TransactWriteItems whose per-item conditions
// are checked together. It is NOT a general DynamoDB emulator — it exists so the
// store's OWN logic (seq-conflict mapping, batch atomicity, encode/decode
// round-trip, version dispatch) is testable here. The DynamoDB-Local-gated
// integration test (store_local_test.go) proves the same against the real engine
// in CI; this proves the Go logic deterministically everywhere.
type fakeDynamo struct {
	mu sync.Mutex
	// key "stream_id\x00seq" -> item
	items   map[string]map[string]ddbtypes.AttributeValue
	noTable bool // when true, DescribeTable fails (startup fail-fast test)
	// inconsistentQueries counts Queries that did not ask for ConsistentRead.
	inconsistentQueries int
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func key(streamID, seq string) string { return streamID + "\x00" + seq }

func (f *fakeDynamo) DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	if f.noTable {
		return nil, errors.New("ResourceNotFoundException: table not found")
	}
	return &dynamodb.DescribeTableOutput{}, nil
}

func (f *fakeDynamo) PutItem(ctx context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, f.putLocked(in.Item, in.ConditionExpression)
}

// putLocked applies one conditional put under the held lock; the condition we
// support is attribute_not_exists(seq) (the only one the store uses).
func (f *fakeDynamo) putLocked(item map[string]ddbtypes.AttributeValue, cond *string) error {
	sid := item["stream_id"].(*ddbtypes.AttributeValueMemberS).Value
	seq := item["seq"].(*ddbtypes.AttributeValueMemberN).Value
	k := key(sid, seq)
	if cond != nil && *cond == "attribute_not_exists(seq)" {
		if _, exists := f.items[k]; exists {
			return &ddbtypes.ConditionalCheckFailedException{Message: aws.String("seq exists")}
		}
	}
	f.items[k] = item
	return nil
}

func (f *fakeDynamo) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// All-or-nothing: first check EVERY condition against the current state (and
	// against the batch's own earlier items), then apply. If any fails, cancel
	// with a ConditionalCheckFailed reason and mutate nothing.
	staged := map[string]map[string]ddbtypes.AttributeValue{}
	reasons := make([]ddbtypes.CancellationReason, len(in.TransactItems))
	failed := false
	for i, ti := range in.TransactItems {
		p := ti.Put
		sid := p.Item["stream_id"].(*ddbtypes.AttributeValueMemberS).Value
		seq := p.Item["seq"].(*ddbtypes.AttributeValueMemberN).Value
		k := key(sid, seq)
		_, inStore := f.items[k]
		_, inStaged := staged[k]
		if p.ConditionExpression != nil && *p.ConditionExpression == "attribute_not_exists(seq)" && (inStore || inStaged) {
			reasons[i] = ddbtypes.CancellationReason{Code: aws.String("ConditionalCheckFailed")}
			failed = true
			continue
		}
		reasons[i] = ddbtypes.CancellationReason{Code: aws.String("None")}
		staged[k] = p.Item
	}
	if failed {
		return nil, &ddbtypes.TransactionCanceledException{
			Message:             aws.String("transaction cancelled"),
			CancellationReasons: reasons,
		}
	}
	for k, it := range staged {
		f.items[k] = it
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (f *fakeDynamo) Query(ctx context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ConsistentRead == nil || !*in.ConsistentRead {
		f.inconsistentQueries++
	}
	sid := in.ExpressionAttributeValues[":sid"].(*ddbtypes.AttributeValueMemberS).Value
	// Collect all items for the partition, order by seq.
	type kv struct {
		seq  int
		item map[string]ddbtypes.AttributeValue
	}
	var rows []kv
	for _, it := range f.items {
		if it["stream_id"].(*ddbtypes.AttributeValueMemberS).Value != sid {
			continue
		}
		var seq int
		fmt.Sscanf(it["seq"].(*ddbtypes.AttributeValueMemberN).Value, "%d", &seq)
		rows = append(rows, kv{seq, it})
	}
	// Order per ScanIndexForward (default true = ascending), like DynamoDB.
	ascending := in.ScanIndexForward == nil || *in.ScanIndexForward
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			swap := rows[j-1].seq > rows[j].seq
			if !ascending {
				swap = rows[j-1].seq < rows[j].seq
			}
			if !swap {
				break
			}
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
	// Honor ExclusiveStartKey: resume AFTER the given (stream_id, seq).
	start := 0
	if in.ExclusiveStartKey != nil {
		if sk, ok := in.ExclusiveStartKey["seq"].(*ddbtypes.AttributeValueMemberN); ok {
			var startSeq int
			fmt.Sscanf(sk.Value, "%d", &startSeq)
			for i, r := range rows {
				if r.seq == startSeq {
					start = i + 1
					break
				}
			}
		}
	}
	// Page by Limit (default: a small page so the store's pagination loop is
	// actually exercised even for modest streams — DynamoDB also pages on a 1 MB
	// cap, which Limit models deterministically here).
	limit := 2
	if in.Limit != nil && *in.Limit > 0 {
		limit = int(*in.Limit)
	}
	end := start + limit
	var last map[string]ddbtypes.AttributeValue
	if end < len(rows) {
		// More pages remain: set LastEvaluatedKey to the last returned item's key.
		le := rows[end-1].item
		last = map[string]ddbtypes.AttributeValue{
			"stream_id": le["stream_id"],
			"seq":       le["seq"],
		}
	} else {
		end = len(rows)
	}
	out := make([]map[string]ddbtypes.AttributeValue, 0, end-start)
	for _, r := range rows[start:end] {
		out = append(out, r.item)
	}
	return &dynamodb.QueryOutput{Items: out, LastEvaluatedKey: last}, nil
}

// --- tests ------------------------------------------------------------------

func newTestStore(t *testing.T) (*Store, *fakeDynamo) {
	t.Helper()
	f := newFakeDynamo()
	s, err := New(context.Background(), f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Deterministic committed_at.
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return s, f
}

func moveEv(ply uint32, seat uint8, uci string) game.Event {
	return game.Event{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{
		Ply: ply, SeatIdx: seat, UCI: uci, ClockAfterMs: 59000, ServerTS: 1,
	}}
}

func gameCreated() game.Event {
	return game.Event{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
		PoolID: "blitz-standard", Rated: true,
		Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
		StartFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", TimeControl: "blitz-5+3",
	}}
}

// Append then Load returns the identical ordered slice (round-trip through the
// real §4A seam + the store).
func TestAppendLoadRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	sid := "game/rt"
	want := []game.Event{gameCreated(), moveEv(1, 0, "e2e4"), moveEv(2, 1, "e7e5")}
	for i, ev := range want {
		if err := s.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := s.Load(ctx, sid)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type {
			t.Fatalf("event %d type %d, want %d", i, got[i].Type, want[i].Type)
		}
	}
	if got[1].MovePlayed == nil || got[1].MovePlayed.UCI != "e2e4" {
		t.Fatalf("move 1 did not round-trip: %+v", got[1].MovePlayed)
	}
}

// A second append at an occupied seq returns the typed ErrSeqConflict.
func TestAppendSeqConflict(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	sid := "game/conflict"
	if err := s.Append(ctx, sid, 0, gameCreated()); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := s.Append(ctx, sid, 0, moveEv(1, 0, "e2e4"))
	if !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("append at occupied seq 0: want ErrSeqConflict, got %v", err)
	}
	// The occupant is unchanged (not overwritten).
	got, _ := s.Load(ctx, sid)
	if len(got) != 1 || got[0].Type != game.EvGameCreated {
		t.Fatalf("occupied seq was overwritten: %+v", got)
	}
}

// AppendBatch is all-or-nothing: a batch whose any slot is occupied commits
// NOTHING and returns ErrSeqConflict.
func TestAppendBatchAtomicAndConflict(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	sid := "game/batch"
	if err := s.Append(ctx, sid, 0, gameCreated()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Happy path: an atomic ending pair at seq 1,2.
	end := []game.Event{
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: []game.Result{game.ResultWin, game.ResultLoss}, Termination: game.TermFlag,
		}},
	}
	if err := s.AppendBatch(ctx, sid, 1, end); err != nil {
		t.Fatalf("append ending batch: %v", err)
	}
	got, _ := s.Load(ctx, sid)
	if len(got) != 3 {
		t.Fatalf("after batch len %d, want 3", len(got))
	}
	// Conflict path: a batch starting at an occupied seq (1) commits nothing.
	before, _ := s.Load(ctx, sid)
	err := s.AppendBatch(ctx, sid, 1, end)
	if !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("batch at occupied seq: want ErrSeqConflict, got %v", err)
	}
	after, _ := s.Load(ctx, sid)
	if len(after) != len(before) {
		t.Fatalf("conflicted batch mutated the stream: %d -> %d", len(before), len(after))
	}
}

// AppendBatch with a mid-batch occupied seq is atomic: neither event lands.
func TestAppendBatchPartialOccupiedCommitsNothing(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	sid := "game/partial"
	// Occupy seq 0 and 2, leave 1 free.
	_ = s.Append(ctx, sid, 0, gameCreated())
	_ = s.Append(ctx, sid, 1, moveEv(1, 0, "e2e4"))
	// Batch targeting seq 1,2 — seq 1 is occupied, seq 2 free. All-or-nothing:
	// nothing commits, seq 2 must NOT be created.
	err := s.AppendBatch(ctx, sid, 1, []game.Event{
		moveEv(2, 1, "e7e5"), moveEv(3, 0, "g1f3"),
	})
	if !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("want ErrSeqConflict, got %v", err)
	}
	got, _ := s.Load(ctx, sid)
	if len(got) != 2 {
		t.Fatalf("partial batch leaked an item: len %d, want 2", len(got))
	}
}

// Unknown stream -> empty slice, nil error (interface contract).
func TestLoadUnknownStream(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.Load(context.Background(), "game/does-not-exist")
	if err != nil {
		t.Fatalf("unknown stream errored: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown stream returned %d events, want 0", len(got))
	}
}

// An item with an unknown encoding_version makes Load error (NFR-9 fidelity),
// never silently misdecode.
func TestLoadUnknownEncodingVersionErrors(t *testing.T) {
	s, f := newTestStore(t)
	ctx := context.Background()
	sid := "game/badver"
	if err := s.Append(ctx, sid, 0, gameCreated()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Tamper the stored item's encoding_version to a value this build doesn't know.
	f.mu.Lock()
	for k, it := range f.items {
		it["encoding_version"] = &ddbtypes.AttributeValueMemberN{Value: "99"}
		f.items[k] = it
	}
	f.mu.Unlock()
	_, err := s.Load(ctx, sid)
	if err == nil {
		t.Fatal("Load accepted an unknown encoding_version — must refuse (NFR-9)")
	}
}

// New fails fast when the table is unreachable (startup guard).
func TestNewFailsWhenTableMissing(t *testing.T) {
	f := newFakeDynamo()
	f.noTable = true
	if _, err := New(context.Background(), f); err == nil {
		t.Fatal("New succeeded with no table — must fail fast")
	}
}

// Load must stitch MULTIPLE pages in strict ascending seq order. The fake pages
// at 2 items/page, so a 7-event stream forces the store's pagination loop
// (ExclusiveStartKey carry + LastEvaluatedKey termination) across 4 pages — a
// path a single-page fake would never exercise (review S4). Catches a dropped
// page, a mis-carried start key, or a wrong ScanIndexForward.
func TestLoadPaginatesInOrder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	sid := "game/paged"
	n := 7
	if err := s.Append(ctx, sid, 0, gameCreated()); err != nil {
		t.Fatalf("seed 0: %v", err)
	}
	for i := 1; i < n; i++ {
		if err := s.Append(ctx, sid, uint32(i), moveEv(uint32(i), uint8(i%2), "e2e4")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	got, err := s.Load(ctx, sid)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != n {
		t.Fatalf("paginated load returned %d events, want %d — a page was dropped or duplicated", len(got), n)
	}
	// Strict ascending seq across page boundaries: event 0 is GameCreated, then
	// MovePlayed plies 1..n-1 in order.
	for i := 1; i < n; i++ {
		if got[i].Type != game.EvMovePlayed || got[i].MovePlayed.Ply != uint32(i) {
			t.Fatalf("event %d out of order across pages: %+v", i, got[i])
		}
	}
}

// Load must read strongly consistently: the session decides game state from it,
// including the re-read that confirms an ambiguous commit (#89). An eventually
// consistent read can miss the write that just landed.
func TestLoadIsStronglyConsistent(t *testing.T) {
	s, f := newTestStore(t)
	ctx := context.Background()
	if err := s.Append(ctx, "game/cr", 0, gameCreated()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, "game/cr"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inconsistentQueries != 0 {
		t.Fatalf("%d Load queries without ConsistentRead", f.inconsistentQueries)
	}
}
