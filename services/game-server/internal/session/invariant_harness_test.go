package session

// GS-7 invariant harness (SPEC-S2-GAME-SERVER-GS7 §9.2).
//
// Drives the REAL session over the real cgo engine and a MemStore with random,
// seeded command sequences, and after every step checks:
//
//   I1  every stream the session wrote folds: game.Fold(store) returns no error;
//   I2  the live position is never empty: snapshot FEN != "";
//   I3  the live session equals a rebuild: a fresh NewSession over the same store
//       holds an IDENTICAL game.State (every field, §3.9) and an identical
//       Snapshot;
//   I4  the pure Fold of the store agrees with live on every engine-free field
//       (ply, turn, clocks, history shape, prompts, baselines, NextSeq);
//   I5  the live draw offer matches the offer ORACLE (gs7Rules, the ratified
//       §6.2-§6.3 rules pinned by TestOfferRulesOracle).
//
// This is the GS-7 fix PR's version: the known-failure machinery that kept CI
// green before the fix (knownFailures, triggers, todayRules, GS7_STRICT) is
// gone, so nothing is excused. The former strict repros are plain regression
// tests at the bottom.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// reproOpts matches the FoldOptions seedLive builds its session with (5+3).
var reproOpts = game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}

// startPositions are cycled across seeds so promotion, castling and en passant
// are exercised, not only the standard opening.
var startPositions = []string{
	startFEN,
	"r3k2r/pppppppp/8/8/8/8/PPPPPPPP/R3K2R w KQkq - 0 1",            // castling both sides, both colours
	"8/P1k5/8/8/8/8/1K4p1/8 w - - 0 1",                              // promotion for both colours
	"rnbqkbnr/ppp1p1pp/8/3pPp2/8/8/PPPP1PPP/RNBQKBNR w KQkq f6 0 3", // en passant available
}

// --- harness world ----------------------------------------------------------

type harness struct {
	t        *testing.T
	ctx      context.Context
	store    *game.MemStore
	oracle   game.LegalityOracle
	clock    *fakeClock
	live     *Session
	stop     context.CancelFunc
	rated    bool
	offers   *offerModel
	lastMove *harnessMove
	log      []string
}

// --- the draw-offer oracle --------------------------------------------------

// offerRules are the draw-offer rules the oracle applies: the ratified target,
// SPEC-S2-GAME-SERVER-GS7 §6.2-§6.3. They must not be edited to match code.
type offerRules struct {
	name string
	// moveClears reports whether an accepted new move by mover clears an offer
	// made by offerer.
	moveClears func(mover, offerer uint8) bool
	// endClears: a game end clears the offer.
	endClears bool
	// gated: an offer needs ply >= 2 and no own offer within the last 20 plies.
	gated bool
}

var gs7Rules = offerRules{
	name:       "gs7",
	moveClears: func(mover, offerer uint8) bool { return mover != offerer }, // §6.3: only the responder's move declines
	endClears:  true,                                                        // §3.9: GameEnded clears DrawOfferBy
	gated:      true,                                                        // §6.3: ply >= 2, 20-ply cooldown
}

// offerModel is the expected pending draw offer under a rule set.
type offerModel struct {
	rules   offerRules
	pending int              // seat with a pending offer, or -1
	last    map[uint8]uint32 // ply of each seat's last offer (cooldown)
}

func newOfferModel(rules offerRules) *offerModel {
	return &offerModel{rules: rules, pending: -1, last: map[uint8]uint32{}}
}

// offer applies OFFER_DRAW by seat at ply. It returns true if the offer was a
// mutual agreement (the other seat's offer was pending), which ends the game.
func (m *offerModel) offer(by uint8, ply uint32) (mutual bool) {
	switch {
	case m.pending >= 0 && m.pending != int(by):
		m.pending = -1 // accept; no gate applies to an accept (§6.3 step 1)
		return true
	case m.pending == int(by):
		return false // repeat by the same seat: no-op
	}
	if m.rules.gated {
		if ply < 2 {
			return false
		}
		if l, ok := m.last[by]; ok && int64(l) >= int64(ply)-20 {
			return false
		}
	}
	m.pending = int(by)
	m.last[by] = ply
	return false
}

func (m *offerModel) move(mover uint8) {
	if m.pending >= 0 && m.rules.moveClears(mover, uint8(m.pending)) {
		m.pending = -1
	}
}

// accept applies a successful ACCEPT by seat.
func (m *offerModel) accept(by uint8) {
	if m.pending >= 0 && m.pending != int(by) {
		m.pending = -1
	}
}

// decline applies DECLINE_DRAW by either seat (§6.3): it clears the offer.
func (m *offerModel) decline() { m.pending = -1 }

// rewind applies a takeback to toPly: the offer is cleared and cooldown entries
// above toPly are dropped (§5.5).
func (m *offerModel) rewind(toPly uint32) {
	m.pending = -1
	for s, l := range m.last {
		if l > toPly {
			delete(m.last, s)
		}
	}
}

// restart: a restart keeps the offer (GS-7 §6.1, #159).
func (m *offerModel) restart() {}

// expected is the offer a live session must hold now.
func (m *offerModel) expected(gameEnded bool) int {
	if gameEnded && m.rules.endClears {
		return -1
	}
	return m.pending
}

type harnessMove struct {
	seat uint8
	ply  uint32
	uci  string
}

func newHarness(t *testing.T, startpos string, rated bool) *harness {
	t.Helper()
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, store, cancel := seedLive(t, startpos, rated, fc.now)
	o, err := engine.New()
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return &harness{
		t: t, ctx: context.Background(), store: store, oracle: o, clock: fc,
		live: s, stop: cancel, rated: rated, offers: newOfferModel(gs7Rules),
	}
}

func (h *harness) close() { h.stop() }

// rebuild builds a fresh session over the same store, as a restart would.
func (h *harness) rebuild() (*Session, context.CancelFunc, error) {
	s, err := NewSession(h.ctx, Config{GameID: gameID, Store: h.store, Oracle: h.oracle, Opts: reproOpts, Now: h.clock.now})
	if err != nil {
		return nil, nil, err
	}
	rctx, cancel := context.WithCancel(h.ctx)
	go s.Run(rctx)
	return s, cancel, nil
}

func (h *harness) note(format string, args ...any) {
	h.log = append(h.log, fmt.Sprintf(format, args...))
}

// --- internal state, read on the actor goroutine ----------------------------

type stateCmd struct {
	st   game.State
	done chan struct{}
}

func (c *stateCmd) apply(_ context.Context, s *Session) {
	defer close(c.done)
	c.st = s.st
}

// readState returns the session's whole game.State (read on its actor).
func readState(ctx context.Context, s *Session) (game.State, error) {
	c := &stateCmd{done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return game.State{}, err
	}
	return c.st, nil
}

// --- invariants -------------------------------------------------------------

// check returns "" when every invariant holds, else what broke.
func (h *harness) check() string {
	events, err := h.store.Load(h.ctx, game.StreamIDFor(gameID))
	if err != nil {
		return fmt.Sprintf("harness: store load: %v", err)
	}
	pure, err := game.Fold(events, reproOpts)
	if err != nil {
		return fmt.Sprintf("I1 stream does not fold: %v", err)
	}
	liveSnap, err := h.live.StateSnapshot(h.ctx)
	if err != nil {
		return fmt.Sprintf("harness: live snapshot: %v", err)
	}
	if liveSnap.FEN == "" {
		return "I2 live FEN is empty"
	}
	rb, cancel, err := h.rebuild()
	if err != nil {
		return fmt.Sprintf("I3 rebuild fails: %v", err)
	}
	defer cancel()
	rbSnap, err := rb.StateSnapshot(h.ctx)
	if err != nil {
		return fmt.Sprintf("harness: rebuild snapshot: %v", err)
	}
	liveSt, err := readState(h.ctx, h.live)
	if err != nil {
		return fmt.Sprintf("harness: live state: %v", err)
	}
	rbSt, err := readState(h.ctx, rb)
	if err != nil {
		return fmt.Sprintf("harness: rebuild state: %v", err)
	}
	var broken []string
	if d := fieldDiff(liveSt, rbSt); d != "" {
		broken = append(broken, "I3 live != rebuild State: "+d)
	}
	if !reflect.DeepEqual(liveSnap, rbSnap) {
		broken = append(broken, fmt.Sprintf("I3 live != rebuild Snapshot:\n live %+v\n rb   %+v", liveSnap, rbSnap))
	}
	if d := pureDiff(liveSt, pure); d != "" {
		broken = append(broken, "I4 live != pure Fold: "+d)
	}
	if want := h.offers.expected(liveSnap.Ended); liveSt.DrawOfferBy != want {
		broken = append(broken, fmt.Sprintf("I5 live drawOfferBy %d but the %s offer rules say %d",
			liveSt.DrawOfferBy, h.offers.rules.name, want))
	}
	return strings.Join(broken, "; ")
}

// fieldDiff names every game.State field that differs.
func fieldDiff(a, b game.State) string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var out []string
	for i := 0; i < va.NumField(); i++ {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			out = append(out, fmt.Sprintf("%s %+v vs %+v", va.Type().Field(i).Name, va.Field(i).Interface(), vb.Field(i).Interface()))
		}
	}
	return strings.Join(out, "; ")
}

// pureDiff compares live with the engine-free Fold on every field the pure
// fold is exact for (§3.2): everything except CurFEN, the start key and the
// History engine fields.
func pureDiff(live, pure game.State) string {
	strip := func(s game.State) game.State {
		s.CurFEN = ""
		s.StartZobrist, s.StartKeyed = 0, false // engine-derived (#100)
		h := make([]game.PlyEntry, len(s.History))
		for i, e := range s.History {
			e.FEN, e.Zobrist, e.Terminal = "", 0, nil
			h[i] = e
		}
		s.History = h
		return s
	}
	return fieldDiff(strip(live), strip(pure))
}

// --- legal move generation via the engine -----------------------------------

// legalMoves returns every legal UCI move from fen. The cgo engine exposes no
// move generator, so candidates are built from the side-to-move's pieces (read
// from the FEN board) to every square, and the engine filters them.
func (h *harness) legalMoves(fen string) []string {
	fields := strings.Fields(fen)
	if len(fields) < 2 {
		return nil
	}
	white := fields[1] == "w"
	var moves []string
	rank := 7
	file := 0
	for _, ch := range fields[0] {
		switch {
		case ch == '/':
			rank--
			file = 0
		case ch >= '1' && ch <= '8':
			file += int(ch - '0')
		default:
			isWhite := ch >= 'A' && ch <= 'Z'
			if isWhite == white {
				from := sq(file, rank)
				pawn := ch == 'P' || ch == 'p'
				for tr := 0; tr < 8; tr++ {
					for tf := 0; tf < 8; tf++ {
						if tf == file && tr == rank {
							continue
						}
						uci := from + sq(tf, tr)
						if pawn && (tr == 7 || tr == 0) {
							for _, promo := range []string{"q", "r", "b", "n"} {
								moves = h.addIfLegal(moves, fen, uci+promo)
							}
							continue
						}
						moves = h.addIfLegal(moves, fen, uci)
					}
				}
			}
			file++
		}
	}
	return moves
}

func (h *harness) addIfLegal(moves []string, fen, uci string) []string {
	v, err := h.oracle.Legality(h.ctx, fen, uci, nil)
	if err == nil && v.Legal {
		return append(moves, uci)
	}
	return moves
}

func sq(file, rank int) string { return string(rune('a'+file)) + string(rune('1'+rank)) }

// --- operations -------------------------------------------------------------

// coverage counts what the harness actually exercised, so a green run can be
// read for what it proves.
type coverage struct {
	moves, promotions, castles, enPassant, takebacks, takebackDeclines, offers, declines,
	agreements, flags, restarts, resends int
}

// step performs one random operation. It returns false when the game cannot
// continue (ended, or no legal move).
func (h *harness) step(r *rand.Rand, cov *coverage) bool {
	snap, err := h.live.StateSnapshot(h.ctx)
	if err != nil {
		h.t.Fatalf("snapshot: %v", err)
	}
	if snap.Ended {
		// After the end, only a resend of the last move is interesting: it must
		// be re-acked (#122), not GAME_OVER.
		if h.lastMove != nil && r.Intn(2) == 0 {
			m := h.lastMove
			out, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
			h.note("resend-after-end %s ply %d -> reacked=%v err=%v", m.uci, m.ply, out.Reacked, err)
			if err == nil && !out.Reacked {
				h.t.Fatalf("resend of the last live move after the end was not re-acked: %+v", out)
			}
			cov.resends++
		}
		return false
	}
	switch p := r.Intn(100); {
	case p < 58:
		return h.opMove(r, snap, cov)
	case p < 64:
		h.opResend(cov)
	case p < 74:
		h.opTakeback(r, snap, cov)
	case p < 81:
		h.opOfferDraw(r, snap, cov)
	case p < 84:
		h.opDeclineDraw(r, cov)
	case p < 87:
		h.opAcceptDraw(r)
	case p < 94:
		h.opRestart(cov)
	case p < 99:
		h.opClock(r, cov)
	default:
		by := uint8(r.Intn(2))
		err := h.live.Resign(h.ctx, by)
		h.note("resign by %d -> err=%v", by, err)
	}
	return true
}

func (h *harness) ts() uint64 { return uint64(h.clock.now().UnixMilli()) }

func (h *harness) opMove(r *rand.Rand, snap Snapshot, cov *coverage) bool {
	moves := h.legalMoves(snap.FEN)
	if len(moves) == 0 {
		h.note("no legal moves from %q", snap.FEN)
		return false
	}
	uci := moves[r.Intn(len(moves))]
	// Prefer rare move kinds half the time, so castling, promotion and en passant
	// are exercised instead of being a 1-in-30 random pick.
	var special []string
	for _, mv := range moves {
		var probe coverage
		countSpecial(snap.FEN, mv, &probe)
		if probe.promotions+probe.castles+probe.enPassant > 0 {
			special = append(special, mv)
		}
	}
	if len(special) > 0 && r.Intn(2) == 0 {
		uci = special[r.Intn(len(special))]
	}
	m := &harnessMove{seat: snap.Turn, ply: snap.Ply + 1, uci: uci}
	h.clock.add(time.Duration(100+r.Intn(900)) * time.Millisecond)
	out, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
	h.note("move seat %d ply %d %s -> accepted=%v uci=%q err=%v", m.seat, m.ply, uci, out.Accepted, out.UCI, err)
	if err == nil && out.Accepted {
		if out.UCI != uci || out.Reacked {
			h.t.Fatalf("new move %s acked as %q (reacked=%v)", uci, out.UCI, out.Reacked)
		}
		h.lastMove = m
		h.offers.move(m.seat)
		cov.moves++
		countSpecial(snap.FEN, uci, cov)
	}
	return true
}

// countSpecial classifies a move for the coverage report.
func countSpecial(fen, uci string, cov *coverage) {
	if len(uci) == 5 {
		cov.promotions++
		return
	}
	piece := pieceAt(fen, uci[:2])
	if (piece == 'K' || piece == 'k') && (uci == "e1g1" || uci == "e1c1" || uci == "e8g8" || uci == "e8c8") {
		cov.castles++
		return
	}
	if fields := strings.Fields(fen); len(fields) > 3 && fields[3] != "-" && uci[2:4] == fields[3] && (piece == 'P' || piece == 'p') {
		cov.enPassant++
	}
}

func pieceAt(fen, square string) rune {
	board := strings.Fields(fen)[0]
	file, rank := int(square[0]-'a'), int(square[1]-'1')
	r, f := 7, 0
	for _, ch := range board {
		switch {
		case ch == '/':
			r--
			f = 0
		case ch >= '1' && ch <= '8':
			f += int(ch - '0')
		default:
			if r == rank && f == file {
				return ch
			}
			f++
		}
	}
	return 0
}

func (h *harness) opResend(cov *coverage) {
	if h.lastMove == nil {
		return
	}
	m := h.lastMove
	out, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
	h.note("resend seat %d ply %d %s -> reacked=%v uci=%q err=%v", m.seat, m.ply, m.uci, out.Reacked, out.UCI, err)
	if err == nil && out.Accepted && (!out.Reacked || out.UCI != m.uci) {
		h.t.Fatalf("resend of the live move %s at ply %d: %+v (want a re-ack of it)", m.uci, m.ply, out)
	}
	cov.resends++
}

// opTakeback proposes a takeback and usually resolves it: the other seat
// accepts or declines, the proposer cancels, or it is left pending for later
// operations (a responder's move declines it).
func (h *harness) opTakeback(r *rand.Rand, snap Snapshot, cov *coverage) {
	by := uint8(r.Intn(2))
	res, err := h.live.ProposeTakeback(h.ctx, by)
	h.note("takeback propose by %d -> effect=%v at=%d err=%v", by, res.Effect, res.AtPly, err)
	if h.rated {
		if err == nil {
			h.t.Fatalf("takeback proposed in a rated game: %+v", res)
		}
		return
	}
	if err != nil {
		h.t.Fatalf("takeback propose: %v", err)
	}
	pending, err := h.live.StateSnapshot(h.ctx)
	if err != nil || pending.TakebackBy < 0 {
		return // nothing pending (ply < 2, or the other seat's request is pending)
	}
	proposer, at := uint8(pending.TakebackBy), pending.TakebackAtPly
	other := 1 - proposer
	switch r.Intn(4) {
	case 0, 1:
		res, err := h.live.AcceptTakeback(h.ctx, other, at)
		h.note("takeback accept by %d at %d -> effect=%v err=%v", other, at, res.Effect, err)
		if err == nil && res.Effect == game.EffectTakebackRewound {
			h.offers.rewind(res.Snap.Ply)
			h.lastMove = nil // the undone move is no longer the last live move
			if res.Snap.Ply > 0 {
				h.lastMove = &harnessMove{seat: 1 - res.Snap.Turn, ply: res.Snap.Ply, uci: res.Snap.LastMoveUCI}
			}
			cov.takebacks++
		}
	case 2:
		decliner := other
		if r.Intn(2) == 0 {
			decliner = proposer // a cancel
		}
		res, err := h.live.DeclineTakeback(h.ctx, decliner, at)
		h.note("takeback decline by %d at %d -> effect=%v err=%v", decliner, at, res.Effect, err)
		if err == nil && res.Effect == game.EffectTakebackDeclined {
			cov.takebackDeclines++
		}
	default:
		// a stale accept (wrong at_ply) must change nothing
		res, err := h.live.AcceptTakeback(h.ctx, other, at+1)
		h.note("stale takeback accept at %d -> effect=%v err=%v", at+1, res.Effect, err)
		if err == nil && res.Effect != game.EffectIgnored {
			h.t.Fatalf("stale takeback accept changed state: %v", res.Effect)
		}
	}
	_ = snap
}

func (h *harness) opOfferDraw(r *rand.Rand, snap Snapshot, cov *coverage) {
	by := uint8(r.Intn(2))
	res, err := h.live.OfferDrawResult(h.ctx, by)
	h.note("offer draw by %d at ply %d -> effect=%v err=%v", by, snap.Ply, res.Effect, err)
	if err != nil {
		return
	}
	cov.offers++
	if h.offers.offer(by, snap.Ply) {
		cov.agreements++
	}
}

func (h *harness) opDeclineDraw(r *rand.Rand, cov *coverage) {
	by := uint8(r.Intn(2))
	res, err := h.live.DeclineDraw(h.ctx, by)
	h.note("decline draw by %d -> effect=%v err=%v", by, res.Effect, err)
	if err == nil && res.Effect == game.EffectDrawDeclined {
		h.offers.decline()
		cov.declines++
	}
}

func (h *harness) opAcceptDraw(r *rand.Rand) {
	by := uint8(r.Intn(2))
	err := h.live.AcceptDraw(h.ctx, by)
	h.note("accept draw by %d -> err=%v", by, err)
	if err == nil {
		h.offers.accept(by)
	}
}

func (h *harness) opRestart(cov *coverage) {
	rb, cancel, err := h.rebuild()
	if err != nil {
		h.t.Fatalf("restart failed: %v", err)
	}
	h.stop()
	h.live, h.stop = rb, cancel
	h.offers.restart()
	h.note("restart: live session replaced by a rebuild from the store")
	cov.restarts++
}

// opClock advances the clock and drives the flag check, as the transport's
// ticker does. One jump in four is long enough to flag the side to move.
func (h *harness) opClock(r *rand.Rand, cov *coverage) {
	d := time.Duration(r.Intn(2000)) * time.Millisecond
	if r.Intn(4) == 0 {
		d = 400 * time.Second
	}
	h.clock.add(d)
	ended, err := h.live.TickFlagCheck(h.ctx)
	h.note("advance clock %v + flag tick -> ended=%v err=%v", d, ended, err)
	if ended {
		cov.flags++
	}
}

// --- the property test ------------------------------------------------------

const harnessSteps = 40

// harnessSeeds is 40 per configuration in CI; GS7_SEEDS=N runs a larger sweep
// locally (e.g. GS7_SEEDS=1000).
var harnessSeeds = func() int64 {
	var n int64 = 40
	if v := os.Getenv("GS7_SEEDS"); v != "" {
		fmt.Sscan(v, &n)
	}
	return n
}()

func TestGS7InvariantHarness(t *testing.T) {
	cov := map[string]*coverage{"unrated": {}, "rated": {}}
	for seed := int64(1); seed <= harnessSeeds; seed++ {
		start := startPositions[int(seed-1)%len(startPositions)]
		for _, rated := range []bool{false, true} {
			name := "unrated"
			if rated {
				name = "rated"
			}
			c := cov[name]
			t.Run(fmt.Sprintf("seed%d/%s", seed, name), func(t *testing.T) {
				h := newHarness(t, start, rated)
				defer h.close()
				r := rand.New(rand.NewSource(seed))
				for i := 0; i < harnessSteps; i++ {
					cont := h.step(r, c)
					if v := h.check(); v != "" {
						t.Fatalf("invariant broken after %d steps: %s\nstart %q\nsequence:\n  %s",
							i+1, v, start, strings.Join(h.log, "\n  "))
					}
					if !cont {
						return
					}
				}
			})
		}
	}
	for name, c := range cov {
		t.Logf("coverage %s: %+v", name, *c)
	}
	u := cov["unrated"]
	if u.takebacks == 0 || u.offers == 0 || u.flags == 0 || u.restarts == 0 || u.resends == 0 || u.declines == 0 {
		t.Fatalf("the harness did not exercise every operation: %+v", *u)
	}
}

// --- regression tests (were the strict repros before the fix) ---------------

func mustClean(t *testing.T, h *harness) {
	t.Helper()
	if v := h.check(); v != "" {
		t.Fatalf("%s\nsequence:\n  %s", v, strings.Join(h.log, "\n  "))
	}
}

func playMoves(t *testing.T, h *harness, ucis ...string) {
	t.Helper()
	for _, u := range ucis {
		snap, err := h.live.StateSnapshot(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		h.clock.add(time.Second)
		out, err := h.live.SubmitMove(h.ctx, snap.Turn, snap.Ply+1, u, h.ts())
		if err != nil || !out.Accepted {
			t.Fatalf("move %s: %+v err=%v", u, out, err)
		}
		h.offers.move(snap.Turn)
	}
}

// TestRegression85_MutualDrawStreamFolds: both seats offer a draw. The ended
// stream must fold, with DrawAccepted and no second DrawOffered (§6.1).
func TestRegression85_MutualDrawStreamFolds(t *testing.T) {
	h := newHarness(t, startFEN, false)
	defer h.close()
	playMoves(t, h, "e2e4", "e7e5")
	for _, by := range []uint8{0, 1} {
		if err := h.live.OfferDraw(h.ctx, by); err != nil {
			t.Fatalf("OfferDraw(%d): %v", by, err)
		}
		h.offers.offer(by, 2)
	}
	snap, _ := h.live.StateSnapshot(h.ctx)
	if !snap.Ended || snap.Terminal != game.TermDrawAgree {
		t.Fatalf("mutual offer did not end in DRAW_AGREE: %+v", snap)
	}
	mustClean(t, h)
}

// TestRegression84_MoveAfterTakeback: e4 e5 Nf3, White takes Nf3 back, Black
// accepts, then Nc3. The new move is acked as itself, and a rebuild agrees.
func TestRegression84_MoveAfterTakeback(t *testing.T) {
	h := newHarness(t, startFEN, false)
	defer h.close()
	playMoves(t, h, "e2e4", "e7e5", "g1f3")
	p, err := h.live.ProposeTakeback(h.ctx, 0)
	if err != nil || p.Effect != game.EffectTakebackProposed || p.AtPly != 3 {
		t.Fatalf("propose: %+v err=%v", p, err)
	}
	a, err := h.live.AcceptTakeback(h.ctx, 1, 3)
	if err != nil || a.Effect != game.EffectTakebackRewound || a.Snap.Ply != 2 || a.Snap.LastMoveUCI != "e7e5" {
		t.Fatalf("accept: %+v err=%v", a, err)
	}
	mustClean(t, h)
	out, err := h.live.SubmitMove(h.ctx, 0, 3, "b1c3", h.ts())
	if err != nil || !out.Accepted || out.Reacked || out.UCI != "b1c3" {
		t.Fatalf("move after takeback: %+v err=%v (want b1c3 accepted as new)", out, err)
	}
	mustClean(t, h)
}

// TestRegression157_FlaggedClockSurvivesRebuild: white's clock runs out and the
// flag tick ends the game; a rebuild reports the same (zeroed) clock.
func TestRegression157_FlaggedClockSurvivesRebuild(t *testing.T) {
	h := newHarness(t, startFEN, false)
	defer h.close()
	playMoves(t, h, "e2e4", "e7e5")
	h.clock.add(400 * time.Second)
	ended, err := h.live.TickFlagCheck(h.ctx)
	if err != nil || !ended {
		t.Fatalf("flag tick: ended=%v err=%v (want ended)", ended, err)
	}
	snap, _ := h.live.StateSnapshot(h.ctx)
	if snap.RemainingMs[0] != 0 {
		t.Fatalf("flagged clock %d, want 0", snap.RemainingMs[0])
	}
	mustClean(t, h)
}

// TestRegression124_IdleBaselineBeforeFirstMove: no move yet, the clock
// advances, then a rebuild charges White from the same instant (ClockStarted).
func TestRegression124_IdleBaselineBeforeFirstMove(t *testing.T) {
	h := newHarness(t, startFEN, false)
	defer h.close()
	h.clock.add(5 * time.Second)
	mustClean(t, h)
	snap, _ := h.live.StateSnapshot(h.ctx)
	if snap.RunningSeat != 0 || snap.RemainingMs[0] != 295_000 {
		t.Fatalf("white must be charged 5 s from the durable clock start: %+v", snap)
	}
}

// TestRegression159_PendingDrawOfferSurvivesRebuild: one move each, white
// offers a draw, then a rebuild still holds the offer.
func TestRegression159_PendingDrawOfferSurvivesRebuild(t *testing.T) {
	h := newHarness(t, startFEN, false)
	defer h.close()
	playMoves(t, h, "e2e4", "e7e5")
	if err := h.live.OfferDraw(h.ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	h.offers.offer(0, 2)
	mustClean(t, h)
	rb, cancel, err := h.rebuild()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if snap, _ := rb.StateSnapshot(h.ctx); snap.DrawOfferBy != 0 {
		t.Fatalf("rebuilt drawOfferBy %d, want 0", snap.DrawOfferBy)
	}
}

// TestOfferRulesOracle pins the offer rules the harness uses as its oracle.
// Each case is taken directly from the ratified spec (SPEC-S2-GAME-SERVER-GS7
// §6.2-§6.3, §5.5) and must not be edited to match code.
func TestOfferRulesOracle(t *testing.T) {
	type step struct {
		op    string // offer, move, accept, decline, rewind, restart
		seat  uint8
		ply   uint32
		want  int  // expected(false) after the step
		ended bool // when true, also assert expected(true)
		wantE int
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"offer before ply 2 is ignored", []step{
			{op: "offer", seat: 0, ply: 0, want: -1},
			{op: "offer", seat: 0, ply: 1, want: -1},
			{op: "offer", seat: 0, ply: 2, want: 0},
		}},
		{"offerer's own move keeps the offer, responder's move declines it", []step{
			{op: "offer", seat: 0, ply: 2, want: 0},
			{op: "move", seat: 0, want: 0},
			{op: "move", seat: 1, want: -1},
		}},
		{"re-offer within 20 plies rejected, boundary exact", []step{
			{op: "offer", seat: 0, ply: 2, want: 0},
			{op: "move", seat: 1, want: -1},
			{op: "offer", seat: 0, ply: 22, want: -1}, // last 2 >= 22-20 → rejected
			{op: "offer", seat: 0, ply: 23, want: 0},  // last 2 <  23-20 → allowed
		}},
		{"cooldown does not block an accept", []step{
			{op: "offer", seat: 1, ply: 2, want: 1},
			{op: "move", seat: 0, want: -1}, // seat 0 declines by moving
			{op: "offer", seat: 0, ply: 4, want: 0},
			{op: "offer", seat: 1, ply: 5, want: -1}, // mutual = accept, despite seat 1's cooldown
		}},
		{"rewind clears the offer and prunes later cooldown entries", []step{
			{op: "offer", seat: 0, ply: 10, want: 0},
			{op: "rewind", ply: 8, want: -1},
			{op: "offer", seat: 0, ply: 9, want: 0}, // the ply-10 entry was dropped
		}},
		{"game end clears the offer; restart keeps it", []step{
			{op: "offer", seat: 0, ply: 2, want: 0},
			{op: "restart", want: 0, ended: true, wantE: -1},
		}},
		{"accept by the other seat clears, accept by the offerer does not", []step{
			{op: "offer", seat: 0, ply: 2, want: 0},
			{op: "accept", seat: 0, want: 0},
			{op: "accept", seat: 1, want: -1},
		}},
		{"explicit decline clears the offer", []step{
			{op: "offer", seat: 0, ply: 2, want: 0},
			{op: "decline", seat: 1, want: -1},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newOfferModel(gs7Rules)
			for i, s := range c.steps {
				switch s.op {
				case "offer":
					m.offer(s.seat, s.ply)
				case "move":
					m.move(s.seat)
				case "accept":
					m.accept(s.seat)
				case "decline":
					m.decline()
				case "rewind":
					m.rewind(s.ply)
				case "restart":
					m.restart()
				}
				if got := m.expected(false); got != s.want {
					t.Fatalf("step %d (%s seat %d ply %d): pending %d, want %d", i, s.op, s.seat, s.ply, got, s.want)
				}
				if s.ended {
					if got := m.expected(true); got != s.wantE {
						t.Fatalf("step %d: after game end pending %d, want %d", i, got, s.wantE)
					}
				}
			}
		})
	}
}
