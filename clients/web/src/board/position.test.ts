import { describe, it, expect } from "vitest";
import { parseFen, applyUci, squareToIndex, plyFromFen } from "./position";

function at(fen: string, uci: string, sq: string): string | null {
  const pos = applyUci(parseFen(fen), uci);
  return pos.board[squareToIndex(sq)] ?? null;
}

describe("board position (mechanical apply — render only, NOT a rules engine)", () => {
  it("parses the start position", () => {
    const p = parseFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1");
    expect(p.board[squareToIndex("e1")]).toBe("K");
    expect(p.board[squareToIndex("e8")]).toBe("k");
    expect(p.turn).toBe("w");
  });

  it("moves a piece and flips the turn", () => {
    const p = applyUci(parseFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"), "e2e4");
    expect(p.board[squareToIndex("e4")]).toBe("P");
    expect(p.board[squareToIndex("e2")]).toBeNull();
    expect(p.turn).toBe("b");
  });

  it("promotion swaps the pawn for the promoted piece (color-correct)", () => {
    // White pawn on a7 promotes to queen on a8.
    expect(at("8/P7/8/8/8/8/8/k6K w - - 0 1", "a7a8q", "a8")).toBe("Q");
    // Black pawn on h2 promotes to knight on h1.
    expect(at("k6K/8/8/8/8/8/7p/8 b - - 0 1", "h2h1n", "h1")).toBe("n");
  });

  it("en-passant removes the captured pawn on the mover's rank", () => {
    // White pawn e5 captures en-passant to d6; the black pawn on d5 is removed.
    const fen = "rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3";
    const pos = applyUci(parseFen(fen), "e5d6");
    expect(pos.board[squareToIndex("d6")]).toBe("P"); // pawn arrived
    expect(pos.board[squareToIndex("d5")]).toBeNull(); // captured pawn gone
    expect(pos.board[squareToIndex("e5")]).toBeNull(); // mover left
  });

  it("castling king-side shifts the rook h→f", () => {
    const fen = "rnbqk2r/pppppppp/8/8/8/8/PPPPPPPP/RNBQK2R w KQkq - 0 1";
    const pos = applyUci(parseFen(fen), "e1g1");
    expect(pos.board[squareToIndex("g1")]).toBe("K");
    expect(pos.board[squareToIndex("f1")]).toBe("R");
    expect(pos.board[squareToIndex("h1")]).toBeNull();
  });

  it("castling queen-side shifts the rook a→d", () => {
    const fen = "r3kbnr/pppppppp/8/8/8/8/PPPPPPPP/R3KBNR w KQkq - 0 1";
    const pos = applyUci(parseFen(fen), "e1c1");
    expect(pos.board[squareToIndex("c1")]).toBe("K");
    expect(pos.board[squareToIndex("d1")]).toBe("R");
    expect(pos.board[squareToIndex("a1")]).toBeNull();
  });

  it("black castling king-side shifts the black rook h8→f8", () => {
    const fen = "rnbqk2r/pppppppp/8/8/8/8/PPPPPPPP/RNBQK2R b KQkq - 0 1";
    const pos = applyUci(parseFen(fen), "e8g8");
    expect(pos.board[squareToIndex("g8")]).toBe("k");
    expect(pos.board[squareToIndex("f8")]).toBe("r");
    expect(pos.board[squareToIndex("h8")]).toBeNull();
  });

  it("a normal capture overwrites the captured piece", () => {
    // White knight on e4 captures a black pawn on d6 (contrived placement).
    const fen = "k6K/8/3p4/8/4N3/8/8/8 w - - 0 1";
    const pos = applyUci(parseFen(fen), "e4d6");
    expect(pos.board[squareToIndex("d6")]).toBe("N"); // knight replaced the pawn
    expect(pos.board[squareToIndex("e4")]).toBeNull();
  });

  it("rejects a malformed FEN that does not fill the board", () => {
    expect(() => parseFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP w KQkq - 0 1")).toThrow();
  });
});

describe("plyFromFen (resync ply recovery — STATE carries no ply)", () => {
  it("startpos is ply 0", () => {
    expect(plyFromFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1")).toBe(0);
  });
  it("after 1.e4 (black to move, fullmove 1) is ply 1", () => {
    expect(plyFromFen("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")).toBe(1);
  });
  it("after 1.e4 e5 (white to move, fullmove 2) is ply 2", () => {
    expect(plyFromFen("rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq e6 0 2")).toBe(2);
  });
  it("a resumed mid-game FEN derives the right ply (fullmove 5, black) → ply 9", () => {
    expect(plyFromFen("r1bqkbnr/pppp1ppp/2n5/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R b KQkq - 2 5")).toBe(9);
  });
  it("a missing/garbled move-number field falls back to fullmove 1 (ply from side only)", () => {
    expect(plyFromFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR b")).toBe(1);
    expect(plyFromFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w")).toBe(0);
  });
});
