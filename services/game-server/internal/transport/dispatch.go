package transport

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/matchmaking"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

// Server wires a connection's message flow to the matchmaker + session registry.
// It owns the seat→connection map so a MATCHED can be delivered to BOTH seats'
// connections. One Server per node; one Handle goroutine per connection.
type Server struct {
	auth    Authenticator
	mm      *matchmaking.Matchmaker
	reg     *Registry
	now     func() time.Time
	joinTTL time.Duration // join-timeout window (GS-5 ghost-claim close); 0 = default
	// flagInterval is how often the per-game flag ticker calls TickFlagCheck (the
	// idle/disconnect forfeit path §3.4 — GS-3 delegates the ticker to the caller,
	// "the session does not own a ticker"). 0 = default.
	flagInterval time.Duration
	// tickerExited, if set (tests only), is called when a game's flag ticker
	// goroutine returns, so a leak test can see the ticker is gone (#88).
	tickerExited func(gameID string)
	// endReadTimeout overrides endBroadcastTimeout (tests only; 0 = default).
	endReadTimeout time.Duration

	mu    sync.Mutex
	conns map[gameSeAT]Conn // (gameID, seatIdx) → the connection seated there
	// userConns maps an authenticated user id to their current connection. Bound
	// at AUTHENTICATE (before SEEK), so when a LATER seeker pairs with an
	// already-waiting one, the pairing handler can reach the waiting seeker's
	// connection by its user id (match.Players) to deliver MATCHED — the waiting
	// seeker's own handler already returned (enqueued) and will not run again.
	userConns map[string]Conn
	// userGames maps a user id to the live games they are seated in (populated at
	// MATCHED). On reconnect (a fresh AUTHENTICATE for the same user) the server
	// consults this to push STATE and rebind the seat — reconnection is
	// server-initiated because §4A has no client resync frame (Option A).
	userGames map[string]map[string]uint8 // userID → (gameID → seat)
	// joined is closed the FIRST time any seat binds a connection for a game, so
	// the join-timeout can distinguish "a seat actually joined" (even if it later
	// disconnects) from "nobody ever joined" (the true GS-5 ghost-claim case).
	// Without this the timer wrongly aborts a LIVE game on a transient
	// double-disconnect (reviewer C1). Removed by dropGame.
	joined map[string]chan struct{} // gameID → closed-once-joined signal
	// endBroadcast latches that a game's GAME_END has been claimed, so concurrent
	// end paths (a terminal move + the flag tick, or two ending commands) do not
	// each broadcast GAME_END to the clients. Set once by claimGameEnd; cleared by
	// dropGame.
	endBroadcast map[string]bool
	// Write pump bounds (#91): frames queued per connection, and one frame's
	// write deadline. Past either, the connection is closed.
	outboundQueue int
	writeDeadline time.Duration
	// pools is the SEEK allow-list: pool id -> its time-control id (#93).
	pools PoolCatalog
	// missedEnds: user -> game -> the seat that did not get GAME_END live, pushed
	// on the next AUTHENTICATE (#102, ended.go). missedOrder is its expiry FIFO.
	missedEnds  map[string]map[string]missedEnd
	missedOrder []missedKey
}

type gameSeAT struct {
	gameID string
	seat   uint8
}

// NewServer builds the transport server.
func NewServer(auth Authenticator, mm *matchmaking.Matchmaker, reg *Registry) *Server {
	return &Server{
		auth:          auth,
		mm:            mm,
		reg:           reg,
		now:           time.Now,
		conns:         make(map[gameSeAT]Conn),
		userConns:     make(map[string]Conn),
		userGames:     make(map[string]map[string]uint8),
		joined:        make(map[string]chan struct{}),
		endBroadcast:  make(map[string]bool),
		outboundQueue: defaultOutboundQueue,
		writeDeadline: defaultWriteDeadline,
		pools:         DefaultPools(),
		missedEnds:    make(map[string]map[string]missedEnd),
	}
}

// conn-local state for one authenticated connection.
type connState struct {
	userID string
	// games this connection is seated in, so teardown can unbind them.
	seats []gameSeAT
	// The connection's budget for stream-backed lookups of games not live here
	// (RESYNC or MOVE of an ended game, ended.go): a window start and a count.
	// Touched only by this connection's own goroutine.
	lookupWindow time.Time
	lookups      int
}

// Handle runs the per-connection loop for the lifetime of c (ADR-12: one
// goroutine per connection). The FIRST frame must be AUTHENTICATE; after that it
// routes each inbound frame until the connection closes or ctx is cancelled. On
// return it unbinds the connection's seats (the games persist in the store).
func (s *Server) Handle(ctx context.Context, c Conn) {
	// Every write to this connection, from any goroutine, goes through its own
	// bounded queue and single writer (#91): the seat map stores this wrapper, so
	// fan-outs and the flag ticker can never block on a peer that stops reading.
	// On return the queued frames are flushed, then the connection is closed.
	pc := newPumpedConn(c, s.outboundQueue, s.writeDeadline)
	c = pc
	defer pc.CloseAfterFlush()
	st := &connState{}
	defer s.unbindAll(st, c)

	// (1) AUTHENTICATE handshake — no anonymous play (§3.1, §7).
	if !s.authenticate(ctx, c, st) {
		return
	}
	// (2) message loop.
	for {
		frame, err := c.Read(ctx)
		if err != nil {
			return // closed / failed; teardown via defers.
		}
		msg, err := codec.DecodeClient(frame)
		if err != nil {
			// A malformed / wrong-direction frame → PROTOCOL_VIOLATION, keep the
			// connection (the client may recover) — a hard close on every bad
			// frame is a cheap DoS lever.
			s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "malformed frame"))
			continue
		}
		if !s.route(ctx, c, st, msg) {
			return // fatal protocol error already reported; close.
		}
	}
}

// authenticate reads and validates the first frame (must be AUTHENTICATE).
func (s *Server) authenticate(ctx context.Context, c Conn, st *connState) bool {
	frame, err := c.Read(ctx)
	if err != nil {
		return false
	}
	msg, err := codec.DecodeClient(frame)
	if err != nil || msg.Type != codec.CMAuthenticate || msg.Authenticate == nil {
		s.send(ctx, c, errorMsg(codec.ECUnauthenticated, "AUTHENTICATE required first"))
		return false
	}
	uid, err := s.auth.Authenticate(ctx, msg.Authenticate.Token)
	if err != nil {
		s.send(ctx, c, errorMsg(codec.ECUnauthenticated, "authentication failed"))
		return false
	}
	st.userID = uid
	// Register this connection as the user's current one (used by MATCHED
	// delivery to reach an already-waiting seeker, and by reconnect rebinding).
	s.mu.Lock()
	s.userConns[uid] = c
	s.mu.Unlock()
	// Reconnection (Option A): §4A has no client resync frame, so on a successful
	// AUTHENTICATE the server itself restores any live game this user is seated
	// in — rebind the seat to THIS (new) connection and push STATE folded from the
	// durable stream. A fresh user with no live game simply proceeds to SEEK.
	s.resyncUser(ctx, c, st)
	return true
}

// resyncUser rebinds and pushes STATE for every live game the authenticated user
// is seated in (normally one in v1). Called right after AUTHENTICATE.
func (s *Server) resyncUser(ctx context.Context, c Conn, st *connState) {
	s.mu.Lock()
	games := make(map[string]uint8, len(s.userGames[st.userID]))
	for gid, seat := range s.userGames[st.userID] {
		games[gid] = seat
	}
	s.mu.Unlock()
	for gid, seat := range games {
		sess, ok := s.reg.Get(gid)
		if !ok {
			continue // game no longer live on this node.
		}
		snap, err := sess.StateSnapshot(ctx)
		if err != nil {
			continue
		}
		// Rebind the seat to this new connection (the old one may be dead).
		s.bindSeat(c, st, gid, seat)
		s.send(ctx, c, codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{
			GameID: gid, State: snapshotToStateBody(snap, seat),
		}})
	}
	// Games that ended while this user was offline: their ended STATE, once (#102).
	s.pushMissedEnds(ctx, c, st.userID)
}

// route dispatches one authenticated client message. Returns false only on a
// fatal error that should close the connection.
func (s *Server) route(ctx context.Context, c Conn, st *connState, msg codec.ClientMsg) bool {
	switch msg.Type {
	case codec.CMAuthenticate:
		// A second AUTHENTICATE is a protocol error (already authenticated).
		s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "already authenticated"))
		return true
	case codec.CMSeek:
		return s.handleSeek(ctx, c, st, msg.Seek)
	case codec.CMMove:
		return s.handleMove(ctx, c, st, msg.Move)
	case codec.CMResign:
		return s.handleEnding(ctx, c, msg.GameID, endingResign, 0)
	case codec.CMAbort:
		return s.handleEnding(ctx, c, msg.GameID, endingAbort, 0)
	case codec.CMOfferDraw:
		return s.handleEnding(ctx, c, msg.GameID, endingOfferDraw, 0)
	case codec.CMDeclineDraw:
		return s.handleEnding(ctx, c, msg.GameID, endingDeclineDraw, 0)
	case codec.CMTakeback:
		return s.handleEnding(ctx, c, msg.GameID, endingTakeback, 0)
	case codec.CMTakebackAccept, codec.CMTakebackDecline:
		if msg.TakebackReply == nil {
			s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "empty takeback reply"))
			return true
		}
		kind := endingTakebackAccept
		if msg.Type == codec.CMTakebackDecline {
			kind = endingTakebackDecline
		}
		return s.handleEnding(ctx, c, msg.TakebackReply.GameID, kind, msg.TakebackReply.AtPly)
	case codec.CMRematch:
		// Rematch is a new-game flow (matchmaking concern) — out of scope for GS-6.
		s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "rematch not supported yet"))
		return true
	case codec.CMPing:
		s.send(ctx, c, codec.ServerMsg{Type: codec.SMPong, Pong: &codec.PongPayload{
			T: msg.Ping.T, ServerT: uint64(s.now().UnixMilli()),
		}})
		return true
	case codec.CMResync:
		return s.handleResync(ctx, c, st, msg.GameID)
	default:
		s.send(ctx, c, errorMsg(codec.ECProtocolViolation, "unknown message type"))
		return true
	}
}

// --- helpers ----------------------------------------------------------------

// seatOf returns the seat this connection's user occupies in gameID, or
// (0,false) if not seated there (routing/authz: a MOVE must come from a seated
// connection).
func (s *Server) seatOf(c Conn, gameID string) (uint8, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for gs, cc := range s.conns {
		if gs.gameID == gameID && cc == c {
			return gs.seat, true
		}
	}
	return 0, false
}

// bindSeat records that connection c occupies (gameID, seat) for user st.userID,
// updating both the (game,seat)→conn map and the userID→game index (the latter
// drives reconnection resync, Option A).
func (s *Server) bindSeat(c Conn, st *connState, gameID string, seat uint8) {
	gs := gameSeAT{gameID: gameID, seat: seat}
	s.mu.Lock()
	s.conns[gs] = c
	if s.userGames[st.userID] == nil {
		s.userGames[st.userID] = make(map[string]uint8)
	}
	s.userGames[st.userID][gameID] = seat
	s.mu.Unlock()
	// Record on the conn-state only once (a reconnect re-binds the same seat).
	for _, existing := range st.seats {
		if existing == gs {
			return
		}
	}
	st.seats = append(st.seats, gs)
}

// connFor returns the connection seated at (gameID, seat), if any.
func (s *Server) connFor(gameID string, seat uint8) (Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[gameSeAT{gameID: gameID, seat: seat}]
	return c, ok
}

// unbindAll removes this connection's bindings on teardown. The game persists in
// the store and the seat may reconnect, so this only clears entries that still
// point at THIS connection (a reconnect may have rebound seat/userConn to a new
// one). It does NOT remove the userID→game index: that is what lets a reconnect
// find and resync the live game.
func (s *Server) unbindAll(st *connState, c Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop any (game,seat)->conn entries still pointing at this connection.
	for gs, cc := range s.conns {
		if cc == c {
			delete(s.conns, gs)
		}
	}
	// Drop the userConns entry only if it is still this connection.
	if st.userID != "" && s.userConns[st.userID] == c {
		delete(s.userConns, st.userID)
	}
}

// send encodes and writes one authoritative server message, reporting whether
// the write was accepted (the connection's pump enqueued it). A write error is
// not fatal to the server (the connection's Read loop will see the close), so
// most callers ignore the result and one dead connection does not break a
// fan-out to the other seat; broadcastGameEnd uses it to note a missed ending.
func (s *Server) send(ctx context.Context, c Conn, msg codec.ServerMsg) bool {
	frame, err := codec.EncodeServer(msg)
	if err != nil {
		// Encoding our OWN message failed — a server bug (an out-of-domain
		// discriminant, or a STATE/MATCHED body that breaks a §4A.6 invariant).
		// Do not loop; drop it, but LOG it: the client never gets this frame and
		// would otherwise hang with nothing for on-call to find.
		log.Printf("transport: dropped unencodable server message (type %d): %v", msg.Type, err)
		return false
	}
	return c.Write(ctx, frame) == nil
}

// errorMsg builds an ERROR server message.
func errorMsg(code uint8, msg string) codec.ServerMsg {
	return codec.ServerMsg{Type: codec.SMError, Error: &codec.ErrorPayload{Code: code, Msg: msg}}
}

// rejectMove builds a MOVE_REJECTED for the given game/ply/reason.
func rejectMove(gameID string, plyExpected uint32, reason uint8) codec.ServerMsg {
	return codec.ServerMsg{Type: codec.SMMoveRejected, MoveRejected: &codec.MoveRejectedPayload{
		GameID: gameID, PlyExpected: plyExpected, Reason: reason,
	}}
}

// snapshotToStateBody is the one STATE builder (GS-7 §7.4 stateFor): it maps a
// session Snapshot to the v1.3 body for ONE recipient (youSeat is that
// connection's seat, codec.NoSeat if none). Clocks are charged up to server_t
// for the running seat (the session did that at snap.ServerT), so a client
// ticks running_seat down from server_t (#123).
func snapshotToStateBody(snap session.Snapshot, youSeat uint8) codec.StateBody {
	clocks := make([]uint32, len(snap.RemainingMs))
	for i, ms := range snap.RemainingMs {
		if ms < 0 {
			ms = 0
		}
		clocks[i] = uint32(ms)
	}
	seatByte := func(seat int) uint8 {
		if seat < 0 {
			return codec.NoSeat
		}
		return uint8(seat)
	}
	body := codec.StateBody{
		FEN:           snap.FEN,
		Turn:          snap.Turn,
		Clocks:        clocks,
		YouSeat:       youSeat,
		Ply:           snap.Ply,
		Status:        codec.StatusMatched,
		ResultVector:  []uint8{},
		LastMoveUCI:   snap.LastMoveUCI,
		PendingDrawBy: seatByte(snap.DrawOfferBy),
		TakebackBy:    seatByte(snap.TakebackBy),
		TakebackAtPly: snap.TakebackAtPly,
		RunningSeat:   seatByte(snap.RunningSeat),
		ServerT:       uint64(snap.ServerT.UnixMilli()),
	}
	switch {
	case snap.Ended:
		body.Status = codec.StatusEnded
		body.Termination = uint8(snap.Terminal)
		body.ResultVector = make([]uint8, len(snap.ResultVector))
		for i, r := range snap.ResultVector {
			body.ResultVector[i] = uint8(r)
		}
	case snap.Phase == game.PhaseLive:
		body.Status = codec.StatusLive
	}
	return body
}

// gameEndFromSnapshot builds a GAME_END from an ended session snapshot. The
// result vector is read from the stream's GameEnded (the authoritative result),
// which the transport fetches via the session's ended snapshot + the store.
func gameEndMsg(gameID string, rv []game.Result, term game.Termination) codec.ServerMsg {
	results := make([]uint8, len(rv))
	for i, r := range rv {
		results[i] = uint8(r)
	}
	return codec.ServerMsg{Type: codec.SMGameEnd, GameEnd: &codec.GameEndPayload{
		GameID: gameID, ResultVector: results, Termination: uint8(term),
		// rating_delta is X-4's; empty until rating integrates (§ out of scope).
		RatingDelta: nil,
	}}
}
