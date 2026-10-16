import { describe, it, expect } from "vitest";
import {
  encode,
  decode,
  encodeServerFrame,
  decodeClientFrame,
  codecProtocolVersion,
} from "./seam";
import { Termination, type ClientMsg, type ServerMsg } from "./messages";

// Step-2 proof: the typed TS layer round-trips through the REAL wasm seam (the
// one shared §4A codec), covering every representation that could drift between
// the TS types and the wire — bigint u64, bare-number enum8, transparent-string
// ids, nested StateBody, and Vec fields. A frame built by encode() and read by
// decodeClientFrame() (and the server mirror) must reproduce the typed object.
// This is the TS-side companion to the Rust node-parity tests.

describe("typed codec seam", () => {
  it("reports the frozen protocol version", () => {
    expect(codecProtocolVersion()).toBe(1);
  });

  it("round-trips every ClientMsg through the real codec", () => {
    const gid = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
    const msgs: ClientMsg[] = [
      { Authenticate: { token: "dev-token-xyz" } },
      { Seek: { pool_id: "blitz-standard", time_control: "blitz-5+3", rated: true } },
      { Move: { game_id: gid, ply_expected: 17, uci: "e2e4" } },
      { OfferDraw: { game_id: gid } },
      { Resign: { game_id: gid } },
      { Ping: { t: 1_728_000_000_123n } }, // bigint u64
      { Resync: { game_id: gid } }, // §4A v1.1
    ];
    for (const msg of msgs) {
      const frame = encode(msg);
      const back = decodeClientFrame(frame);
      expect(back).toEqual(msg);
    }
  });

  it("round-trips a large u64 Ping losslessly (bigint, > 2^53)", () => {
    const msg: ClientMsg = { Ping: { t: 18_000_000_000_000_000_000n } };
    const back = decodeClientFrame(encode(msg));
    expect(back).toEqual(msg);
    // The field must survive as a bigint, not a lossy number.
    if ("Ping" in back) {
      expect(typeof back.Ping.t).toBe("bigint");
    } else {
      throw new Error("expected a Ping");
    }
  });

  it("round-trips every ServerMsg through the real codec", () => {
    const gid = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
    const state = { fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", turn: 0, clocks: [300000, 300000] };
    const msgs: ServerMsg[] = [
      { Matched: { game_id: gid, seat_idx: 0, opponent: "grandmaster_jane", start_state: state } },
      { MoveAccepted: { game_id: gid, ply: 18, uci: "e2e4", clock_after_ms: 299100 } },
      { MoveRejected: { game_id: gid, ply_expected: 18, reason: 2 } }, // StalePly, bare u8
      { State: { game_id: gid, state } },
      { GameEnd: { game_id: gid, result_vector: [2, 0], termination: Termination.TimeoutVsInsufficient, rating_delta: [12, -12] } },
      { Pong: { t: 1_728_000_000_123n, server_t: 1_728_000_000_456n } },
      { DrawOffered: { game_id: gid, by: 1 } }, // §4A v1.1
      { Error: { code: 5, msg: "slow down" } },
    ];
    for (const msg of msgs) {
      const frame = encodeServerFrame(msg);
      const back = decode(frame);
      expect(back).toEqual(msg);
    }
  });

  it("throws (does not trap) on a malformed frame", () => {
    expect(() => decode(new Uint8Array([0xff]))).toThrow();
    expect(() => decodeClientFrame(new Uint8Array([]))).toThrow();
  });
});
