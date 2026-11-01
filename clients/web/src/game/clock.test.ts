// SPEC-S4-WEB-F01 §3.3.1 — the pure clock model (C1-C8, C10 format/cadence).
// Times are the reducer's `at` anchors (performance.now): no timers needed.

import { describe, it, expect } from "vitest";
import { reduce, initialState, type GameState } from "./reducer";
import { displayedMs, formatClock, refreshIntervalMs, runningSeat } from "./clock";
import { Reason, Termination, type ServerMsg } from "../codec/messages";

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const E4_FEN_B = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1";
const GID = "g-clock";

const srv = (msg: ServerMsg, at: number) => ({ kind: "server", msg, at }) as const;
const matched = (seat: number, clocks = [300000, 300000]): ServerMsg => ({
  Matched: { game_id: GID, seat_idx: seat, opponent: "x", start_state: { fen: START_FEN, turn: 0, clocks } },
});
const accepted = (ply: number, uci: string, clock: number): ServerMsg => ({
  MoveAccepted: { game_id: GID, ply, uci, clock_after_ms: clock },
});
const shown = (s: GameState, seat: number, now: number): number => displayedMs(s, seat, now);

/** We are White (seat 0), matched at t=1000. */
const start = (): GameState => reduce(initialState, srv(matched(0), 1000));

describe("clock model (§3.3.1)", () => {
  it("C1: MATCHED and STATE set every seat; MOVE_ACCEPTED sets only the mover", () => {
    let s = reduce(initialState, srv(matched(0, [111000, 222000]), 0));
    expect(s.clocks).toEqual([111000, 222000]);
    s = reduce(s, srv(accepted(1, "e2e4", 100000), 10));
    expect(s.clocks[0]).toBe(100000); // White moved
    expect(shown(s, 1, 10)).toBe(222000); // black's value untouched (and not yet running at 10)
    s = reduce(s, srv({ State: { game_id: GID, state: { fen: E4_FEN_B, turn: 1, clocks: [5000, 6000] } } }, 20));
    expect(s.clocks).toEqual([5000, 6000]);
  });

  it("C2: only the side to move runs; nothing runs before MATCHED or after GAME_END", () => {
    expect(runningSeat(initialState)).toBeNull();
    expect(runningSeat(reduce(initialState, { kind: "seek" }))).toBeNull();
    let s = start();
    expect(runningSeat(s)).toBe(0);
    expect(shown(s, 0, 6000)).toBe(295000);
    expect(shown(s, 1, 6000)).toBe(300000);
    s = reduce(s, srv(accepted(1, "e2e4", 299000), 7000));
    expect(runningSeat(s)).toBe(1);
    s = reduce(s, srv({ GameEnd: { game_id: GID, result_vector: [2, 0], termination: Termination.Resign, rating_delta: [0, 0] } }, 8000));
    expect(runningSeat(s)).toBeNull();
  });

  it("C2: White runs from MATCHED (no first-move grace)", () => {
    const s = start();
    expect(shown(s, 0, 1000)).toBe(300000);
    expect(shown(s, 0, 1000 + 2500)).toBe(297500);
  });

  it("C3: the value is derived from the anchor, so a jump of the clock stays correct; floored at 0", () => {
    const s = start();
    expect(shown(s, 0, 1000 + 60_000)).toBe(240000); // backgrounded 60 s, foregrounded
    expect(shown(s, 0, 1000 + 60_000)).toBe(shown(s, 0, 1000 + 60_000)); // pure: no per-read decrement
    expect(shown(s, 0, 1000 + 999_999_999)).toBe(0);
  });

  it("C4: own move freezes own at its displayed value and starts the opponent", () => {
    const s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    expect(shown(s, 0, 4000)).toBe(297000);
    expect(shown(s, 0, 20_000)).toBe(297000); // frozen
    expect(shown(s, 1, 6000)).toBe(298000); // opponent counting from the move
  });

  it("C4: accept sets own clock to clock_after_ms; the opponent keeps running", () => {
    let s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    s = reduce(s, srv(accepted(1, "e2e4", 296500), 4500));
    expect(shown(s, 0, 9000)).toBe(296500);
    expect(shown(s, 1, 6000)).toBe(298000); // unchanged continuity
  });

  it("C4: reject resumes own clock as if never stopped; opponent returns to its prior value", () => {
    let s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    s = reduce(s, srv({ MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.Illegal } }, 6000));
    // value at the ORIGINAL anchor (1000) minus all elapsed time: 300000 - (9000-1000)
    expect(shown(s, 0, 9000)).toBe(292000);
    expect(shown(s, 1, 9000)).toBe(300000);
    expect(runningSeat(s)).toBe(0);
  });

  it("C5: an opponent move sets their value and stops them; ours starts at receipt from its last value", () => {
    let s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    s = reduce(s, srv(accepted(1, "e2e4", 296500), 4500));
    s = reduce(s, srv(accepted(2, "e7e5", 290000), 8000));
    expect(shown(s, 1, 8000)).toBe(290000);
    expect(shown(s, 1, 30_000)).toBe(290000); // stopped
    expect(shown(s, 0, 8000)).toBe(296500); // ours starts from its last value
    expect(shown(s, 0, 10_000)).toBe(294500);
  });

  it("C6: the display stops at 0 and the client never leaves the game itself", () => {
    const s = reduce(initialState, srv(matched(0, [1000, 1000]), 0));
    expect(shown(s, 0, 5000)).toBe(0);
    expect(s.phase).toBe("playing");
    expect(reduce(s, { kind: "syncReplyTimeout", seq: 99 })).toBe(s); // nothing to flag with
  });

  it("C7: GAME_END stops every clock at its displayed value", () => {
    let s = start();
    s = reduce(s, srv({ GameEnd: { game_id: GID, result_vector: [0, 2], termination: Termination.Resign, rating_delta: [0, 0] } }, 6000));
    expect(shown(s, 0, 6000)).toBe(295000);
    expect(shown(s, 0, 99_000)).toBe(295000);
    expect(shown(s, 1, 99_000)).toBe(300000);
  });

  it("C7: GAME_END during an un-confirmed own move freezes both at their displayed values", () => {
    let s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    s = reduce(s, srv({ GameEnd: { game_id: GID, result_vector: [2, 0], termination: Termination.Mate, rating_delta: [0, 0] } }, 5000));
    expect(shown(s, 0, 50_000)).toBe(297000);
    expect(shown(s, 1, 50_000)).toBe(299000);
  });

  it("C8: clocks keep running through SYNCING/RECONNECTING; the next STATE overrides every value", () => {
    let s = start();
    s = reduce(s, srv(accepted(5, "e2e4", 1), 2000)); // gap -> SYNCING
    expect(s.sync.phase).toBe("SYNCING");
    expect(shown(s, 0, 11_000)).toBe(290000); // still counting
    let r = s;
    for (let i = 0; i < 3; i++) {
      r = reduce(r, { kind: "syncReplyTimeout", seq: r.sync.seq });
      if (r.sync.step === "backoff") r = reduce(r, { kind: "syncBackoffElapsed", seq: r.sync.seq });
    }
    expect(r.sync.phase).toBe("RECONNECTING");
    expect(shown(r, 0, 21_000)).toBe(280000);
    r = reduce(r, srv({ State: { game_id: GID, state: { fen: E4_FEN_B, turn: 1, clocks: [250000, 260000] } } }, 22_000));
    expect(shown(r, 0, 22_000)).toBe(250000);
    expect(shown(r, 1, 22_000)).toBe(260000);
    expect(runningSeat(r)).toBe(1);
  });

  it("MATCHED for a new game resets the clocks and anchors", () => {
    let s = start();
    s = reduce(s, srv(accepted(1, "e2e4", 280000), 5000));
    s = reduce(s, srv(matched(1, [60000, 60000]), 9000));
    expect(s.clocks).toEqual([60000, 60000]);
    expect(shown(s, 0, 10_000)).toBe(59000);
  });

  it("an own move accepted AFTER a mid-move STATE: the STATE discards the pending move and re-anchors", () => {
    let s = reduce(start(), { kind: "localMove", uci: "e2e4", at: 4000 });
    s = reduce(s, srv({ State: { game_id: GID, state: { fen: START_FEN, turn: 0, clocks: [290000, 300000] } } }, 5000));
    expect(s.pendingMove).toBeNull();
    expect(shown(s, 0, 6000)).toBe(289000);
    expect(shown(s, 1, 6000)).toBe(300000);
  });

  it("a reject when no move is pending does not touch the clocks", () => {
    const s = start();
    const r = reduce(s, srv({ MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.Illegal } }, 3000));
    expect(r.clocks).toEqual(s.clocks);
    expect(r.clockAt).toEqual(s.clockAt);
  });
});

describe("clock format and cadence (C10)", () => {
  it("formats m:ss, with tenths below 10 s and h:mm:ss from 1 h", () => {
    expect(formatClock(300000)).toBe("5:00");
    expect(formatClock(10_000)).toBe("0:10");
    expect(formatClock(9_999)).toBe("0:09.9");
    expect(formatClock(9_000)).toBe("0:09.0");
    expect(formatClock(0)).toBe("0:00.0");
    expect(formatClock(-5)).toBe("0:00.0");
    expect(formatClock(59_999 + 1)).toBe("1:00");
    expect(formatClock(3_599_999)).toBe("59:59");
    expect(formatClock(3_600_000)).toBe("1:00:00");
    expect(formatClock(3_661_000)).toBe("1:01:01");
  });
  it("refreshes at 10 Hz below 10 s and 1 Hz otherwise", () => {
    expect(refreshIntervalMs(9_999)).toBe(100);
    expect(refreshIntervalMs(10_000)).toBe(1000);
    expect(refreshIntervalMs(0)).toBe(100);
  });
});
