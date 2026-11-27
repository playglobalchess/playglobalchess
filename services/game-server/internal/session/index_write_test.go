package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// GS-8 section 4.3: every write that contains GameEnded also removes the game's
// live rows and puts its user_recent_games rows, in the same atomic write.

var idxCfg = game.IndexConfig{Shards: 4, NodeID: "n1", Incarnation: "i1", RecentTTL: time.Hour}

// idxStore is a MemStore that counts and can fail the write paths, so a test can
// see WHICH write an ending used and what an ambiguous commit leaves behind.
type idxStore struct {
	*game.MemStore
	mu         sync.Mutex
	plain      int   // Append and AppendBatch calls
	indexed    int   // AppendIndexed calls
	commitThen error // next AppendIndexed commits, then reports this
	errBefore  error // next AppendIndexed fails, committing nothing
}

func (s *idxStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	s.mu.Lock()
	s.plain++
	s.mu.Unlock()
	return s.MemStore.Append(ctx, id, seq, ev)
}

func (s *idxStore) AppendBatch(ctx context.Context, id string, seq uint32, evs []game.Event) error {
	s.mu.Lock()
	s.plain++
	s.mu.Unlock()
	return s.MemStore.AppendBatch(ctx, id, seq, evs)
}

func (s *idxStore) AppendIndexed(ctx context.Context, id string, seq uint32, evs []game.Event, ops []game.IndexOp) error {
	s.mu.Lock()
	s.indexed++
	then, before := s.commitThen, s.errBefore
	s.commitThen, s.errBefore = nil, nil
	s.mu.Unlock()
	if before != nil {
		return before
	}
	err := s.MemStore.AppendIndexed(ctx, id, seq, evs, ops)
	if err == nil && then != nil {
		return then
	}
	return err
}

func (s *idxStore) counts() (plain, indexed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plain, s.indexed
}

// arm sets the next AppendIndexed fault under the lock.
func (s *idxStore) arm(commitThen, errBefore error) {
	s.mu.Lock()
	s.commitThen, s.errBefore = commitThen, errBefore
	s.mu.Unlock()
}

var opening = []game.Event{
	{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
		PoolID: "blitz-standard", Seats: []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
		StartFEN: startFEN, TimeControl: "blitz-5+3",
	}},
	{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
	{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
}

// seedOpening writes the opening and its index rows, as the matchmaker does.
func seedOpening(t *testing.T, st *idxStore) {
	t.Helper()
	ops, err := idxCfg.OpeningOps(gameID, []string{"u0", "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MemStore.AppendIndexed(context.Background(), game.StreamIDFor(gameID), 0, opening, ops); err != nil {
		t.Fatalf("seed opening: %v", err)
	}
}

// indexedGame seeds a live game WITH its opening index rows and returns a
// running session that has the index config on.
func indexedGame(t *testing.T) (*Session, *idxStore, *fakeClock) {
	t.Helper()
	fc := &fakeClock{t: time.Now().Truncate(time.Millisecond)}
	st := &idxStore{MemStore: game.NewMemStore()}
	seedOpening(t, st)
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := idxCfg
	s, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: st, Oracle: o, Now: fc.now, Index: &cfg,
		Opts: game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(context.Background())
	go s.Run(rctx)
	t.Cleanup(func() {
		if !t.Failed() {
			assertRebuildMatches(t, s, st)
		}
		cancel()
	})
	return s, st, fc
}

// wantLive asserts the game's live rows are all present (or all gone).
func wantLive(t *testing.T, st *idxStore, present bool) {
	t.Helper()
	ctx := context.Background()
	for _, u := range []string{"u0", "u1"} {
		rows, err := st.UserLiveGames(ctx, u, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(rows) == 1; got != present {
			t.Fatalf("user_live_games for %s: %v, want present=%v", u, rows, present)
		}
	}
	shard := game.IndexShard(gameID, idxCfg.Shards)
	rows, err := st.LiveGames(ctx, shard)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rows) == 1; got != present {
		t.Fatalf("live_games shard %d: %v, want present=%v", shard, rows, present)
	}
}

// wantRecent asserts both players have the game in user_recent_games with the
// right seat and the time it ended.
func wantRecent(t *testing.T, st *idxStore, endedAt time.Time) {
	t.Helper()
	for seat, u := range []string{"u0", "u1"} {
		rows, err := st.UserRecentGames(context.Background(), u, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].GameID != gameID || int(rows[0].Seat) != seat {
			t.Fatalf("user_recent_games for %s: %v, want %s at seat %d", u, rows, gameID, seat)
		}
		if !rows[0].EndedAt.Equal(endedAt) {
			t.Fatalf("EndedAt for %s = %v, want %v", u, rows[0].EndedAt, endedAt)
		}
	}
}

func playIndexed(t *testing.T, s *Session, fc *fakeClock, moves ...string) {
	t.Helper()
	for i, m := range moves {
		fc.add(time.Second)
		if o, err := s.SubmitMove(context.Background(), uint8(i%2), uint32(i+1), m, uint64(i+1)); err != nil || !o.Accepted {
			t.Fatalf("move %d %s: %+v %v", i+1, m, o, err)
		}
	}
}

func TestEveryEndingPathWritesTheIndexInTheSameWrite(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		end  func(t *testing.T, s *Session, fc *fakeClock)
	}{
		{"resign", func(t *testing.T, s *Session, fc *fakeClock) {
			playIndexed(t, s, fc, "e2e4")
			if err := s.Resign(ctx, 1); err != nil {
				t.Fatal(err)
			}
		}},
		{"abort before ply 2", func(t *testing.T, s *Session, fc *fakeClock) {
			if err := s.Abort(ctx, 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"checkmate by move", func(t *testing.T, s *Session, fc *fakeClock) {
			playIndexed(t, s, fc, "f2f3", "e7e5", "g2g4", "d8h4")
		}},
		{"flag on a tick", func(t *testing.T, s *Session, fc *fakeClock) {
			playIndexed(t, s, fc, "e2e4", "e7e5")
			fc.add(10 * time.Minute)
			if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
				t.Fatalf("tick: ended=%v err=%v", ended, err)
			}
		}},
		{"draw by agreement", func(t *testing.T, s *Session, fc *fakeClock) {
			playIndexed(t, s, fc, "e2e4", "e7e5")
			if _, err := s.OfferDrawResult(ctx, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.OfferDrawResult(ctx, 1); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, st, fc := indexedGame(t)
			wantLive(t, st, true)
			tc.end(t, s, fc)
			if snap, _ := s.StateSnapshot(ctx); !snap.Ended {
				t.Fatal("the game did not end")
			}
			wantLive(t, st, false)
			wantRecent(t, st, fc.now())
			// The ending itself used exactly one indexed write.
			if _, indexed := st.counts(); indexed != 1 {
				t.Fatalf("%d indexed writes, want exactly the ending", indexed)
			}
		})
	}
}

func TestMovesDoNotTouchTheIndex(t *testing.T) {
	s, st, fc := indexedGame(t)
	playIndexed(t, s, fc, "e2e4")
	if plain, indexed := st.counts(); indexed != 0 || plain == 0 {
		t.Fatalf("plain=%d indexed=%d: a move must use a plain write", plain, indexed)
	}
	wantLive(t, st, true)
}

// The repair path appends a lone GameEnded to a stream that has a ClockFlagged
// and no ending. It must carry the index changes too, or the game would end
// with its live rows left behind.
func TestHalfTerminalRepairWritesTheIndex(t *testing.T) {
	ctx := context.Background()
	st := &idxStore{MemStore: game.NewMemStore()}
	seedOpening(t, st)
	for i, ev := range []game.Event{
		{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: 1, SeatIdx: 0, UCI: "e2e4", ClockAfterMs: 9000, ServerTS: 1000}},
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}},
	} {
		if err := st.MemStore.Append(ctx, game.StreamIDFor(gameID), uint32(3+i), ev); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := engine.New()
	cfg := idxCfg
	now := time.Now().Truncate(time.Millisecond)
	if _, err := NewSession(ctx, Config{
		GameID: gameID, Store: st, Oracle: o, Now: func() time.Time { return now }, Index: &cfg,
		Opts: game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 10_000}},
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, indexed := st.counts(); indexed != 1 {
		t.Fatalf("%d indexed writes, want the one repair write", indexed)
	}
	wantLive(t, st, false)
	wantRecent(t, st, now)
}

// A write that commits and then reports an error is acked once (#89), and the
// index is consistent with the stream because they committed together.
func TestAmbiguousEndingCommitLeavesTheIndexConsistent(t *testing.T) {
	s, st, fc := indexedGame(t)
	playIndexed(t, s, fc, "e2e4")
	st.arm(errors.New("fault: timeout after commit"), nil)
	if err := s.Resign(context.Background(), 1); err != nil {
		t.Fatalf("resign over an ambiguous commit: %v, want acked", err)
	}
	wantLive(t, st, false)
	wantRecent(t, st, fc.now())
}

// A write that fails before committing changes neither the stream nor the index.
func TestFailedEndingWriteChangesNothing(t *testing.T) {
	s, st, fc := indexedGame(t)
	ctx := context.Background()
	playIndexed(t, s, fc, "e2e4")
	st.arm(nil, errors.New("fault: store down"))
	if err := s.Resign(ctx, 1); err == nil {
		t.Fatal("resign over a failing store succeeded")
	}
	wantLive(t, st, true)
	for _, u := range []string{"u0", "u1"} {
		if rows, _ := st.UserRecentGames(ctx, u, 10); len(rows) != 0 {
			t.Fatalf("%s has recent rows after a failed ending: %v", u, rows)
		}
	}
	if err := s.Resign(ctx, 1); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	wantLive(t, st, false)
}

// onlyEvents hides the IndexedStore methods of the store it wraps.
type onlyEvents struct{ game.EventStore }

func TestIndexNeedsAnIndexedStore(t *testing.T) {
	o, _ := engine.New()
	cfg := idxCfg
	if _, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: onlyEvents{game.NewMemStore()}, Oracle: o, Index: &cfg,
	}); err == nil {
		t.Fatal("an index config over a store with no index support was accepted")
	}
	bad := game.IndexConfig{}
	if _, err := NewSession(context.Background(), Config{
		GameID: gameID, Store: game.NewMemStore(), Oracle: o, Index: &bad,
	}); !errors.Is(err, game.ErrBadIndexConfig) {
		t.Fatalf("zero index config: %v, want ErrBadIndexConfig", err)
	}
}
