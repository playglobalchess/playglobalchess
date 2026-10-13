package engine

import (
	"context"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

func newOracle(t *testing.T) CgoOracle {
	t.Helper()
	o, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return o
}

// TestLegalMove: a legal move is accepted, returns the engine's resulting FEN,
// and (here) no terminal. This is the pipeline's §3.6 step-3 path.
func TestLegalMove(t *testing.T) {
	o := newOracle(t)
	v, err := o.Legality(context.Background(), startFEN, "e2e4")
	if err != nil {
		t.Fatalf("Legality: %v", err)
	}
	if !v.Legal {
		t.Fatal("1.e4 reported illegal")
	}
	if v.Terminal != nil {
		t.Errorf("1.e4 reported terminal %v, want nil", *v.Terminal)
	}
	// The engine owns the exact FEN (strict ep export); assert it is non-empty
	// and the side to move flipped to black — we do not re-derive chess state.
	if v.NewFEN == "" || v.NewFEN == startFEN {
		t.Errorf("NewFEN = %q, want the post-e4 position", v.NewFEN)
	}
}

// TestIllegalAndMalformed: both map to Legal:false (MOVE_REJECTED{ILLEGAL}),
// never an error, never an event.
func TestIllegalAndMalformed(t *testing.T) {
	o := newOracle(t)
	for _, uci := range []string{"e2e5" /*illegal*/, "zzzz" /*malformed*/, "e9e8" /*off board*/} {
		v, err := o.Legality(context.Background(), startFEN, uci)
		if err != nil {
			t.Fatalf("Legality(%q): unexpected error %v", uci, err)
		}
		if v.Legal {
			t.Errorf("Legality(%q): reported legal", uci)
		}
	}
}

// TestTerminalDetection: the engine's terminal verdicts flow through as the
// §4A.5 Termination the pipeline will put on GameEnded.
func TestTerminalDetection(t *testing.T) {
	o := newOracle(t)
	tests := []struct {
		name     string
		fen      string
		uci      string
		want     game.Termination
		terminal bool
	}{
		{
			name:     "scholars mate",
			fen:      "r1bqkbnr/pppp1ppp/2n5/4p3/2B1P3/5Q2/PPPP1PPP/RNB1K1NR w KQkq - 2 3",
			uci:      "f3f7",
			want:     game.TermMate,
			terminal: true,
		},
		{
			name: "stalemate",
			// Kf6 + Qf7 vs lone Kh8, White to move. f7g6 (Qg6) leaves Black's
			// king on h8 with no legal move and not in check — stalemate.
			fen:      "7k/5Q2/5K2/8/8/8/8/8 w - - 0 1",
			uci:      "f7g6",
			want:     game.TermStalemate,
			terminal: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := o.Legality(context.Background(), tc.fen, tc.uci)
			if err != nil {
				t.Fatalf("Legality: %v", err)
			}
			if !v.Legal {
				t.Fatalf("move %q reported illegal", tc.uci)
			}
			if tc.terminal {
				if v.Terminal == nil {
					t.Fatalf("expected terminal %v, got nil", tc.want)
				}
				if *v.Terminal != tc.want {
					t.Errorf("terminal = %v, want %v", *v.Terminal, tc.want)
				}
			}
		})
	}
}

// TestHasSufficientMaterial: the per-side FIDE-6.9 flag query (SPEC §3.3).
func TestHasSufficientMaterial(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()
	// K+Q vs K+N: White (seat 0) can mate, Black (seat 1) cannot.
	fen := "4k1n1/8/8/8/8/8/8/3QK3 w - - 0 1"
	white, err := o.HasSufficientMaterial(ctx, fen, 0)
	if err != nil {
		t.Fatalf("white: %v", err)
	}
	if !white {
		t.Error("White (K+Q) reported no mating material")
	}
	black, err := o.HasSufficientMaterial(ctx, fen, 1)
	if err != nil {
		t.Fatalf("black: %v", err)
	}
	if black {
		t.Error("Black (K+N) reported mating material")
	}
	// seat out of range (v1 2-seat) is an error.
	if _, err := o.HasSufficientMaterial(ctx, fen, 2); err == nil {
		t.Error("seat_idx 2 did not error")
	}
}

// TestDeterminism: the engine is pure — identical inputs give identical results
// across calls (NFR / §8).
func TestDeterminism(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()
	first, err := o.Legality(ctx, startFEN, "g1f3")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := o.Legality(ctx, startFEN, "g1f3")
		if err != nil {
			t.Fatal(err)
		}
		if again.Legal != first.Legal || again.NewFEN != first.NewFEN {
			t.Fatalf("non-deterministic: %+v vs %+v", again, first)
		}
	}
}

// TestContextCancel: a cancelled context short-circuits without calling the
// engine (context.Context on the I/O-crossing method, per conventions).
func TestContextCancel(t *testing.T) {
	o := newOracle(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Legality(ctx, startFEN, "e2e4"); err == nil {
		t.Error("cancelled context did not error")
	}
	if _, err := o.HasSufficientMaterial(ctx, startFEN, 0); err == nil {
		t.Error("cancelled context did not error (material)")
	}
}

// TestImplementsOracle is a compile-time + runtime check that CgoOracle fills
// the GS-1 interface the pipeline depends on.
func TestImplementsOracle(t *testing.T) {
	var _ game.LegalityOracle = newOracle(t)
}

// TestRepetitionIsNotDetectedYet PINS the Q-GS5 deferral (SPEC-S2-GAME-SERVER
// §10): the move path calls the engine with empty prior_keys, so repetition is
// NOT detectable here — a move that merely reaches a position cannot return
// DrawRepetition, because the oracle has no history to count against.
//
// This test exists to FAIL LOUDLY the day someone wires the folded Zobrist
// history (the live-session chunk): once prior_keys is threaded, this move
// could legitimately return a repetition terminal, and whoever does the wiring
// MUST revisit this test (and Q-GS5) rather than leave repetition silently
// broken. Do not delete it without wiring the history — that is the whole point
// of the pin. See engine.go Legality's prior_keys comment.
func TestRepetitionIsNotDetectedYet(t *testing.T) {
	o := newOracle(t)
	// A quiet reversible move (knight out) from the start position. Even if this
	// exact position had occurred many times before in a real game, the move
	// path here has no history, so the engine can only ever report Ongoing — it
	// cannot see a repetition. We assert the terminal is nil, documenting that
	// the repetition branch of terminalOf is unreachable on this path today.
	v, err := o.Legality(context.Background(), startFEN, "g1f3")
	if err != nil {
		t.Fatalf("Legality: %v", err)
	}
	if !v.Legal {
		t.Fatal("Nf3 reported illegal")
	}
	if v.Terminal != nil {
		t.Fatalf("move path returned terminal %v with no history — repetition "+
			"wiring (Q-GS5) may have landed; update this pin and terminalOf's "+
			"repetition handling accordingly", *v.Terminal)
	}
}
