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
// matched. GameSocket exposes resync() but does NOT itself observe the reducer's
// needsResync flag — it never reads reducer output. Wiring "reducer flags a ply
// gap → call sock.resync(gameId)" belongs to the React layer (step 5), which
// owns both the dispatch into the reducer and the resulting state. The primary
// resync path (auto-STATE on re-AUTHENTICATE) is handled here; resync() is the
// secondary on-demand path the UI layer drives.

import { encode, decode } from "../codec/seam";
import { ErrorCode, Status, type ClientMsg, type ServerMsg } from "../codec/messages";

/** The minimal WebSocket surface GameSocket needs — real `WebSocket` satisfies it. */
export interface SocketLike {
  binaryType: string;
  /** WebSocket.readyState (0 CONNECTING, 1 OPEN, 2 CLOSING, 3 CLOSED). Optional
   * so a test fake may omit it; when absent GameSocket treats the socket as
   * send-ready once onopen has fired (it tracks open state itself). */
  readyState?: number;
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
  /** The game we are playing, from MATCHED / a live STATE; cleared when it ends.
   * On every reconnect we RESYNC it (#102): the server pushes STATE only for a
   * game still live on that node, or an ending it knows we missed, and a
   * GAME_END queued to a half-open socket just before the drop is lost. RESYNC
   * returns the live or ENDED state on any node, so the board never stays live
   * for a game that is over. */
  private liveGameId: string | null = null;
  /** The game id of a reconnect RESYNC still awaiting its answer. That RESYNC is
   * best effort: a game live on ANOTHER node is answered UNKNOWN_GAME (there is
   * no node routing yet, G-E), and that answer is not an error for the player, so
   * it is dropped here instead of reaching the UI as a banner. It is retried on
   * the next reconnect (the game may have ended by then, which any node serves). */
  private quietResync: string | null = null;
  private closedByUs = false;
  private attempt = 0;
  /** True between onopen and the next (re)open/close — gates direct sends. */
  private isOpen = false;
  /** Frames requested before the socket was OPEN; flushed in order on open. A
   * real WebSocket throws on send() before OPEN, and connect() returns before
   * the async open completes, so an eager seek()/move() must queue, not throw. */
  private outbox: ClientMsg[] = [];

  constructor(opts: GameSocketOptions) {
    this.opts = opts;
    this.factory = opts.factory ?? defaultFactory;
  }

  /** Open the socket and run the connect→AUTHENTICATE flow. */
  connect(): void {
    // Guard against a double connect() (React StrictMode double-mount, a retry):
    // tear down any existing socket first so we never leak a live one (S2/B1).
    if (this.sock != null) {
      this.detach(this.sock);
      this.sock = null;
    }
    this.closedByUs = false;
    this.open();
  }

  /** Null a socket's handlers so a superseded socket can never call back into us. */
  private detach(sock: SocketLike): void {
    sock.onopen = null;
    sock.onclose = null;
    sock.onerror = null;
    sock.onmessage = null;
  }

  private open(): void {
    // Detach the previous socket before replacing it — otherwise its live
    // onclose/onmessage closures keep firing after the new socket opens, which
    // spawns concurrent reconnect chains and lets stale frames through (B1).
    if (this.sock != null) this.detach(this.sock);
    this.isOpen = false; // the new socket is CONNECTING until its onopen fires

    this.status("connecting");
    const sock = this.factory(this.opts.url);
    sock.binaryType = "arraybuffer";
    this.sock = sock;

    sock.onopen = () => {
      // Ignore if this socket was superseded or we closed deliberately (S1/B1).
      if (sock !== this.sock || this.closedByUs) return;
      this.isOpen = true;
      this.status("connected");
      // First frame MUST be AUTHENTICATE (§3.1). The server auto-pushes STATE
      // for any live game this user is seated in (reconnect resync, Option A).
      this.sendMsg({ Authenticate: { token: this.opts.token } });
      // Re-SEEK only if we were seeking and had not matched before the drop.
      if (this.pendingSeek != null && !this.matched) {
        this.sendMsg(this.pendingSeek);
      }
      // Ask for the game we were in: its live state, or how it ended (#102).
      if (this.liveGameId != null) {
        this.quietResync = this.liveGameId;
        this.sendMsg({ Resync: { game_id: this.liveGameId } });
      }
      // Flush anything requested before the socket was open (an eager
      // seek()/move() right after connect()), in order.
      const queued = this.outbox;
      this.outbox = [];
      for (const m of queued) this.sendMsg(m);
      // NOTE: `attempt` (backoff) is reset on the FIRST decoded message, not
      // here — a raw socket open that the server immediately rejects must not
      // reset backoff into a tight reconnect loop (S3).
    };

    sock.onmessage = (ev: { data: unknown }) => {
      if (sock !== this.sock) return; // drop frames from a superseded socket (B1)
      const bytes = toBytes(ev.data);
      if (bytes == null) {
        console.warn("GameSocket: ignoring non-binary frame");
        return;
      }
      let msg: ServerMsg;
      try {
        msg = decode(bytes);
      } catch (err) {
        // A garbled/oversized frame: log + ignore, never break the read loop (§5).
        console.warn("GameSocket: ignoring undecodable frame", err);
        return;
      }
      // The connection is proven usable — now reset the backoff counter (S3).
      this.attempt = 0;
      // The reconnect RESYNC's answer: a STATE for that game settles it; an
      // UNKNOWN_GAME (game live elsewhere, or gone) is swallowed, not shown.
      if (this.quietResync != null) {
        if ("State" in msg && msg.State.game_id === this.quietResync) {
          this.quietResync = null;
        } else if ("Error" in msg && msg.Error.code === ErrorCode.UnknownGame) {
          console.info("GameSocket: reconnect RESYNC of", this.quietResync, "not served here:", msg.Error.msg);
          this.quietResync = null;
          return;
        }
      }
      if ("Matched" in msg) {
        this.matched = true;
        this.pendingSeek = null; // matched — the seek is consumed (explicit invariant)
        this.liveGameId = msg.Matched.game_id;
      } else if ("State" in msg) {
        this.liveGameId = msg.State.state.status === Status.Ended ? null : msg.State.game_id;
      } else if ("GameEnd" in msg) {
        this.liveGameId = null;
      }
      // §5: an UNSUPPORTED_VERSION error is unrecoverable — the client's protocol
      // major mismatches the server. Forward it (so the UI can show "please
      // refresh"), suppress reconnect, and close — never busy-loop re-auth (S5).
      if ("Error" in msg && msg.Error.code === ErrorCode.UnsupportedVersion) {
        this.safeDispatch(msg);
        this.closedByUs = true;
        if (this.timer != null) clearTimeout(this.timer);
        this.status("closed");
        this.sock?.close();
        return;
      }
      this.safeDispatch(msg);
    };

    sock.onclose = () => {
      if (sock !== this.sock) return; // a superseded socket closing — ignore (B1)
      this.isOpen = false;
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

  /** Dispatch to the reducer callback, isolating a throwing callback from the read loop. */
  private safeDispatch(msg: ServerMsg): void {
    try {
      this.opts.onServerMsg(msg);
    } catch (err) {
      console.warn("GameSocket: onServerMsg threw", err);
    }
  }

  private scheduleReconnect(): void {
    const base = this.opts.backoffMs ?? 500;
    const max = this.opts.maxBackoffMs ?? 10_000;
    const delay = Math.min(base * 2 ** this.attempt, max);
    this.attempt += 1;
    this.timer = setTimeout(() => this.open(), delay);
  }

  private timer: ReturnType<typeof setTimeout> | null = null;

  /** Enter matchmaking. The seek is remembered as pendingSeek and (re)sent by
   * onopen — on first connect AND after a reconnect before MATCHED. If the
   * socket is ALREADY open we send it now; otherwise onopen will. We do NOT
   * route it through the outbox, so it can never be sent twice (a double SEEK
   * enqueues the user twice and corrupts pairing). */
  seek(poolId: string, timeControl: string, rated: boolean): void {
    const msg: ClientMsg = { Seek: { pool_id: poolId, time_control: timeControl, rated } };
    this.pendingSeek = msg;
    this.matched = false;
    // Send now ONLY if open; otherwise onopen re-sends pendingSeek. We do NOT
    // route SEEK through the outbox — that plus the onopen pendingSeek re-send
    // would send it twice (the double-SEEK the e2e caught). pendingSeek is the
    // single source of truth for "we want to seek".
    const open = this.isOpen && (this.sock?.readyState == null || this.sock.readyState === 1);
    if (open) {
      const bytes = encode(msg);
      this.sock?.send(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
    }
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
  /** §4A v1.3: decline the opponent's draw offer, or withdraw our own. */
  declineDraw(gameId: string): void {
    this.sendMsg({ DeclineDraw: { game_id: gameId } });
  }
  /** §4A v1.3: TAKEBACK is a request only; the opponent must accept. */
  requestTakeback(gameId: string): void {
    this.sendMsg({ Takeback: { game_id: gameId } });
  }
  acceptTakeback(gameId: string, atPly: number): void {
    this.sendMsg({ TakebackAccept: { game_id: gameId, at_ply: atPly } });
  }
  declineTakeback(gameId: string, atPly: number): void {
    this.sendMsg({ TakebackDecline: { game_id: gameId, at_ply: atPly } });
  }
  /** Force a fresh STATE (§4A v1.1 RESYNC). Called by the React layer when the
   * reducer's needsResync flag is set (GameSocket does not observe it itself). */
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

  /** Encode a ClientMsg via the shared seam and send it as a binary frame. If
   * the socket is not OPEN yet, queue it for the onopen flush instead of
   * sending (a real WebSocket throws on send() before OPEN). */
  private sendMsg(msg: ClientMsg): void {
    if (this.sock == null) return;
    // The socket is sendable only when OUR lifecycle flag says we have a live,
    // not-torn-down socket AND (when the socket exposes it) its readyState is
    // OPEN. Requiring both means a socket we have already seen close — even if a
    // fake's readyState lags — is never treated as open.
    const open = this.isOpen && (this.sock.readyState == null || this.sock.readyState === 1);
    if (!open) {
      // Only RECONNECT-SAFE frames may wait for the open flush: AUTHENTICATE and
      // SEEK are idempotent-by-design and are re-sent on every (re)open anyway.
      // A game-mutating frame (MOVE/RESIGN/OFFER_DRAW/RESYNC) buffered across a
      // disconnect would flush AFTER the server auto-pushes STATE on reconnect
      // (which restores ply) — a stale MOVE at an old ply_expected the server
      // rejects StalePly, or worse applies against a board that moved on. Drop
      // it: the UI re-drives from the resynced STATE. (The outbox exists for the
      // pre-FIRST-open eager seek/auth, not reconnect replay of game moves.)
      if (isReconnectSafe(msg)) this.outbox.push(msg);
      return;
    }
    const bytes = encode(msg); // §4A bytes produced in Rust (NFR-5)
    // Send the exact byte view (copy to a fresh ArrayBuffer to avoid shipping a
    // larger backing buffer than the frame).
    this.sock.send(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
  }

  private status(s: ConnStatus): void {
    this.opts.onStatus?.(s);
  }
}

/** Coerce an incoming WebSocket message payload to Uint8Array, or null if not
 * binary. Detection is REALM-ROBUST: `instanceof ArrayBuffer` is false for a
 * buffer created in a different JS realm (e.g. a test harness, or a worker),
 * so we tag-check with Object.prototype.toString and fall back to the
 * structural shape (a numeric byteLength + a slice/ArrayBuffer.isView view).
 * A real browser has one realm so instanceof would also work there; this just
 * never misclassifies a genuine binary frame as "non-binary" and drops it. */
/** Only AUTHENTICATE and SEEK may sit in the pre-open outbox: both are re-sent
 * on every (re)open by design, so buffering them is safe and idempotent. Every
 * other frame is game-mutating and must not be replayed across a reconnect. */
function isReconnectSafe(msg: ClientMsg): boolean {
  return "Authenticate" in msg || "Seek" in msg;
}

function toBytes(data: unknown): Uint8Array | null {
  if (data == null || typeof data !== "object") return null;
  if (ArrayBuffer.isView(data)) {
    // Any typed-array / DataView view → copy its exact byte window.
    const v = data as ArrayBufferView;
    return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  }
  const tag = Object.prototype.toString.call(data);
  if (tag === "[object ArrayBuffer]" || tag === "[object SharedArrayBuffer]") {
    return new Uint8Array(data as ArrayBufferLike);
  }
  return null;
}
