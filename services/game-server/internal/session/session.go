// Package session is the live in-memory game session: the single in-process
// owner of one running game.
//
// GS-7 (SPEC-S2-GAME-SERVER-GS7 §3.4): the session holds ONE game.State and
// changes it only through the canonical loop
//
//	decide -> evolve (before persisting) -> append at State.NextSeq -> commit
//
// so a live session can never disagree with a rebuild from the store, and no
// event it writes can be unfoldable. On a seq conflict it reloads the stream,
// rebuilds with FoldWithEngine and re-decides the same command.
//
// Ownership: a Session is a single-writer ACTOR. All work runs on one
// goroutine (Run); callers submit commands over a channel and wait for replies.
package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Session owns one live game. Construct with NewSession, run the actor with
// Run, and talk to it through the exported methods (safe from any goroutine).
type Session struct {
	gameID string
	stream string
	store  game.EventStore
	oracle game.LegalityOracle
	opts   game.FoldOptions
	now    func() time.Time // wall clock (fakeable); read once per command

	commands  chan command
	quit      chan struct{}
	closeOnce sync.Once

	// st is the game: the only state, owned by the actor goroutine.
	st game.State
	// abortBeforePly is the exclusive ply ceiling under which Abort is allowed
	// (GS-3b Q-EG1). Default 2.
	abortBeforePly uint32
}

// Config bundles a Session's dependencies. Now may be nil (time.Now).
type Config struct {
	GameID         string
	Store          game.EventStore
	Oracle         game.LegalityOracle
	Opts           game.FoldOptions
	Now            func() time.Time
	AbortBeforePly uint32 // zero means the default (2)
}

// Command errors, re-exported from the game package for callers.
var (
	ErrGameNotLive          = game.ErrGameNotLive
	ErrAbortTooLate         = game.ErrAbortTooLate
	ErrNoDrawToAccept       = game.ErrNoDrawToAccept
	ErrTakebackNotAllowed   = game.ErrTakebackNotAllowed
	ErrUnsupportedSeatCount = game.ErrUnsupportedSeatCount
)

// maxConflictRetries bounds reload-and-redecide on ErrSeqConflict.
const maxConflictRetries = 8

// rereadRetries bounds the stream re-reads that confirm an ambiguous commit
// (50, 100 ms apart: about 150 ms worst case).
const rereadRetries = 3

// NewSession loads the game's stream and folds it with the engine. It then
// makes the stream current before serving (§3.5, §3.6): a half-terminal stream
// gets its missing GameEnded, and a LIVE game whose clock has not started gets
// ClockStarted{now} (today's first-clock rule, made durable). Both go through
// the same decide/evolve/append loop as every command.
func NewSession(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Store == nil || cfg.Oracle == nil {
		return nil, fmt.Errorf("session: nil store or oracle")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Session{
		gameID:         cfg.GameID,
		stream:         game.StreamIDFor(cfg.GameID),
		store:          cfg.Store,
		oracle:         cfg.Oracle,
		opts:           cfg.Opts,
		now:            now,
		commands:       make(chan command),
		quit:           make(chan struct{}),
		abortBeforePly: cfg.AbortBeforePly,
	}
	if s.abortBeforePly == 0 {
		s.abortBeforePly = 2 // Q-EG3: before both players have moved once
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	if s.st.Phase == game.PhaseNone {
		return nil, fmt.Errorf("session: unknown game %q", cfg.GameID)
	}
	if _, err := s.exec(ctx, game.CmdFinishHalfTerminal{}); err != nil {
		return nil, fmt.Errorf("session: half-terminal recovery: %w", err)
	}
	if _, err := s.exec(ctx, game.CmdStartClock{}); err != nil {
		return nil, fmt.Errorf("session: start clock: %w", err)
	}
	return s, nil
}

// reload rebuilds the state from the store with the engine.
func (s *Session) reload(ctx context.Context) error {
	events, err := s.store.Load(ctx, s.stream)
	if err != nil {
		return fmt.Errorf("session: load stream: %w", err)
	}
	if len(events) == 0 {
		s.st = game.State{}
		return nil
	}
	st, err := game.FoldWithEngine(ctx, events, s.opts, s.oracle)
	if err != nil {
		return fmt.Errorf("session: fold: %w", err)
	}
	s.st = st
	return nil
}

// exec runs one command through the canonical loop (§3.4). It is the ONLY
// place the session's state changes. On success the decision's events are
// durably committed (persist-before-ack, NFR-7) and s.st includes them.
func (s *Session) exec(ctx context.Context, cmd game.Command) (game.Decision, error) {
	for attempt := 0; ; attempt++ {
		nowMs := uint64(s.now().UnixMilli()) // one read per decide (§3.1)
		d, err := game.Decide(ctx, s.st, cmd, nowMs, s.opts, s.oracle)
		if err != nil || len(d.Events) == 0 {
			return d, err
		}
		// Evolve BEFORE persisting: an event the fold would reject is caught
		// here and never reaches the store.
		next := s.st
		for i := range d.Events {
			if next, err = game.Evolve(next, d.Events[i], d.Verdicts[i], s.opts); err != nil {
				return game.Decision{}, fmt.Errorf("session: decided an unfoldable event %s: %w", d.Events[i].Type, err)
			}
		}
		if len(d.Events) == 1 {
			err = s.store.Append(ctx, s.stream, s.st.NextSeq, d.Events[0]) // the move hot path
		} else {
			err = s.store.AppendBatch(ctx, s.stream, s.st.NextSeq, d.Events)
		}
		if err == nil {
			s.st = next
			return d, nil
		}
		if !errors.Is(err, game.ErrSeqConflict) {
			// An error does not prove the write failed: a store can commit and
			// still report a timeout (#89, #95). Re-read the stream: if our
			// events are there, the command DID happen, so report success and
			// let the caller ack it to everyone. Either way, adopt the store's
			// state so the session is never left behind it.
			if s.committedAnyway(ctx, s.st.NextSeq, d.Events) {
				return d, nil
			}
			return game.Decision{}, fmt.Errorf("session: persist: %w", err)
		}
		if attempt >= maxConflictRetries {
			return game.Decision{}, fmt.Errorf("session: persist: %w", err)
		}
		// Another writer took the seq: rebuild from the store and re-decide the
		// same command (it may now be a re-ack, stale, or still valid).
		if rerr := s.reload(ctx); rerr != nil {
			return game.Decision{}, rerr
		}
	}
}

// committedAnyway reloads the stream after an append error and reports whether
// evs were committed at seqs at, at+1, ... (the error was ambiguous). When the
// reload succeeds the session adopts the store's state either way, so a write
// that did land is never decided against again on a stale state. A reload
// failure, or events that do not match ours, reports false: the caller treats
// the command as not done, as before. The match compares each event by value;
// a store whose round trip normalised a body would only fall back to that.
func (s *Session) committedAnyway(ctx context.Context, at uint32, evs []game.Event) bool {
	// The outage that made the write ambiguous often fails the next read too,
	// so retry the read a few times (bounded: the actor is blocked meanwhile).
	var events []game.Event
	var err error
	for i, wait := 0, 50*time.Millisecond; ; i, wait = i+1, wait*2 {
		if events, err = s.store.Load(ctx, s.stream); err == nil {
			break
		}
		if i >= rereadRetries-1 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
	st, err := game.FoldWithEngine(ctx, events, s.opts, s.oracle)
	if err != nil {
		return false
	}
	s.st = st
	if len(events) < int(at)+len(evs) {
		return false
	}
	for i := range evs {
		if !reflect.DeepEqual(events[int(at)+i], evs[i]) {
			return false
		}
	}
	return true
}

// Run is the actor loop. Exactly one Run per Session.
func (s *Session) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quit:
			return
		case cmd := <-s.commands:
			s.applyRecovered(ctx, cmd)
		}
	}
}

// applyRecovered runs one command with a panic barrier, so a handler bug fails
// that command (its done channel is closed by its own defer) and keeps the
// actor and every other game on this process alive.
func (s *Session) applyRecovered(ctx context.Context, cmd command) {
	defer func() { _ = recover() }()
	cmd.apply(ctx, s)
}

// Close stops the actor loop. Idempotent; never closes the commands channel.
func (s *Session) Close() { s.closeOnce.Do(func() { close(s.quit) }) }

// command is a unit of work run on the actor goroutine.
type command interface {
	apply(ctx context.Context, s *Session)
}

// dispatch sends a command to the actor and waits for it. Before the actor
// accepts it the caller's ctx may abandon it; after, it runs to completion so
// a durably committed move is never reported as a failure.
func (s *Session) dispatch(ctx context.Context, c command, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.quit:
		return errSessionClosed
	case s.commands <- c:
	}
	select {
	case <-s.quit:
		return errSessionClosed
	case <-done:
		return nil
	}
}

var errSessionClosed = fmt.Errorf("session: closed")

// execCmd marshals a game.Command onto the actor and runs exec.
type execCmd struct {
	cmd  game.Command
	d    game.Decision
	snap Snapshot
	err  error
	done chan struct{}
}

func (c *execCmd) apply(ctx context.Context, s *Session) {
	defer close(c.done)
	c.d, c.err = s.exec(ctx, c.cmd)
	c.snap = s.snapshot()
}

// run executes cmd on the actor and returns the decision and the snapshot
// taken right after it.
func (s *Session) run(ctx context.Context, cmd game.Command) (game.Decision, Snapshot, error) {
	c := &execCmd{cmd: cmd, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return game.Decision{}, Snapshot{}, err
	}
	return c.d, c.snap, c.err
}
