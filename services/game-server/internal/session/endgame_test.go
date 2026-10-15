package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// GS-3b tests (SPEC-S2-GAME-SERVER-GS3B §6): resign / abort / draw, edge-first.
// All drive the real Session actor + MemStore via seedLive.

func lastEvents(t *testing.T, store *game.MemStore, n int) []game.Event {
	t.Helper()
	evs, err := store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(evs) < n {
		t.Fatalf("stream has %d events, want >= %d", len(evs), n)
	}
	return evs[len(evs)-n:]
}

// --- Resign -----------------------------------------------------------------

func TestResignEndsGameLoserWins(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	if err := s.Resign(ctx, 0); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	ev := lastEvents(t, store, 2)
	if ev[0].Type != game.EvResigned || ev[0].SeatAction.SeatIdx != 0 {
		t.Fatalf("expected Resigned{0}, got %+v", ev[0])
	}
	if ev[1].Type != game.EvGameEnded || ev[1].GameEnded.Termination != game.TermResign {
		t.Fatalf("expected GameEnded{RESIGN}, got %+v", ev[1])
	}
	rv := ev[1].GameEnded.ResultVector
	if rv[0] != game.ResultLoss || rv[1] != game.ResultWin {
		t.Fatalf("result vector = %v, want [loss,win]", rv)
	}
	snap, _ := s.StateSnapshot(ctx)
	if !snap.Ended || snap.Terminal != game.TermResign {
		t.Fatalf("snapshot = {%v %v}, want ended+RESIGN", snap.Ended, snap.Terminal)
	}
	// Idempotent: a resend after end is a no-op (no second ending).
	if err := s.Resign(ctx, 0); err != nil {
		t.Fatalf("resign resend: %v", err)
	}
	if got := lastEvents(t, store, 2); got[0].Type != game.EvResigned {
		t.Fatalf("resend appended extra events: %+v", got)
	}
}

func TestResignBySideNotToMoveIsValid(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	// It is seat 0 (white) to move at the start; seat 1 resigns anyway — valid.
	if err := s.Resign(ctx, 1); err != nil {
		t.Fatalf("resign by side-not-to-move should be valid: %v", err)
	}
	snap, _ := s.StateSnapshot(ctx)
	if !snap.Ended {
		t.Fatal("game should be ended after resign")
	}
}

// --- Abort ------------------------------------------------------------------

func TestAbortBeforeStartSucceeds(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	// ply 0 — before any move. Abort allowed.
	if err := s.Abort(ctx, 0); err != nil {
		t.Fatalf("Abort at ply 0: %v", err)
	}
	ev := lastEvents(t, store, 2)
	if ev[0].Type != game.EvAborted || ev[0].Aborted.By != 0 {
		t.Fatalf("expected Aborted{0}, got %+v", ev[0])
	}
	if ev[1].Type != game.EvGameEnded || ev[1].GameEnded.Termination != game.TermAbort {
		t.Fatalf("expected GameEnded{ABORT}, got %+v", ev[1])
	}
	for i, r := range ev[1].GameEnded.ResultVector {
		if r != game.ResultDraw {
			t.Fatalf("abort result[%d] = %v, want draw", i, r)
		}
	}
}

func TestAbortAfterStartTooLate(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	// Play both opening moves → ply 2 (>= default abortBeforePly=2).
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted {
		t.Fatalf("e4: %v %+v", err, out)
	}
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 2); err != nil || !out.Accepted {
		t.Fatalf("e5: %v %+v", err, out)
	}
	before := lastEvents(t, store, 1)[0]
	err := s.Abort(ctx, 0)
	if !errors.Is(err, ErrAbortTooLate) {
		t.Fatalf("Abort after start = %v, want ErrAbortTooLate", err)
	}
	// Stream untouched.
	if after := lastEvents(t, store, 1)[0]; after.Type != before.Type {
		t.Fatalf("abort-too-late must not append: last was %v now %v", before.Type, after.Type)
	}
	snap, _ := s.StateSnapshot(ctx)
	if snap.Ended {
		t.Fatal("game must still be live after a rejected abort")
	}
}

// --- Draw: happy path -------------------------------------------------------

func TestDrawOfferAccept(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()

	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	if got := lastEvents(t, store, 1)[0]; got.Type != game.EvDrawOffered || got.SeatAction.SeatIdx != 0 {
		t.Fatalf("expected DrawOffered{0}, got %+v", got)
	}
	if err := s.AcceptDraw(ctx, 1); err != nil {
		t.Fatalf("AcceptDraw: %v", err)
	}
	ev := lastEvents(t, store, 2)
	if ev[0].Type != game.EvDrawAccepted || ev[0].SeatAction.SeatIdx != 1 {
		t.Fatalf("expected DrawAccepted{1}, got %+v", ev[0])
	}
	if ev[1].Type != game.EvGameEnded || ev[1].GameEnded.Termination != game.TermDrawAgree {
		t.Fatalf("expected GameEnded{DRAW_AGREE}, got %+v", ev[1])
	}
}

// --- Draw: rejections -------------------------------------------------------

func TestAcceptDrawNoOffer(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	if err := s.AcceptDraw(context.Background(), 1); !errors.Is(err, ErrNoDrawToAccept) {
		t.Fatalf("accept with no offer = %v, want ErrNoDrawToAccept", err)
	}
}

func TestAcceptOwnDrawOfferRejected(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	// Seat 0 cannot accept its OWN offer.
	if err := s.AcceptDraw(ctx, 0); !errors.Is(err, ErrNoDrawToAccept) {
		t.Fatalf("self-accept = %v, want ErrNoDrawToAccept", err)
	}
}

// --- Draw: invalidation on a move (the review-caught edge) ------------------

func TestDrawOfferWithdrawnByOffererMove(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted {
		t.Fatalf("offerer move: %v %+v", err, out)
	}
	// Offer withdrawn by the offerer's own move.
	if err := s.AcceptDraw(ctx, 1); !errors.Is(err, ErrNoDrawToAccept) {
		t.Fatalf("accept after offerer moved = %v, want ErrNoDrawToAccept (withdrawn)", err)
	}
}

func TestDrawOfferDeclinedByOpponentMove(t *testing.T) {
	// The exact edge the spec review caught: a move by the side that did NOT
	// offer must implicitly DECLINE the offer, not leave it live for a later
	// accept. Here seat 1 (not to move) offers; seat 0 (to move) then plays e4,
	// which must clear seat 1's pending offer. A later AcceptDraw by seat 0
	// against seat 1's (now-gone) offer must be rejected.
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.OfferDraw(ctx, 1); err != nil { // seat 1 offers (not its turn)
		t.Fatalf("OfferDraw(1): %v", err)
	}
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted { // seat 0 plays on
		t.Fatalf("opponent (seat 0) move: %v %+v", err, out)
	}
	// Seat 1's offer is now declined by seat 0's move; seat 0 cannot accept it.
	if err := s.AcceptDraw(ctx, 0); !errors.Is(err, ErrNoDrawToAccept) {
		t.Fatalf("accept after opponent moved = %v, want ErrNoDrawToAccept (declined)", err)
	}
}

// --- Draw: mutual offer stream shape (Q-EG5) --------------------------------

func TestMutualDrawOfferPairsInStream(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw(0): %v", err)
	}
	// Seat 1 offers while seat 0's offer is pending → mutual agreement.
	if err := s.OfferDraw(ctx, 1); err != nil {
		t.Fatalf("OfferDraw(1) mutual: %v", err)
	}
	ev := lastEvents(t, store, 3)
	if ev[0].Type != game.EvDrawOffered || ev[0].SeatAction.SeatIdx != 1 {
		t.Fatalf("expected DrawOffered{1} first, got %+v", ev[0])
	}
	if ev[1].Type != game.EvDrawAccepted || ev[1].SeatAction.SeatIdx != 1 {
		t.Fatalf("expected DrawAccepted{1}, got %+v", ev[1])
	}
	if ev[2].Type != game.EvGameEnded || ev[2].GameEnded.Termination != game.TermDrawAgree {
		t.Fatalf("expected GameEnded{DRAW_AGREE}, got %+v", ev[2])
	}
	// The whole stream: every DrawAccepted{X} has a preceding DrawOffered{X}.
	all, _ := store.Load(ctx, game.StreamIDFor(gameID))
	offered := map[uint8]bool{}
	for _, e := range all {
		switch e.Type {
		case game.EvDrawOffered:
			offered[e.SeatAction.SeatIdx] = true
		case game.EvDrawAccepted:
			if !offered[e.SeatAction.SeatIdx] {
				t.Fatalf("DrawAccepted{%d} with no preceding DrawOffered{%d}", e.SeatAction.SeatIdx, e.SeatAction.SeatIdx)
			}
		}
	}
}

func TestRepeatOfferSameSeatIsNoop(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	n1 := len(mustLoad(t, store))
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("repeat OfferDraw: %v", err)
	}
	if n2 := len(mustLoad(t, store)); n2 != n1 {
		t.Fatalf("repeat offer appended an event: %d -> %d", n1, n2)
	}
}

func mustLoad(t *testing.T, store *game.MemStore) []game.Event {
	t.Helper()
	evs, err := store.Load(context.Background(), game.StreamIDFor(gameID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return evs
}

// --- Persist-before-ack (NFR-7): a failed ending append must NOT end the game -

func TestResignPersistFailureDoesNotEnd(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	store.FailNextAppend(errFakePersist)
	if err := s.Resign(ctx, 0); err == nil {
		t.Fatal("expected a persist error from Resign")
	}
	snap, _ := s.StateSnapshot(ctx)
	if snap.Ended {
		t.Fatal("game marked ended despite a failed ending append (NFR-7 violation)")
	}
	// And the game is still playable — a subsequent move works.
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted {
		t.Fatalf("game should still be live after failed resign: %v %+v", err, out)
	}
}

// --- CRITICAL regression (reviewer C1): endgame commands must reject a
// non-LIVE (MATCHED) game, else they persist a Live-only event that makes the
// stream permanently unfoldable. Abort is the one exception (valid from MATCHED).

// seedMatched builds a game stream with GameCreated + ONE PlayerJoined, so the
// game folds to PhaseMatched (not all players joined), and returns a running
// Session on it. The game is NOT live.
func seedMatched(t *testing.T, now func() time.Time) (*Session, *game.MemStore, context.CancelFunc) {
	t.Helper()
	store := game.NewMemStore()
	ctx := context.Background()
	sid := game.StreamIDFor(gameID)
	for i, ev := range []game.Event{
		{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz-standard", Rated: false,
			Seats:       []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN:    startFEN,
			TimeControl: "blitz-5+3",
		}},
		{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 0, UserID: "u0"}},
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

func TestEndgameCommandsRejectedWhenNotLive(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	ctx := context.Background()
	// Resign / OfferDraw / AcceptDraw must all return ErrGameNotLive on a MATCHED
	// game and append NOTHING (the stream stays foldable).
	for _, tc := range []struct {
		name string
		call func(s *Session) error
	}{
		{"resign", func(s *Session) error { return s.Resign(ctx, 0) }},
		{"offer_draw", func(s *Session) error { return s.OfferDraw(ctx, 0) }},
		{"accept_draw", func(s *Session) error { return s.AcceptDraw(ctx, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, cancel := seedMatched(t, fc.now)
			defer cancel()
			n := len(mustLoad(t, store))
			if err := tc.call(s); !errors.Is(err, ErrGameNotLive) {
				t.Fatalf("%s on MATCHED game = %v, want ErrGameNotLive", tc.name, err)
			}
			if after := len(mustLoad(t, store)); after != n {
				t.Fatalf("%s appended into a MATCHED stream (%d -> %d) — poisons the fold", tc.name, n, after)
			}
			// The stream must still be foldable (a fresh NewSession succeeds).
			o, _ := engine.New()
			opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
			if _, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fc.now}); err != nil {
				t.Fatalf("%s left the MATCHED stream unfoldable: %v", tc.name, err)
			}
		})
	}
}

func TestAbortAllowedFromMatched(t *testing.T) {
	// Abort IS valid from MATCHED (the correct pre-start exit; the fold permits
	// Aborted there). It must NOT be rejected by the live guard.
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedMatched(t, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.Abort(ctx, 0); err != nil {
		t.Fatalf("Abort from MATCHED should be valid: %v", err)
	}
	ev := lastEvents(t, store, 2)
	if ev[0].Type != game.EvAborted || ev[1].Type != game.EvGameEnded || ev[1].GameEnded.Termination != game.TermAbort {
		t.Fatalf("expected Aborted+GameEnded{ABORT}, got %+v / %+v", ev[0], ev[1])
	}
	// And the aborted stream rebuilds cleanly.
	o, _ := engine.New()
	opts := game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}
	if _, err := NewSession(ctx, Config{GameID: gameID, Store: store, Oracle: o, Opts: opts, Now: fc.now}); err != nil {
		t.Fatalf("aborted-from-matched stream unfoldable: %v", err)
	}
}

// --- Takeback voids a pending draw offer (reviewer finding 2) ---------------

func TestTakebackVoidsPendingDrawOffer(t *testing.T) {
	// offer-then-takeback-then-accept must NOT end the game: a takeback rewinds
	// game state, so a pending offer is voided. Without the fix, the ephemeral
	// drawOfferBy survived the rewind and AcceptDraw ended the game on a
	// withdrawn offer.
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, _, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	// Play two plies so there is something to take back.
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 0, 1, "e2e4", 1); err != nil || !out.Accepted {
		t.Fatalf("e4: %v %+v", err, out)
	}
	fc.add(time.Second)
	if out, err := s.SubmitMove(ctx, 1, 2, "e7e5", 2); err != nil || !out.Accepted {
		t.Fatalf("e5: %v %+v", err, out)
	}
	// Seat 0 offers a draw (its turn), then a takeback rewinds a ply.
	if err := s.OfferDraw(ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	if err := s.Takeback(ctx, 1, 1); err != nil {
		t.Fatalf("Takeback: %v", err)
	}
	// The offer is voided by the takeback — seat 1 cannot accept it.
	if err := s.AcceptDraw(ctx, 1); !errors.Is(err, ErrNoDrawToAccept) {
		t.Fatalf("accept after takeback = %v, want ErrNoDrawToAccept (offer voided by rewind)", err)
	}
	snap, _ := s.StateSnapshot(ctx)
	if snap.Ended {
		t.Fatal("game ended on a draw offer the takeback should have voided")
	}
}

// --- Idempotency after end --------------------------------------------------

func TestEndingCommandsNoopAfterEnd(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s, store, cancel := seedLive(t, startFEN, false, fc.now)
	defer cancel()
	ctx := context.Background()
	if err := s.Resign(ctx, 0); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	n := len(mustLoad(t, store))
	// All ending commands are no-ops once the game is over.
	_ = s.Resign(ctx, 1)
	_ = s.Abort(ctx, 0)
	_ = s.OfferDraw(ctx, 1)
	_ = s.AcceptDraw(ctx, 1)
	if after := len(mustLoad(t, store)); after != n {
		t.Fatalf("ending commands appended after game end: %d -> %d", n, after)
	}
}
