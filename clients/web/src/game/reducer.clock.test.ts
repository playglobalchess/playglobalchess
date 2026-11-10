import { describe, expect, it } from "vitest";
import { reduce, initialState, displayClocks, type GameState } from "./reducer";
import { NO_SEAT, Reason, Status, type ServerMsg } from "../codec/messages";
import { sb } from "../test/stateBody";

// Ticking clocks (#112): the reducer keeps the anchor, displayClocks reads it.

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const AFTER_E4 = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1";
const GID = "f47ac10b-58cc-4372-a567-0e02b2c3d479";

const server = (s: GameState, msg: ServerMsg, at: number): GameState => reduce(s, { kind: "server", msg, at });

/** Seat `seat` matched at local time `at`; White's clock runs from MATCHED. */
function matched(seat: number, at: number): GameState {
  const s = reduce(initialState, { kind: "seek", rated: false });
  return server(
    s,
    {
      Matched: {
        game_id: GID,
        seat_idx: seat,
        opponent: "x",
        start_state: sb(START_FEN, 0, [300000, 300000], { you_seat: seat, running_seat: 0, status: Status.Live }),
      },
    },
    at,
  );
}

const accepted = (s: GameState, ply: number, uci: string, clockAfter: number, at: number): GameState =>
  server(s, { MoveAccepted: { game_id: GID, ply, uci, clock_after_ms: clockAfter } }, at);

describe("ticking clocks (#112)", () => {
  it("only the side to move ticks", () => {
    const s = matched(1, 1000);
    expect(s.clockRun).toEqual({ seat: 0, since: 1000 });
    expect(displayClocks(s, 6000)).toEqual([295000, 300000]);
  });

  it("MOVE_ACCEPTED sets the mover's clock and starts the other side's", () => {
    let s = matched(1, 1000);
    s = accepted(s, 1, "e2e4", 296500, 4500);
    expect(s.clockRun).toEqual({ seat: 1, since: 4500 });
    // White is frozen at the server value; Black ticks from the accept.
    expect(displayClocks(s, 7500)).toEqual([296500, 297000]);
  });

  it("each server update resyncs (corrects) the display", () => {
    let s = matched(1, 1000);
    expect(displayClocks(s, 11000)).toEqual([290000, 300000]);
    // The server says White actually has 292 s left at this point: trust it.
    s = server(
      s,
      { State: { game_id: GID, state: sb(START_FEN, 0, [292000, 300000], { you_seat: 1, running_seat: 0, status: Status.Live }) } },
      11000,
    );
    expect(displayClocks(s, 11000)).toEqual([292000, 300000]);
    expect(displayClocks(s, 12000)).toEqual([291000, 300000]);
  });

  it("clamps at 0 and never goes negative (the server decides the flag)", () => {
    const s = matched(1, 0);
    expect(displayClocks(s, 400000)).toEqual([0, 300000]);
    expect(s.phase).toBe("playing");
  });

  it("GAME_END freezes the shown clocks", () => {
    let s = matched(1, 1000);
    s = server(s, { GameEnd: { game_id: GID, result_vector: [0, 2], termination: 0x02, rating_delta: [] } }, 21000);
    expect(s.clockRun).toBeNull();
    expect(displayClocks(s, 99000)).toEqual([280000, 300000]);
  });

  it("an ended STATE runs no clock", () => {
    const s = server(
      matched(1, 0),
      { State: { game_id: GID, state: sb(START_FEN, 0, [1000, 2000], { status: Status.Ended, running_seat: NO_SEAT }) } },
      5000,
    );
    expect(s.clockRun).toBeNull();
    expect(displayClocks(s, 60000)).toEqual([1000, 2000]);
  });

  it("STATE with no running seat (clock not started) does not tick", () => {
    const s = server(matched(0, 0), { State: { game_id: GID, state: sb(START_FEN, 0, [300000, 300000]) } }, 1000);
    expect(s.clockRun).toBeNull();
  });

  it("our move stops our clock at once; the accept starts the opponent's", () => {
    let s = matched(0, 1000);
    s = reduce(s, { kind: "localMove", uci: "e2e4", at: 3000 });
    expect(s.clockRun).toBeNull();
    expect(displayClocks(s, 9000)).toEqual([298000, 300000]);
    s = accepted(s, 1, "e2e4", 297900, 3100);
    expect(s.clockRun).toEqual({ seat: 1, since: 3100 });
    expect(displayClocks(s, 4100)).toEqual([297900, 299000]);
  });

  it("a rejected move restarts our clock as if it never stopped", () => {
    let s = matched(0, 1000);
    s = reduce(s, { kind: "localMove", uci: "e2e4", at: 3000 });
    s = server(s, { MoveRejected: { game_id: GID, ply_expected: 1, reason: Reason.Illegal } }, 3200);
    expect(s.clockRun).toEqual({ seat: 0, since: 1000 });
    expect(displayClocks(s, 5000)).toEqual([296000, 300000]);
  });

  it("a MOVE_ACCEPTED after GAME_END (#92 reordering) does not restart a clock", () => {
    let s = matched(0, 0);
    s = server(s, { GameEnd: { game_id: GID, result_vector: [2, 0], termination: 0x02, rating_delta: [] } }, 1000);
    s = accepted(s, 1, "e2e4", 299000, 1100);
    expect(s.clockRun).toBeNull();
  });

  it("STATE after a reload picks up the running seat (Black to move)", () => {
    const s = server(
      initialState,
      { State: { game_id: GID, state: sb(AFTER_E4, 1, [298000, 300000], { you_seat: 0 }) } },
      500,
    );
    expect(s.clockRun).toEqual({ seat: 1, since: 500 });
    expect(displayClocks(s, 2500)).toEqual([298000, 298000]);
  });
});
