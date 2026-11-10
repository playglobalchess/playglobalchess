// Package transport is the GS-6 WebSocket edge of the game-server
// (SPEC-S2-GAME-SERVER-GS6). It accepts connections, authenticates them, and
// wires the frozen SPEC-S2-PROTOCOL §4A message flow to the already-built server
// behaviour: SEEK → GS-5 matchmaking → MATCHED; MOVE → GS-1 pipeline / GS-3
// session → MOVE_ACCEPTED/MOVE_REJECTED; STATE resync; the social commands
// (RESIGN/ABORT/OFFER_DRAW/DRAW_ACCEPT/TAKEBACK) → the GS-3/GS-3b session
// commands; PING/PONG heartbeat; and teardown.
//
// The server stays the sole authority (NFR-5): the transport does NO chess
// logic. It decodes an intent via the codec seam (codec.DecodeClient), calls the
// matchmaker or the seat's live session, and encodes the AUTHORITATIVE reply
// (codec.EncodeServer) — it never trusts a client-asserted clock/result/ply.
//
// This file holds the socket-agnostic core: the Conn abstraction (so dispatch is
// testable without a real WebSocket, Q-GS6a), the Authenticator and
// TimeControlResolver seams (dev impls until X-3 / the REST pools endpoint land,
// mirroring GS-5's RatingSource), and the live-session Registry. The coder/websocket
// WebSocket Conn implementation and the HTTP server live in a sibling file so
// the choice of WS library stays behind the interface.
package transport

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// Conn is one client connection, abstracted over the WebSocket library (Q-GS6a).
// Dispatch depends only on this, so the whole message flow is tested with a fake
// Conn and no socket. Implementations serialize writes internally (one write
// pump per connection) — ADR-12 one-goroutine-per-connection means Read is
// called from a single goroutine, but Write may be called from the matchmaker's
// MATCHED fan-out to the OTHER seat's Conn, so Write must be safe for concurrent
// use.
type Conn interface {
	// Read returns the next inbound §4A frame, or an error on close/failure.
	Read(ctx context.Context) ([]byte, error)
	// Write sends one §4A frame. Safe for concurrent callers.
	Write(ctx context.Context, frame []byte) error
	// Close tears the connection down (idempotent).
	Close() error
	// RemoteAddr is for logging/rate-limiting; may be "".
	RemoteAddr() string
}

// Authenticator validates an AUTHENTICATE token and yields the user id (X-3 seam,
// Q-GS6c). A dev impl stands in until accounts/X-3 lands, mirroring GS-5's
// RatingSource — the transport depends on the interface, so X-3 swaps in with no
// transport change.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (userID string, err error)
}

// ErrAuthFailed is returned by an Authenticator for a rejected token.
var ErrAuthFailed = errors.New("transport: authentication failed")

// DevAuthenticator accepts any non-empty token and treats it verbatim as the
// user id. DEV ONLY (no signature check) — replaced by the X-3 token validator.
type DevAuthenticator struct{}

// Authenticate returns the token as the user id, rejecting only an empty token.
func (DevAuthenticator) Authenticate(_ context.Context, token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", ErrAuthFailed
	}
	return token, nil
}

// TimeControlResolver expands a TimeControlId string (§3.3, e.g. "blitz-5+3")
// into the clock.TimeControl the session needs. The spec puts this expansion in
// REST GET /v1/pools (X-13), which is not built yet, so the transport depends on
// this seam with a dev impl parsing the documented id conventions; the REST
// resolver swaps in later with no transport change. (Same placeholder-behind-a-
// seam pattern as Authenticator / GS-5's RatingSource.)
type TimeControlResolver interface {
	Resolve(id string) (clock.TimeControl, error)
}

// ConventionResolver parses the documented TimeControlId conventions (§3.3):
//
//	"<name>-<base_min>+<inc_sec>"   e.g. "blitz-5+3" -> base 300000ms, inc 3000ms
//	"custom-<base_ms>+<inc_ms>"      e.g. "custom-300000+3000" (raw ms)
//	"daily-<days>"                   e.g. "daily-3" -> per-move 3 days in ms
//
// It is a stand-in for the REST pools table, not the source of truth; when
// GET /v1/pools lands, a resolver backed by it replaces this.
type ConventionResolver struct{}

// Bounds on a resolvable time control (#93). Each operand is checked BEFORE it
// is scaled to milliseconds, so no id can overflow int64.
const (
	maxBaseMs      = 3 * 60 * 60 * 1000 // 3 h of base time
	maxIncrementMs = 3 * 60 * 1000      // 3 min per-move increment
	maxDailyDays   = 14
)

// Resolve expands an id per the conventions above. An unparseable,
// out-of-range or invalid (clock.TimeControl.Validate) id is an error: the
// transport replies ERROR rather than guessing a clock.
func (r ConventionResolver) Resolve(id string) (clock.TimeControl, error) {
	tc, err := r.parse(id)
	if err != nil {
		return clock.TimeControl{}, err
	}
	if tc.BaseMs > maxBaseMs || tc.IncrementMs > maxIncrementMs {
		return clock.TimeControl{}, fmt.Errorf("transport: time control %q out of range", id)
	}
	if err := tc.Validate(); err != nil {
		return clock.TimeControl{}, fmt.Errorf("transport: time control %q: %w", id, err)
	}
	return tc, nil
}

func (ConventionResolver) parse(id string) (clock.TimeControl, error) {
	switch {
	case strings.HasPrefix(id, "custom-"):
		// custom-<base_ms>+<inc_ms> — raw milliseconds.
		base, inc, err := splitPlus(strings.TrimPrefix(id, "custom-"))
		if err != nil {
			return clock.TimeControl{}, err
		}
		return clock.TimeControl{BaseMs: base, IncrementMs: inc}, nil
	case strings.HasPrefix(id, "daily-"):
		days, err := strconv.ParseInt(strings.TrimPrefix(id, "daily-"), 10, 64)
		if err != nil || days <= 0 || days > maxDailyDays {
			return clock.TimeControl{}, fmt.Errorf("transport: bad daily time control %q", id)
		}
		return clock.TimeControl{PerMoveMs: days * 24 * 60 * 60 * 1000}, nil
	default:
		// "<name>-<base_min>+<inc_sec>" — the common blitz/rapid/bullet form.
		dash := strings.LastIndex(id, "-")
		if dash < 0 {
			return clock.TimeControl{}, fmt.Errorf("transport: unrecognized time control %q", id)
		}
		baseMin, incSec, err := splitPlus(id[dash+1:])
		if err != nil {
			return clock.TimeControl{}, fmt.Errorf("transport: unrecognized time control %q", id)
		}
		if baseMin > maxBaseMs/(60*1000) || incSec > maxIncrementMs/1000 {
			return clock.TimeControl{}, fmt.Errorf("transport: time control %q out of range", id)
		}
		return clock.TimeControl{BaseMs: baseMin * 60 * 1000, IncrementMs: incSec * 1000}, nil
	}
}

// splitPlus parses "<a>+<b>" into two non-negative int64s.
func splitPlus(s string) (a, b int64, err error) {
	plus := strings.IndexByte(s, '+')
	if plus < 0 {
		return 0, 0, fmt.Errorf("transport: expected <a>+<b>, got %q", s)
	}
	a, err = strconv.ParseInt(s[:plus], 10, 64)
	if err != nil || a < 0 {
		return 0, 0, fmt.Errorf("transport: bad left operand in %q", s)
	}
	b, err = strconv.ParseInt(s[plus+1:], 10, 64)
	if err != nil || b < 0 {
		return 0, 0, fmt.Errorf("transport: bad right operand in %q", s)
	}
	return a, b, nil
}

// --- live-session registry --------------------------------------------------

// Registry holds the live GS-3 Sessions on this node, keyed by game id. Both the
// MOVE/command path and reconnection resolve a game through it: GetOrCreate
// builds a Session from the durable stream (NewSession folds GS-4) and starts its
// actor loop the first time a game is touched here, and returns the existing one
// after. Any node can build any game's session from the store (no node affinity,
// §6.3) — the Registry is just this node's live set, not a source of truth.
type Registry struct {
	store    game.EventStore
	oracle   game.LegalityOracle
	resolver TimeControlResolver
	now      func() time.Time // optional clock injector for tests; nil = session default (time.Now)

	mu       sync.Mutex
	sessions map[string]*registered
	rootCtx  context.Context
}

type registered struct {
	s      *session.Session
	cancel context.CancelFunc
}

// NewRegistry builds a Registry. rootCtx bounds every session actor's lifetime
// (cancel it to stop them all on server shutdown).
func NewRegistry(rootCtx context.Context, store game.EventStore, oracle game.LegalityOracle, resolver TimeControlResolver) *Registry {
	return &Registry{
		store:    store,
		oracle:   oracle,
		resolver: resolver,
		sessions: make(map[string]*registered),
		rootCtx:  rootCtx,
	}
}

// GetOrCreate returns the live Session for gameID, building and starting it from
// the durable stream on first touch. The timeControlID expands the clock via the
// resolver (the stream itself carries only the id, §3.3). Concurrent callers for
// the same game get the same Session (mu-guarded).
func (r *Registry) GetOrCreate(gameID, timeControlID string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reg, ok := r.sessions[gameID]; ok {
		return reg.s, nil
	}
	tc, err := r.resolver.Resolve(timeControlID)
	if err != nil {
		return nil, err
	}
	cfg := session.Config{
		GameID: gameID,
		Store:  r.store,
		Oracle: r.oracle,
		Opts:   game.FoldOptions{TimeControl: tc},
	}
	if r.now != nil {
		cfg.Now = r.now
	}
	s, err := session.NewSession(r.rootCtx, cfg)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(r.rootCtx)
	go s.Run(sctx)
	r.sessions[gameID] = &registered{s: s, cancel: cancel}
	return s, nil
}

// Get returns the live Session for gameID if this node has it, else (nil,false).
// Used by the MOVE/command path, which requires an already-live game.
func (r *Registry) Get(gameID string) (*session.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.sessions[gameID]
	if !ok {
		return nil, false
	}
	return reg.s, true
}

// Drop stops and removes a game's session from this node (e.g. after it ends and
// both connections leave). Idempotent.
func (r *Registry) Drop(gameID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reg, ok := r.sessions[gameID]; ok {
		reg.cancel()
		delete(r.sessions, gameID)
	}
}

// CloseAll stops every live session (server shutdown).
func (r *Registry) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, reg := range r.sessions {
		reg.cancel()
		delete(r.sessions, id)
	}
}

// loadStream reads a game's durable event stream (used to read the authoritative
// GameEnded result for a GAME_END frame). Read-only; does not touch live state.
func (r *Registry) loadStream(ctx context.Context, gameID string) ([]game.Event, error) {
	return r.store.Load(ctx, game.StreamIDFor(gameID))
}

// errNotEnded is EndedView's answer for a game that exists but has not ended.
var errNotEnded = errors.New("transport: game has not ended")

// EndedView rebuilds an ENDED game's projection from the durable stream (#102),
// without standing up a live session (no actor, no ticker, nothing to drop).
// It also returns the user seated at each seat (from PlayerJoined), so the
// caller can check who may see it. found is false if the stream is empty (no
// such game); errNotEnded if the game is still running.
func (r *Registry) EndedView(ctx context.Context, gameID string) (snap session.Snapshot, seats map[string]uint8, found bool, err error) {
	events, err := r.loadStream(ctx, gameID)
	if err != nil {
		return session.Snapshot{}, nil, false, err
	}
	if len(events) == 0 {
		return session.Snapshot{}, nil, false, nil
	}
	seats = make(map[string]uint8, 2)
	tcID := ""
	for _, ev := range events {
		switch {
		case ev.GameCreated != nil:
			tcID = ev.GameCreated.TimeControl
		case ev.PlayerJoined != nil:
			seats[ev.PlayerJoined.UserID] = ev.PlayerJoined.SeatIdx
		}
	}
	tc, err := r.resolver.Resolve(tcID)
	if err != nil {
		return session.Snapshot{}, nil, true, err
	}
	st, err := game.FoldWithEngine(ctx, events, game.FoldOptions{TimeControl: tc}, r.oracle)
	if err != nil {
		return session.Snapshot{}, nil, true, err
	}
	if st.Phase != game.PhaseEnded {
		return session.Snapshot{}, seats, true, errNotEnded
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	return session.SnapshotOf(st, now, tc), seats, true, nil
}
