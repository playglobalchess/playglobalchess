import { describe, expect, it } from "vitest";
import { reduce, initialState, canOfferDraw, type GameState } from "./reducer";
import { canRequestTakeback } from "./useGame";
import { NO_SEAT, Status, type ServerMsg } from "../codec/messages";
import { sb } from "../test/stateBody";

// GS-7 client state rules (SPEC-S2-GAME-SERVER-GS7 §6.4, §7.4).

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const GID = "f47ac10b-58cc-4372-a567-0e02b2c3d479";

const server = (s: GameState, msg: ServerMsg): GameState => reduce(s, { kind: "server", msg });

/** Seat `seat` in an unrated game at the start position. */
function matched(seat: number, rated = false): GameState {
  const s = reduce(initialState, { kind: "seek", rated });
  return server(s, { Matched: { game_id: GID, seat_idx: seat, opponent: "x", start_state: sb(START_FEN, 0, [300000, 300000]) } });
}

function accepted(s: GameState, ply: number, uci: string): GameState {
  return server(s, { MoveAccepted: { game_id: GID, ply, uci, clock_after_ms: 299000 } });
}

/** Seat 0 (white) after 1. e4 e5. */
function afterTwoPlies(seat = 0): GameState {
  return accepted(accepted(matched(seat), 1, "e2e4"), 2, "e7e5");
}

describe("GS-7 reducer: draw prompts (§6.4)", () => {
  it("an opponent's move clears our pending offer; our own move keeps it", () => {
    let s = reduce(afterTwoPlies(0), { kind: "localOfferDraw" });
    expect(s.myDrawOffered).toBe(true);
    s = accepted(s, 3, "g1f3"); // our move (white, ply 3)
    expect(s.myDrawOffered).toBe(true);
    s = accepted(s, 4, "b8c6"); // opponent's move declines it
    expect(s.myDrawOffered).toBe(false);
  });

  it("our move clears the opponent's incoming offer", () => {
    let s = server(afterTwoPlies(0), { DrawOffered: { game_id: GID, by: 1 } });
    expect(s.drawOffer).toEqual({ by: 1 });
    s = accepted(s, 3, "g1f3");
    expect(s.drawOffer).toBeNull();
  });

  it("a duplicate MOVE_ACCEPTED (re-ack) clears nothing", () => {
    let s = server(afterTwoPlies(0), { DrawOffered: { game_id: GID, by: 1 } });
    s = accepted(s, 2, "e7e5"); // the same ply again
    expect(s.drawOffer).toEqual({ by: 1 });
  });

  it("DRAW_DECLINED clears our offer; local decline clears theirs", () => {
    let s = reduce(afterTwoPlies(0), { kind: "localOfferDraw" });
    s = server(s, { DrawDeclined: { game_id: GID, seat_idx: 1 } });
    expect(s.myDrawOffered).toBe(false);
    s = server(s, { DrawOffered: { game_id: GID, by: 1 } });
    s = reduce(s, { kind: "localDeclineDraw" });
    expect(s.drawOffer).toBeNull();
  });

  it("offering while the opponent's offer is pending is the accept, not our own offer", () => {
    let s = server(afterTwoPlies(0), { DrawOffered: { game_id: GID, by: 1 } });
    s = reduce(s, { kind: "localOfferDraw" });
    expect(s.myDrawOffered).toBe(false);
  });
});

describe("GS-7 reducer: takeback prompts (§6.4)", () => {
  it("TAKEBACK_OFFERED sets the incoming request; our move declines it", () => {
    let s = server(afterTwoPlies(0), { TakebackOffered: { game_id: GID, seat_idx: 1, at_ply: 2 } });
    expect(s.incomingTakeback).toEqual({ by: 1, atPly: 2 });
    s = accepted(s, 3, "g1f3");
    expect(s.incomingTakeback).toBeNull();
  });

  it("our request is cleared by the opponent's move, by TAKEBACK_DECLINED and by GAME_END", () => {
    let s = reduce(afterTwoPlies(0), { kind: "localRequestTakeback" });
    expect(s.myTakebackSent).toEqual({ atPly: 2 });
    expect(reduce(s, { kind: "localRequestTakeback" }).myTakebackSent).toEqual({ atPly: 2 });
    expect(server(s, { TakebackDeclined: { game_id: GID, seat_idx: 1 } }).myTakebackSent).toBeNull();
    expect(
      server(s, { GameEnd: { game_id: GID, result_vector: [2, 0], termination: 2, rating_delta: [] } }).myTakebackSent,
    ).toBeNull();
    s = reduce(accepted(s, 3, "g1f3"), { kind: "localRequestTakeback" }); // a re-request after our own move
    s = accepted(s, 4, "b8c6");
    expect(s.myTakebackSent).toBeNull();
  });

  it("a takeback can be requested only in an unrated game from ply 2 with nothing pending", () => {
    expect(canRequestTakeback(matched(0))).toBe(false); // ply 0
    expect(canRequestTakeback(afterTwoPlies(0))).toBe(true);
    const rated = accepted(accepted(matched(0, true), 1, "e2e4"), 2, "e7e5");
    expect(canRequestTakeback(rated)).toBe(false);
    const unknown = { ...afterTwoPlies(0), rated: null };
    expect(canRequestTakeback(unknown)).toBe(false); // after a reload we do not know: hide it
    expect(canRequestTakeback(reduce(afterTwoPlies(0), { kind: "localRequestTakeback" }))).toBe(false);
    expect(
      canRequestTakeback(server(afterTwoPlies(0), { TakebackOffered: { game_id: GID, seat_idx: 1, at_ply: 2 } })),
    ).toBe(false);
  });
});

describe("GS-7 reducer: STATE v1.3 drives everything (§7.4, #101)", () => {
  it("a STATE with no prior MATCHED gives a playable game: seat, ply, prompts", () => {
    const fen = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1";
    const s = server(initialState, {
      State: { game_id: GID, state: sb(fen, 1, [290000, 300000], { you_seat: 1, ply: 1, pending_draw_by: 0, takeback_by: 1, takeback_at_ply: 1 }) },
    });
    expect(s.phase).toBe("playing");
    expect(s.mySeat).toBe(1);
    expect(s.ply).toBe(1);
    expect(s.position?.turn).toBe("b");
    expect(s.drawOffer).toEqual({ by: 0 }); // the opponent's offer
    expect(s.myDrawOffered).toBe(false);
    expect(s.myTakebackSent).toEqual({ atPly: 1 }); // our own request
    expect(s.incomingTakeback).toBeNull();
  });

  it("an ENDED STATE goes to game_over with the result", () => {
    const s = server(afterTwoPlies(0), {
      State: {
        game_id: GID,
        state: sb(START_FEN, 0, [1, 0], { status: Status.Ended, result_vector: [2, 0], termination: 3, running_seat: NO_SEAT }),
      },
    });
    expect(s.phase).toBe("game_over");
    expect(s.result).toEqual({ termination: 3, resultVector: [2, 0] });
  });

  it("a STATE with a lower ply after a rewind replaces ply, board and prompts, and drops a pending move", () => {
    // White at ply 2 requests a takeback, then makes an optimistic move that is
    // still awaiting its ack when the rewind STATE (ply 1) arrives.
    let s = reduce(afterTwoPlies(0), { kind: "localRequestTakeback" });
    s = reduce(s, { kind: "localMove", uci: "g1f3" });
    expect(s.pendingMove?.uci).toBe("g1f3"); // really pending (white to move)
    const fen = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1";
    s = server(s, { State: { game_id: GID, state: sb(fen, 1, [301000, 300000], { ply: 1, last_move_uci: "e2e4" }) } });
    expect(s.ply).toBe(1);
    expect(s.position?.turn).toBe("b");
    expect(s.pendingMove).toBeNull();
    expect(s.myTakebackSent).toBeNull();
    expect(s.needsResync).toBe(false);
  });

  it("rated survives MATCHED (the server does not send it back)", () => {
    expect(matched(0, false).rated).toBe(false);
    expect(matched(0, true).rated).toBe(true);
  });
});

describe("GS-7 reducer: the draw-offer gate (§6.3)", () => {
  it("an offer the server would refuse records nothing (no false waiting line)", () => {
    // ply < 2
    const early = accepted(matched(0), 1, "e2e4");
    expect(canOfferDraw(early)).toBe(false);
    expect(reduce(early, { kind: "localOfferDraw" }).myDrawOffered).toBe(false);
    // within our own 20-ply cooldown: offer at ply 2, declined, try again at 22
    let s = reduce(afterTwoPlies(0), { kind: "localOfferDraw" });
    expect(s.myLastDrawOfferPly).toBe(2);
    s = server(s, { DrawDeclined: { game_id: GID, seat_idx: 1 } });
    expect(canOfferDraw({ ...s, ply: 22 })).toBe(false); // 2 >= 22 - 20
    expect(canOfferDraw({ ...s, ply: 23 })).toBe(true); //  2 <  23 - 20
    expect(reduce({ ...s, ply: 22 }, { kind: "localOfferDraw" }).myDrawOffered).toBe(false);
  });

  it("a rewind below our last offer reopens the gate", () => {
    let s = reduce(afterTwoPlies(0), { kind: "localOfferDraw" });
    s = server(s, { DrawDeclined: { game_id: GID, seat_idx: 1 } });
    s = server(s, { State: { game_id: GID, state: sb(START_FEN, 0, [300000, 300000], { ply: 0 }) } });
    expect(s.myLastDrawOfferPly).toBeNull();
  });
});
