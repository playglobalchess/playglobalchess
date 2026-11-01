// @vitest-environment jsdom
//
// useGame wiring tests — the load-bearing glue between GameSocket and the pure
// reducer. We mount the hook with react-dom/client + act (no extra testing
// library) and inject a FakeSocket.
//
// Codec note: this test MOCKS ../codec/seam with a trivial JSON identity codec.
// That is deliberate and correct here — the real §4A wasm codec round-trip is
// already proven byte-for-byte by src/codec/seam.test.ts and
// src/net/GameSocket.test.ts under the node environment. This file runs under
// jsdom (to mount React), where vite's bundler-target wasm `?url` asset is not
// loadable; and the thing under test is the WIRING (dispatch into the reducer,
// ply+1 on send, the §3.4.1 recovery driver), not the wire format. The
// mock makes GameSocket's internal encode/decode and the test's frame builders
// use the same identity codec, so frames round-trip consistently.

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../codec/seam", () => {
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  return {
    encode: (msg: unknown) => enc.encode(JSON.stringify(msg)),
    decode: (bytes: Uint8Array) => JSON.parse(dec.decode(bytes)),
    encodeServerFrame: (msg: unknown) => enc.encode(JSON.stringify(msg)),
    decodeClientFrame: (bytes: Uint8Array) => JSON.parse(dec.decode(bytes)),
    codecProtocolVersion: () => 1,
  };
});

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useGame, type UseGameResult } from "./useGame";
import type { ClientMsg, ServerMsg } from "../codec/messages";
import type { SocketLike } from "../net/GameSocket";

const enc = new TextEncoder();
const dec = new TextDecoder();
const encodeFrame = (msg: unknown): Uint8Array => enc.encode(JSON.stringify(msg));
const decodeClientFrame = (bytes: Uint8Array): ClientMsg =>
  JSON.parse(dec.decode(bytes)) as ClientMsg;

// A controllable fake socket: records sent frames and lets the test push server
// frames in. Mirrors the GameSocket.test fake.
class FakeSocket implements SocketLike {
  binaryType = "blob";
  sent: Uint8Array[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: ((err: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  closed = false;

  send(data: ArrayBufferLike): void {
    this.sent.push(new Uint8Array(data));
  }
  close(): void {
    this.closed = true;
    this.onclose?.();
  }
  open(): void {
    this.onopen?.();
  }
  push(msg: ServerMsg): void {
    this.onmessage?.({ data: encodeFrame(msg).buffer });
  }
  /** Decoded ClientMsg frames the hook/socket sent, in order. */
  sentMsgs(): ClientMsg[] {
    return this.sent.map(decodeClientFrame);
  }
}

let root: Root | null = null;
let fakes: FakeSocket[] = [];

function mountHook(): { get: () => UseGameResult; fake: () => FakeSocket } {
  fakes = [];
  const factory = (_url: string): SocketLike => {
    const f = new FakeSocket();
    fakes.push(f);
    return f;
  };
  let latest: UseGameResult | null = null;
  function Harness(): null {
    latest = useGame({ url: "ws://test/ws", token: "dev-token", factory });
    return null;
  }
  const container = document.createElement("div");
  root = createRoot(container);
  act(() => {
    root!.render(<Harness />);
  });
  return {
    get: () => {
      if (latest == null) throw new Error("hook not mounted");
      return latest;
    },
    fake: () => fakes[fakes.length - 1]!,
  };
}

afterEach(() => {
  act(() => {
    root?.unmount();
  });
  root = null;
});

function countMsgs(fake: FakeSocket, tag: string): number {
  return fake.sentMsgs().filter((m) => tag in m).length;
}

const matched: ServerMsg = {
  Matched: {
    game_id: "g1",
    seat_idx: 0,
    opponent: "bob",
    start_state: {
      fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
      turn: 0,
      clocks: [300000, 300000],
    },
  },
};

describe("useGame", () => {
  it("constructs one socket and sends AUTHENTICATE first on open", () => {
    const h = mountHook();
    expect(fakes.length).toBe(1);
    act(() => h.fake().open());
    const first = h.fake().sentMsgs()[0];
    expect(first != null && "Authenticate" in first).toBe(true);
  });

  it("dispatches a decoded ServerMsg into the reducer (MATCHED advances state)", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched));
    const s = h.get().state;
    expect(s.phase).toBe("playing");
    expect(s.gameId).toBe("g1");
    expect(s.mySeat).toBe(0);
  });

  it("move() applies optimistically and sends MOVE with ply+1 from the current ply", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched)); // ply 0, white (seat 0) to move
    act(() => h.get().commands.move("e2e4"));
    expect(h.get().state.pendingMove?.uci).toBe("e2e4");
    const moveMsg = h.fake().sentMsgs().find((m) => "Move" in m) as
      | { Move: { game_id: string; ply_expected: number; uci: string } }
      | undefined;
    expect(moveMsg?.Move).toEqual({ game_id: "g1", ply_expected: 1, uci: "e2e4" });
  });

  it("move() refuses to send a second MOVE while one is pending (S1 guard)", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched)); // ply 0, white to move
    act(() => h.get().commands.move("e2e4")); // first move → pending
    expect(h.get().state.pendingMove?.uci).toBe("e2e4");
    act(() => h.get().commands.move("d2d4")); // second, while pending
    // Exactly ONE Move frame was sent — the guard blocked the second (which
    // would otherwise ship a frame with the same ply_expected and no optimistic
    // state behind it).
    expect(countMsgs(h.fake(), "Move")).toBe(1);
  });

  it("closes the socket on unmount", () => {
    const h = mountHook();
    const f = h.fake();
    act(() => root?.unmount());
    root = null;
    expect(f.closed).toBe(true);
  });
});

// --- SPEC-S4-WEB-F01 §3.4.1 recovery driver (useGame owns every timer/send) ---

const E4_FEN_B = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1";
const goodState: ServerMsg = {
  State: { game_id: "g1", state: { fen: E4_FEN_B, turn: 1, clocks: [299000, 300000] } },
};
// FEN says black to move, wire turn says white: trigger T5 / an inconsistent reply.
const badState: ServerMsg = {
  State: { game_id: "g1", state: { fen: E4_FEN_B, turn: 0, clocks: [299000, 300000] } },
};
const gapFrame: ServerMsg = {
  MoveAccepted: { game_id: "g1", ply: 3, uci: "e2e4", clock_after_ms: 299000 },
};

describe("useGame recovery policy (§3.4.1)", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  /** Mounted, connected, matched. Fake timers on. */
  function inGame(): ReturnType<typeof mountHook> {
    vi.useFakeTimers();
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched));
    return h;
  }
  // Advance in 1 ms acts: the driver arms its next timer from an effect, which
  // React runs when the act scope exits, so one big act would arm every timer
  // late and make the backoff boundaries untestable.
  const tick = (ms: number): void => {
    for (let i = 0; i < ms; i++) {
      act(() => {
        vi.advanceTimersByTime(1);
      });
    }
  };
  const resyncs = (f: FakeSocket): number => countMsgs(f, "Resync");
  const totalResyncs = (): number => fakes.reduce((n, f) => n + resyncs(f), 0);

  it("sends exactly ONE RESYNC per attempt, however long the attempt waits", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    expect(h.get().state.sync).toMatchObject({ phase: "SYNCING", attempt: 1 });
    expect(resyncs(fakes[0]!)).toBe(1);
    tick(999); // still inside T_reply
    expect(resyncs(fakes[0]!)).toBe(1);
  });

  it("sends nothing before the socket is connected; sends once it connects", () => {
    const h = inGame();
    act(() => fakes[0]!.onclose?.()); // drop
    act(() => fakes[0]!.push(gapFrame)); // trigger arrives while disconnected
    expect(h.get().state.sync.phase).toBe("SYNCING");
    expect(totalResyncs()).toBe(0);
    tick(500); // GameSocket backoff -> new transport, still connecting
    expect(fakes.length).toBe(2);
    expect(totalResyncs()).toBe(0);
    act(() => fakes[1]!.open());
    expect(resyncs(fakes[1]!)).toBe(1);
  });

  it("a timeout advances to the next attempt only after backoff B*2^(n-1)", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000); // T_reply elapses -> backoff 250
    expect(h.get().state.sync).toMatchObject({ attempt: 1, step: "backoff" });
    tick(249);
    expect(totalResyncs()).toBe(1);
    tick(1);
    expect(totalResyncs()).toBe(2);
    expect(h.get().state.sync.attempt).toBe(2);
    tick(1000); // second timeout -> backoff 500
    tick(499);
    expect(totalResyncs()).toBe(2);
    tick(1);
    expect(totalResyncs()).toBe(3);
  });

  it("an inconsistent STATE reply advances after backoff, not immediately", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    act(() => h.fake().push(badState));
    expect(h.get().state.sync).toMatchObject({ attempt: 1, step: "backoff" });
    tick(249);
    expect(totalResyncs()).toBe(1);
    tick(1);
    expect(totalResyncs()).toBe(2);
  });

  it("after N=3 failed attempts it calls reconnect() and sends no RESYNC in RECONNECTING", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000 + 250 + 1000 + 500 + 1000); // three attempts, both backoffs, last timeout
    expect(totalResyncs()).toBe(3);
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 1 });
    expect(fakes.length).toBe(2); // reconnect() opened a fresh transport
    act(() => fakes[1]!.open());
    expect(resyncs(fakes[1]!)).toBe(0);
    // The server's auto-STATE after re-AUTH counts as the reply.
    act(() => fakes[1]!.push(goodState));
    expect(h.get().state.sync.phase).toBe("IN_SYNC");
    tick(10_000);
    expect(fakes.length).toBe(2);
  });

  it("after K=2 failed cycles it is FAILED, logs ONE error, and sends nothing more", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000 + 250 + 1000 + 500 + 1000); // -> RECONNECTING(1), transport #2
    act(() => fakes[1]!.open());
    tick(1000); // cycle 1 gets no STATE -> RECONNECTING(2), transport #3
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 2 });
    expect(fakes.length).toBe(3);
    act(() => fakes[2]!.open());
    tick(1000); // cycle 2 fails
    expect(h.get().state.sync.phase).toBe("FAILED");
    const sentBefore = fakes.map((f) => f.sent.length);
    const countBefore = fakes.length;
    tick(60_000);
    expect(fakes.map((f) => f.sent.length)).toEqual(sentBefore);
    expect(fakes.length).toBe(countBefore);
    expect(err).toHaveBeenCalledTimes(1);
    expect(err.mock.calls[0]![1]).toEqual({ gameId: "g1", trigger: "T1_GAP" });
    // FAILED: no move can be sent.
    act(() => h.get().commands.move("e2e4"));
    expect(countMsgs(fakes[2]!, "Move")).toBe(0);
  });

  it("a later consistent STATE (the socket's own reconnect) recovers from FAILED", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000 + 250 + 1000 + 500 + 1000);
    act(() => fakes[1]!.open());
    tick(1000);
    act(() => fakes[2]!.open());
    tick(1000);
    expect(h.get().state.sync.phase).toBe("FAILED");
    act(() => fakes[2]!.push(goodState));
    expect(h.get().state.sync).toMatchObject({ phase: "IN_SYNC", attempt: 0, cycle: 0 });
    expect(h.get().state.ply).toBe(1);
  });

  it("a drop mid-recovery re-sends on the new connection WITHOUT consuming an attempt; an outage pauses recovery", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    expect(resyncs(fakes[0]!)).toBe(1);
    act(() => fakes[0]!.onclose?.()); // RESYNC is lost with the socket
    tick(500); // transport #2 created, not yet open
    tick(30_000); // a long outage: T_reply must not run
    expect(h.get().state.sync).toMatchObject({ phase: "SYNCING", attempt: 1, step: "await_reply" });
    act(() => fakes[1]!.open());
    expect(resyncs(fakes[1]!)).toBe(1);
    expect(h.get().state.sync.attempt).toBe(1);
    tick(1000); // now T_reply runs and the attempt fails normally
    expect(h.get().state.sync).toMatchObject({ attempt: 1, step: "backoff" });
  });

  it("triggers during recovery coalesce: no extra RESYNC, no extra attempt", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    act(() => h.fake().push(gapFrame));
    act(() => h.fake().push({ MoveRejected: { game_id: "g1", ply_expected: 1, reason: 2 } }));
    expect(totalResyncs()).toBe(1);
    expect(h.get().state.sync.attempt).toBe(1);
  });

  it("a consistent STATE resets the counters; a later episode starts at attempt 1 and re-sends", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1250); // attempt 2
    expect(h.get().state.sync.attempt).toBe(2);
    act(() => h.fake().push(goodState));
    expect(h.get().state.sync).toMatchObject({ phase: "IN_SYNC", attempt: 0, cycle: 0 });
    tick(10_000); // the cleared timers fire nothing
    expect(totalResyncs()).toBe(2);
    act(() =>
      h.fake().push({ MoveAccepted: { game_id: "g1", ply: 9, uci: "e7e5", clock_after_ms: 1 } }),
    );
    expect(h.get().state.sync).toMatchObject({ phase: "SYNCING", attempt: 1 });
    expect(totalResyncs()).toBe(3);
  });

  it("no MOVE is sent while recovering", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    act(() => h.get().commands.move("e2e4"));
    expect(countMsgs(h.fake(), "Move")).toBe(0);
    expect(h.get().state.pendingMove).toBeNull();
  });

  it("RECONNECTING: T_reply does not run while the new transport is still connecting (outage pauses recovery)", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000 + 250 + 1000 + 500 + 1000); // -> RECONNECTING(1); transport #2 created, NOT open
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 1 });
    tick(5000); // still connecting: no reply timer may run
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 1 });
    act(() => fakes[1]!.open());
    tick(999);
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 1 });
    tick(1);
    expect(h.get().state.sync).toMatchObject({ phase: "RECONNECTING", cycle: 2 });
  });

  it("a trigger and a move in the SAME act send no MOVE (the reducer decides, not a lagging mirror)", () => {
    const h = inGame();
    const { move } = h.get().commands;
    act(() => {
      h.fake().push(gapFrame); // trigger first ...
      move("e2e4"); // ... then a move before React has re-rendered
    });
    expect(h.get().state.sync.phase).toBe("SYNCING");
    expect(countMsgs(h.fake(), "Move")).toBe(0);
    expect(h.get().state.pendingMove).toBeNull();
  });

  it("a move that arrives before the trigger in the same act is sent (control)", () => {
    const h = inGame();
    const { move } = h.get().commands;
    act(() => {
      move("e2e4");
      h.fake().push(gapFrame);
    });
    expect(countMsgs(h.fake(), "Move")).toBe(1);
  });

  it("a drop and reconnect inside ONE React batch still re-sends the RESYNC on the new connection", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    expect(resyncs(fakes[0]!)).toBe(1);
    act(() => {
      fakes[0]!.onclose?.();
      vi.advanceTimersByTime(500); // GameSocket backoff -> transport #2
      fakes[1]!.open();
    });
    expect(h.get().state.sync).toMatchObject({ phase: "SYNCING", attempt: 1 });
    expect(resyncs(fakes[1]!)).toBe(1);
  });

  it("GAME_END during RECONNECTING ends recovery: no further reconnect or send", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000 + 250 + 1000 + 500 + 1000);
    expect(h.get().state.sync.phase).toBe("RECONNECTING");
    act(() => fakes[1]!.open());
    act(() =>
      fakes[1]!.push({
        GameEnd: { game_id: "g1", result_vector: [2, 0], termination: 1, rating_delta: [0, 0] },
      }),
    );
    expect(h.get().state.phase).toBe("game_over");
    expect(h.get().state.sync.phase).toBe("IN_SYNC");
    const n = fakes.length;
    tick(60_000);
    expect(fakes.length).toBe(n);
  });

  it("unmount during recovery clears every timer and closes the socket", () => {
    const h = inGame();
    act(() => h.fake().push(gapFrame));
    tick(1000); // mid-backoff
    act(() => root?.unmount());
    root = null;
    expect(vi.getTimerCount()).toBe(0);
    expect(fakes[0]!.closed).toBe(true);
  });
});

