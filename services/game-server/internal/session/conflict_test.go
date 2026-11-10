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

// GS-7 §3.4 step 5: on ErrSeqConflict the session reloads the stream, rebuilds
// with FoldWithEngine and re-decides the SAME command. These tests stand in for
// a second node by writing to the store right before the session's append.

// racingStore runs `before` (once) just before the next append, so a
// competing writer takes the seq the session expected.
type racingStore struct {
	*game.MemStore
	mu     sync.Mutex
	before func()
	// alwaysConflict makes every append fail with ErrSeqConflict.
	alwaysConflict bool
}

func (r *racingStore) race() error {
	r.mu.Lock()
	f := r.before
	r.before = nil
	always := r.alwaysConflict
	r.mu.Unlock()
	if always {
		return game.ErrSeqConflict
	}
	if f != nil {
		f()
	}
	return nil
}

func (r *racingStore) Append(ctx context.Context, sid string, seq uint32, ev game.Event) error {
	if err := r.race(); err != nil {
		return err
	}
	return r.MemStore.Append(ctx, sid, seq, ev)
}

func (r *racingStore) AppendBatch(ctx context.Context, sid string, seq uint32, evs []game.Event) error {
	if err := r.race(); err != nil {
		return err
	}
	return r.MemStore.AppendBatch(ctx, sid, seq, evs)
}

var conflictOpts = game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}

// openingStore holds GameCreated + both PlayerJoined (a LIVE game, no clock yet).
func openingStore(t *testing.T) *racingStore {
	t.Helper()
	m := game.NewMemStore()
	sid := game.StreamIDFor(gameID)
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "casual", Seats: []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		if err := m.Append(context.Background(), sid, uint32(i), ev); err != nil {
			t.Fatal(err)
		}
	}
	return &racingStore{MemStore: m}
}

func runSession(t *testing.T, store game.EventStore, now func() time.Time) (*Session, game.LegalityOracle, func()) {
	t.Helper()
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(context.Background(), Config{GameID: gameID, Store: store, Oracle: o, Opts: conflictOpts, Now: now})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	return s, o, cancel
}

func mustFoldCount(t *testing.T, store game.EventStore, typ game.EventType) int {
	t.Helper()
	evs, err := store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := game.Fold(evs, conflictOpts); err != nil {
		t.Fatalf("stream does not fold: %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// competingMove appends, as another node would, the move a second session
// over the same store decides (so the bytes are exactly what a real node writes).
func competingMove(t *testing.T, store *racingStore, o game.LegalityOracle, now func() time.Time, seat uint8, ply uint32, uci string) {
	t.Helper()
	evs, err := store.MemStore.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatal(err)
	}
	st, err := game.FoldWithEngine(context.Background(), evs, conflictOpts, o)
	if err != nil {
		t.Fatal(err)
	}
	d, err := game.Decide(context.Background(), st, game.CmdMove{Seat: seat, PlyExpected: ply, UCI: uci}, uint64(now().UnixMilli()), conflictOpts, o)
	if err != nil || len(d.Events) != 1 {
		t.Fatalf("competing decide: %+v err=%v", d, err)
	}
	if err := store.MemStore.Append(context.Background(), game.StreamIDFor(gameID), st.NextSeq, d.Events[0]); err != nil {
		t.Fatal(err)
	}
}

// The same move lands first on another node: the session re-decides it as a
// re-ack of that move and appends nothing (no duplicate, no reject).
func TestSeqConflictSameMoveBecomesReack(t *testing.T) {
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := openingStore(t)
	s, o, cancel := runSession(t, store, fc.now)
	defer cancel()
	store.mu.Lock()
	store.before = func() { competingMove(t, store, o, fc.now, 0, 1, "e2e4") }
	store.mu.Unlock()

	out, err := s.SubmitMove(context.Background(), 0, 1, "e2e4", 0)
	if err != nil || !out.Accepted || !out.Reacked || out.UCI != "e2e4" {
		t.Fatalf("got %+v err=%v, want a re-ack of e2e4", out, err)
	}
	if n := mustFoldCount(t, store, game.EvMovePlayed); n != 1 {
		t.Fatalf("%d MovePlayed in the stream, want 1", n)
	}
	snap, _ := s.StateSnapshot(context.Background())
	if snap.Ply != 1 || snap.Turn != 1 {
		t.Fatalf("session after re-decide: %+v", snap)
	}
}

// A different event takes the seq (the opponent offers a draw on another
// node); the move is still valid, so it is re-sequenced at the new head.
func TestSeqConflictOtherEventReSequencesMove(t *testing.T) {
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := openingStore(t)
	s, o, cancel := runSession(t, store, fc.now)
	defer cancel()
	ctx := context.Background()
	for i, u := range []string{"e2e4", "e7e5"} {
		if out, err := s.SubmitMove(ctx, uint8(i), uint32(i+1), u, 0); err != nil || !out.Accepted {
			t.Fatalf("setup %s: %+v %v", u, out, err)
		}
	}
	store.mu.Lock()
	store.before = func() {
		evs, _ := store.MemStore.Load(ctx, game.StreamIDFor(gameID))
		_ = store.MemStore.Append(ctx, game.StreamIDFor(gameID), uint32(len(evs)),
			game.Event{Type: game.EvDrawOffered, SeatAction: &game.SeatAction{SeatIdx: 1}})
	}
	store.mu.Unlock()
	out, err := s.SubmitMove(ctx, 0, 3, "g1f3", 0)
	if err != nil || !out.Accepted || out.Reacked {
		t.Fatalf("got %+v err=%v, want g1f3 accepted as new", out, err)
	}
	if n := mustFoldCount(t, store, game.EvMovePlayed); n != 3 {
		t.Fatalf("%d MovePlayed, want 3", n)
	}
	// The other node's offer is in the session's state; White's move did not
	// clear it (only the responder's move would... white IS the responder here).
	snap, _ := s.StateSnapshot(ctx)
	if snap.Ply != 3 || snap.DrawOfferBy != game.NoSeat {
		t.Fatalf("after re-sequence: ply %d drawOfferBy %d (white's move declines black's offer)", snap.Ply, snap.DrawOfferBy)
	}
	_ = o
}

// Every append conflicts: after the bounded retries the command fails and the
// session state is unchanged (no partial apply).
func TestSeqConflictRetryExhaustionLeavesStateUntouched(t *testing.T) {
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := openingStore(t)
	s, _, cancel := runSession(t, store, fc.now)
	defer cancel()
	before, _ := s.StateSnapshot(context.Background())
	store.mu.Lock()
	store.alwaysConflict = true
	store.mu.Unlock()
	_, err := s.SubmitMove(context.Background(), 0, 1, "e2e4", 0)
	if err == nil || !errors.Is(err, game.ErrSeqConflict) {
		t.Fatalf("err = %v, want a persist error wrapping ErrSeqConflict", err)
	}
	after, _ := s.StateSnapshot(context.Background())
	if after.Ply != before.Ply || after.NextSeq != before.NextSeq {
		t.Fatalf("state changed after a failed command: before %+v after %+v", before, after)
	}
}

// Two nodes build the same LIVE game at once: exactly one ClockStarted is
// appended, both sessions build, and they agree on the baseline (§3.5).
func TestConcurrentBuildsAppendOneClockStarted(t *testing.T) {
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	store := openingStore(t)
	o, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	sessions := make([]*Session, 8)
	errs := make([]error, 8)
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessions[i], errs[i] = NewSession(context.Background(), Config{GameID: gameID, Store: store, Oracle: o, Opts: conflictOpts, Now: fc.now})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if n := mustFoldCount(t, store, game.EvClockStarted); n != 1 {
		t.Fatalf("%d ClockStarted appended, want exactly 1", n)
	}
	want := sessions[0].st.LastChangeTS
	for i, s := range sessions {
		if !s.st.ClockStartedSet || s.st.LastChangeTS != want {
			t.Fatalf("session %d baseline %d set=%v, want %d", i, s.st.LastChangeTS, s.st.ClockStartedSet, want)
		}
	}
}
