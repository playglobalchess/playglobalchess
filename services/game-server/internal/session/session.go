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
	"log"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"
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
	// cmdTimeout bounds one command's store and engine calls (#125).
	cmdTimeout time.Duration
	// index, with indexed, makes an ending write carry the index changes.
	index   *game.IndexConfig
	indexed game.IndexedStore
	// needReload is set after a command panicked: the next command rebuilds
	// the projection from the store first.
	needReload bool
	// stale is set when a write could not be confirmed: the projection may be
	// behind the store. The next command tries to rebuild, and carries on if
	// it cannot (a seq conflict on its write heals it).
	stale bool

	// tick is the published flag deadline (#182), rewritten by the actor
	// after every command; tickWake is signalled (coalesced, capacity 1) when
	// it changes. One reader: the transport's flag timer.
	tick     atomic.Pointer[Tick]
	tickWake chan struct{}
}

// Tick is when the session next needs a flag check (#182).
type Tick struct {
	// AtMs is the epoch ms at which a tick can first end the game; valid only
	// when Due. No tick can change anything while Due is false.
	AtMs uint64
	Due  bool
	// Ended reports the game is over; the transport still checks it once, to
	// repair a GAME_END broadcast that did not go out (#88).
	Ended bool
}

// NextTick returns the session's current flag deadline. Safe from any goroutine.
func (s *Session) NextTick() Tick {
	if t := s.tick.Load(); t != nil {
		return *t
	}
	return Tick{}
}

// TickChanged is signalled when NextTick changes (coalesced: one pending
// signal at most). It has a single intended reader, the flag timer.
func (s *Session) TickChanged() <-chan struct{} { return s.tickWake }

// Closed is closed when the session's actor has stopped.
func (s *Session) Closed() <-chan struct{} { return s.quit }

// staleRecheck is how soon a stale or reloading session is checked again.
const staleRecheck = time.Second

// publishTick recomputes the flag deadline from s.st. Actor goroutine only.
func (s *Session) publishTick() {
	at, due := game.NextTickMs(s.st, s.opts)
	if s.stale || s.needReload {
		// s.st is behind the store: a deadline derived from it can be minutes
		// late. Re-check after staleRecheck (the old 1 s poll's healing, kept);
		// the check reloads first, then republishes a real deadline.
		at, due = uint64(s.now().Add(staleRecheck).UnixMilli()), true
	}
	next := Tick{AtMs: at, Due: due, Ended: s.st.Phase == game.PhaseEnded}
	if old := s.tick.Load(); old != nil && *old == next {
		return
	}
	s.tick.Store(&next)
	select {
	case s.tickWake <- struct{}{}:
	default:
	}
}

// Config bundles a Session's dependencies. Now may be nil (time.Now).
type Config struct {
	GameID         string
	Store          game.EventStore
	Oracle         game.LegalityOracle
	Opts           game.FoldOptions
	Now            func() time.Time
	AbortBeforePly uint32 // zero means the default (2)
	// CommandTimeout bounds each command's store and engine calls (zero:
	// defaultCommandTimeout). The caller's ctx never governs a command once
	// accepted, so this is the only bound on a hung store call.
	CommandTimeout time.Duration
	// Index, when set, makes every write that contains GameEnded also delete the
	// game's live index rows and put its user_recent_games rows, in the same
	// atomic write (GS-8 §4.3). The store must implement game.IndexedStore.
	Index *game.IndexConfig
}

// defaultCommandTimeout bounds one command (#125).
const defaultCommandTimeout = 10 * time.Second

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
		tickWake:       make(chan struct{}, 1),
		abortBeforePly: cfg.AbortBeforePly,
	}
	if cfg.Index != nil {
		if err := cfg.Index.Validate(); err != nil {
			return nil, fmt.Errorf("session: %w", err)
		}
		is, ok := cfg.Store.(game.IndexedStore)
		if !ok {
			return nil, fmt.Errorf("session: an index is configured but %T does not implement game.IndexedStore", cfg.Store)
		}
		idx := *cfg.Index
		s.index, s.indexed = &idx, is
	}
	s.cmdTimeout = cfg.CommandTimeout
	if s.cmdTimeout <= 0 {
		s.cmdTimeout = defaultCommandTimeout
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
	s.publishTick()
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
		err = s.persist(ctx, d.Events, nowMs)
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
			// Unconfirmed: the write may have landed (the deadline or an outage
			// can also fail the re-read), so the projection may be behind the
			// store. Try a rebuild before the next command (#125).
			s.stale = true
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
	// However Run ends, close the session so a pending or later dispatch
	// returns errSessionClosed instead of blocking on the unread channel (#125).
	defer s.Close()
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

// applyRecovered runs one command under a per-command deadline and a panic
// barrier. A panic is logged, fails that command with errCommandPanic (never a
// silent zero-value success), marks the projection for a rebuild from the store
// before the next command, and keeps the actor and every other game on this
// process alive. finish runs last, so the caller wakes after the result is set.
func (s *Session) applyRecovered(ctx context.Context, cmd command) {
	defer cmd.finish()
	// Any command, a failed or panicked one included, may have changed s.st
	// (a reload, a confirmed commit): republish before the caller wakes.
	defer s.publishTick()
	cctx, cancel := context.WithTimeout(ctx, s.cmdTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("session %s: command %T panicked: %v\n%s", s.gameID, cmd, r, debug.Stack())
			s.needReload = true
			cmd.fail(fmt.Errorf("%w: %v", errCommandPanic, r))
		}
	}()
	if s.needReload {
		if err := s.reload(cctx); err != nil {
			cmd.fail(err)
			return
		}
		s.needReload = false
	}
	if s.stale {
		// Best effort: during an outage the rebuild fails too, and the command
		// then runs as before so its write is still attempted.
		if err := s.reload(cctx); err == nil {
			s.stale = false
		}
	}
	cmd.apply(cctx, s)
}

// Close stops the actor loop. Idempotent; never closes the commands channel.
func (s *Session) Close() { s.closeOnce.Do(func() { close(s.quit) }) }

// command is a unit of work run on the actor goroutine.
type command interface {
	apply(ctx context.Context, s *Session)
	// fail records err as the command's result (a panic, a failed rebuild).
	fail(err error)
	// finish releases the waiting caller; the actor calls it exactly once,
	// after apply or fail, so the caller never reads a half-written result.
	finish()
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
	case <-done:
		return nil
	case <-s.quit:
		// Run's exit closes quit right after a command's finish closes done, so
		// both can be ready: a command that finished is never reported closed.
		select {
		case <-done:
			return nil
		default:
			return errSessionClosed
		}
	}
}

var errSessionClosed = fmt.Errorf("session: closed")

// errCommandPanic is returned to a caller whose command panicked.
var errCommandPanic = fmt.Errorf("session: command panicked")

// execCmd marshals a game.Command onto the actor and runs exec.
type execCmd struct {
	cmd  game.Command
	d    game.Decision
	snap Snapshot
	err  error
	done chan struct{}
}

func (c *execCmd) apply(ctx context.Context, s *Session) {
	c.d, c.err = s.exec(ctx, c.cmd)
	c.snap = s.snapshot()
}

func (c *execCmd) fail(err error) { c.d, c.snap, c.err = game.Decision{}, Snapshot{}, err }
func (c *execCmd) finish()        { close(c.done) }

// run executes cmd on the actor and returns the decision and the snapshot
// taken right after it.
func (s *Session) run(ctx context.Context, cmd game.Command) (game.Decision, Snapshot, error) {
	c := &execCmd{cmd: cmd, done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return game.Decision{}, Snapshot{}, err
	}
	return c.d, c.snap, c.err
}

// persist commits one decision's events. A decision that ends the game also
// carries the index changes (GS-8 §4.3) in the same atomic write, whichever
// path produced the GameEnded (a move, a resign, a flag, an abort, or the lone
// GameEnded of the half-terminal repair). Everything else takes the same writes
// as before.
func (s *Session) persist(ctx context.Context, evs []game.Event, nowMs uint64) error {
	if s.index != nil && game.HasEnding(evs) {
		ops, err := s.index.EndingOps(s.gameID, s.st.Players, time.UnixMilli(int64(nowMs)))
		if err != nil {
			return err
		}
		return s.indexed.AppendIndexed(ctx, s.stream, s.st.NextSeq, evs, ops)
	}
	if len(evs) == 1 {
		return s.store.Append(ctx, s.stream, s.st.NextSeq, evs[0]) // the move hot path
	}
	return s.store.AppendBatch(ctx, s.stream, s.st.NextSeq, evs)
}
