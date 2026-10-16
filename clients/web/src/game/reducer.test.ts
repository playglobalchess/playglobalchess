import { describe, it, expect } from "vitest";
import { reduce, initialState, type GameState } from "./reducer";
import { indexToSquare, squareToIndex } from "../board/position";
import { Termination, type ServerMsg } from "../codec/messages";

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const GID = "f47ac10b-58cc-4372-a567-0e02b2c3d479";

function matched(seat: number): ServerMsg {
  return {
    Matched: {
      game_id: GID,
      seat_idx: seat,
      opponent: "grandmaster_jane",
      start_state: { fen: START_FEN, turn: 0, clocks: [300000, 300000] },
    },
  };
}

/** A playing state as seat 0 (white), fresh from MATCHED. */
function playingAsWhite(): GameState {
  return reduce(initialState, { kind: "server", msg: matched(0) });
}

function pieceAt(s: GameState, sq: string): string | null {
  if (s.position == null) throw new Error("no position");
  return s.position.board[squareToIndex(sq)] ?? null;
}

describe("game reducer", () => {
  it("MATCHED enters playing with the start position + clocks", () => {
    const s = playingAsWhite();
    expect(s.phase).toBe("playing");
    expect(s.gameId).toBe(GID);
    expect(s.mySeat).toBe(0);
    expect(pieceAt(s, "e2")).toBe("P");
    expect(s.clocks).toEqual([300000, 300000]);
  });

  it("optimistic localMove shows the move and records a revert point", () => {
    const s0 = playingAsWhite();
    const s1 = reduce(s0, { kind: "localMove", uci: "e2e4" });
    expect(pieceAt(s1, "e4")).toBe("P");
    expect(pieceAt(s1, "e2")).toBeNull();
    expect(s1.pendingMove?.uci).toBe("e2e4");
  });

  it("MOVE_ACCEPTED confirming the pending move clears pending, keeps the board", () => {
    const s0 = playingAsWhite();
    const s1 = reduce(s0, { kind: "localMove", uci: "e2e4" });
    const s2 = reduce(s1, {
      kind: "server",
      msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } },
    });
    expect(s2.pendingMove).toBeNull();
    expect(pieceAt(s2, "e4")).toBe("P");
    expect(s2.ply).toBe(1);
    // Mover is white (seat 0): its clock updated.
    expect(s2.clocks[0]).toBe(299000);
    expect(s2.clocks[1]).toBe(300000);
  });

  it("MOVE_REJECTED reverts the optimistic move and surfaces the reason", () => {
    const s0 = playingAsWhite();
    const s1 = reduce(s0, { kind: "localMove", uci: "e2e4" });
    const s2 = reduce(s1, {
      kind: "server",
      msg: { MoveRejected: { game_id: GID, ply_expected: 0, reason: 2 } }, // StalePly
    });
    // Reverted: e2 pawn back, e4 empty, no pending, reason surfaced.
    expect(pieceAt(s2, "e2")).toBe("P");
    expect(pieceAt(s2, "e4")).toBeNull();
    expect(s2.pendingMove).toBeNull();
    expect(s2.lastReject).toBe(2);
  });

  it("opponent MOVE_ACCEPTED (no pending) applies the move + the mover's clock", () => {
    // As white (seat 0): first our own move, then black replies.
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "e2e4" });
    s = reduce(s, { kind: "server", msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } } });
    // Black's reply e7e5 — we have NO pending, so the reducer applies the uci.
    s = reduce(s, { kind: "server", msg: { MoveAccepted: { game_id: GID, ply: 2, uci: "e7e5", clock_after_ms: 298000 } } });
    expect(pieceAt(s, "e5")).toBe("p");
    expect(pieceAt(s, "e7")).toBeNull();
    expect(s.ply).toBe(2);
    // Mover was black (seat 1): its clock updated; white's unchanged.
    expect(s.clocks[1]).toBe(298000);
    expect(s.clocks[0]).toBe(299000);
  });

  it("STATE resync rebuilds from the FEN fold and drops pending", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "e2e4" }); // optimistic, pending
    const resyncFen = "rnbqkbnr/pppp1ppp/8/4p3/4P3/8/PPPP1PPP/RNBQKBNR w KQkq - 0 2";
    s = reduce(s, {
      kind: "server",
      msg: { State: { game_id: GID, state: { fen: resyncFen, turn: 0, clocks: [280000, 275000] } } },
    });
    expect(s.pendingMove).toBeNull();
    expect(pieceAt(s, "e4")).toBe("P");
    expect(pieceAt(s, "e5")).toBe("p");
    expect(s.clocks).toEqual([280000, 275000]);
  });

  it("DRAW_OFFERED records a pending offer; GAME_END ends the game", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "server", msg: { DrawOffered: { game_id: GID, by: 1 } } });
    expect(s.drawOffer).toEqual({ by: 1 });
    s = reduce(s, {
      kind: "server",
      msg: { GameEnd: { game_id: GID, result_vector: [2, 0], termination: Termination.Mate, rating_delta: [12, -12] } },
    });
    expect(s.phase).toBe("game_over");
    expect(s.drawOffer).toBeNull();
    expect(s.result?.termination).toBe(Termination.Mate);
    expect(s.result?.resultVector).toEqual([2, 0]);
  });

  it("a second localMove while one is pending is ignored (no double-apply)", () => {
    const s0 = playingAsWhite();
    const s1 = reduce(s0, { kind: "localMove", uci: "e2e4" });
    const s2 = reduce(s1, { kind: "localMove", uci: "d2d4" });
    expect(s2).toBe(s1); // unchanged
  });

  it("localMove outside playing phase is ignored", () => {
    const s = reduce(initialState, { kind: "localMove", uci: "e2e4" });
    expect(s).toBe(initialState);
  });

  it("square/index helpers round-trip", () => {
    for (const sq of ["a8", "h8", "a1", "h1", "e4", "d5"]) {
      expect(indexToSquare(squareToIndex(sq))).toBe(sq);
    }
  });
});
