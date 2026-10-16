import { describe, it, expect } from "vitest";
import { parseFen, applyUci, squareToIndex } from "./position";

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

  it("rejects a malformed FEN that does not fill the board", () => {
    expect(() => parseFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP w KQkq - 0 1")).toThrow();
  });
});
