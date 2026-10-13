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
	// goroutine (the actor). Closed by Close.
	commands chan command

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
// context is cancelled or Close is called. Launch it in its own goroutine:
//
//	go s.Run(ctx)
//
// Exactly one Run per Session. All exported methods block until Run processes
// their command, so no caller ever touches Session fields concurrently.
func (s *Session) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-s.commands:
			if !ok {
				return
			}
			cmd.apply(ctx, s)
		}
	}
}

// Close stops the actor loop after in-flight commands drain. Safe to call once.
func (s *Session) Close() { close(s.commands) }

// command is a unit of work for the actor goroutine. apply runs ON that
// goroutine and may freely touch Session fields.
type command interface {
	apply(ctx context.Context, s *Session)
}

// dispatch sends a command and waits for the owner goroutine to run it.
func (s *Session) dispatch(ctx context.Context, c command, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.commands <- c:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
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
