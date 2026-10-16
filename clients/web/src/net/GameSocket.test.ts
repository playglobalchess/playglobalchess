import { describe, it, expect, beforeEach } from "vitest";
import { GameSocket, type SocketLike } from "./GameSocket";
import { encodeServerFrame, decodeClientFrame } from "../codec/seam";
import type { ClientMsg, ServerMsg } from "../codec/messages";

// A fake WebSocket that captures outgoing frames and lets the test feed incoming
// ones. It uses the REAL codec seam, so this exercises the client's encode/decode
// end to end with no browser and no network (SPEC-S4-WEB-F01 §6 fake-socket tier).
class FakeSocket implements SocketLike {
  binaryType = "";
  sent: Uint8Array[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: ((err: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  closed = false;

  send(data: ArrayBufferLike): void {
    this.sent.push(new Uint8Array(data as ArrayBuffer));
  }
  close(): void {
    this.closed = true;
    this.onclose?.();
  }
  // --- test helpers ---
  open(): void {
    this.onopen?.();
  }
  /** Feed a typed ServerMsg to the client (encoded via the real seam). */
  feed(msg: ServerMsg): void {
    const frame = encodeServerFrame(msg);
    this.onmessage?.({ data: frame.buffer.slice(frame.byteOffset, frame.byteOffset + frame.byteLength) });
  }
  /** Decode an outgoing frame the client sent, as a typed ClientMsg. */
  decodeSent(i: number): ClientMsg {
    const f = this.sent[i];
    if (f == null) throw new Error(`no sent frame at ${i}`);
    return decodeClientFrame(f);
  }
}

const GID = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

describe("GameSocket (fake socket, real codec)", () => {
  let fakes: FakeSocket[];
  let received: ServerMsg[];
  let sock: GameSocket;

  beforeEach(() => {
    fakes = [];
    received = [];
    sock = new GameSocket({
      url: "ws://test",
      token: "dev-token",
      onServerMsg: (m) => received.push(m),
      factory: () => {
        const f = new FakeSocket();
        fakes.push(f);
        return f;
      },
    });
  });

  it("sends AUTHENTICATE as the first frame on open", () => {
    sock.connect();
    fakes[0]!.open();
    const first = fakes[0]!.decodeSent(0);
    expect("Authenticate" in first).toBe(true);
    if ("Authenticate" in first) expect(first.Authenticate.token).toBe("dev-token");
  });

  it("drives seek → matched → move → accepted end to end", () => {
    sock.connect();
    const f = fakes[0]!;
    f.open();
    sock.seek("blitz-standard", "blitz-5+3", false);
    // Frames so far: [0]=AUTHENTICATE, [1]=SEEK.
    const seek = f.decodeSent(1);
    expect("Seek" in seek).toBe(true);

    // Server matches us as seat 0.
    f.feed({ Matched: { game_id: GID, seat_idx: 0, opponent: "jane", start_state: { fen: START_FEN, turn: 0, clocks: [300000, 300000] } } });
    expect(received.at(-1) && "Matched" in received.at(-1)!).toBe(true);

    // We move; the client sends MOVE with the ply we pass.
    sock.move(GID, 1, "e2e4");
    const mv = f.decodeSent(2);
    expect("Move" in mv).toBe(true);
    if ("Move" in mv) {
      expect(mv.Move.uci).toBe("e2e4");
      expect(mv.Move.ply_expected).toBe(1);
    }

    // Server accepts (v1.2: carries uci, to both seats).
    f.feed({ MoveAccepted: { game_id: GID, ply: 1, uci: "e2e4", clock_after_ms: 299000 } });
    const last = received.at(-1)!;
    expect("MoveAccepted" in last).toBe(true);
    if ("MoveAccepted" in last) expect(last.MoveAccepted.uci).toBe("e2e4");
  });

  it("ignores a non-binary / garbled frame without crashing the loop", () => {
    sock.connect();
    const f = fakes[0]!;
    f.open();
    const before = received.length;
    f.onmessage?.({ data: "not binary" }); // non-binary → ignored
    f.onmessage?.({ data: new Uint8Array([0xff]).buffer }); // garbage → decode throws → ignored
    expect(received.length).toBe(before);
    // The socket still works afterward.
    f.feed({ Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: START_FEN, turn: 0, clocks: [1, 1] } } });
    expect("Matched" in received.at(-1)!).toBe(true);
  });

  it("reconnects after an unexpected close and re-sends AUTHENTICATE + pending SEEK", () => {
    sock = new GameSocket({
      url: "ws://test",
      token: "tok",
      onServerMsg: (m) => received.push(m),
      factory: () => {
        const f = new FakeSocket();
        fakes.push(f);
        return f;
      },
      backoffMs: 1,
    });
    sock.connect();
    fakes[0]!.open();
    sock.seek("p", "tc", false); // seeking, not yet matched
    fakes[0]!.onclose?.(); // unexpected drop
    // A new socket is created; drive its open and assert re-AUTH + re-SEEK.
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        expect(fakes.length).toBe(2);
        fakes[1]!.open();
        const reAuth = fakes[1]!.decodeSent(0);
        const reSeek = fakes[1]!.decodeSent(1);
        expect("Authenticate" in reAuth).toBe(true);
        expect("Seek" in reSeek).toBe(true); // re-sent because not matched
        resolve();
      }, 10);
    });
  });

  it("does NOT re-seek on reconnect once matched", () => {
    sock = new GameSocket({
      url: "ws://test", token: "t", onServerMsg: (m) => received.push(m),
      factory: () => { const f = new FakeSocket(); fakes.push(f); return f; },
      backoffMs: 1,
    });
    sock.connect();
    fakes[0]!.open();
    sock.seek("p", "tc", false);
    fakes[0]!.feed({ Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: START_FEN, turn: 0, clocks: [1, 1] } } });
    fakes[0]!.onclose?.();
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        fakes[1]!.open();
        // Frame 0 = AUTHENTICATE; there must be NO second (SEEK) frame — we are matched,
        // the server auto-pushes STATE on re-auth (Option A).
        expect(fakes[1]!.sent.length).toBe(1);
        resolve();
      }, 10);
    });
  });

  it("close() is deliberate — no reconnect", () => {
    sock.connect();
    fakes[0]!.open();
    sock.close();
    expect(fakes[0]!.closed).toBe(true);
    expect(fakes.length).toBe(1); // no reconnect socket created
  });
});
