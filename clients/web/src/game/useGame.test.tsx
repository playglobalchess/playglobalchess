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
// ply+1 on send, the needsResync→resync-once seam), not the wire format. The
// mock makes GameSocket's internal encode/decode and the test's frame builders
// use the same identity codec, so frames round-trip consistently.

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../codec/seam", () => {
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  // u64 fields (STATE.server_t, PONG) are bigint; JSON needs them tagged.
  const toJson = (msg: unknown) =>
    JSON.stringify(msg, (_k, v: unknown) => (typeof v === "bigint" ? { $big: v.toString() } : v));
  const fromJson = (s: string): unknown =>
    JSON.parse(s, (_k, v: unknown) =>
      v != null && typeof v === "object" && "$big" in v ? BigInt((v as { $big: string }).$big) : v,
    );
  return {
    encode: (msg: unknown) => enc.encode(toJson(msg)),
    decode: (bytes: Uint8Array) => fromJson(dec.decode(bytes)),
    encodeServerFrame: (msg: unknown) => enc.encode(toJson(msg)),
    decodeClientFrame: (bytes: Uint8Array) => fromJson(dec.decode(bytes)),
    codecProtocolVersion: () => 1,
  };
});

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useGame, type UseGameResult } from "./useGame";
import { ErrorCode, type ClientMsg, type ServerMsg } from "../codec/messages";
import type { SocketLike } from "../net/GameSocket";
import { sb } from "../test/stateBody";

const enc = new TextEncoder();
const dec = new TextDecoder();
const encodeFrame = (msg: unknown): Uint8Array =>
  enc.encode(JSON.stringify(msg, (_k, v: unknown) => (typeof v === "bigint" ? { $big: v.toString() } : v)));
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
    start_state: sb("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", 0, [300000, 300000]),
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

  it("sends RESYNC exactly once on a ply gap and re-arms after STATE clears it", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched));

    // MOVE_ACCEPTED skipping ahead (ply 3 at ply 0) → gap → needsResync.
    act(() =>
      h.fake().push({
        MoveAccepted: { game_id: "g1", ply: 3, uci: "e2e4", clock_after_ms: 299000 },
      }),
    );
    expect(h.get().state.needsResync).toBe(true);
    expect(countMsgs(h.fake(), "Resync")).toBe(1);

    // A clearing STATE drops needsResync and re-arms the guard (no extra resync).
    act(() =>
      h.fake().push({
        State: {
          game_id: "g1",
          state: sb("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1", 1, [299000, 300000]),
        },
      }),
    );
    expect(h.get().state.needsResync).toBe(false);
    expect(countMsgs(h.fake(), "Resync")).toBe(1);

    // A later gap fires a SECOND resync (re-armed).
    act(() =>
      h.fake().push({
        MoveAccepted: { game_id: "g1", ply: 9, uci: "e7e5", clock_after_ms: 298000 },
      }),
    );
    expect(countMsgs(h.fake(), "Resync")).toBe(2);
  });

  it("an INTERNAL error mid-game (move not confirmed) sends ONE RESYNC and never resends the MOVE", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.fake().push(matched));
    act(() => h.get().commands.move("e2e4"));
    act(() => h.fake().push({ Error: { code: ErrorCode.Internal, msg: "move not confirmed" } }));
    expect(h.get().state.error).toBeNull();
    expect(h.get().state.needsResync).toBe(true);
    expect(countMsgs(h.fake(), "Resync")).toBe(1);
    expect(countMsgs(h.fake(), "Move")).toBe(1); // the original, not a resend
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

  it("GS-7: takeback and draw-decline commands send the v1.3 frames", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.get().commands.seek("p", "blitz-5+3", false)); // unrated
    act(() => h.fake().push(matched));
    for (const [ply, uci] of [[1, "e2e4"], [2, "e7e5"]] as const) {
      act(() => h.fake().push({ MoveAccepted: { game_id: "g1", ply, uci, clock_after_ms: 299000 } }));
    }
    act(() => h.get().commands.requestTakeback());
    expect(countMsgs(h.fake(), "Takeback")).toBe(1);
    expect(h.get().state.myTakebackSent).toEqual({ atPly: 2 });
    act(() => h.get().commands.requestTakeback()); // already pending: no second frame
    expect(countMsgs(h.fake(), "Takeback")).toBe(1);

    act(() => h.fake().push({ TakebackDeclined: { game_id: "g1", seat_idx: 1 } }));
    act(() => h.fake().push({ TakebackOffered: { game_id: "g1", seat_idx: 1, at_ply: 2 } }));
    act(() => h.get().commands.acceptTakeback());
    const acc = h.fake().sentMsgs().find((m) => "TakebackAccept" in m) as
      | { TakebackAccept: { game_id: string; at_ply: number } }
      | undefined;
    expect(acc?.TakebackAccept).toEqual({ game_id: "g1", at_ply: 2 });

    // A second request from the opponent, which we decline this time.
    act(() => h.fake().push({ TakebackOffered: { game_id: "g1", seat_idx: 1, at_ply: 2 } }));
    act(() => h.get().commands.declineTakeback());
    const dec = h.fake().sentMsgs().find((m) => "TakebackDecline" in m) as
      | { TakebackDecline: { game_id: string; at_ply: number } }
      | undefined;
    expect(dec?.TakebackDecline).toEqual({ game_id: "g1", at_ply: 2 });
    expect(h.get().state.incomingTakeback).toBeNull();

    act(() => h.fake().push({ DrawOffered: { game_id: "g1", by: 1 } }));
    act(() => h.get().commands.declineDraw());
    expect(countMsgs(h.fake(), "DeclineDraw")).toBe(1);
    expect(h.get().state.drawOffer).toBeNull();
  });

  it("GS-7: no takeback in a rated game", () => {
    const h = mountHook();
    act(() => h.fake().open());
    act(() => h.get().commands.seek("p", "blitz-5+3", true));
    act(() => h.fake().push(matched));
    for (const [ply, uci] of [[1, "e2e4"], [2, "e7e5"]] as const) {
      act(() => h.fake().push({ MoveAccepted: { game_id: "g1", ply, uci, clock_after_ms: 299000 } }));
    }
    act(() => h.get().commands.requestTakeback());
    expect(countMsgs(h.fake(), "Takeback")).toBe(0);
  });
});
