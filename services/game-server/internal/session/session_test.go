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
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz-standard", Rated: rated,
			Seats:       []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN:    startpos,
			TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		if err := store.Append(ctx, sid, uint32(i), ev); err != nil {
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
	// Phase must also flip to Ended — not left at Live (the stale-phase bug: a
	// snapshot that says both Live and Ended is contradictory).
	if snap.Phase != game.PhaseEnded {
		t.Errorf("post-end snapshot Phase = %v, want PhaseEnded (stale-phase bug?)", snap.Phase)
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
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-10+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, uint32(i), ev)
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

// TestRepetitionAutoDraw closes Q-GS5 at the session level AND pins the
// claimable-vs-automatic boundary (the question the independent fix-review
// raised): a repeated position stays LIVE while it is only claimable
// (threefold) and auto-draws only when it becomes automatic (fivefold). A
// regression that auto-drew early, or never, fails here.
//
// Occurrence counting (verified empirically, not assumed): the session
// accumulates a Zobrist key per MovePlayed RESULTING position; the game's
// INITIAL position is never a resulting key, so it is not counted. Driving the
// knight cycle Nf3 Nf6 Ng1 Ng8 (each 4-ply cycle returns to the start and
// revisits the post-Nf3 position), the FIRST position to reach five counted
// occurrences is the post-Nf3 position on the 5th cycle's opening move (ply 17).
// So: no auto-draw through the end of cycle 4 (ply 16), auto-draw at ply 17.
func TestRepetitionAutoDraw(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	cycle := []struct {
		seat uint8
		uci  string
	}{{0, "g1f3"}, {1, "g8f6"}, {0, "f3g1"}, {1, "f6g8"}}

	ply := uint32(0)
	drewAtPly := uint32(0)
	for round := 1; round <= 5 && drewAtPly == 0; round++ {
		for _, step := range cycle {
			ply++
			fc.add(time.Second)
			out, err := s.SubmitMove(ctx, step.seat, ply, step.uci, uint64(ply))
			if err != nil {
				t.Fatalf("cycle %d %q: %v", round, step.uci, err)
			}
			if !out.Accepted {
				t.Fatalf("cycle %d %q not accepted: %+v", round, step.uci, out)
			}
			if out.Terminal != nil && *out.Terminal == game.TermRepetition {
				drewAtPly = ply
				break
			}
			// Boundary: no auto-draw may occur at or before ply 16 (the position
			// has only reached a CLAIMABLE count there; auto-draw before fivefold
			// would mean a claimable draw was wrongly auto-ended).
			if ply <= 16 && out.Terminal != nil {
				t.Fatalf("ply %d auto-ended with terminal %v — a claimable repetition "+
					"must not auto-end before fivefold", ply, *out.Terminal)
			}
		}
	}
	if drewAtPly == 0 {
		t.Fatal("fivefold repetition was never auto-drawn — Q-GS5 wiring broken")
	}
	if drewAtPly != 17 {
		t.Fatalf("auto-draw fired at ply %d, want ply 17 (first position to reach "+
			"five counted occurrences = fivefold-automatic)", drewAtPly)
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
		if snap.Phase != folded.Phase {
			t.Errorf("ply %d: snapshot Phase %v != folded %v", i+1, snap.Phase, folded.Phase)
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
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "bullet", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "bullet-1+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, uint32(i), ev)
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

// takeback runs the two-step GS-7 flow: `by` proposes, `accepter` accepts at
// the proposed at_ply, and the rewind is asserted.
func takeback(t *testing.T, s *Session, by, accepter uint8) TakebackResult {
	t.Helper()
	ctx := context.Background()
	p, err := s.ProposeTakeback(ctx, by)
	if err != nil || p.Effect != game.EffectTakebackProposed {
		t.Fatalf("ProposeTakeback(%d): %v %+v", by, err, p)
	}
	r, err := s.AcceptTakeback(ctx, accepter, p.AtPly)
	if err != nil || r.Effect != game.EffectTakebackRewound {
		t.Fatalf("AcceptTakeback(%d, %d): %v %+v", accepter, p.AtPly, err, r)
	}
	return r
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
	// Seat 1 asks to take back its own e5 (at_ply 2); seat 0 accepts at_ply 2.
	takeback(t, s, 1, 0)
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
	if r, err := rs.ProposeTakeback(ctx, 0); err == nil && r.Effect == game.EffectTakebackProposed {
		t.Error("rated takeback was proposed, want refusal")
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
	if !reack.Accepted || !reack.Reacked {
		t.Fatalf("re-ack = %+v, want Accepted+Reacked", reack)
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
	takeback(t, s, 0, 1) // seat 0 asks about its own Nf3; seat 1 accepts
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
	takeback(t, s, 0, 1)
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
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-2+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		store.Append(ctx, sid, uint32(i), ev)
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

// TestCloseConcurrentWithDispatchNoPanic covers finding 4.2: Close() must be
// safe while other goroutines are dispatching — closing the commands channel
// would panic a racing send. Run with -race; the pass criterion is "no panic".
func TestCloseConcurrentWithDispatchNoPanic(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// These may succeed or return errSessionClosed once Close lands;
			// neither may panic.
			_, _ = s.StateSnapshot(ctx)
			_, _ = s.TickFlagCheck(ctx)
		}()
	}
	// Close while the dispatchers are in flight.
	s.Close()
	wg.Wait()
	// Idempotent + safe to call again.
	s.Close()
}

// TestCancelledCallerStillCommits covers finding 4.1: if the caller's ctx
// cancels AFTER the actor accepted a mutating command, the move must still
// commit durably — the caller's ctx governs delivery, not execution. We cannot
// deterministically hit the exact nanosecond window, so we assert the invariant
// that matters: a move the pipeline persisted is ALWAYS reflected in a
// subsequent snapshot, regardless of what the submitting call returned.
func TestCancelledCallerStillCommits(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	// An already-cancelled context: already-cancelled before the result wait. The command
	// is still delivered and runs to completion (dispatch does not abandon after
	// the send). The move must be persisted and the projection advanced.
	cctx, ccancel := context.WithCancel(ctx)
	fc.add(time.Second)
	// Cancel right away; dispatch's first select may still send (unbuffered, the
	// actor is ready), and the second select no longer honours ctx.
	ccancel()
	_, _ = s.SubmitMove(cctx, 0, 1, "e2e4", 1)

	// Whatever the call returned, durability + projection must agree: either the
	// move committed (ply 1 in the stream AND snapshot) or it did not run at all
	// (ply 0 in both). They must NEVER disagree (the finding was: committed in
	// the store but reported failed / projection stale).
	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	evs, _ := store.Load(ctx, game.StreamIDFor(gameID))
	moves := 0
	for _, e := range evs {
		if e.Type == game.EvMovePlayed {
			moves++
		}
	}
	if int(snap.Ply) != moves {
		t.Fatalf("ack/durability divergence: snapshot ply %d but %d MovePlayed in stream", snap.Ply, moves)
	}
}

// TestRecoverHalfTerminalFlag covers finding 2.1 case (a): a stream left with a
// ClockFlagged and NO GameEnded (the GameEnded append failed mid-flag) must be
// FINISHED on rebuild, not resume as LIVE.
func TestRecoverHalfTerminalFlag(t *testing.T) {
	ctx := context.Background()
	store := game.NewMemStore()
	sid := game.StreamIDFor(gameID)
	// Seed a live game, then a MovePlayed, then a bare ClockFlagged with NO
	// GameEnded — the half-terminal flag stream.
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-10+0",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
		{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: 1, SeatIdx: 0, UCI: "e2e4", ClockAfterMs: 9000, ServerTS: 1000}},
		{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}}, // black flagged, no GameEnded
	} {
		if err := store.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 10_000, IncrementMs: 0}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: func() time.Time { return time.UnixMilli(5000) }})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(rctx)

	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.Ended || snap.Phase != game.PhaseEnded {
		t.Fatalf("half-terminal flag stream rebuilt as not-ended: Ended=%v Phase=%v", snap.Ended, snap.Phase)
	}
	// The recovery appended the missing GameEnded.
	evs, _ := store.Load(ctx, sid)
	if evs[len(evs)-1].Type != game.EvGameEnded {
		t.Fatalf("recovery did not append GameEnded; last=%v", evs[len(evs)-1].Type)
	}
	// A move on the recovered game is rejected (game over), not accepted.
	out, _ := s.SubmitMove(ctx, 0, 2, "e4e5", 2)
	if !out.Rejected {
		t.Errorf("move on recovered-terminal game = %+v, want rejected", out)
	}
}

// TestRecoverHalfTerminalMate covers finding 2.1 case (b): a stream whose last
// MovePlayed is checkmate but has NO GameEnded (the GameEnded append failed
// after the move persisted) must be FINISHED on rebuild, not resume as LIVE.
func TestRecoverHalfTerminalMate(t *testing.T) {
	ctx := context.Background()
	store := game.NewMemStore()
	sid := game.StreamIDFor(gameID)
	// Scholar's mate as persisted MovePlayed events, with NO trailing GameEnded.
	seed := []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Rated: false,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "t-300+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	}
	mates := []struct {
		ply  uint32
		seat uint8
		uci  string
	}{{1, 0, "e2e4"}, {2, 1, "e7e5"}, {3, 0, "f1c4"}, {4, 1, "b8c6"}, {5, 0, "d1h5"}, {6, 1, "g8f6"}, {7, 0, "h5f7"}}
	for _, m := range mates {
		seed = append(seed, game.Event{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: m.ply, SeatIdx: m.seat, UCI: m.uci, ClockAfterMs: 300000, ServerTS: uint64(m.ply) * 1000}})
	}
	for i, ev := range seed {
		if err := store.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: func() time.Time { return time.UnixMilli(10000) }})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(rctx)

	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.Ended || snap.Phase != game.PhaseEnded || snap.Terminal != game.TermMate {
		t.Fatalf("half-terminal mate rebuilt wrong: Ended=%v Phase=%v Terminal=%v", snap.Ended, snap.Phase, snap.Terminal)
	}
	evs, _ := store.Load(ctx, sid)
	if evs[len(evs)-1].Type != game.EvGameEnded {
		t.Fatalf("recovery did not append GameEnded; last=%v", evs[len(evs)-1].Type)
	}
}

// TestDifferentUCIAtPlayedPlyIsStale: GS-7 §5 (reversed from the old
// persisted-UCI echo): a DIFFERENT uci at an already-played ply is STALE_PLY.
func TestDifferentUCIAtPlayedPlyIsStale(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	fc.add(time.Second)
	if o, _ := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !o.Accepted {
		t.Fatal("first move not accepted")
	}
	n := len(mustLoad(t, store))
	out, err := s.SubmitMove(ctx, 0, 1, "d2d4", 2)
	if err != nil {
		t.Fatalf("SubmitMove: %v", err)
	}
	if !out.Rejected || out.Reason != game.RejectStalePly {
		t.Fatalf("different uci at played ply = %+v, want STALE_PLY", out)
	}
	if after := len(mustLoad(t, store)); after != n {
		t.Fatalf("stale move appended events: %d -> %d", n, after)
	}
}

// TestReackAfterTakebackSameUCI: after a rewind the ply recurs; the same UCI
// at the recurring (re-played) ply is re-acked, a different one is STALE_PLY.
func TestReackAfterTakebackSameUCI(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	playOpening(t, s, fc)
	takeback(t, s, 1, 0) // back to ply 1, black to move
	fc.add(time.Second)
	if o, _ := s.SubmitMove(ctx, 1, 2, "c7c5", 3); !o.Accepted {
		t.Fatal("replacement ply-2 move not accepted")
	}
	re, err := s.SubmitMove(ctx, 1, 2, "c7c5", 4)
	if err != nil || !re.Accepted || !re.Reacked {
		t.Fatalf("resend after takeback = %+v %v, want Reacked", re, err)
	}
	st, err := s.SubmitMove(ctx, 1, 2, "e7e5", 5) // the undone move
	if err != nil || !st.Rejected || st.Reason != game.RejectStalePly {
		t.Fatalf("undone move resent = %+v %v, want STALE_PLY", st, err)
	}
}

// TestClockStartedAppendedOnceOnFirstBuild: GS-7 §5.4: a LIVE game appends
// exactly one ClockStarted on first build; a second build appends none.
func TestClockStartedAppendedOnceOnFirstBuild(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	count := func() int {
		n := 0
		for _, e := range mustLoad(t, store) {
			if e.Type == game.EvClockStarted {
				n++
			}
		}
		return n
	}
	if got := count(); got != 1 {
		t.Fatalf("ClockStarted after first build = %d, want 1", got)
	}
	snap, _ := s.StateSnapshot(context.Background())
	if !snap.ClockStartedSet {
		t.Fatal("snapshot ClockStartedSet = false after first build")
	}
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	if _, err := NewSession(context.Background(), Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fc.now}); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("ClockStarted after second build = %d, want 1", got)
	}
}

// TestTakebackSessionEdges: GS-7 §9.6 session-level edges.
func TestTakebackSessionEdges(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	// Propose at ply < 2 is a no-op.
	if r, err := s.ProposeTakeback(ctx, 0); err != nil || r.Effect == game.EffectTakebackProposed {
		t.Fatalf("propose at ply 0 = %+v %v, want no proposal", r, err)
	}
	playOpening(t, s, fc)
	p, err := s.ProposeTakeback(ctx, 1)
	if err != nil || p.Effect != game.EffectTakebackProposed || p.AtPly != 2 {
		t.Fatalf("propose = %+v %v", p, err)
	}
	// The responder's TAKEBACK while a request is pending is ignored.
	if r, _ := s.ProposeTakeback(ctx, 0); r.Effect != game.EffectIgnored {
		t.Fatalf("responder TAKEBACK = %+v, want Ignored", r)
	}
	// Stale accept (wrong at_ply) is ignored and leaves the request pending.
	if r, _ := s.AcceptTakeback(ctx, 0, 1); r.Effect != game.EffectIgnored {
		t.Fatalf("stale accept = %+v, want Ignored", r)
	}
	if r, err := s.AcceptTakeback(ctx, 0, 2); err != nil || r.Effect != game.EffectTakebackRewound || r.Snap.Ply != 1 {
		t.Fatalf("accept = %+v %v", r, err)
	}
	// A repeated accept after a lost ack is ignored (the transport pushes STATE).
	if r, _ := s.AcceptTakeback(ctx, 0, 2); r.Effect != game.EffectIgnored {
		t.Fatalf("repeated accept = %+v, want Ignored", r)
	}
	// Second takeback after the first works.
	fc.add(time.Second)
	if o, _ := s.SubmitMove(ctx, 1, 2, "c7c5", 9); !o.Accepted {
		t.Fatal("re-play not accepted")
	}
	takeback(t, s, 1, 0)
	// Decline path.
	if r, _ := s.AcceptTakeback(ctx, 0, 0); r.Effect != game.EffectIgnored {
		t.Fatalf("accept with nothing pending = %+v", r)
	}
	fc.add(time.Second)
	s.SubmitMove(ctx, 1, 2, "e7e5", 10)
	p, _ = s.ProposeTakeback(ctx, 0)
	if d, _ := s.DeclineTakeback(ctx, 1, p.AtPly); d.Effect != game.EffectTakebackDeclined || d.Snap.TakebackBy != game.NoSeat {
		t.Fatalf("decline = %+v", d)
	}
}
