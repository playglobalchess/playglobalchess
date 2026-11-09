package session

// GS-7 invariant harness (SPEC-S2-GAME-SERVER-GS7 §9.2).
//
// Drives the REAL session over the real cgo engine and a MemStore with random,
// seeded command sequences, and after every step checks the invariants no
// existing test checked:
//
//   I1  every stream the session wrote folds: game.Fold(store) returns no error;
//   I2  the live position is never empty: snapshot FEN != "";
//   I3  the live session equals a rebuild: a fresh NewSession over the same store
//       reports the same FEN, ply, turn, phase, ended, terminal and clocks.
//
// On today's code three invariant breaks are KNOWN (#84 takeback, #85 mutual
// draw, #157 flagged clock not durable).
// They are handled as expected failures so CI stays green while the harness
// still FAILS on anything else:
//
//   - a random sequence that breaks an invariant is excused ONLY if it contains a
//     known trigger (a takeback, a mutual draw offer, a flag) AND the break matches that
//     issue's signature (knownSignature); any other break fails;
//   - every seed also runs in an "avoid-known" configuration that never takes
//     back, never makes a mutual draw offer, never jumps the clock far enough to
//     flag, and never excuses anything, so
//     the rest of the session surface is covered at full length;
//   - the deterministic repro cases are STRICT: they must keep failing until the
//     GS-7 fix lands. If one starts passing, the test fails and tells you to
//     delete it from knownFailures.
//
// GS7_STRICT=1 disables every excuse. The GS-7 fix PR must pass with it set and
// then delete knownFailures, the trigger tracking and this env switch.
//
// When the GS-7 refactor adds State fields (§3.9), extend snapshotDiff so every
// field is compared.

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
// code. Each must have a strict repro test below.
var knownFailures = map[string]string{
	"#84":  "takeback followed by a move: undone move re-acked, FEN empties, rebuild disagrees",
	"#85":  "mutual draw offer: the persisted stream does not fold",
	"#157": "flag: live zeroes the flagged clock, a rebuild keeps its last ClockAfterMs",
}

// reproOpts matches the FoldOptions seedLive builds its session with (5+3).
var reproOpts = game.FoldOptions{TimeControl: clock.TimeControl{BaseMs: 300_000, IncrementMs: 3_000}}

func strictMode() bool { return os.Getenv("GS7_STRICT") == "1" }

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

	// Trigger tracking: which known-failure paths this sequence exercised.
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

func newHarness(t *testing.T, rated, avoidKnown bool) *harness {
	t.Helper()
	fc := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, store, cancel := seedLive(t, startFEN, rated, fc.now)
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

// --- invariants -------------------------------------------------------------

// check returns "" when every invariant holds, else a description of the break.
func (h *harness) check() string {
	events, err := h.store.Load(h.ctx, game.StreamIDFor(gameID))
	if err != nil {
		return fmt.Sprintf("store load: %v", err)
	}
	if _, err := game.Fold(events, reproOpts); err != nil {
		return fmt.Sprintf("I1 stream does not fold: %v", err)
	}
	live, err := h.live.StateSnapshot(h.ctx)
	if err != nil {
		return fmt.Sprintf("live snapshot: %v", err)
	}
	if live.FEN == "" {
		return "I2 live FEN is empty"
	}
	rb, cancel, err := h.rebuild()
	if err != nil {
		return fmt.Sprintf("I3 rebuild fails: %v", err)
	}
	defer cancel()
	rbSnap, err := rb.StateSnapshot(h.ctx)
	if err != nil {
		return fmt.Sprintf("I3 rebuild snapshot: %v", err)
	}
	if d := snapshotDiff(live, rbSnap); d != "" {
		return "I3 live != rebuild: " + d
	}
	return ""
}

func snapshotDiff(a, b Snapshot) string {
	var d []string
	if a.FEN != b.FEN {
		d = append(d, fmt.Sprintf("fen %q vs %q", a.FEN, b.FEN))
	}
	if a.Ply != b.Ply {
		d = append(d, fmt.Sprintf("ply %d vs %d", a.Ply, b.Ply))
	}
	if a.Turn != b.Turn {
		d = append(d, fmt.Sprintf("turn %d vs %d", a.Turn, b.Turn))
	}
	if a.Phase != b.Phase {
		d = append(d, fmt.Sprintf("phase %v vs %v", a.Phase, b.Phase))
	}
	if a.Ended != b.Ended {
		d = append(d, fmt.Sprintf("ended %v vs %v", a.Ended, b.Ended))
	}
	if a.Ended && b.Ended && a.Terminal != b.Terminal {
		d = append(d, fmt.Sprintf("terminal %v vs %v", a.Terminal, b.Terminal))
	}
	if fmt.Sprint(a.RemainingMs) != fmt.Sprint(b.RemainingMs) {
		d = append(d, fmt.Sprintf("clocks %v vs %v", a.RemainingMs, b.RemainingMs))
	}
	return strings.Join(d, "; ")
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
							for _, promo := range []string{"q", "n"} {
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

// step performs one random operation. It returns false when the game cannot
// continue (ended, or no legal move).
func (h *harness) step(r *rand.Rand) bool {
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
		}
		return false
	}
	switch p := r.Intn(100); {
	case p < 60:
		return h.opMove(r, snap)
	case p < 67:
		h.opResend()
	case p < 75:
		h.opTakeback(r, snap)
	case p < 83:
		h.opOfferDraw(r)
	case p < 87:
		h.opAcceptDraw(r)
	case p < 94:
		h.opRestart()
	case p < 98:
		// Advance the clock and drive the flag check, as the transport's ticker
		// does. One jump in four is long enough to flag the side to move.
		d := time.Duration(r.Intn(2000)) * time.Millisecond
		if !h.avoidKnown && r.Intn(4) == 0 {
			d = 400 * time.Second // long enough to flag: the #157 trigger
		}
		h.clock.add(d)
		ended, err := h.live.TickFlagCheck(h.ctx)
		h.note("advance clock %v + flag tick -> ended=%v err=%v", d, ended, err)
		if ended {
			h.triggers["#157"] = true
		}
	default:
		by := uint8(r.Intn(2))
		err := h.live.Resign(h.ctx, by)
		h.note("resign by %d -> err=%v", by, err)
	}
	return true
}

func (h *harness) ts() uint64 { return uint64(h.clock.now().UnixMilli()) }

func (h *harness) opMove(r *rand.Rand, snap Snapshot) bool {
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
	m := &harnessMove{seat: snap.Turn, ply: snap.Ply + 1, uci: uci}
	h.clock.add(time.Duration(100+r.Intn(900)) * time.Millisecond)
	out, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
	h.note("move seat %d ply %d %s -> accepted=%v uci=%q err=%v", m.seat, m.ply, uci, out.Accepted, out.UCI, err)
	if err == nil && out.Accepted {
		h.lastMove = m
		h.pendingOffer = -1 // today's rule: any move clears a pending offer
	}
	return true
}

func (h *harness) opResend() {
	if h.lastMove == nil {
		return
	}
	m := h.lastMove
	out, err := h.live.SubmitMove(h.ctx, m.seat, m.ply, m.uci, h.ts())
	h.note("resend seat %d ply %d %s -> accepted=%v uci=%q err=%v", m.seat, m.ply, m.uci, out.Accepted, out.UCI, err)
}

func (h *harness) opTakeback(r *rand.Rand, snap Snapshot) {
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
	}
}

func (h *harness) opOfferDraw(r *rand.Rand) {
	by := uint8(r.Intn(2))
	if h.pendingOffer >= 0 && h.pendingOffer != int(by) {
		if h.avoidKnown {
			return // a mutual offer is the #85 trigger
		}
		h.triggers["#85"] = true
	}
	err := h.live.OfferDraw(h.ctx, by)
	h.note("offer draw by %d -> err=%v", by, err)
	if err == nil && h.pendingOffer < 0 {
		h.pendingOffer = int(by)
	}
}

func (h *harness) opAcceptDraw(r *rand.Rand) {
	by := uint8(r.Intn(2))
	err := h.live.AcceptDraw(h.ctx, by)
	h.note("accept draw by %d -> err=%v", by, err)
}

func (h *harness) opRestart() {
	rb, cancel, err := h.rebuild()
	if err != nil {
		h.note("restart failed: %v", err)
		return
	}
	h.stop()
	h.live, h.stop = rb, cancel
	h.note("restart: live session replaced by a rebuild from the store")
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
	{"unrated", false, false},      // everything, known triggers excused by signature
	{"unrated-clean", false, true}, // no known triggers, nothing excused
	{"rated", true, false},         // no takeback by rule; mutual draw still possible
}

func TestGS7InvariantHarness(t *testing.T) {
	excused := map[string]int{}
	noBreak, gameOver := 0, 0
	for seed := int64(1); seed <= harnessSeeds; seed++ {
		for _, cfg := range harnessConfigs {
			name := fmt.Sprintf("seed%d/%s", seed, cfg.name)
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, cfg.rated, cfg.avoidKnown)
				defer h.close()
				r := rand.New(rand.NewSource(seed))
				for i := 0; i < harnessSteps; i++ {
					cont := h.step(r)
					if v := h.check(); v != "" {
						if issue := h.excuse(v); issue != "" {
							excused[issue]++
							t.Logf("known failure %s after %d steps: %s", issue, i+1, v)
							return
						}
						t.Fatalf("invariant broken after %d steps (no known trigger): %s\nsequence:\n  %s",
							i+1, v, strings.Join(h.log, "\n  "))
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
	t.Logf("runs: %d; no break: %d (of which game over before %d steps: %d); excused known failures: %v (GS7_STRICT=1 makes these fail)",
		int(harnessSeeds)*len(harnessConfigs), noBreak, harnessSteps, gameOver, excused)
}

// knownSignature reports whether a break looks like the given known issue.
//
//	#85: the fold rejects a DrawAccepted as accepting the seat's own offer.
//	#157: after a flag, live and rebuild disagree ONLY in clocks.
//	#84: after a takeback, the live session and a rebuild disagree, the live FEN
//	     empties, or a later move makes the stream unfoldable (out of turn /
//	     not sequential), all because only the session cuts undone moves.
func knownSignature(issue, v string) bool {
	switch issue {
	case "#85":
		return strings.Contains(v, "DrawAccepted") && strings.Contains(v, "cannot accept its own offer")
	case "#157":
		return strings.HasPrefix(v, "I3 live != rebuild: clocks ") && !strings.Contains(v, ";")
	case "#84":
		return strings.HasPrefix(v, "I3 live != rebuild") ||
			strings.HasPrefix(v, "I2 live FEN is empty") ||
			strings.Contains(v, "moved out of turn") ||
			strings.Contains(v, "not sequential")
	}
	return false
}

// excuse returns the known issue that explains a break, or "" if none applies.
func (h *harness) excuse(violation string) string {
	if strictMode() || h.avoidKnown {
		return ""
	}
	for _, issue := range []string{"#84", "#85", "#157"} {
		if h.triggers[issue] && knownSignature(issue, violation) {
			return issue
		}
	}
	return ""
}

// --- strict repro cases (formerly repro_blockers_test.go) -------------------

// expectKnownFailure enforces a strict expected failure: the repro must still
// break an invariant until the fix lands; once it passes, the test fails so the
// entry is removed from knownFailures.
func expectKnownFailure(t *testing.T, issue, violation string) {
	t.Helper()
	if _, ok := knownFailures[issue]; !ok {
		t.Fatalf("%s is not in knownFailures", issue)
	}
	switch {
	case strictMode() && violation != "":
		t.Fatalf("%s still reproduces (GS7_STRICT=1): %s", issue, violation)
	case violation == "":
		if !strictMode() {
			t.Fatalf("%s no longer reproduces: the fix landed, so delete it from knownFailures "+
				"and make this a plain regression test", issue)
		}
	default:
		t.Logf("%s reproduces as expected: %s", issue, violation)
	}
}

// TestRepro85_MutualDrawStreamFolds: both seats offer a draw. The ended stream
// must fold (I1).
func TestRepro85_MutualDrawStreamFolds(t *testing.T) {
	h := newHarness(t, false, false)
	defer h.close()
	if err := h.live.OfferDraw(h.ctx, 0); err != nil {
		t.Fatalf("OfferDraw(0): %v", err)
	}
	if err := h.live.OfferDraw(h.ctx, 1); err != nil {
		t.Fatalf("OfferDraw(1): %v", err)
	}
	expectKnownFailure(t, "#85", h.check())
}

// TestRepro84_MoveAfterTakeback: e4 e5 Nf3, takeback one ply, then Nc3. The new
// move must be acked as itself, the FEN must stay valid, and a rebuild must agree.
func TestRepro84_MoveAfterTakeback(t *testing.T) {
	h := newHarness(t, false, false)
	defer h.close()
	for i, m := range []struct {
		seat uint8
		uci  string
	}{{0, "e2e4"}, {1, "e7e5"}, {0, "g1f3"}} {
		if _, err := h.live.SubmitMove(h.ctx, m.seat, uint32(i+1), m.uci, h.ts()); err != nil {
			t.Fatalf("setup move %s: %v", m.uci, err)
		}
	}
	if err := h.live.Takeback(h.ctx, 0, 1); err != nil {
		t.Fatalf("Takeback: %v", err)
	}
	out, err := h.live.SubmitMove(h.ctx, 0, 3, "b1c3", h.ts())
	var v string
	switch {
	case err != nil:
		v = fmt.Sprintf("move after takeback errored: %v", err)
	case out.UCI != "b1c3":
		v = fmt.Sprintf("acked %q instead of b1c3 (undone move re-acked)", out.UCI)
	default:
		v = h.check()
	}
	expectKnownFailure(t, "#84", v)
}

// TestRepro157_FlaggedClockSurvivesRebuild: white moves, black moves, white's
// clock runs out and the flag tick ends the game. A rebuild must report the same
// clocks as the live session (I3).
func TestRepro157_FlaggedClockSurvivesRebuild(t *testing.T) {
	h := newHarness(t, false, false)
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
	expectKnownFailure(t, "#157", h.check())
}
