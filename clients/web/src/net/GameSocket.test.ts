import { describe, it, expect, beforeEach } from "vitest";
import { GameSocket, type SocketLike } from "./GameSocket";
import { encodeServerFrame, decodeClientFrame } from "../codec/seam";
import type { ClientMsg, ServerMsg } from "../codec/messages";

// A fake WebSocket that captures outgoing frames and lets the test feed incoming
// ones. It uses the REAL codec seam, so this exercises the client's encode/decode
// end to end with no browser and no network (SPEC-S4-WEB-F01 §6 fake-socket tier).
class FakeSocket implements SocketLike {
  binaryType = "";
  readyState = 0; // CONNECTING until open()
  sent: Uint8Array[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: ((err: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  closed = false;

  send(data: ArrayBufferLike): void {
    // Mirror the real WebSocket contract: send() before OPEN throws. This is
    // what the e2e surfaced — a fake that accepts an early send hides the bug.
    if (this.readyState !== 1) throw new Error("Sent before connected.");
    this.sent.push(new Uint8Array(data as ArrayBuffer));
  }
  close(): void {
    this.closed = true;
    this.readyState = 3; // CLOSED
    this.onclose?.();
  }
  // --- test helpers ---
  open(): void {
    this.readyState = 1; // OPEN
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

  it("UNSUPPORTED_VERSION error closes the socket and does NOT reconnect (§5)", () => {
    sock = new GameSocket({
      url: "ws://test", token: "t", onServerMsg: (m) => received.push(m),
      factory: () => { const f = new FakeSocket(); fakes.push(f); return f; },
      backoffMs: 1,
    });
    sock.connect();
    fakes[0]!.open();
    // Server rejects the protocol major → UNSUPPORTED_VERSION (code 1).
    fakes[0]!.feed({ Error: { code: 1, msg: "unsupported version" } });
    // The error is forwarded (UI shows refresh), the socket is closed...
    expect("Error" in received.at(-1)!).toBe(true);
    expect(fakes[0]!.closed).toBe(true);
    // ...and the subsequent onclose must NOT spawn a reconnect (closedByUs set).
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        expect(fakes.length).toBe(1); // no reconnect — would busy-loop otherwise
        resolve();
      }, 10);
    });
  });

  it("stale old-socket callbacks are ignored after a reconnect (B1)", () => {
    sock = new GameSocket({
      url: "ws://test", token: "t", onServerMsg: (m) => received.push(m),
      factory: () => { const f = new FakeSocket(); fakes.push(f); return f; },
      backoffMs: 1,
    });
    sock.connect();
    const first = fakes[0]!;
    first.open();
    first.onclose?.(); // unexpected drop → schedules reconnect
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        expect(fakes.length).toBe(2);
        fakes[1]!.open();
        const beforeStale = received.length;
        const sockets1 = fakes.length;
        // The OLD socket now emits a late frame AND a second close. Both must be
        // ignored — no dispatch, and crucially NO new reconnect socket.
        first.feed({ Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: START_FEN, turn: 0, clocks: [1, 1] } } });
        first.onclose?.();
        expect(received.length).toBe(beforeStale); // stale frame dropped
        setTimeout(() => {
          expect(fakes.length).toBe(sockets1); // stale onclose spawned no reconnect
          resolve();
        }, 10);
      }, 10);
    });
  });

  it("close() during a pending reconnect timer stops the reconnect", () => {
    sock = new GameSocket({
      url: "ws://test", token: "t", onServerMsg: (m) => received.push(m),
      factory: () => { const f = new FakeSocket(); fakes.push(f); return f; },
      backoffMs: 50, // long enough that we close() before it fires
    });
    sock.connect();
    fakes[0]!.open();
    fakes[0]!.onclose?.(); // schedules a reconnect in ~50ms
    sock.close(); // deliberate close BEFORE the timer fires
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        expect(fakes.length).toBe(1); // the pending reconnect timer was cancelled
        resolve();
      }, 80);
    });
  });

  // --- regressions for bugs the real-server e2e surfaced (fake-socket masked) ---

  it("seek() BEFORE open does not throw and sends exactly ONE Seek after open", () => {
    // The real WebSocket throws on send() before OPEN, and connect() returns
    // before the async open. A seek issued immediately must queue, not throw,
    // and must reach the wire exactly once (a double SEEK enqueues the user
    // twice and corrupts pairing — the e2e symptom).
    sock.connect();
    const f = fakes[0]!;
    expect(() => sock.seek("blitz", "blitz-5+3", false)).not.toThrow();
    f.open(); // now OPEN → AUTHENTICATE + the queued SEEK flush
    const sent = f.sent.map((_, i) => f.decodeSent(i));
    const seeks = sent.filter((m) => "Seek" in m);
    expect(seeks.length).toBe(1);
    expect("Authenticate" in sent[0]!).toBe(true); // AUTH is still first
  });

  it("F1: a game frame (move) sent while NOT open is dropped, not replayed on reconnect", () => {
    sock.connect();
    const f1 = fakes[0]!;
    f1.open();
    sock.seek("blitz", "blitz-5+3", false);
    f1.feed({ Matched: { game_id: GID, seat_idx: 0, opponent: "x", start_state: { fen: START_FEN, turn: 0, clocks: [1, 1] } } });
    // Socket drops; the UI issues a move while down. A game-mutating frame must
    // NOT be buffered — on reconnect the server auto-pushes STATE (restoring
    // ply), so a replayed stale MOVE at an old ply would wedge the player.
    f1.onclose?.(); // disconnect → schedules reconnect, opens a new socket
    sock.move(GID, 1, "e2e4"); // issued while not open
    const f2 = fakes[fakes.length - 1]!;
    f2.open(); // reconnect opens
    const sent = f2.sent.map((_, i) => f2.decodeSent(i));
    expect(sent.some((m) => "Authenticate" in m)).toBe(true); // AUTH re-sent
    expect(sent.some((m) => "Move" in m)).toBe(false); // stale MOVE dropped
  });
});
