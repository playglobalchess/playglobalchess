package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
const gameID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

// fakeClock is an injectable monotonic time source: time only moves when the
// test advances it, so clock/flag behavior is deterministic. Mutex-guarded
// because the concurrency test reads it (through the actor) from several
// goroutines while the mover advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
func (f *fakeClock) add(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// seedLive appends GameCreated + both PlayerJoined to the store so the game is
// LIVE from startFEN with a 5+3 control, and returns a running Session.
func seedLive(t *testing.T, startpos string, rated bool, now func() time.Time) (*Session, *game.MemStore, context.CancelFunc) {
	t.Helper()
	store := game.NewMemStore()
	ctx := context.Background()
	sid := game.StreamIDFor(gameID)
	for _, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz-standard", Rated: rated,
			Seats:       []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN:    startpos,
			TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		if _, err := store.Append(ctx, sid, ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	o, err := engine.New()
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: now})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	go s.Run(rctx)
	return s, store, cancel
}

// TestFullGameToMate drives a real Scholar's mate through the session.
func TestFullGameToMate(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	moves := []struct {
		seat uint8
		ply  uint32
		uci  string
	}{
		{0, 1, "e2e4"}, {1, 2, "e7e5"},
		{0, 3, "f1c4"}, {1, 4, "b8c6"},
		{0, 5, "d1h5"}, {1, 6, "g8f6"},
		{0, 7, "h5f7"}, // Qxf7#
	}
	var last game.MoveOutcome
	for i, m := range moves {
		fc.add(2 * time.Second)
		out, err := s.SubmitMove(ctx, m.seat, m.ply, m.uci, uint64(i+1))
		if err != nil {
			t.Fatalf("move %d %q: %v", i+1, m.uci, err)
		}
		if !out.Accepted {
			t.Fatalf("move %d %q not accepted: %+v", i+1, m.uci, out)
		}
		last = out
	}
	if last.Terminal == nil || *last.Terminal != game.TermMate {
		t.Fatalf("final terminal = %v, want MATE", last.Terminal)
	}
	// GameEnded was appended to the stream.
	evs, _ := store.Load(ctx, game.StreamIDFor(gameID))
	if evs[len(evs)-1].Type != game.EvGameEnded {
		t.Fatalf("last event = %v, want GameEnded", evs[len(evs)-1].Type)
	}
	// Further moves are rejected (game over).
	out, _ := s.SubmitMove(ctx, 1, 8, "d7d5", 99)
	if !out.Rejected || out.Reason != game.RejectGameOver {
		t.Errorf("post-end move = %+v, want GAME_OVER", out)
	}
	// A snapshot after the game ends carries the real terminal (not the zero
	// value) so a reconnecting client can show the correct game-over reason.
	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("post-end snapshot: %v", err)
	}
	if !snap.Ended || snap.Terminal != game.TermMate {
		t.Errorf("post-end snapshot = {Ended:%v Terminal:%v}, want {true MATE}", snap.Ended, snap.Terminal)
	}
}

// TestFlagUsesAdvancedClock: after real time is burned on prior moves, the idle
// TickFlagCheck must charge against the ADVANCED clock, not the base — i.e. a
// side that has used most of its time flags on a shorter idle than a fresh one
// would. This exercises the clock-advancement-on-move fix (finding 1).
func TestFlagUsesAdvancedClock(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	// 10s base, no increment, no delay — easy arithmetic.
	store := game.NewMemStore()
	ctx := context.Background()
	sid := game.StreamIDFor(gameID)
	for _, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-10+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, ev)
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 10_000, IncrementMs: 0}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fc.now})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(rctx)

	// White spends 8s on move 1 (2s left), black spends 1s (9s left).
	fc.add(8 * time.Second)
	if o1, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !o1.Accepted {
		t.Fatal("move 1 not accepted")
	}
	fc.add(1 * time.Second)
	if o2, _ := s.SubmitMove(ctx, 1, 2, "e7e5", 2); !o2.Accepted {
		t.Fatal("move 2 not accepted")
	}
	// Now it's white to move with ~2s left. An idle of 3s MUST flag white — a
	// stale (base 10s) clock would NOT flag on 3s, so this proves the clock was
	// advanced by the moves.
	snap, _ := s.StateSnapshot(ctx)
	if snap.RemainingMs[0] >= 10_000 {
		t.Fatalf("white clock not advanced: %d ms (still at/above base)", snap.RemainingMs[0])
	}
	fc.add(3 * time.Second)
	ended, err := s.TickFlagCheck(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !ended {
		t.Fatal("white did not flag on a 3s idle despite only ~2s left — clock stale?")
	}
}

// TestRepetitionAutoDraw closes Q-GS5 at the session level: a knight shuffle
// repeated to the fivefold threshold is auto-drawn, because the session threads
// its accumulated Zobrist history into the engine.
func TestRepetitionAutoDraw(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	// Nf3 Nf6 Ng1 Ng8 returns to the start position each full cycle.
	cycle := []struct {
		seat uint8
		uci  string
	}{{0, "g1f3"}, {1, "g8f6"}, {0, "f3g1"}, {1, "f6g8"}}

	ply := uint32(0)
	var drew bool
	for round := 0; round < 6 && !drew; round++ {
		for _, step := range cycle {
			ply++
			fc.add(time.Second)
			out, err := s.SubmitMove(ctx, step.seat, ply, step.uci, uint64(ply))
			if err != nil {
				t.Fatalf("round %d %q: %v", round, step.uci, err)
			}
			if !out.Accepted {
				t.Fatalf("round %d %q not accepted: %+v", round, step.uci, out)
			}
			if out.Terminal != nil && *out.Terminal == game.TermRepetition {
				drew = true
				break
			}
		}
	}
	if !drew {
		t.Fatal("fivefold repetition was never auto-drawn — Q-GS5 wiring broken at the session")
	}
}

// TestSnapshotEqualsFold: the live snapshot equals a fresh fold of the stream
// at every ply (reconnection resync correctness, §3.5).
func TestSnapshotEqualsFold(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}

	for i, uci := range []string{"e2e4", "e7e5", "g1f3"} {
		fc.add(time.Second)
		if _, err := s.SubmitMove(ctx, uint8(i%2), uint32(i+1), uci, uint64(i+1)); err != nil {
			t.Fatalf("move %q: %v", uci, err)
		}
		snap, err := s.StateSnapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		evs, _ := store.Load(ctx, game.StreamIDFor(gameID))
		folded, err := game.FoldWithEngine(ctx, evs, opts, o)
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		if snap.FEN != folded.CurFEN {
			t.Errorf("ply %d: snapshot FEN %q != folded %q", i+1, snap.FEN, folded.CurFEN)
		}
		if snap.Ply != folded.Ply || snap.Turn != folded.TurnIdx {
			t.Errorf("ply %d: snapshot ply/turn (%d,%d) != folded (%d,%d)", i+1, snap.Ply, snap.Turn, folded.Ply, folded.TurnIdx)
		}
		// The clock projection must match the fold too — this is the assertion
		// that catches a stale-clock bug (the session must advance s.clocks
		// incrementally on each accepted move, not leave it at the base).
		if len(snap.RemainingMs) != len(folded.Clock.RemainingMs) {
			t.Fatalf("ply %d: snapshot clock len %d != folded %d", i+1, len(snap.RemainingMs), len(folded.Clock.RemainingMs))
		}
		for seat := range snap.RemainingMs {
			if snap.RemainingMs[seat] != folded.Clock.RemainingMs[seat] {
				t.Errorf("ply %d seat %d: snapshot remaining %d != folded %d (clock not advanced on move?)",
					i+1, seat, snap.RemainingMs[seat], folded.Clock.RemainingMs[seat])
			}
		}
	}
}

// TestFlagOnTimeout: an idle TickFlagCheck past the clock ends the game FLAG.
func TestFlagOnTimeout(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	// Short base so one idle tick flags: use a 1-second control via a custom seed.
	store := game.NewMemStore()
	ctx := context.Background()
	sid := game.StreamIDFor(gameID)
	for _, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "bullet", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "bullet-1+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, ev)
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 1_000, IncrementMs: 0}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fc.now})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(rctx)

	fc.add(2 * time.Second) // side to move idles past its 1s
	ended, err := s.TickFlagCheck(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !ended {
		t.Fatal("idle past the clock did not flag")
	}
	evs, _ := store.Load(ctx, sid)
	// Expect ClockFlagged then GameEnded at the tail.
	if evs[len(evs)-2].Type != game.EvClockFlagged || evs[len(evs)-1].Type != game.EvGameEnded {
		t.Fatalf("tail events = %v,%v, want ClockFlagged,GameEnded", evs[len(evs)-2].Type, evs[len(evs)-1].Type)
	}
}

// TestTakebackUnratedRollsBack: an unrated takeback rolls the projection back a
// ply; a rated game refuses.
func TestTakebackUnratedRollsBack(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(time.Second)
	o1, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	fc.add(time.Second)
	o2, _ := s.SubmitMove(ctx, 1, 2, "e7e5", 2)
	if !o1.Accepted || !o2.Accepted {
		t.Fatal("setup moves not accepted")
	}
	before, _ := s.StateSnapshot(ctx)
	if before.Ply != 2 {
		t.Fatalf("ply before takeback = %d, want 2", before.Ply)
	}
	// Take back 1 ply (black's e5). Seat 1 requests; seat 0 accepts (internally).
	if err := s.Takeback(ctx, 1, 1); err != nil {
		t.Fatalf("takeback: %v", err)
	}
	after, _ := s.StateSnapshot(ctx)
	if after.Ply != 1 {
		t.Fatalf("ply after takeback = %d, want 1", after.Ply)
	}
	if after.Turn != 1 {
		t.Errorf("turn after undoing black's move = %d, want 1 (black to move again)", after.Turn)
	}

	// Rated game refuses.
	fc2 := &fakeClock{t: time.Unix(0, 0)}
	rs, _, rc := seedLive(t, startFEN, true, fc2.now)
	defer rc()
	fc2.add(time.Second)
	rs.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err := rs.Takeback(ctx, 0, 1); err == nil {
		t.Error("rated takeback was allowed, want refusal")
	}
}

// TestPersistFailureNoAdvance: a store append failure leaves the projection
// unchanged (NFR-7 at the session level).
func TestPersistFailureNoAdvance(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	before, _ := s.StateSnapshot(ctx)
	store.FailNextAppend(errFakePersist)
	fc.add(time.Second)
	_, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err == nil {
		t.Fatal("expected persist error")
	}
	after, _ := s.StateSnapshot(ctx)
	if after.FEN != before.FEN || after.Ply != before.Ply {
		t.Fatalf("projection advanced on persist failure: before=%+v after=%+v", before, after)
	}
}

var errFakePersist = &persistErr{}

type persistErr struct{}

func (*persistErr) Error() string { return "fake persist failure" }

// TestConcurrentCallsRaceSafe: many goroutines hammering the session's exported
// methods must be race-free (the actor serializes them). Run with -race.
func TestConcurrentCallsRaceSafe(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	done := make(chan struct{})
	// One mover goroutine does the legal move; others only read snapshots and
	// tick — all marshalled onto the single owner, so no data race.
	go func() {
		fc.add(time.Second)
		s.SubmitMove(ctx, 0, 1, "e2e4", 1)
		close(done)
	}()
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				s.StateSnapshot(ctx)
				s.TickFlagCheck(ctx)
			}
		}()
	}
	<-done
	// Give readers a moment to finish before cancel.
	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("final snapshot: %v", err)
	}
	if snap.Ply != 1 {
		t.Errorf("ply = %d, want 1", snap.Ply)
	}
}

// TestIdempotentReackDoesNotCorrupt covers the finding that a client resend
// (lost ack) must NOT advance/corrupt the projection. The pipeline re-acks an
// already-persisted ply with NewFEN="" / NewZobrist=0; the session must detect
// that (ply does not advance) and leave curFEN / priorKeys / clocks / turn
// untouched.
func TestIdempotentReackDoesNotCorrupt(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	fc.add(time.Second)
	if o, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !o.Accepted {
		t.Fatal("first move not accepted")
	}
	before, _ := s.StateSnapshot(ctx)

	// Resend the SAME (ply 1) move — a routine client retry after a lost ack.
	fc.add(time.Second)
	reack, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err != nil {
		t.Fatalf("re-ack: %v", err)
	}
	if !reack.Accepted {
		t.Fatalf("re-ack not accepted: %+v", reack)
	}
	after, _ := s.StateSnapshot(ctx)
	if after.FEN != before.FEN {
		t.Errorf("re-ack corrupted FEN: %q -> %q", before.FEN, after.FEN)
	}
	if after.Ply != before.Ply {
		t.Errorf("re-ack advanced ply: %d -> %d", before.Ply, after.Ply)
	}
	if after.Turn != before.Turn {
		t.Errorf("re-ack flipped turn: %d -> %d", before.Turn, after.Turn)
	}
	if after.RemainingMs[0] != before.RemainingMs[0] {
		t.Errorf("re-ack changed clock: %d -> %d", before.RemainingMs[0], after.RemainingMs[0])
	}
	// A genuine next move still works (the session is not wedged).
	fc.add(time.Second)
	if o, _ := s.SubmitMove(ctx, 1, 2, "e7e5", 2); !o.Accepted || o.Ply != 2 {
		t.Fatalf("real next move after re-ack failed: %+v", o)
	}
}

// TestDoubleTakeback covers the finding that two takebacks over the same ply
// region must re-fold from the correct prefix (one cut algorithm). A prior
// takeback leaves undone MovePlayed events in the stream with recurring ply
// numbers; the second takeback must still project the right position.
func TestDoubleTakeback(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	play := func(seat uint8, ply uint32, uci string) {
		fc.add(time.Second)
		if o, _ := s.SubmitMove(ctx, seat, ply, uci, uint64(ply)); !o.Accepted {
			t.Fatalf("move %q (ply %d) not accepted: %+v", uci, ply, o)
		}
	}
	// 1.e4 e5 2.Nf3
	play(0, 1, "e2e4")
	play(1, 2, "e7e5")
	play(0, 3, "g1f3")
	afterThree, _ := s.StateSnapshot(ctx)
	if afterThree.Ply != 3 {
		t.Fatalf("ply = %d, want 3", afterThree.Ply)
	}

	// First takeback: undo Nf3 (1 ply) -> back to ply 2, black... no, white to move? ply2 means black just moved, white to move. White re-plays a DIFFERENT move, re-using ply 3.
	if err := s.Takeback(ctx, 0, 1); err != nil {
		t.Fatalf("takeback 1: %v", err)
	}
	back1, _ := s.StateSnapshot(ctx)
	if back1.Ply != 2 {
		t.Fatalf("after takeback1 ply = %d, want 2", back1.Ply)
	}
	// White re-plays ply 3 with a different move (Nc3). This appends a new
	// MovePlayed ply 3, so the stream now has TWO ply-3 moves (the undone Nf3 and
	// the new Nc3) — the recurring-ply condition the finding is about.
	play(0, 3, "b1c3")

	// Second takeback over the same region: undo the new ply-3 move. The cut
	// must target the FIRST ply-3... actually the re-fold must land on the
	// correct current position. We assert it rolls back to ply 2 and the board
	// matches a fresh fold of the retained stream (the real correctness check).
	if err := s.Takeback(ctx, 0, 1); err != nil {
		t.Fatalf("takeback 2: %v", err)
	}
	back2, _ := s.StateSnapshot(ctx)
	if back2.Ply != 2 {
		t.Fatalf("after takeback2 ply = %d, want 2", back2.Ply)
	}
	// The position after both takebacks must equal the position after 1.e4 e5
	// (ply 2) — not corrupted by the stale undone moves. back1 was that position;
	// back2 must match it exactly.
	if back2.FEN != back1.FEN {
		t.Errorf("double-takeback desync: back2 FEN %q != back1 %q", back2.FEN, back1.FEN)
	}
}

// TestResyncPreservesIdleThink covers the finding that a reconstructed session
// must not forget pre-rebuild idle time. We play a move at a known server time,
// then build a FRESH session from the same stream at a much later wall time and
// assert that an immediate TickFlagCheck sees the elapsed idle (and flags if the
// side to move is out of time), rather than restarting the idle baseline from
// the reconstruction instant.
func TestResyncPreservesIdleThink(t *testing.T) {
	ctx := context.Background()
	// 2s base, no increment. Seed + play one move, stamping ServerTS from a clock.
	store := game.NewMemStore()
	sid := game.StreamIDFor(gameID)
	for _, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-2+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, ev)
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 2_000, IncrementMs: 0}}

	// Session A plays White's move at t=1000ms (ServerTS stamped from its clock).
	fcA := &fakeClock{t: time.UnixMilli(0)}
	sA, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fcA.now})
	if err != nil {
		t.Fatalf("session A: %v", err)
	}
	rctxA, cancelA := context.WithCancel(ctx)
	go sA.Run(rctxA)
	fcA.add(1000 * time.Millisecond) // 1s think -> white has 1s left; it's black to move
	if oA, _ := sA.SubmitMove(ctx, 0, 1, "e2e4", 0); !oA.Accepted {
		t.Fatal("session A move not accepted")
	}
	cancelA()

	// Node failover: a FRESH session B rebuilds from the stream at a much later
	// wall time (the move was stamped ~t=1000ms; B boots at t=5000ms). Black (to
	// move) has a 2s base and has now idled ~4s since the move.
	fcB := &fakeClock{t: time.UnixMilli(5000)}
	sB, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fcB.now})
	if err != nil {
		t.Fatalf("session B: %v", err)
	}
	rctxB, cancelB := context.WithCancel(ctx)
	defer cancelB()
	go sB.Run(rctxB)

	// An immediate tick on B must flag black: ~4s idle > black's 2s. If the idle
	// baseline had reset to B's boot instant, no time would have elapsed and it
	// would NOT flag — that is the bug this guards.
	ended, err := sB.TickFlagCheck(ctx)
	if err != nil {
		t.Fatalf("tick B: %v", err)
	}
	if !ended {
		t.Fatal("rebuilt session did not flag despite ~4s idle since the last move " +
			"(idle baseline reset on rebuild?)")
	}
}
