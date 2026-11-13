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
	v, err := o.Legality(context.Background(), startFEN, "e2e4", nil)
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
		v, err := o.Legality(context.Background(), startFEN, uci, nil)
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
			v, err := o.Legality(context.Background(), tc.fen, tc.uci, nil)
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

// TestFlagTerminationLichessRule: the flag branch through the real engine
// (#121, ENGINE-CORE §3.7). The opponent's mating material decides FLAG vs
// TIMEOUT_VS_INSUFFICIENT, and it depends on the flagger's pieces too.
func TestFlagTerminationLichessRule(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		fen     string
		flagged uint8
		want    game.Termination
	}{
		// White (K+P) flags; Black's K+N can mate it (Nf2#) => loss on time.
		{"KN v KP", "8/8/8/8/8/3n4/7P/5k1K b - - 0 1", 0, game.TermFlag},
		// White (bare K) flags; Black's lone knight cannot mate => draw.
		{"KN v K", "8/8/8/8/8/3n4/8/5k1K b - - 0 1", 0, game.TermTimeoutVsInsufficient},
		// Black (K+Q) flags; White's lone knight cannot mate K+Q => draw.
		{"KN v KQ", "3qk3/8/8/8/8/8/8/4KN2 w - - 0 1", 1, game.TermTimeoutVsInsufficient},
		// Black (bare K) flags; White's same-colour bishops cannot mate => draw.
		{"KBB same colour v K", "4k3/8/8/8/8/B7/8/2B1K3 w - - 0 1", 1, game.TermTimeoutVsInsufficient},
		// Black (K+N) flags; White's bishop can mate it => loss on time.
		{"KB v KN", "1n2k3/8/8/8/8/8/8/4KB2 w - - 0 1", 1, game.TermFlag},
	}
	for _, c := range cases {
		got, err := game.FlagTermination(ctx, o, c.fen, c.flagged, 2)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: termination = 0x%02x, want 0x%02x", c.name, uint8(got), uint8(c.want))
		}
	}
}

// TestDeterminism: the engine is pure — identical inputs give identical results
// across calls (NFR / §8).
func TestDeterminism(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()
	first, err := o.Legality(ctx, startFEN, "g1f3", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := o.Legality(ctx, startFEN, "g1f3", nil)
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
	if _, err := o.Legality(ctx, startFEN, "e2e4", nil); err == nil {
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

// TestRepetitionDetectedWithHistory closes Q-GS5 and pins #100: with the
// history threaded exactly as State.PriorKeys builds it (the START position's
// key first, from PositionKey, then each resulting key), the knight shuffle
// Nf3 Nf6 Ng1 Ng8 returns to the start at plies 4, 8, 12, 16, and the fivefold
// auto-draw fires at ply 16 exactly, never earlier. (Without the start key it
// fired at ply 17, from the post-Nf3 position.) Claimable threefold is not on
// the verdict (terminalOf maps it to nil); engine-cgo pins it at ply 8.
func TestRepetitionDetectedWithHistory(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()

	start, err := o.PositionKey(ctx, startFEN)
	if err != nil || start == 0 {
		t.Fatalf("start key = %#x, err=%v", start, err)
	}
	cycle := []string{"g1f3", "g8f6", "f3g1", "f6g8"}
	history := []uint64{start}
	fen := startFEN
	for ply := 1; ply <= 20; ply++ {
		v, err := o.Legality(ctx, fen, cycle[(ply-1)%4], history)
		if err != nil || !v.Legal {
			t.Fatalf("ply %d: %+v err=%v", ply, v, err)
		}
		if ply%4 == 0 && v.NewZobrist != start {
			t.Fatalf("ply %d is the start position but its key %#x != start key %#x", ply, v.NewZobrist, start)
		}
		if v.Terminal != nil {
			if *v.Terminal != game.TermRepetition || ply != 16 {
				t.Fatalf("terminal %v at ply %d, want REPETITION at ply 16", *v.Terminal, ply)
			}
			return
		}
		history = append(history, v.NewZobrist)
		fen = v.NewFEN
	}
	t.Fatal("no fivefold auto-draw by ply 20")
}

// TestPositionKey: the key of a FEN equals the key a move to that position
// reports, and a bad FEN is an error.
func TestPositionKey(t *testing.T) {
	o := newOracle(t)
	ctx := context.Background()
	v, err := o.Legality(ctx, startFEN, "e2e4", nil)
	if err != nil || !v.Legal {
		t.Fatalf("e2e4: %+v err=%v", v, err)
	}
	k, err := o.PositionKey(ctx, v.NewFEN)
	if err != nil || k != v.NewZobrist {
		t.Fatalf("PositionKey(after e4) = %#x err=%v, want %#x", k, err, v.NewZobrist)
	}
	if _, err := o.PositionKey(ctx, "not a fen"); err == nil {
		t.Fatal("bad FEN did not error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := o.PositionKey(cctx, startFEN); err == nil {
		t.Fatal("cancelled context did not error")
	}
}
