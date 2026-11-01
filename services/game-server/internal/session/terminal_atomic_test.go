package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// splitEndFailStore fails any write whose FIRST event is GameEnded, i.e. a
// GameEnded appended SEPARATELY after the MovePlayed (the pre-#67 shape). A
// MovePlayed+GameEnded batch (first event MovePlayed) is unaffected.
type splitEndFailStore struct{ *game.MemStore }

var errSplitEnd = errors.New("injected: separate GameEnded write failed")

func (s splitEndFailStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	if ev.Type == game.EvGameEnded {
		return errSplitEnd
	}
	return s.MemStore.Append(ctx, id, seq, ev)
}

func (s splitEndFailStore) AppendBatch(ctx context.Context, id string, seq uint32, evs []game.Event) error {
	if len(evs) > 0 && evs[0].Type == game.EvGameEnded {
		return errSplitEnd
	}
	return s.MemStore.AppendBatch(ctx, id, seq, evs)
}

// faultStore injects AMBIGUOUS failures (commit, then report an error) on the
// next write whose first event has type ambiguousOn, and can fail every Load
// while failLoads is set (so reconcile itself fails).
type faultStore struct {
	*game.MemStore
	ambiguousOn atomic.Int32 // game.EventType+1 to arm; 0 = off
	failLoads   atomic.Bool
}

var (
	errAmbiguous = errors.New("injected: ambiguous store error after commit")
	errLoad      = errors.New("injected: load outage")
)

func (s *faultStore) armAmbiguous(t game.EventType) { s.ambiguousOn.Store(int32(t) + 1) }

func (s *faultStore) hit(first game.EventType) bool {
	return s.ambiguousOn.CompareAndSwap(int32(first)+1, 0)
}

func (s *faultStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	err := s.MemStore.Append(ctx, id, seq, ev)
	if err == nil && s.hit(ev.Type) {
		return errAmbiguous
	}
	return err
}

func (s *faultStore) AppendBatch(ctx context.Context, id string, seq uint32, evs []game.Event) error {
	err := s.MemStore.AppendBatch(ctx, id, seq, evs)
	if err == nil && len(evs) > 0 && s.hit(evs[0].Type) {
		return errAmbiguous
	}
	return err
}

func (s *faultStore) Load(ctx context.Context, id string) ([]game.Event, error) {
	if s.failLoads.Load() {
		return nil, errLoad
	}
	return s.MemStore.Load(ctx, id)
}

// matePrefix is Scholar's mate minus the mating move (h5f7, ply 7, seat 0).
var matePrefix = []string{"e2e4", "e7e5", "f1c4", "b8c6", "d1h5", "g8f6"}

// newSessionOver seeds mem with a fresh 2-seat game (base ms, +3s) and builds a
// live session that writes through st.
func newSessionOver(t *testing.T, mem *game.MemStore, st game.EventStore, baseMs int64) (*Session, *fakeClock, context.CancelFunc) {
	t.Helper()
	ctx := context.Background()
	sid := game.StreamIDFor(gameID)
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "x", Seats: []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: startFEN, TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u1"}},
	} {
		if err := mem.Append(ctx, sid, uint32(i), ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	o, err := engine.New()
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	fc := &fakeClock{t: time.Unix(0, 0)}
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: baseMs, IncrementMs: 3_000}}
	s, err := NewSession(ctx, Config{GameID: gameID, Store: st, Oracle: o, Opts: opts, Now: fc.now})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	go s.Run(rctx)
	return s, fc, cancel
}

// newMateSession is newSessionOver advanced to just before the mating move.
func newMateSession(t *testing.T, mem *game.MemStore, st game.EventStore) (*Session, *fakeClock, context.CancelFunc) {
	t.Helper()
	s, fc, cancel := newSessionOver(t, mem, st, 300_000)
	for i, uci := range matePrefix {
		fc.add(2 * time.Second)
		if _, err := s.SubmitMove(context.Background(), uint8(i%2), uint32(i+1), uci, uint64(i+1)); err != nil {
			t.Fatalf("prefix move %d: %v", i+1, err)
		}
	}
	return s, fc, cancel
}

func streamLenOf(t *testing.T, mem *game.MemStore) int {
	t.Helper()
	evs, err := mem.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return len(evs)
}

// ambiguousMate plays the mating move with an ambiguous commit armed and
// returns the committed stream length.
func ambiguousMate(t *testing.T, s *Session, fs *faultStore, fc *fakeClock) int {
	t.Helper()
	fs.armAmbiguous(game.EvMovePlayed)
	fc.add(2 * time.Second)
	if _, err := s.SubmitMove(context.Background(), 0, 7, "h5f7", 7); !errors.Is(err, errAmbiguous) {
		t.Fatalf("mating move err = %v, want the ambiguous store error", err)
	}
	evs, _ := fs.MemStore.Load(context.Background(), game.StreamIDFor(gameID))
	if evs[len(evs)-1].Type != game.EvGameEnded {
		t.Fatalf("setup: batch did not commit, tail=%v", evs[len(evs)-1].Type)
	}
	return len(evs)
}

// Issue #67: a mating move persists MovePlayed + GameEnded atomically, so a
// store that cannot take a separate GameEnded write still leaves the LIVE
// session ended (no rebuild needed) and the stream complete.
func TestMateMoveCommitsMovePlayedAndGameEndedAtomically(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	s, fc, cancel := newMateSession(t, mem, splitEndFailStore{mem})
	defer cancel()
	sid := game.StreamIDFor(gameID)

	fc.add(2 * time.Second)
	last, err := s.SubmitMove(ctx, 0, 7, "h5f7", 7)
	if err != nil {
		t.Fatalf("mating move: %v", err)
	}
	if !last.Accepted || last.Terminal == nil || *last.Terminal != game.TermMate {
		t.Fatalf("final move = %+v, want accepted MATE", last)
	}
	snap, err := s.StateSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.Ended || snap.Phase != game.PhaseEnded || snap.Terminal != game.TermMate {
		t.Fatalf("live session not ended: Ended=%v Phase=%v Terminal=%v", snap.Ended, snap.Phase, snap.Terminal)
	}
	evs, _ := mem.Load(ctx, sid)
	n := len(evs)
	if evs[n-2].Type != game.EvMovePlayed || evs[n-1].Type != game.EvGameEnded {
		t.Fatalf("tail = %v,%v, want MovePlayed,GameEnded", evs[n-2].Type, evs[n-1].Type)
	}
	rv := evs[n-1].GameEnded.ResultVector
	if rv[0] != game.ResultWin || rv[1] != game.ResultLoss {
		t.Fatalf("result vector = %v, want [win,loss]", rv)
	}
}

// Clean batch failure: nothing committed, session stays live, resend ends the
// game exactly once.
func TestMateMoveBatchFailureCommitsNothingThenResendEnds(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	s, fc, cancel := newMateSession(t, mem, mem)
	defer cancel()
	before := streamLenOf(t, mem)

	mem.FailNextAppend(errors.New("injected outage"))
	fc.add(2 * time.Second)
	if _, err := s.SubmitMove(ctx, 0, 7, "h5f7", 7); err == nil {
		t.Fatal("expected persist error on the mating move")
	}
	if n := streamLenOf(t, mem); n != before {
		t.Fatalf("failed batch committed events: %d -> %d", before, n)
	}
	if snap, _ := s.StateSnapshot(ctx); snap.Ended {
		t.Fatal("session marked ended despite failed write")
	}
	out, err := s.SubmitMove(ctx, 0, 7, "h5f7", 7)
	if err != nil || !out.Accepted || out.Terminal == nil || *out.Terminal != game.TermMate {
		t.Fatalf("resend = %+v, %v; want accepted MATE", out, err)
	}
	if n := streamLenOf(t, mem); n != before+2 {
		t.Fatalf("resend appended %d events, want 2", n-before)
	}
}

// Ambiguous mate commit: the session reconciles to Ended, a flag tick reports
// the game over and appends nothing, the resend is re-acked with the persisted
// UCI AND carries the terminal (so the transport delivers GAME_END), and a
// different move is GAME_OVER.
func TestMateMoveAmbiguousCommitReconcilesAndResendReAcks(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	fs := &faultStore{MemStore: mem}
	s, fc, cancel := newMateSession(t, mem, fs)
	defer cancel()
	committed := ambiguousMate(t, s, fs, fc)

	snap, err := s.StateSnapshot(ctx)
	if err != nil || !snap.Ended || snap.Phase != game.PhaseEnded || snap.Terminal != game.TermMate || snap.FEN == "" || snap.Ply != 7 {
		t.Fatalf("session stale after ambiguous commit: %+v, %v", snap, err)
	}
	fc.add(time.Hour)
	if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
		t.Fatalf("tick on ended game = %v, %v; want ended, no error", ended, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("flag tick appended to an ended stream: %d -> %d", committed, n)
	}
	out, err := s.SubmitMove(ctx, 0, 7, "h5f7", 7)
	if err != nil || !out.Accepted || out.UCI != "h5f7" || out.Terminal == nil || *out.Terminal != game.TermMate {
		t.Fatalf("resend = %+v, %v; want Accepted h5f7 carrying MATE", out, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("resend appended events: %d -> %d", committed, n)
	}
	out, _ = s.SubmitMove(ctx, 1, 8, "d7d5", 8)
	if !out.Rejected || out.Reason != game.RejectGameOver {
		t.Fatalf("post-end new move = %+v, want GAME_OVER", out)
	}
}

// A resign arriving right after an ambiguous mate commit must see the reconciled
// (ended) game: idempotent no-op, nothing appended.
func TestResignOnStaleSessionAppendsNothing(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	fs := &faultStore{MemStore: mem}
	s, fc, cancel := newMateSession(t, mem, fs)
	defer cancel()
	committed := ambiguousMate(t, s, fs, fc)

	if err := s.Resign(ctx, 1); err != nil {
		t.Fatalf("resign on ended game: %v", err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("resign appended to an ended stream: %d -> %d", committed, n)
	}
	if snap, _ := s.StateSnapshot(ctx); snap.Terminal != game.TermMate {
		t.Fatalf("terminal = %v, want MATE (not overwritten by resign)", snap.Terminal)
	}
}

// When reconcile itself fails, the session stays stale and every command fails
// without acting; once the store recovers, the next command reconciles.
func TestReconcileFailureBlocksCommandsUntilItSucceeds(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	fs := &faultStore{MemStore: mem}
	s, fc, cancel := newMateSession(t, mem, fs)
	defer cancel()
	committed := ambiguousMate(t, s, fs, fc)

	fs.failLoads.Store(true)
	fc.add(time.Hour)
	if ended, err := s.TickFlagCheck(ctx); !errors.Is(err, errLoad) || ended {
		t.Fatalf("tick while reconcile fails = %v, %v; want errLoad", ended, err)
	}
	if err := s.Resign(ctx, 1); !errors.Is(err, errLoad) {
		t.Fatalf("resign while reconcile fails = %v; want errLoad", err)
	}
	if _, err := s.SubmitMove(ctx, 1, 8, "d7d5", 8); !errors.Is(err, errLoad) {
		t.Fatalf("move while reconcile fails = %v; want errLoad", err)
	}
	if _, err := s.StateSnapshot(ctx); !errors.Is(err, errLoad) {
		t.Fatalf("snapshot while reconcile fails = %v; want errLoad", err)
	}
	fs.failLoads.Store(false)
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("stream changed while stale: %d -> %d", committed, n)
	}
	if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
		t.Fatalf("tick after recovery = %v, %v; want ended", ended, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("tick after recovery appended: %d -> %d", committed, n)
	}
}

// Ambiguous commit on a NON-terminal move: the session reconciles to the new ply
// and position, the pending draw offer is cleared (a move clears it on the live
// path too), the resend is re-acked without an append, and play continues.
func TestAmbiguousPlainMoveReconcilesAndClearsDrawOffer(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	fs := &faultStore{MemStore: mem}
	s, fc, cancel := newSessionOver(t, mem, fs, 300_000)
	defer cancel()

	if offered, err := s.OfferDrawResult(ctx, 1); err != nil || !offered {
		t.Fatalf("seat 1 offer = %v, %v", offered, err)
	}
	fs.armAmbiguous(game.EvMovePlayed)
	fc.add(2 * time.Second)
	if _, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); !errors.Is(err, errAmbiguous) {
		t.Fatalf("move err = %v, want ambiguous", err)
	}
	committed := streamLenOf(t, mem)
	snap, err := s.StateSnapshot(ctx)
	if err != nil || snap.Ply != 1 || snap.Turn != 1 || snap.Ended {
		t.Fatalf("not reconciled to ply 1: %+v, %v", snap, err)
	}
	// Seat 0 offering now is a FRESH offer, not acceptance of seat 1's stale one.
	if offered, err := s.OfferDrawResult(ctx, 0); err != nil || !offered {
		t.Fatalf("seat 0 offer after reconcile = %v, %v; want a fresh offer", offered, err)
	}
	if snap, _ := s.StateSnapshot(ctx); snap.Ended {
		t.Fatal("stale draw offer was accepted after a move")
	}
	committed++ // the DrawOffered just appended
	out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1)
	if err != nil || !out.Accepted || out.Ply != 1 || out.Terminal != nil {
		t.Fatalf("resend = %+v, %v; want plain re-ack", out, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("resend appended: %d -> %d", committed, n)
	}
	fc.add(2 * time.Second)
	if out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 2); err != nil || !out.Accepted || out.Ply != 2 {
		t.Fatalf("next move = %+v, %v", out, err)
	}
}

// Ambiguous ClockFlagged+GameEnded batch on the idle-flag path: the first tick
// errors, the next tick reports ended and appends NOTHING (no second ending).
func TestAmbiguousFlagBatchSecondTickAppendsNothing(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	fs := &faultStore{MemStore: mem}
	s, fc, cancel := newSessionOver(t, mem, fs, 1_000)
	defer cancel()

	fs.armAmbiguous(game.EvClockFlagged)
	fc.add(5 * time.Second)
	if _, err := s.TickFlagCheck(ctx); !errors.Is(err, errAmbiguous) {
		t.Fatalf("first tick err = %v, want ambiguous", err)
	}
	committed := streamLenOf(t, mem)
	if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
		t.Fatalf("second tick = %v, %v; want ended", ended, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatalf("second tick appended a second ending: %d -> %d", committed, n)
	}
	if snap, _ := s.StateSnapshot(ctx); !snap.Ended || snap.Terminal != game.TermFlag {
		t.Fatalf("snapshot = %+v, want ended FLAG", snap)
	}
}

// Cross-node: another writer committed the mating batch; the pipeline re-acks
// ply s.ply+1 with no FEN. The session reconciles (ended, real FEN) and the
// re-ack carries the terminal.
func TestMateReAckFromOtherWriterReconciles(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	s, fc, cancel := newMateSession(t, mem, mem)
	defer cancel()
	sid := game.StreamIDFor(gameID)

	if err := mem.AppendBatch(ctx, sid, uint32(streamLenOf(t, mem)), []game.Event{
		{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{Ply: 7, SeatIdx: 0, UCI: "h5f7", ClockAfterMs: 290000, ServerTS: 14000}},
		{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: game.MoverResultVector(0, game.TermMate, 2), Termination: game.TermMate}},
	}); err != nil {
		t.Fatalf("other writer: %v", err)
	}
	committed := streamLenOf(t, mem)

	fc.add(2 * time.Second)
	out, err := s.SubmitMove(ctx, 0, 7, "h5f7", 7)
	if err != nil || !out.Accepted || out.Terminal == nil || *out.Terminal != game.TermMate {
		t.Fatalf("re-ack = %+v, %v; want Accepted carrying MATE", out, err)
	}
	snap, _ := s.StateSnapshot(ctx)
	if !snap.Ended || snap.Terminal != game.TermMate || snap.FEN == "" || snap.Ply != 7 {
		t.Fatalf("session not reconciled after foreign re-ack: %+v", snap)
	}
	fc.add(time.Hour)
	if ended, err := s.TickFlagCheck(ctx); err != nil || !ended {
		t.Fatalf("tick = %v, %v; want ended", ended, err)
	}
	if n := streamLenOf(t, mem); n != committed {
		t.Fatal("stream grew after reconcile")
	}
}

// A re-ack that is MORE than one ply ahead of the projection (two moves landed
// via another writer) reconciles instead of returning a permanent desync.
func TestReAckPlyGapReconciles(t *testing.T) {
	ctx := context.Background()
	mem := game.NewMemStore()
	s, fc, cancel := newSessionOver(t, mem, mem, 300_000)
	defer cancel()
	sid := game.StreamIDFor(gameID)
	base := uint32(streamLenOf(t, mem))
	for i, mv := range []game.MovePlayed{
		{Ply: 1, SeatIdx: 0, UCI: "e2e4", ClockAfterMs: 300000, ServerTS: 1000},
		{Ply: 2, SeatIdx: 1, UCI: "e7e5", ClockAfterMs: 300000, ServerTS: 2000},
	} {
		mv := mv
		if err := mem.Append(ctx, sid, base+uint32(i), game.Event{Type: game.EvMovePlayed, MovePlayed: &mv}); err != nil {
			t.Fatalf("other writer: %v", err)
		}
	}
	out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 2)
	if err != nil || !out.Accepted || out.Ply != 2 {
		t.Fatalf("gap re-ack = %+v, %v; want Accepted ply 2", out, err)
	}
	snap, _ := s.StateSnapshot(ctx)
	if snap.Ply != 2 || snap.Turn != 0 || snap.FEN == "" {
		t.Fatalf("not reconciled: %+v", snap)
	}
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 3, "f1c4", 3); err != nil || !out.Accepted || out.Ply != 3 {
		t.Fatalf("move after reconcile = %+v, %v", out, err)
	}
}

// Re-ack on a game that is no longer live is restricted to the FINAL move ply
// with the same uci; anything else is GAME_OVER (pins the re-ack-before-phase
// order in the pipeline).
func TestReAckAfterEndOnlyFinalPlySameUCI(t *testing.T) {
	ctx := context.Background()
	t.Run("resign", func(t *testing.T) {
		mem := game.NewMemStore()
		s, fc, cancel := newSessionOver(t, mem, mem, 300_000)
		defer cancel()
		for i, uci := range []string{"e2e4", "e7e5"} {
			fc.add(time.Second)
			if _, err := s.SubmitMove(ctx, uint8(i), uint32(i+1), uci, 0); err != nil {
				t.Fatalf("move: %v", err)
			}
		}
		if err := s.Resign(ctx, 1); err != nil {
			t.Fatalf("resign: %v", err)
		}
		n := streamLenOf(t, mem)
		out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 0)
		if err != nil || !out.Accepted || out.UCI != "e7e5" || out.Terminal == nil || *out.Terminal != game.TermResign {
			t.Fatalf("final-ply resend = %+v, %v; want Accepted carrying RESIGN", out, err)
		}
		for _, c := range []struct {
			seat uint8
			ply  uint32
			uci  string
		}{{1, 2, "d7d5"}, {0, 1, "e2e4"}} {
			out, _ := s.SubmitMove(ctx, c.seat, c.ply, c.uci, 0)
			if !out.Rejected || out.Reason != game.RejectGameOver {
				t.Fatalf("resend %+v after end = %+v, want GAME_OVER", c, out)
			}
		}
		if streamLenOf(t, mem) != n {
			t.Fatal("re-acks after end appended events")
		}
	})
	t.Run("abort", func(t *testing.T) {
		mem := game.NewMemStore()
		s, fc, cancel := newSessionOver(t, mem, mem, 300_000)
		defer cancel()
		fc.add(time.Second)
		if _, err := s.SubmitMove(ctx, 0, 1, "e2e4", 0); err != nil {
			t.Fatalf("move: %v", err)
		}
		if err := s.Abort(ctx, 1); err != nil {
			t.Fatalf("abort: %v", err)
		}
		out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 0)
		if err != nil || !out.Accepted || out.Terminal == nil || *out.Terminal != game.TermAbort {
			t.Fatalf("final-ply resend after abort = %+v, %v", out, err)
		}
	})
}
