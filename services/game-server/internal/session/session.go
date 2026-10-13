// Package session is the live in-memory game session (SPEC-S2-GAME-SERVER-GS3):
// the single in-process owner of one running game. It owns the authoritative
// live projection — current position (CurFEN), per-seat clocks, the folded
// Zobrist history, phase/ply/turn — drives the GS-1 move pipeline against the
// GS-2 real engine, applies the resulting events to the event store, and serves
// the §3.5 reconnection resync.
//
// Ownership (Q-GS3A, §3.5): a Session is a single-writer ACTOR. All mutating
// work runs on ONE goroutine (Run); callers submit commands over a channel and
// receive replies. There is no shared mutable Session state touched by many
// goroutines, so the hot path is lock-free and "one owner per game" is a
// structural guarantee, not a discipline reviewers must re-check. This maps onto
// the §6.3 goroutine-per-game model.
//
// Out of scope here (later chunks): the WS transport that feeds this session,
// matchmaking that creates the game stream, the real event store (ADR-10 — this
// uses whatever game.EventStore it is given, the GS-1 stub for now), and
// sharding. No network is opened here.
package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Session owns one live game. Construct with NewSession, then run the actor loop
// with Run and talk to it via SubmitMove / StateSnapshot / TickFlagCheck /
// Takeback (all of which marshal onto the owning goroutine). The exported
// methods are safe to call from any goroutine; the fields are owned by Run and
// must NOT be read or written directly from outside.
type Session struct {
	gameID string
	stream string
	store  game.EventStore
	oracle game.LegalityOracle
	pipe   *game.Pipeline
	opts   game.FoldOptions
	now    func() time.Time // injected clock source (monotonic; fakeable in tests)

	// commands marshals all mutating/reading work onto the single owner
	// goroutine (the actor). It is NEVER closed (that would race dispatchers into
	// a send-on-closed panic); shutdown uses quit instead.
	commands chan command
	// quit is closed once by Close() (guarded by closeOnce) to stop the actor
	// without closing commands.
	quit      chan struct{}
	closeOnce sync.Once

	// --- owner-goroutine-only live projection ---
	curFEN     string
	priorKeys  []uint64 // folded Zobrist history (one key per MovePlayed, in order)
	clocks     clock.Clock
	phase      game.Phase
	ply        uint32
	turn       uint8
	seatCount  int
	rated      bool
	ended      bool
	terminal   game.Termination // the ending condition, set when ended=true
	lastMoveAt time.Time
}

// Config bundles a Session's dependencies. now may be nil (defaults to
// time.Now); tests inject a fake for deterministic clocks.
type Config struct {
	GameID string
	Store  game.EventStore
	Oracle game.LegalityOracle
	Opts   game.FoldOptions
	Now    func() time.Time
}

// NewSession builds a Session by loading the game's durable stream and folding
// it (FoldWithEngine, GS-2) to seed the live projection — CurFEN, clocks,
// phase/ply/turn, and the folded Zobrist history. This is the one O(ply) rebuild
// (resume/construction cost, §3.5), NOT the per-move path. It does not start the
// actor loop; call Run.
func NewSession(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Store == nil || cfg.Oracle == nil {
		return nil, fmt.Errorf("session: nil store or oracle")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	stream := game.StreamIDFor(cfg.GameID)
	events, err := cfg.Store.Load(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("session: load stream: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("session: unknown game %q", cfg.GameID)
	}
	st, err := game.FoldWithEngine(ctx, events, cfg.Opts, cfg.Oracle)
	if err != nil {
		return nil, fmt.Errorf("session: fold: %w", err)
	}
	// RECOVERY (half-terminal stream, §5 + the GS-3 non-atomic-ending finding).
	// A game ends with a GameEnded event, but the session appends it SEPARATELY
	// from the ClockFlagged / terminal MovePlayed that precedes it, so a store
	// hiccup or crash between the two can leave a stream that is terminal in
	// substance but has no GameEnded — which the pure fold projects as still
	// LIVE, letting a finished game resume and accept moves. On rebuild we detect
	// and FINISH such a stream before serving it, so it never resumes live. (A
	// real event store, GS-4, will make the ending append atomic; until then this
	// recovery is the correctness guarantee.) finishIfHalfTerminal appends the
	// missing GameEnded and returns the re-folded state + the terminal.
	// finishIfHalfTerminal appends the missing GameEnded (if the stream is
	// half-terminal) and returns the re-folded state, so the end-state fields
	// below (st.Phase == PhaseEnded, st.Termination) are correct for the
	// recovered case exactly as for a normally-ended stream.
	st, err = finishIfHalfTerminal(ctx, cfg, events, st)
	if err != nil {
		return nil, fmt.Errorf("session: half-terminal recovery: %w", err)
	}
	// Seed the idle baseline from the LAST move's server timestamp, not now():
	// after a node-failover/reconnect rebuild (§3.5), the side to move may be
	// deep in a think, and that already-elapsed idle time must not be forgotten.
	// MovePlayed.ServerTS (§4A.8, epoch ms) is the move's wall-clock instant, so
	// now()-lastMoveAt correctly includes pre-rebuild idle time. A game with no
	// moves yet falls back to now() (the clock has not started ticking on a
	// specific side's think).
	lastMoveAt := now()
	if ts, ok := lastMoveServerTS(events); ok {
		lastMoveAt = time.UnixMilli(int64(ts))
	}
	s := &Session{
		gameID:     cfg.GameID,
		stream:     stream,
		store:      cfg.Store,
		oracle:     cfg.Oracle,
		pipe:       &game.Pipeline{Oracle: cfg.Oracle, Store: cfg.Store, Opts: cfg.Opts},
		opts:       cfg.Opts,
		now:        now,
		commands:   make(chan command),
		quit:       make(chan struct{}),
		curFEN:     st.CurFEN,
		priorKeys:  st.PriorKeys,
		clocks:     st.Clock,
		phase:      st.Phase,
		ply:        st.Ply,
		turn:       st.TurnIdx,
		seatCount:  len(st.Seats),
		rated:      st.Rated,
		ended:      st.Phase == game.PhaseEnded,
		terminal:   st.Termination, // meaningful when already ended at construction
		lastMoveAt: lastMoveAt,
	}
	return s, nil
}

// Run is the actor loop: it owns all Session state and serves commands until the
// context is cancelled or Close() is called. Launch it in its own goroutine:
//
//	go s.Run(ctx)
//
// Exactly one Run per Session. All exported methods block until Run processes
// their command, so no caller ever touches Session fields concurrently. On
// shutdown Run returns; the `commands` channel is NEVER closed (closing it would
// race concurrent dispatchers into a send-on-closed-channel panic), so shutdown
// is signalled via the separate `quit` channel instead.
func (s *Session) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quit:
			return
		case cmd := <-s.commands:
			cmd.apply(s.runCtx(ctx), s)
		}
	}
}

// runCtx returns the context the actor uses for a command's own I/O: the Run
// context. A command's EXECUTION is deliberately governed by the actor's
// lifetime, NOT by the caller's per-call ctx — once the actor accepts a mutating
// command it must run to completion so a durably-committed move is never
// reported to the caller as a failure (the caller's ctx governs delivery only;
// see dispatch).
func (s *Session) runCtx(ctx context.Context) context.Context { return ctx }

// Close stops the actor loop. Idempotent and safe to call concurrently with
// in-flight dispatchers: it closes the `quit` signal (via sync.Once) rather than
// the `commands` channel, so a racing send never panics. Prefer cancelling the
// Run context for shutdown; Close() is the explicit alternative.
func (s *Session) Close() { s.closeOnce.Do(func() { close(s.quit) }) }

// command is a unit of work for the actor goroutine. apply runs ON that
// goroutine and may freely touch Session fields.
type command interface {
	apply(ctx context.Context, s *Session)
}

// dispatch sends a command to the actor and waits for it to run.
//
// Two phases with DIFFERENT cancellation semantics (this is deliberate — it is
// the fix for "a cancelled caller is told the move failed while the actor still
// commits it"):
//   - BEFORE the actor accepts the command, the caller's ctx (or session
//     shutdown) may abandon it — nothing has been committed, so returning an
//     error is correct and safe.
//   - AFTER the send succeeds, the command is owned by the actor and WILL run to
//     completion (it may append to the store and advance the projection). We no
//     longer honour the caller's ctx for the result wait — we wait for `done`
//     (or session shutdown). The caller's ctx governs DELIVERY, not EXECUTION,
//     so a move the server durably commits stays ackable and is never silently
//     lost. (A read-only command is harmless either way; mutating ones must not
//     diverge ack from durability — NFR-7.)
func (s *Session) dispatch(ctx context.Context, c command, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.quit:
		return errSessionClosed
	case s.commands <- c:
		// Accepted by the actor: from here the command runs to completion.
	}
	select {
	case <-s.quit:
		// Session shut down before the command finished. The actor loop has
		// returned, so `done` may never close; report shutdown rather than hang.
		return errSessionClosed
	case <-done:
		return nil
	}
}

// errSessionClosed is returned when a command cannot be completed because the
// session is shutting down.
var errSessionClosed = fmt.Errorf("session: closed")

// finishIfHalfTerminal closes a half-terminal stream on rebuild (the GS-3
// non-atomic-ending finding). A game ends with a GameEnded event appended
// SEPARATELY from the ClockFlagged / terminal MovePlayed before it, so a store
// hiccup or crash between the two can leave a stream that is terminal in
// substance but lacks GameEnded — which the pure fold projects as LIVE. If
// `events` already ends in GameEnded, nothing to do. Otherwise, if the stream is
// half-terminal — (a) its last substantive event is a ClockFlagged, or (b) the
// final move produced a terminal (st.LastMoveTerminal != nil) — append the
// missing GameEnded and re-fold so the returned state is PhaseEnded with the
// right termination, exactly as a normally-ended stream would be. (A real event
// store, GS-4, makes the ending append atomic and this a no-op.)
func finishIfHalfTerminal(ctx context.Context, cfg Config, events []game.Event, st game.State) (game.State, error) {
	if st.Phase == game.PhaseEnded {
		return st, nil // already closed by a committed GameEnded
	}
	stream := game.StreamIDFor(cfg.GameID)

	// Case (a): a trailing ClockFlagged with no GameEnded after it.
	if seat, ok := trailingUnendedFlag(events); ok {
		term, err := game.FlagTermination(ctx, cfg.Oracle, st.CurFEN, seat, len(st.Seats))
		if err != nil {
			return game.State{}, fmt.Errorf("recover flag termination: %w", err)
		}
		rv := flagResultVector(term, seat, len(st.Seats))
		if _, err := cfg.Store.Append(ctx, stream, game.Event{
			Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: term},
		}); err != nil {
			return game.State{}, fmt.Errorf("recover append GameEnded(flag): %w", err)
		}
		return refold(ctx, cfg, stream)
	}

	// Case (b): the last move produced a terminal but no GameEnded was appended.
	if st.LastMoveTerminal != nil {
		mover := lastMover(events)
		rv := winnerFromMover(mover, *st.LastMoveTerminal, len(st.Seats))
		if _, err := cfg.Store.Append(ctx, stream, game.Event{
			Type: game.EvGameEnded, GameEnded: &game.GameEnded{ResultVector: rv, Termination: *st.LastMoveTerminal},
		}); err != nil {
			return game.State{}, fmt.Errorf("recover append GameEnded(terminal move): %w", err)
		}
		return refold(ctx, cfg, stream)
	}

	return st, nil // not half-terminal — a genuinely live game
}

func refold(ctx context.Context, cfg Config, stream string) (game.State, error) {
	events, err := cfg.Store.Load(ctx, stream)
	if err != nil {
		return game.State{}, fmt.Errorf("recover reload: %w", err)
	}
	st, err := game.FoldWithEngine(ctx, events, cfg.Opts, cfg.Oracle)
	if err != nil {
		return game.State{}, fmt.Errorf("recover refold: %w", err)
	}
	return st, nil
}

// trailingUnendedFlag reports the flagged seat of a ClockFlagged that is the last
// substantive game event with no GameEnded after it (and true). A GameEnded
// anywhere after the last ClockFlagged means the flag was already closed.
func trailingUnendedFlag(events []game.Event) (uint8, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case game.EvGameEnded:
			return 0, false // already ended
		case game.EvClockFlagged:
			if events[i].SeatAction != nil {
				return events[i].SeatAction.SeatIdx, true
			}
			return 0, false
		}
	}
	return 0, false
}

// lastMover returns the seat of the most recent MovePlayed (0 if none).
func lastMover(events []game.Event) uint8 {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == game.EvMovePlayed && events[i].MovePlayed != nil {
			return events[i].MovePlayed.SeatIdx
		}
	}
	return 0
}

// lastMoveServerTS returns the ServerTS of the most recent MovePlayed in the
// stream (and true), or (0,false) if no move has been played. Used to seed the
// idle baseline so a reconnect/failover rebuild does not forget pre-rebuild
// think time (§3.5).
func lastMoveServerTS(events []game.Event) (uint64, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == game.EvMovePlayed && events[i].MovePlayed != nil {
			return events[i].MovePlayed.ServerTS, true
		}
	}
	return 0, false
}
