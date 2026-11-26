package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// commitThenErr makes the next write commit and then report an error, the way
// a timeout after a DynamoDB commit looks to the client (#176, #89, #95).
type commitThenErr struct {
	DynamoAPI
	armed atomic.Bool
}

var errAfterCommit = errors.New("fault: timeout after commit")

func (c *commitThenErr) PutItem(ctx context.Context, in *dynamodb.PutItemInput, o ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	out, err := c.DynamoAPI.PutItem(ctx, in, o...)
	if err == nil && c.armed.CompareAndSwap(true, false) {
		return nil, errAfterCommit
	}
	return out, err
}

func (c *commitThenErr) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, o ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	out, err := c.DynamoAPI.TransactWriteItems(ctx, in, o...)
	if err == nil && c.armed.CompareAndSwap(true, false) {
		return nil, errAfterCommit
	}
	return out, err
}

// realSession builds a live session over the REAL store (codec encode and
// decode, DynamoDB item round trip) on a faulting client.
func realSession(t *testing.T, gid string, rated bool) (*session.Session, *commitThenErr, *Store, *time.Time, game.LegalityOracle) {
	t.Helper()
	ctx := context.Background()
	// DynamoDB Local when CI provides it (acceptance of #176), else the
	// in-process fake, so the check also runs everywhere.
	var api DynamoAPI = newFakeDynamo()
	if os.Getenv("DYNAMODB_LOCAL_ENDPOINT") != "" {
		db := dynamoLocal(t)
		ensureTable(t, db)
		api = db
	}
	fd := &commitThenErr{DynamoAPI: api}
	st, err := New(ctx, fd)
	if err != nil {
		t.Fatal(err)
	}
	sid := game.StreamIDFor(gid)
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz-standard", Rated: rated,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		if err := st.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatal(err)
		}
	}
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	s, err := session.NewSession(ctx, session.Config{
		GameID: gid, Store: st, Oracle: o, Now: func() time.Time { return now },
		Opts: game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go s.Run(rctx)
	return s, fd, st, &now, o
}

// TestAmbiguousCommitConfirmedThroughTheRealStore (#176): the session confirms
// an ambiguous commit by re-reading through the real store and comparing the
// decoded events with the decided ones by value. This test pins that the round
// trip is lossless for EVERY event shape the session writes (move, flag,
// resign, abort, draw agreement, takeback rewind): a store or codec change that
// normalised any field would make the session report INTERNAL for a command
// that landed, and fails here. DynamoDB Local runs the same store code in CI
// (store_local_test.go); only the client differs.
func TestAmbiguousCommitConfirmedThroughTheRealStore(t *testing.T) {
	ctx := context.Background()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	step := func(now *time.Time) { *now = now.Add(time.Second) }
	type tc struct {
		name string
		run  func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time)
	}
	mv := func(t *testing.T, s *session.Session, now *time.Time, seat uint8, ply uint32, uci string) {
		t.Helper()
		step(now)
		if out, err := s.SubmitMove(ctx, seat, ply, uci, 1); err != nil || !out.Accepted {
			t.Fatalf("setup move %s: %+v %v", uci, out, err)
		}
	}
	cases := []tc{
		{"move", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			mv(t, s, now, 0, 1, "e2e4")
			step(now)
			fd.armed.Store(true)
			if out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 1); err != nil || !out.Accepted {
				t.Fatalf("committed move reported as failed: %+v %v", out, err)
			}
		}},
		{"resign", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			mv(t, s, now, 0, 1, "e2e4")
			mv(t, s, now, 1, 2, "e7e5")
			fd.armed.Store(true)
			if err := s.Resign(ctx, 0); err != nil {
				t.Fatalf("committed resign reported as failed: %v", err)
			}
		}},
		{"abort", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			fd.armed.Store(true)
			if err := s.Abort(ctx, 0); err != nil {
				t.Fatalf("committed abort reported as failed: %v", err)
			}
		}},
		{"flag", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			mv(t, s, now, 0, 1, "e2e4")
			mv(t, s, now, 1, 2, "e7e5")
			*now = now.Add(400 * time.Second)
			fd.armed.Store(true)
			if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
				t.Fatalf("committed flag reported as failed: ended=%v %v", ended, err)
			}
		}},
		{"draw-agreed", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			mv(t, s, now, 0, 1, "e2e4")
			mv(t, s, now, 1, 2, "e7e5")
			if err := s.OfferDraw(ctx, 0); err != nil {
				t.Fatal(err)
			}
			fd.armed.Store(true)
			if err := s.AcceptDraw(ctx, 1); err != nil {
				t.Fatalf("committed draw agreement reported as failed: %v", err)
			}
		}},
		{"takeback-rewind", func(t *testing.T, s *session.Session, fd *commitThenErr, now *time.Time) {
			mv(t, s, now, 0, 1, "e2e4")
			mv(t, s, now, 1, 2, "e7e5")
			mv(t, s, now, 0, 3, "g1f3")
			r, err := s.ProposeTakeback(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			fd.armed.Store(true)
			if _, err := s.AcceptTakeback(ctx, 1, r.AtPly); err != nil {
				t.Fatalf("committed takeback reported as failed: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gid := "g176-" + c.name
			if os.Getenv("DYNAMODB_LOCAL_ENDPOINT") != "" {
				gid += fmt.Sprintf("-%d", time.Now().UnixNano()) // the table outlives the test
			}
			s, fd, st, now, o := realSession(t, gid, c.name != "takeback-rewind")
			c.run(t, s, fd, now)
			if fd.armed.Load() {
				t.Fatal("the armed fault never fired: the case did not reach its write")
			}
			evs, err := st.Load(ctx, game.StreamIDFor(gid))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := game.FoldWithEngine(ctx, evs, opts, o); err != nil {
				t.Fatalf("stored stream does not fold: %v", err)
			}
		})
	}
}
