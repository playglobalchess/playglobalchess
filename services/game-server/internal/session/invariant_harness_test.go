package session

// GS-7 invariant harness (SPEC-S2-GAME-SERVER-GS7 §9.2).
//
// Drives the REAL session over the real cgo engine and a MemStore with random,
// seeded command sequences, and after every step checks invariants no existing
// test checked:
//
//   I1  every stream the session wrote folds: game.Fold(store) returns no error;
//   I2  the live position is never empty: snapshot FEN != "";
//   I3  the live session equals a rebuild: a fresh NewSession over the same store
//       has the same FEN, ply, turn, phase, ended, terminal and clocks (public
//       Snapshot) AND the same repetition history, pending draw offer and idle
//       clock baseline (internal state, read through the actor).
//
// Known failures on today's code are handled as STRICT expected failures so CI
// stays green while the harness still fails on anything new:
//
//   - a break is reported as a list of differing parts. #84 (takeback) excuses a
//     break only if it has its core signature: FEN, ply or turn differ, the live
//     FEN is empty, or the stream fails to fold as out of turn / not sequential.
//     Otherwise EVERY part must be covered by a triggered issue that owns that
//     part (#85 own-offer fold error, #157 clocks, #124 idle baseline, #159
//     pending draw offer). Any uncovered part fails the test;
//   - every seed also runs an "unrated-clean" configuration that never takes back,
//     never offers a draw, never flags and never moves the clock before the first
//     move, and excuses nothing;
//   - each known issue has a strict repro test that must keep failing with that
//     issue's own signature; once it passes, the test fails and says to delete it
//     from knownFailures.
//
// GS7_STRICT=1 excuses nothing. The GS-7 fix PR must pass with it set, then
// delete knownFailures, the trigger tracking and this switch.
//
// When the GS-7 refactor adds State fields (§3.9), extend snapshotDiff and
// internalDiff so every field is compared.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// knownFailures lists the issues whose invariant breaks are expected on today's
// code. Each has a strict repro test below.
var knownFailures = map[string]string{
	"#84":  "takeback: only the session cuts undone moves; rebuild replays them, a later move is re-acked as the undone one",
	"#85":  "mutual draw offer: the persisted stream does not fold",
	"#124": "no move yet: the idle-clock baseline is the session's construction time, not in the stream",
	"#157": "flag: live zeroes the flagged clock, a rebuild keeps its last ClockAfterMs",
	"#159": "a pending draw offer is not rebuilt from the stream",
}

// reproOpts matches the FoldOptions seedLive builds its session with (5+3).
var reproOpts = game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}

func strictMode() bool { return os.Getenv("GS7_STRICT") == "1" }

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
	t      *testing.T
	ctx    context.Context
	store  *game.MemStore
	oracle game.LegalityOracle
	clock  *fakeClock
	live   *Session
	stop   context.CancelFunc
	rated  bool
	// avoidKnown: never exercise a known trigger and never excuse a break.
	avoidKnown bool

	// Triggers set by operations: which known-failure paths were exercised.
	triggers map[string]bool
	// Mirror of the session's pending draw offer, to detect a mutual offer.
	pendingOffer int // seat, or -1
	lastMove     *harnessMove
	log          []string
}

type harnessMove struct {
	seat uint8
	ply  uint32
	uci  string
}

func newHarness(t *testing.T, startpos string, rated, avoidKnown bool) *harness {
	t.Helper()
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, store, cancel := seedLive(t, startpos, rated, fc.now)
	o, err := engine.New()
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return &harness{
		t: t, ctx: context.Background(), store: store, oracle: o, clock: fc,
		live: s, stop: cancel, rated: rated, avoidKnown: avoidKnown,
		triggers: map[string]bool{}, pendingOffer: -1,
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

// internalView is session state the public Snapshot does not expose but a
// rebuild must reproduce.
type internalView struct {
	priorKeys   []uint64
	drawOfferBy int // -1 none
	lastMoveMs  int64
}

type internalCmd struct {
	v    internalView
	done chan struct{}
}

func (c *internalCmd) apply(_ context.Context, s *Session) {
	defer close(c.done)
	c.v.priorKeys = append([]uint64(nil), s.priorKeys...)
	c.v.drawOfferBy = -1
	if s.drawOfferBy != nil {
		c.v.drawOfferBy = int(*s.drawOfferBy)
	}
	c.v.lastMoveMs = s.lastMoveAt.UnixMilli()
}

func readInternal(ctx context.Context, s *Session) (internalView, error) {
	c := &internalCmd{done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return internalView{}, err
	}
	return c.v, nil
}

// --- invariants -------------------------------------------------------------

// violation describes a broken invariant. parts names what differs; the excuse
// logic matches issues to parts.
type violation struct {
	text  string
	parts []string
}

func (v *violation) String() string {
	if v == nil {
		return ""
	}
	return v.text
}

func (v *violation) has(part string) bool {
	for _, p := range v.parts {
		if p == part {
			return true
		}
	}
	return false
}

func brk(part, format string, args ...any) *violation {
	return &violation{text: fmt.Sprintf(format, args...), parts: []string{part}}
}

// check returns nil when every invariant holds.
func (h *harness) check() *violation {
	events, err := h.store.Load(h.ctx, game.StreamIDFor(gameID))
	if err != nil {
		return brk("harness", "store load: %v", err)
	}
	if _, err := game.Fold(events, reproOpts); err != nil {
		return brk("fold", "I1 stream does not fold: %v", err)
	}
	live, err := h.live.StateSnapshot(h.ctx)
	if err != nil {
		return brk("harness", "live snapshot: %v", err)
	}
	if live.FEN == "" {
		return brk("emptyFEN", "I2 live FEN is empty")
	}
	rb, cancel, err := h.rebuild()
	if err != nil {
		return brk("rebuild", "I3 rebuild fails: %v", err)
	}
	defer cancel()
	rbSnap, err := rb.StateSnapshot(h.ctx)
	if err != nil {
		return brk("harness", "I3 rebuild snapshot: %v", err)
	}
	liveIn, err := readInternal(h.ctx, h.live)
	if err != nil {
		return brk("harness", "live internal: %v", err)
	}
	rbIn, err := readInternal(h.ctx, rb)
	if err != nil {
		return brk("harness", "rebuild internal: %v", err)
	}
	parts, texts := snapshotDiff(live, rbSnap)
	p2, t2 := internalDiff(liveIn, rbIn, h.pendingOffer)
	parts, texts = append(parts, p2...), append(texts, t2...)
	// The live offer must match the harness's model of today's offer rules.
	// This compares live against the RULES, not against a rebuild, so it is
	// never excusable: it is what catches offer-clearing bugs while #159 is open.
	if liveIn.drawOfferBy != h.pendingOffer {
		parts = append(parts, "offerModel")
		texts = append(texts, fmt.Sprintf("live drawOfferBy %d but the offer rules say %d", liveIn.drawOfferBy, h.pendingOffer))
	}
	if len(parts) == 0 {
		return nil
	}
	return &violation{text: "I3 live != rebuild: " + strings.Join(texts, "; "), parts: parts}
}

func snapshotDiff(a, b Snapshot) (parts, texts []string) {
	add := func(p, f string, args ...any) {
		parts = append(parts, p)
		texts = append(texts, fmt.Sprintf(f, args...))
	}
	if a.FEN != b.FEN {
		add("fen", "fen %q vs %q", a.FEN, b.FEN)
	}
	if a.Ply != b.Ply {
		add("ply", "ply %d vs %d", a.Ply, b.Ply)
	}
	if a.Turn != b.Turn {
		add("turn", "turn %d vs %d", a.Turn, b.Turn)
	}
	if a.Phase != b.Phase {
		add("phase", "phase %v vs %v", a.Phase, b.Phase)
	}
	if a.Ended != b.Ended {
		add("ended", "ended %v vs %v", a.Ended, b.Ended)
	}
	if a.Ended && b.Ended && a.Terminal != b.Terminal {
		add("terminal", "terminal %v vs %v", a.Terminal, b.Terminal)
	}
	if fmt.Sprint(a.RemainingMs) != fmt.Sprint(b.RemainingMs) {
		add("clocks", "clocks %v vs %v", a.RemainingMs, b.RemainingMs)
	}
	return parts, texts
}

// internalDiff compares live (a) with a rebuild (b). model is the harness's
// expected live offer. A pending offer the rebuild lost while live holds exactly
// the expected offer is the known #159 gap and gets its own part name; any other
// offer difference is the unowned part "drawOfferBy".
func internalDiff(a, b internalView, model int) (parts, texts []string) {
	if fmt.Sprint(a.priorKeys) != fmt.Sprint(b.priorKeys) {
		parts = append(parts, "priorKeys")
		texts = append(texts, fmt.Sprintf("priorKeys len %d vs %d", len(a.priorKeys), len(b.priorKeys)))
	}
	if a.drawOfferBy != b.drawOfferBy {
		part := "drawOfferBy"
		if b.drawOfferBy == -1 && a.drawOfferBy >= 0 && a.drawOfferBy == model {
			part = "offerLostOnRebuild"
		}
		parts = append(parts, part)
		texts = append(texts, fmt.Sprintf("drawOfferBy %d vs %d", a.drawOfferBy, b.drawOfferBy))
	}
	if a.lastMoveMs != b.lastMoveMs {
		parts = append(parts, "lastMoveAt")
		texts = append(texts, fmt.Sprintf("lastMoveAt %d vs %d ms", a.lastMoveMs, b.lastMoveMs))
	}
	return parts, texts
}

// --- known-issue signatures -------------------------------------------------

// sig84 is #84's core signature: the rebuild replays the undone move, so the
// position differs; or the live FEN empties; or a later move makes the stream
// unfoldable.
func sig84(v *violation) bool {
	if v == nil {
		return false
	}
	return v.has("fen") || v.has("ply") || v.has("turn") || v.has("emptyFEN") ||
		(v.has("fold") && (strings.Contains(v.text, "moved out of turn") || strings.Contains(v.text, "not sequential")))
}

// partOwner maps a single differing part to the issue that explains it.
func partOwner(part, text string) string {
	switch part {
	case "fold":
		if strings.Contains(text, "DrawAccepted") && strings.Contains(text, "cannot accept its own offer") {
			return "#85"
		}
	case "clocks":
		return "#157"
	case "lastMoveAt":
		return "#124"
	case "offerLostOnRebuild":
		return "#159"
	}
	return ""
}

// neverExcused lists parts no known issue may explain: harness failures, a
// failing rebuild, and a live offer that contradicts the offer rules.
var neverExcused = map[string]bool{"harness": true, "rebuild": true, "offerModel": true}

// excuse returns the known issues that together explain v, or nil if any part
// is unexplained.
func (h *harness) excuse(v *violation) []string {
	if strictMode() || h.avoidKnown || v == nil {
		return nil
	}
	for _, p := range v.parts {
		if neverExcused[p] {
			return nil
		}
	}
	if h.triggers["#84"] && sig84(v) {
		return []string{"#84"}
	}
	var used []string
	for _, p := range v.parts {
		owner := partOwner(p, v.text)
		if owner == "" || !h.triggered(owner) {
			return nil
		}
		used = append(used, owner)
	}
	return used
}

// triggered reports whether this sequence reached the state an issue needs.
// #124 and #159 are states, not operations, so they are read live.
func (h *harness) triggered(issue string) bool {
	switch issue {
	case "#124":
		snap, err := h.live.StateSnapshot(h.ctx)
		return err == nil && snap.Ply == 0
	case "#159":
		// Fully encoded in the part itself (offerLostOnRebuild requires the
		// rebuild to have lost exactly the offer the rules expect).
		return true
	}
	return h.triggers[issue]
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
	moves, promotions, castles, enPassant, takebacks, offers, flags, restarts, resends int
}

// step performs one random operation. It returns false when the game cannot
// continue (ended, or no legal move).
func (h *harness) step(r *rand.Rand, cov *coverage) bool {
	snap, err := h.live.StateSnapshot(h.ctx)
	if err != nil {
		h.t.Fatalf("snapshot: %v", err)
	}
	if snap.Ended {
		// After the end, only a resend of the last move is interesting.
		if h.lastMove != nil && r.Intn(2) == 0 {
			m := h.lastMove
			_, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
			h.note("resend-after-end %s ply %d -> err=%v", m.uci, m.ply, err)
			cov.resends++
		}
		return false
	}
	switch p := r.Intn(100); {
	case p < 62:
		return h.opMove(r, snap, cov)
	case p < 69:
		h.opResend(cov)
	case p < 77:
		h.opTakeback(r, snap, cov)
	case p < 84:
		h.opOfferDraw(r, cov)
	case p < 88:
		h.opAcceptDraw(r)
	case p < 95:
		h.opRestart(cov)
	case p < 99:
		h.opClock(r, snap, cov)
	default:
		by := uint8(r.Intn(2))
		err := h.live.Resign(h.ctx, by)
		h.note("resign by %d -> err=%v", by, err)
	}
	return true
}

func (h *harness) ts() uint64 { return uint64(h.clock.now().UnixMilli()) }

func (h *harness) opMove(r *rand.Rand, snap Snapshot, cov *coverage) bool {
	if snap.FEN == "" {
		h.note("move skipped: live FEN empty")
		return false
	}
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
		h.lastMove = m
		h.pendingOffer = -1 // today's rule: any move clears a pending offer
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
	h.note("resend seat %d ply %d %s -> accepted=%v uci=%q err=%v", m.seat, m.ply, m.uci, out.Accepted, out.UCI, err)
	cov.resends++
}

func (h *harness) opTakeback(r *rand.Rand, snap Snapshot, cov *coverage) {
	if h.rated || h.avoidKnown || snap.Ply == 0 {
		return
	}
	by := uint8(r.Intn(2))
	err := h.live.Takeback(h.ctx, by, 1) // the transport always passes 1
	h.note("takeback by %d -> err=%v", by, err)
	if err == nil {
		h.triggers["#84"] = true
		h.pendingOffer = -1
		h.lastMove = nil // the undone move is no longer the last live move
		cov.takebacks++
	}
}

func (h *harness) opOfferDraw(r *rand.Rand, cov *coverage) {
	if h.avoidKnown {
		return // any pending offer is the #159 state; a second one is #85
	}
	// Offer model (today's rules): a first offer sets it, a repeat by the same
	// seat is a no-op, an offer by the other seat is mutual and ends the game;
	// accepted new moves, takebacks, accepts and restarts clear it; resign and
	// flag leave it.
	by := uint8(r.Intn(2))
	mutual := h.pendingOffer >= 0 && h.pendingOffer != int(by)
	err := h.live.OfferDraw(h.ctx, by)
	h.note("offer draw by %d -> err=%v", by, err)
	if err != nil {
		return
	}
	cov.offers++
	if mutual {
		h.triggers["#85"] = true
		h.pendingOffer = -1 // the mutual offer ended the game
		return
	}
	if h.pendingOffer < 0 {
		h.pendingOffer = int(by)
	}
}

func (h *harness) opAcceptDraw(r *rand.Rand) {
	by := uint8(r.Intn(2))
	err := h.live.AcceptDraw(h.ctx, by)
	h.note("accept draw by %d -> err=%v", by, err)
	if err == nil && h.pendingOffer >= 0 && h.pendingOffer != int(by) {
		h.pendingOffer = -1 // an accepted offer is cleared (endgame.go acceptDrawCmd)
	}
}

func (h *harness) opRestart(cov *coverage) {
	rb, cancel, err := h.rebuild()
	if err != nil {
		h.note("restart failed: %v", err)
		return
	}
	h.stop()
	h.live, h.stop = rb, cancel
	// Today a rebuilt session holds no pending offer (that loss IS #159), so the
	// offer model follows the code: after a restart nothing is pending.
	h.pendingOffer = -1
	h.note("restart: live session replaced by a rebuild from the store")
	cov.restarts++
}

// opClock advances the clock and drives the flag check, as the transport's
// ticker does. One jump in four is long enough to flag the side to move.
func (h *harness) opClock(r *rand.Rand, snap Snapshot, cov *coverage) {
	if h.avoidKnown && snap.Ply == 0 {
		return // moving the clock before the first move is the #124 state
	}
	d := time.Duration(r.Intn(2000)) * time.Millisecond
	if !h.avoidKnown && r.Intn(4) == 0 {
		d = 400 * time.Second // long enough to flag: the #157 trigger
	}
	h.clock.add(d)
	ended, err := h.live.TickFlagCheck(h.ctx)
	h.note("advance clock %v + flag tick -> ended=%v err=%v", d, ended, err)
	if ended {
		h.triggers["#157"] = true
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

type harnessConfig struct {
	name       string
	rated      bool
	avoidKnown bool
}

var harnessConfigs = []harnessConfig{
	{"unrated", false, false},      // everything; known breaks excused by part ownership
	{"unrated-clean", false, true}, // no known trigger or state; nothing excused
	{"rated", true, false},         // no takeback by rule
}

func TestGS7InvariantHarness(t *testing.T) {
	excused := map[string]int{}
	cov := map[string]*coverage{}
	noBreak, gameOver := 0, 0
	for seed := int64(1); seed <= harnessSeeds; seed++ {
		start := startPositions[int(seed-1)%len(startPositions)]
		for _, cfg := range harnessConfigs {
			if cov[cfg.name] == nil {
				cov[cfg.name] = &coverage{}
			}
			c := cov[cfg.name]
			name := fmt.Sprintf("seed%d/%s", seed, cfg.name)
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, start, cfg.rated, cfg.avoidKnown)
				defer h.close()
				r := rand.New(rand.NewSource(seed))
				for i := 0; i < harnessSteps; i++ {
					cont := h.step(r, c)
					v := h.check()
					if v == nil {
						// A clean check closes every operation trigger, so a
						// takeback or flag earlier in the run cannot excuse a
						// later, unrelated break.
						h.triggers = map[string]bool{}
					}
					if v != nil {
						if issues := h.excuse(v); issues != nil {
							for _, is := range issues {
								excused[is]++
							}
							t.Logf("known failure %v after %d steps: %s", issues, i+1, v)
							if !stateOnly(issues) {
								return // #84/#85 corrupt the stream: nothing after is meaningful
							}
							// #124/#157/#159 differ only in rebuilt state; the stream
							// is sound, so keep exercising the session.
						} else {
							t.Fatalf("invariant broken after %d steps (not explained by a known issue): %s\nstart %q\nsequence:\n  %s",
								i+1, v, start, strings.Join(h.log, "\n  "))
						}
					}
					if !cont {
						noBreak++
						gameOver++
						return
					}
				}
				noBreak++
			})
		}
	}
	t.Logf("runs: %d; reached the end or game over without an unexcused break: %d (game over before %d steps: %d); excused known failures: %v (GS7_STRICT=1 makes these fail)",
		int(harnessSeeds)*len(harnessConfigs), noBreak, harnessSteps, gameOver, excused)
	for _, cfg := range harnessConfigs {
		t.Logf("coverage %s: %+v", cfg.name, *cov[cfg.name])
	}
}

// stateOnly reports whether every excused issue leaves the stream sound.
func stateOnly(issues []string) bool {
	for _, is := range issues {
		if is != "#124" && is != "#157" && is != "#159" {
			return false
		}
	}
	return true
}

// --- strict repro cases -----------------------------------------------------

// expectKnownFailure enforces a strict expected failure. In the default mode the
// repro must still break WITH that issue's own signature (a different break is a
// failure, so a repro cannot pass for the wrong reason); once it no longer breaks
// the test fails so the entry is removed from knownFailures. In strict mode any
// remaining break fails.
func expectKnownFailure(t *testing.T, issue string, v *violation, matches func(*violation) bool) {
	t.Helper()
	if _, ok := knownFailures[issue]; !ok {
		t.Fatalf("%s is not in knownFailures", issue)
	}
	switch {
	case strictMode() && v != nil:
		t.Fatalf("%s still reproduces (GS7_STRICT=1): %s", issue, v)
	case v == nil:
		if !strictMode() {
			t.Fatalf("%s no longer reproduces: the fix landed, so delete it from knownFailures "+
				"and make this a plain regression test", issue)
		}
	case !matches(v):
		t.Fatalf("%s repro broke with a DIFFERENT signature (new or changed bug): %s", issue, v)
	default:
		t.Logf("%s reproduces as expected: %s", issue, v)
	}
}

// onlyPart matches a break whose parts are exactly one named part.
func onlyPart(part string) func(*violation) bool {
	return func(v *violation) bool { return len(v.parts) == 1 && v.parts[0] == part }
}

// TestRepro85_MutualDrawStreamFolds: both seats offer a draw. The ended stream
// must fold (I1).
func TestRepro85_MutualDrawStreamFolds(t *testing.T) {
	h := newHarness(t, startFEN, false, false)
	defer h.close()
	if err := h.live.OfferDraw(h.ctx, 0); err != nil {
		t.Fatalf("OfferDraw(0): %v", err)
	}
	if err := h.live.OfferDraw(h.ctx, 1); err != nil {
		t.Fatalf("OfferDraw(1): %v", err)
	}
	expectKnownFailure(t, "#85", h.check(), func(v *violation) bool {
		return len(v.parts) == 1 && partOwner(v.parts[0], v.text) == "#85"
	})
}

// TestRepro84_MoveAfterTakeback: e4 e5 Nf3, takeback one ply, then Nc3. The new
// move must be acked as itself, the FEN must stay valid, and a rebuild must agree.
func TestRepro84_MoveAfterTakeback(t *testing.T) {
	h := newHarness(t, startFEN, false, false)
	defer h.close()
	for i, m := range []struct {
		seat uint8
		uci  string
	}{{0, "e2e4"}, {1, "e7e5"}, {0, "g1f3"}} {
		h.clock.add(time.Second)
		if _, err := h.live.SubmitMove(h.ctx, m.seat, uint32(i+1), m.uci, h.ts()); err != nil {
			t.Fatalf("setup move %s: %v", m.uci, err)
		}
	}
	if err := h.live.Takeback(h.ctx, 0, 1); err != nil {
		t.Fatalf("Takeback: %v", err)
	}
	out, err := h.live.SubmitMove(h.ctx, 0, 3, "b1c3", h.ts())
	var v *violation
	switch {
	case err != nil:
		v = brk("reack", "move after takeback errored: %v", err)
	case out.UCI != "b1c3":
		v = brk("reack", "acked %q instead of b1c3 (undone move re-acked)", out.UCI)
	default:
		v = h.check()
	}
	expectKnownFailure(t, "#84", v, func(v *violation) bool { return v.has("reack") || sig84(v) })
}

// TestRepro157_FlaggedClockSurvivesRebuild: one move each, then white's clock
// runs out and the flag tick ends the game. A rebuild must report the same clocks.
func TestRepro157_FlaggedClockSurvivesRebuild(t *testing.T) {
	h := newHarness(t, startFEN, false, false)
	defer h.close()
	for i, m := range []struct {
		seat uint8
		uci  string
	}{{0, "e2e4"}, {1, "e7e5"}} {
		h.clock.add(time.Second)
		if _, err := h.live.SubmitMove(h.ctx, m.seat, uint32(i+1), m.uci, h.ts()); err != nil {
			t.Fatalf("setup move %s: %v", m.uci, err)
		}
	}
	h.clock.add(400 * time.Second)
	ended, err := h.live.TickFlagCheck(h.ctx)
	if err != nil || !ended {
		t.Fatalf("flag tick: ended=%v err=%v (want ended)", ended, err)
	}
	expectKnownFailure(t, "#157", h.check(), onlyPart("clocks"))
}

// TestRepro124_IdleBaselineBeforeFirstMove: no move yet, the clock advances,
// then a rebuild. The rebuild must charge White from the same instant.
func TestRepro124_IdleBaselineBeforeFirstMove(t *testing.T) {
	h := newHarness(t, startFEN, false, false)
	defer h.close()
	h.clock.add(5 * time.Second)
	expectKnownFailure(t, "#124", h.check(), onlyPart("lastMoveAt"))
}

// TestRepro159_PendingDrawOfferSurvivesRebuild: one move each, white offers a
// draw, then a rebuild. The rebuilt session must still hold the offer.
func TestRepro159_PendingDrawOfferSurvivesRebuild(t *testing.T) {
	h := newHarness(t, startFEN, false, false)
	defer h.close()
	for i, m := range []struct {
		seat uint8
		uci  string
	}{{0, "e2e4"}, {1, "e7e5"}} {
		if _, err := h.live.SubmitMove(h.ctx, m.seat, uint32(i+1), m.uci, h.ts()); err != nil {
			t.Fatalf("setup move %s: %v", m.uci, err)
		}
	}
	if err := h.live.OfferDraw(h.ctx, 0); err != nil {
		t.Fatalf("OfferDraw: %v", err)
	}
	h.pendingOffer = 0 // the offer rules: white's first offer is now pending
	expectKnownFailure(t, "#159", h.check(), onlyPart("offerLostOnRebuild"))
}
