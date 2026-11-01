import { describe, it, expect } from "vitest";
import { reduce, initialState, type GameState, type SyncTrigger } from "./reducer";
import { SYNC_MAX_ATTEMPTS, SYNC_MAX_CYCLES } from "./recovery";
import { indexToSquare, squareToIndex } from "../board/position";
import { Termination, Reason, ErrorCode, type ServerMsg } from "../codec/messages";

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

  // --- adversarial paths (found by context-free review) ---------------------

  it("MOVE_ACCEPTED with a DIFFERENT uci than pending rebases, does not stack moves", () => {
    // We optimistically play e2e4 (pending). The server's accept at ply 1 carries
    // a DIFFERENT move d2d4 (a race / stale frame). The board must show ONLY d2d4
    // from the start position — never e2e4 AND d2d4 both.
    const s0 = playingAsWhite();
    const s1 = reduce(s0, { kind: "localMove", uci: "e2e4" });
    const s2 = reduce(s1, {
      kind: "server",
      msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "d2d4", clock_after_ms: 299000 } },
    });
    expect(pieceAt(s2, "d4")).toBe("P"); // the authoritative move applied
    expect(pieceAt(s2, "e4")).toBeNull(); // the optimistic move reverted
    expect(pieceAt(s2, "e2")).toBe("P"); // e-pawn back home
    expect(pieceAt(s2, "d2")).toBeNull();
    expect(s2.pendingMove).toBeNull();
    expect(s2.ply).toBe(1);
    expect(s2.clocks[0]).toBe(299000); // mover = white (odd ply) by parity
  });

  it("a duplicate MOVE_ACCEPTED for the same ply is idempotent (no double-apply)", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "e2e4" });
    s = reduce(s, { kind: "server", msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } } });
    const afterFirst = s;
    // Replay the SAME accept (at-least-once delivery / reconnect replay).
    s = reduce(s, { kind: "server", msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } } });
    // Board + ply unchanged — the second frame did not re-apply e2e4.
    expect(s.position?.board).toEqual(afterFirst.position?.board);
    expect(s.ply).toBe(1);
    expect(pieceAt(s, "e4")).toBe("P");
  });

  it("a MOVE_ACCEPTED that skips a ply starts SYNCING and does not jump the board", () => {
    const s0 = playingAsWhite(); // ply 0
    const s1 = reduce(s0, {
      kind: "server",
      msg: { MoveAccepted: { game_id: GID, ply: 3, uci: "e2e4", clock_after_ms: 299000 } }, // gap: expected 1
    });
    expect(s1.sync.phase).toBe("SYNCING");
    expect(s1.ply).toBe(0); // did not jump
    expect(pieceAt(s1, "e4")).toBeNull(); // did not apply out-of-order
  });

  it("STATE returns to IN_SYNC (resync fulfilled)", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "server", msg: { MoveAccepted: { game_id: GID, ply: 3, uci: "e2e4", clock_after_ms: 1 } } });
    expect(s.sync.phase).toBe("SYNCING");
    s = reduce(s, { kind: "server", msg: { State: { game_id: GID, state: { fen: START_FEN, turn: 0, clocks: [300000, 300000] } } } });
    expect(s.sync.phase).toBe("IN_SYNC");
  });

  it("localMove off our turn is ignored (defense in depth)", () => {
    // As white (seat 0), it is white's turn at the start. A move string that
    // belongs to black's turn is still gated by the pending/turn guard once a
    // move is pending; here we assert the turn guard directly by playing as
    // white when the position says black to move.
    const blackToMove = reduce(initialState, {
      kind: "server",
      msg: { Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR b KQkq - 0 1", turn: 1, clocks: [300000, 300000] } } },
    });
    const s = reduce(blackToMove, { kind: "localMove", uci: "e2e4" });
    expect(s).toBe(blackToMove); // ignored — not white's turn
  });

  // --- review-fix regressions (step 5 context-free review) -----------------

  it("B1: STATE restores ply from the FEN so a move after resync uses the right ply_expected", () => {
    // Reach a gap: at ply 0, a MOVE_ACCEPTED jumps to ply 5 → SYNCING.
    let s = playingAsWhite();
    s = reduce(s, {
      kind: "server",
      msg: { MoveAccepted: { game_id: GID, ply: 5, uci: "e2e4", clock_after_ms: 1 } },
    });
    expect(s.sync.phase).toBe("SYNCING");
    expect(s.ply).toBe(0); // still stale — not advanced on a gap

    // The server returns an authoritative STATE at fullmove 3, white to move
    // (= ply 4). Before the fix, ply stayed 0 and the next move sent ply 1.
    const resyncFen = "r1bqkb1r/pppp1ppp/2n2n2/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R w KQkq - 4 3";
    s = reduce(s, {
      kind: "server",
      msg: { State: { game_id: GID, state: { fen: resyncFen, turn: 0, clocks: [1, 1] } } },
    });
    expect(s.sync.phase).toBe("IN_SYNC");
    expect(s.ply).toBe(4); // RESTORED from the FEN (was the bug: stayed 0)

    // Now a move sends ply_expected = ply + 1 = 5 (what the server expects),
    // not the stale 1 that would be rejected StalePly forever.
    expect(s.ply + 1).toBe(5);
  });

  it("S2: a StalePly reject raises SYNCING (the recovery trigger)", () => {
    let s = playingAsWhite();
    // A reject arriving with NO pending move (e.g. after a reconnect) must still
    // trigger a resync when it is StalePly.
    s = reduce(s, {
      kind: "server",
      msg: { MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.StalePly } },
    });
    expect(s.sync.phase).toBe("SYNCING");
    expect(s.lastReject).toBe(Reason.StalePly);
  });

  it("S2: a non-stale reject (Illegal) does NOT raise SYNCING", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "e2e4" });
    s = reduce(s, {
      kind: "server",
      msg: { MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.Illegal } },
    });
    expect(s.sync.phase).toBe("IN_SYNC"); // illegal is not a desync
    expect(s.lastReject).toBe(Reason.Illegal);
    expect(pieceAt(s, "e2")).toBe("P"); // reverted
  });

  it("S3: an Error (UNSUPPORTED_VERSION) is surfaced in state.error", () => {
    let s = playingAsWhite();
    s = reduce(s, {
      kind: "server",
      msg: { Error: { code: ErrorCode.UnsupportedVersion, msg: "bad major" } },
    });
    expect(s.error).toBe(ErrorCode.UnsupportedVersion);
  });

  it("N2: GAME_END ends recovery and clears a stale / lastReject", () => {
    let s = playingAsWhite();
    s = reduce(s, {
      kind: "server",
      msg: { MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.StalePly } },
    });
    expect(s.sync.phase).toBe("SYNCING");
    s = reduce(s, {
      kind: "server",
      msg: {
        GameEnd: {
          game_id: GID,
          result_vector: [2, 0],
          termination: Termination.Resign,
          rating_delta: [0, 0],
        },
      },
    });
    expect(s.phase).toBe("game_over");
    expect(s.sync.phase).toBe("IN_SYNC");
    expect(s.lastReject).toBeNull();
  });

  it("F2: lastMove is set on accept and cleared by a STATE resync", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "e2e4" });
    s = reduce(s, {
      kind: "server",
      msg: { MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } },
    });
    expect(s.lastMove).toEqual({ uci: "e2e4", moverSeat: 0 });
    // A resync STATE is a fold — the "most recent move" is no longer meaningful
    // and must be cleared so the SR announcement does not re-read a stale move.
    s = reduce(s, {
      kind: "server",
      msg: {
        State: {
          game_id: GID,
          state: {
            fen: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1",
            turn: 1,
            clocks: [299000, 300000],
          },
        },
      },
    });
    expect(s.lastMove).toBeNull();
  });
});

// --- SPEC-S4-WEB-F01 §3.4.1 consistency & recovery (reducer rows of §6) -------

const E4_FEN_B = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1";

function srv(msg: ServerMsg) {
  return { kind: "server", msg } as const;
}
function accepted(ply: number, uci: string): ServerMsg {
  return { MoveAccepted: { game_id: GID, ply, uci, clock_after_ms: 299000 } };
}
function stateMsg(fen: string, turn: number, clocks = [300000, 300000]): ServerMsg {
  return { State: { game_id: GID, state: { fen, turn, clocks } } };
}
/** In SYNCING(1) via a T1 gap. */
function syncing(): GameState {
  return reduce(playingAsWhite(), srv(accepted(3, "e2e4")));
}
/** Fail the current attempt by timeout, completing backoff when there is one. */
function failByTimeout(s: GameState): GameState {
  let n = reduce(s, { kind: "syncReplyTimeout", seq: s.sync.seq });
  if (n.sync.step === "backoff") n = reduce(n, { kind: "syncBackoffElapsed", seq: n.sync.seq });
  return n;
}
function toFailed(): GameState {
  let s = syncing();
  for (let i = 0; i < SYNC_MAX_ATTEMPTS + SYNC_MAX_CYCLES; i++) s = failByTimeout(s);
  return s;
}

describe("recovery triggers (§3.4.1 T1-T5)", () => {
  function expectTrigger(s: GameState, t: SyncTrigger): void {
    expect(s.sync.phase).toBe("SYNCING");
    expect(s.sync.attempt).toBe(1);
    expect(s.sync.trigger).toBe(t);
  }
  it("T1: MOVE_ACCEPTED ply > local+1 raises", () => {
    expectTrigger(reduce(playingAsWhite(), srv(accepted(2, "e2e4"))), "T1_GAP");
  });
  it("T2: ply below local raises", () => {
    let s = playingAsWhite();
    s = reduce(s, srv(accepted(1, "e2e4")));
    s = reduce(s, srv(accepted(2, "e7e5")));
    expectTrigger(reduce(s, srv(accepted(1, "e2e4"))), "T2_MISMATCH");
  });
  it("T2: same ply but a DIFFERENT uci than the last applied move raises", () => {
    let s = playingAsWhite();
    s = reduce(s, srv(accepted(1, "e2e4")));
    expectTrigger(reduce(s, srv(accepted(1, "d2d4"))), "T2_MISMATCH");
  });
  it("T2: the exact duplicate (same ply + same uci) is ignored silently", () => {
    let s = playingAsWhite();
    s = reduce(s, srv(accepted(1, "e2e4")));
    const dup = reduce(s, srv(accepted(1, "e2e4")));
    expect(dup).toBe(s);
    expect(dup.sync.phase).toBe("IN_SYNC");
  });
  it("T3: an accepted uci the binding cannot apply raises", () => {
    // ply is contiguous, but there is no piece on e5 to move.
    expectTrigger(reduce(playingAsWhite(), srv(accepted(1, "e5e6"))), "T3_UNAPPLIABLE");
  });
  it("T4: MOVE_REJECTED STALE_PLY raises; a non-stale reject does not", () => {
    const stale = reduce(playingAsWhite(), srv({ MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.StalePly } }));
    expectTrigger(stale, "T4_STALE");
    const illegal = reduce(playingAsWhite(), srv({ MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.Illegal } }));
    expect(illegal.sync.phase).toBe("IN_SYNC");
  });
  it("T5: STATE whose FEN side-to-move disagrees with wire turn raises and is NOT applied", () => {
    const s0 = playingAsWhite();
    const s1 = reduce(s0, srv(stateMsg(E4_FEN_B, 0))); // FEN says black, wire says white
    expectTrigger(s1, "T5_INCONSISTENT");
    expect(s1.position).toBe(s0.position);
    expect(s1.ply).toBe(0);
  });
  it("T5: STATE with a missing or non-positive fullmove raises", () => {
    expectTrigger(reduce(playingAsWhite(), srv(stateMsg("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq -", 1))), "T5_INCONSISTENT");
    expectTrigger(reduce(playingAsWhite(), srv(stateMsg("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 0", 1))), "T5_INCONSISTENT");
  });
  it("T5: an inconsistent MATCHED start_state raises on the new game", () => {
    const s = reduce(initialState, srv({
      Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: START_FEN, turn: 1, clocks: [1, 1] } },
    }));
    expectTrigger(s, "T5_INCONSISTENT");
  });
  it("a trigger outside a live game (game_over) does not start recovery", () => {
    let s = playingAsWhite();
    s = reduce(s, srv({ GameEnd: { game_id: GID, result_vector: [2, 0], termination: Termination.Mate, rating_delta: [0, 0] } }));
    expect(reduce(s, srv(accepted(9, "e2e4"))).sync.phase).toBe("IN_SYNC");
  });
});

describe("recovery state machine (§3.4.1 S)", () => {
  it("a consistent STATE with a LOWER ply than local is applied (takeback) and resets to IN_SYNC", () => {
    let s = playingAsWhite();
    s = reduce(s, srv(accepted(1, "e2e4")));
    s = reduce(s, srv(accepted(2, "e7e5")));
    s = reduce(s, srv(stateMsg(E4_FEN_B, 1)));
    expect(s.ply).toBe(1);
    expect(pieceAt(s, "e5")).toBeNull();
    expect(s.sync.phase).toBe("IN_SYNC");
  });
  it("a consistent STATE resets counters, including from FAILED", () => {
    const failed = toFailed();
    expect(failed.sync.phase).toBe("FAILED");
    const s = reduce(failed, srv(stateMsg(E4_FEN_B, 1)));
    expect(s.sync).toMatchObject({ phase: "IN_SYNC", attempt: 0, cycle: 0 });
    expect(s.ply).toBe(1);
  });
  it("SYNCING(n) fails after timeout -> backoff -> SYNCING(n+1); a new seq each attempt", () => {
    const s1 = syncing();
    const b = reduce(s1, { kind: "syncReplyTimeout", seq: s1.sync.seq });
    expect(b.sync).toMatchObject({ phase: "SYNCING", attempt: 1, step: "backoff" });
    const s2 = reduce(b, { kind: "syncBackoffElapsed", seq: b.sync.seq });
    expect(s2.sync).toMatchObject({ phase: "SYNCING", attempt: 2, step: "await_reply" });
    expect(s2.sync.seq).toBeGreaterThan(s1.sync.seq);
  });
  it("an inconsistent STATE reply fails the attempt like a timeout (not applied)", () => {
    const s1 = syncing();
    const b = reduce(s1, srv(stateMsg(E4_FEN_B, 0)));
    expect(b.sync).toMatchObject({ phase: "SYNCING", attempt: 1, step: "backoff" });
    expect(b.position).toBe(s1.position);
  });
  it("after N failed attempts -> RECONNECTING(1); after K failed cycles -> FAILED", () => {
    let s = syncing();
    for (let n = 1; n < SYNC_MAX_ATTEMPTS; n++) s = failByTimeout(s);
    expect(s.sync).toMatchObject({ phase: "SYNCING", attempt: SYNC_MAX_ATTEMPTS });
    s = failByTimeout(s);
    expect(s.sync).toMatchObject({ phase: "RECONNECTING", cycle: 1, step: "await_reply" });
    s = failByTimeout(s);
    expect(s.sync).toMatchObject({ phase: "RECONNECTING", cycle: 2 });
    s = failByTimeout(s);
    expect(s.sync.phase).toBe("FAILED");
  });
  it("a stale timer action (old seq) is a no-op", () => {
    const s1 = syncing();
    const b = reduce(s1, { kind: "syncReplyTimeout", seq: s1.sync.seq });
    expect(reduce(b, { kind: "syncReplyTimeout", seq: s1.sync.seq })).toBe(b);
    expect(reduce(b, { kind: "syncBackoffElapsed", seq: b.sync.seq + 5 })).toBe(b);
  });
  it("a timeout armed for an OLDER attempt cannot fail the current attempt (seq guard)", () => {
    const s1 = syncing();
    const b = reduce(s1, { kind: "syncReplyTimeout", seq: s1.sync.seq });
    const s2 = reduce(b, { kind: "syncBackoffElapsed", seq: b.sync.seq }); // attempt 2, newer seq
    expect(reduce(s2, { kind: "syncReplyTimeout", seq: s1.sync.seq })).toBe(s2);
  });
  it("coalescing: a new trigger during SYNCING/RECONNECTING does not restart or add attempts", () => {
    const s1 = syncing();
    expect(reduce(s1, srv(accepted(9, "e2e4"))).sync).toEqual(s1.sync);
    let r = s1;
    for (let i = 0; i < SYNC_MAX_ATTEMPTS; i++) r = failByTimeout(r);
    expect(r.sync.phase).toBe("RECONNECTING");
    expect(reduce(r, srv({ MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.StalePly } })).sync).toEqual(r.sync);
  });
  it("FAILED is terminal for triggers (no new episode)", () => {
    const f = toFailed();
    expect(reduce(f, srv(accepted(9, "e2e4"))).sync).toEqual(f.sync);
  });
  it("MOVE_ACCEPTED before the awaited STATE is not applied; after the STATE it is", () => {
    const s1 = syncing();
    const during = reduce(s1, srv(accepted(1, "e2e4")));
    expect(during.ply).toBe(0);
    expect(pieceAt(during, "e4")).toBeNull();
    // Also not applied during backoff or RECONNECTING.
    const b = reduce(s1, { kind: "syncReplyTimeout", seq: s1.sync.seq });
    expect(reduce(b, srv(accepted(1, "e2e4"))).ply).toBe(0);
    const after = reduce(reduce(s1, srv(stateMsg(E4_FEN_B, 1))), srv(accepted(2, "e7e5")));
    expect(after.ply).toBe(2);
    expect(pieceAt(after, "e5")).toBe("p");
  });
  it("a pending optimistic move is discarded when the STATE lands", () => {
    let s = playingAsWhite();
    s = reduce(s, { kind: "localMove", uci: "d2d4" });
    s = reduce(s, srv(accepted(4, "e2e4"))); // gap while pending -> SYNCING
    expect(s.pendingMove).not.toBeNull();
    s = reduce(s, srv(stateMsg(E4_FEN_B, 1)));
    expect(s.pendingMove).toBeNull();
    expect(pieceAt(s, "e4")).toBe("P");
    expect(pieceAt(s, "d4")).toBeNull();
  });
  it("localMove is refused while recovering", () => {
    const s = syncing();
    expect(reduce(s, { kind: "localMove", uci: "e2e4" })).toBe(s);
  });
  it("GAME_END applies immediately and ends recovery in EVERY phase", () => {
    const end: ServerMsg = { GameEnd: { game_id: GID, result_vector: [0, 2], termination: Termination.Resign, rating_delta: [0, 0] } };
    const s1 = syncing();
    const backoff = reduce(s1, { kind: "syncReplyTimeout", seq: s1.sync.seq });
    let reconnecting = s1;
    for (let i = 0; i < SYNC_MAX_ATTEMPTS; i++) reconnecting = failByTimeout(reconnecting);
    for (const s of [s1, backoff, reconnecting, toFailed()]) {
      const e = reduce(s, srv(end));
      expect(e.phase).toBe("game_over");
      expect(e.sync.phase).toBe("IN_SYNC");
      expect(e.result?.resultVector).toEqual([0, 2]);
    }
  });
  it("DRAW_OFFERED is recorded in every recovery phase", () => {
    for (const s of [syncing(), toFailed()]) {
      expect(reduce(s, srv({ DrawOffered: { game_id: GID, by: 1 } })).drawOffer).toEqual({ by: 1 });
    }
  });
  it("MATCHED for a different game resets recovery to IN_SYNC", () => {
    const s = reduce(syncing(), srv({
      Matched: { game_id: "other-game", seat_idx: 1, opponent: "y", start_state: { fen: START_FEN, turn: 0, clocks: [1, 1] } },
    }));
    expect(s.gameId).toBe("other-game");
    expect(s.sync).toMatchObject({ phase: "IN_SYNC", attempt: 0, cycle: 0 });
  });
  it("seq is monotonic across episodes (a resync guard never collides with an old key)", () => {
    const first = syncing();
    const healed = reduce(first, srv(stateMsg(E4_FEN_B, 1)));
    const second = reduce(healed, srv(accepted(9, "e7e5")));
    expect(second.sync.phase).toBe("SYNCING");
    expect(second.sync.seq).toBeGreaterThan(first.sync.seq);
  });
});
