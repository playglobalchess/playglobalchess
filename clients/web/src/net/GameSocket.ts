// GameSocket — the binary WebSocket client for the F-01 loop (SPEC-S4-WEB-F01
// §3.1/§3.4). It is the ONLY I/O boundary: it wraps a binary WebSocket, builds
// ClientMsg frames via the shared codec seam (encode), decodes incoming
// ServerMsg frames (decode), and hands each decoded message to a dispatch
// callback (the reducer, injected). It holds NO game truth — it is transport.
//
// Testability (step 7): the socket is created through an injected factory
// (SocketFactory) and typed to the minimal SocketLike surface, so a fake socket
// can drive the client in a unit test with no browser and no real network.
//
// Reconnection (§3.4): on close, it reconnects, re-sends AUTHENTICATE (the
// server auto-pushes STATE, Option A), and re-sends SEEK only if we had not yet
// matched. The caller can also request an explicit RESYNC (e.g. when the reducer
// sets needsResync on a detected ply gap).

import { encode, decode } from "../codec/seam";
import type { ClientMsg, ServerMsg } from "../codec/messages";

/** The minimal WebSocket surface GameSocket needs — real `WebSocket` satisfies it. */
export interface SocketLike {
  binaryType: string;
  send(data: ArrayBufferLike): void;
  close(): void;
  onopen: (() => void) | null;
  onclose: (() => void) | null;
  onerror: ((err: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
}

export type SocketFactory = (url: string) => SocketLike;

export interface GameSocketOptions {
  url: string;
  token: string;
  /** Called with each decoded ServerMsg (the reducer dispatch). */
  onServerMsg: (msg: ServerMsg) => void;
  /** Called on connection state changes, for the UI status line. */
  onStatus?: (status: ConnStatus) => void;
  /** Factory for the underlying socket (defaults to the real WebSocket). */
  factory?: SocketFactory;
  /** Reconnect backoff base ms (default 500); capped at maxBackoffMs. */
  backoffMs?: number;
  maxBackoffMs?: number;
}

export type ConnStatus = "connecting" | "connected" | "disconnected" | "closed";

function defaultFactory(url: string): SocketLike {
  // The real browser WebSocket satisfies SocketLike structurally.
  return new WebSocket(url) as unknown as SocketLike;
}

export class GameSocket {
  private readonly opts: GameSocketOptions;
  private readonly factory: SocketFactory;
  private sock: SocketLike | null = null;
  /** The SEEK to (re)send after auth, if we are seeking and not yet matched. */
  private pendingSeek: ClientMsg | null = null;
  private matched = false;
  private closedByUs = false;
  private attempt = 0;

  constructor(opts: GameSocketOptions) {
    this.opts = opts;
    this.factory = opts.factory ?? defaultFactory;
  }

  /** Open the socket and run the connect→AUTHENTICATE flow. */
  connect(): void {
    this.closedByUs = false;
    this.open();
  }

  private open(): void {
    this.status("connecting");
    const sock = this.factory(this.opts.url);
    sock.binaryType = "arraybuffer";
    this.sock = sock;

    sock.onopen = () => {
      this.attempt = 0;
      this.status("connected");
      // First frame MUST be AUTHENTICATE (§3.1). The server auto-pushes STATE
      // for any live game this user is seated in (reconnect resync, Option A).
      this.sendMsg({ Authenticate: { token: this.opts.token } });
      // Re-SEEK only if we were seeking and had not matched before the drop.
      if (this.pendingSeek != null && !this.matched) {
        this.sendMsg(this.pendingSeek);
      }
    };

    sock.onmessage = (ev: { data: unknown }) => {
      const bytes = toBytes(ev.data);
      if (bytes == null) return; // ignore a non-binary frame rather than crash
      let msg: ServerMsg;
      try {
        msg = decode(bytes);
      } catch {
        // A garbled/oversized frame: log + ignore, never break the read loop (§5).
        return;
      }
      if ("Matched" in msg) this.matched = true;
      this.opts.onServerMsg(msg);
    };

    sock.onclose = () => {
      if (this.closedByUs) {
        this.status("closed");
        return;
      }
      this.status("disconnected");
      this.scheduleReconnect();
    };

    sock.onerror = () => {
      // onclose follows; reconnect is driven there. Nothing to do here.
    };
  }

  private scheduleReconnect(): void {
    const base = this.opts.backoffMs ?? 500;
    const max = this.opts.maxBackoffMs ?? 10_000;
    const delay = Math.min(base * 2 ** this.attempt, max);
    this.attempt += 1;
    this.timer = setTimeout(() => this.open(), delay);
  }

  private timer: ReturnType<typeof setTimeout> | null = null;

  /** Enter matchmaking. Remembers the seek so a reconnect before MATCHED re-sends it. */
  seek(poolId: string, timeControl: string, rated: boolean): void {
    const msg: ClientMsg = { Seek: { pool_id: poolId, time_control: timeControl, rated } };
    this.pendingSeek = msg;
    this.matched = false;
    this.sendMsg(msg);
  }

  /** Propose a move. ply_expected is the server ply + 1 (the caller supplies it). */
  move(gameId: string, plyExpected: number, uci: string): void {
    this.sendMsg({ Move: { game_id: gameId, ply_expected: plyExpected, uci } });
  }

  resign(gameId: string): void {
    this.sendMsg({ Resign: { game_id: gameId } });
  }
  offerDraw(gameId: string): void {
    this.sendMsg({ OfferDraw: { game_id: gameId } });
  }
  /** Force a fresh STATE (§4A v1.1 RESYNC) — e.g. when the reducer flags needsResync. */
  resync(gameId: string): void {
    this.sendMsg({ Resync: { game_id: gameId } });
  }
  ping(t: bigint): void {
    this.sendMsg({ Ping: { t } });
  }

  /** Close the socket deliberately (no reconnect). */
  close(): void {
    this.closedByUs = true;
    if (this.timer != null) clearTimeout(this.timer);
    this.sock?.close();
  }

  /** Encode a ClientMsg via the shared seam and send it as a binary frame. */
  private sendMsg(msg: ClientMsg): void {
    if (this.sock == null) return;
    const bytes = encode(msg); // §4A bytes produced in Rust (NFR-5)
    // Send the exact byte view (copy to a fresh ArrayBuffer to avoid shipping a
    // larger backing buffer than the frame).
    this.sock.send(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
  }

  private status(s: ConnStatus): void {
    this.opts.onStatus?.(s);
  }
}

/** Coerce an incoming WebSocket message payload to Uint8Array, or null if not binary. */
function toBytes(data: unknown): Uint8Array | null {
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (data instanceof Uint8Array) return data;
  return null;
}
