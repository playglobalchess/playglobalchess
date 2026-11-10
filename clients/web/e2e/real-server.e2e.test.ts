// @vitest-environment node
//
// REAL-SERVER END-TO-END (SPEC-S4-WEB-F01 §6, the headline proof of this
// slice). Everything here is real except the two human players:
//   - a real Go game-server (cmd/e2e-server) booted as a subprocess, serving
//     the actual §4A WebSocket edge with the real matchmaker + engine + store;
//   - two real GameSocket clients (the production class under test) talking
//     over Node's native WebSocket;
//   - the real shared §4A wasm codec on both ends.
// It drives a matched game far enough to prove the thing the whole v1.2 slice
// exists for: a move reaches BOTH seats with the correct uci (ADR-18), so a
// second human sees the first's move.
//
// This test is OUTSIDE the unit glob (it lives under e2e/, run via `npm run
// e2e`) because it spawns a subprocess and needs the server binary prebuilt.
// It is SKIPPED (not failed) when the binary is absent, so a plain `vitest run`
// without the Go build is still green; CI builds the binary then runs it.

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { spawn, type ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { GameSocket } from "../src/net/GameSocket";
import type { ServerMsg } from "../src/codec/messages";
import { DEV_POOL, DEV_TIME_CONTROL } from "../src/devConfig";

// The harness builds the binary to this path (see npm run e2e). Overridable.
const SERVER_BIN = process.env.PGC_E2E_SERVER ?? "../../services/game-server/bin/e2e-server";
const HOST = "127.0.0.1";
const PORT = Number(process.env.PGC_E2E_PORT ?? 7791);
const WS_URL = `ws://${HOST}:${PORT}/ws`;

const binResolved = existsSync(SERVER_BIN)
  ? SERVER_BIN
  : new URL(SERVER_BIN, import.meta.url).pathname;
const haveBin = existsSync(binResolved);
const d = haveBin ? describe : describe.skip;

let server: ChildProcess | null = null;

/** Boot the e2e-server and resolve once it prints its readiness line. */
function startServer(): Promise<void> {
  return new Promise((resolve, reject) => {
    server = spawn(binResolved, ["-addr", `${HOST}:${PORT}`, "-path", "/ws"], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    const timer = setTimeout(() => reject(new Error("server did not become ready in 10s")), 10_000);
    server.stdout?.on("data", (buf: Buffer) => {
      if (buf.toString().includes("listening")) {
        clearTimeout(timer);
        resolve();
      }
    });
    server.on("error", reject);
  });
}

/** A thin promise-based client wrapper around GameSocket for the test driver. */
class Client {
  private readonly sock: GameSocket;
  readonly inbox: ServerMsg[] = [];
  constructor(token: string) {
    this.sock = new GameSocket({
      url: WS_URL,
      token,
      onServerMsg: (m) => this.inbox.push(m),
    });
  }
  connect(): void {
    this.sock.connect();
  }
  seek(): void {
    // The SAME pool + time control the UI sends (src/devConfig), so a malformed
    // value (e.g. the "300+0" the server rejected with "could not start game")
    // fails this e2e in CI rather than only a manual browser run.
    this.sock.seek(DEV_POOL, DEV_TIME_CONTROL, false);
  }
  move(gameId: string, ply: number, uci: string): void {
    this.sock.move(gameId, ply, uci);
  }
  requestTakeback(gameId: string): void {
    this.sock.requestTakeback(gameId);
  }
  acceptTakeback(gameId: string, atPly: number): void {
    this.sock.acceptTakeback(gameId, atPly);
  }
  close(): void {
    this.sock.close();
  }
  /** Wait until a predicate matches an UNCONSUMED inbox frame, or time out.
   * Consumption is tracked PER-INDEX (a Set), NOT via a single moving cursor:
   * matching a MOVE_ACCEPTED must not step a shared cursor PAST a GAME_END that
   * happens to sit at an earlier index (server interleave ordering is not
   * guaranteed), which would make a later waitFor(isGameEnd) miss it. Each frame
   * is consumed at most once; a non-matching frame is left for its own waitFor. */
  private readonly consumedIdx = new Set<number>();
  async waitFor<T extends ServerMsg>(pred: (m: ServerMsg) => m is T, ms = 5000): Promise<T> {
    const deadline = Date.now() + ms;
    for (;;) {
      for (let i = 0; i < this.inbox.length; i++) {
        if (this.consumedIdx.has(i)) continue;
        const m = this.inbox[i]!;
        if (pred(m)) {
          this.consumedIdx.add(i);
          return m;
        }
      }
      if (Date.now() > deadline) throw new Error("timed out waiting for a server message");
      await new Promise((r) => setTimeout(r, 20));
    }
  }
}

const isMatched = (m: ServerMsg): m is Extract<ServerMsg, { Matched: unknown }> => "Matched" in m;
const isMoveAccepted = (m: ServerMsg): m is Extract<ServerMsg, { MoveAccepted: unknown }> =>
  "MoveAccepted" in m;
const isGameEnd = (m: ServerMsg): m is Extract<ServerMsg, { GameEnd: unknown }> => "GameEnd" in m;
const isTakebackOffered = (m: ServerMsg): m is Extract<ServerMsg, { TakebackOffered: unknown }> =>
  "TakebackOffered" in m;
const isState = (m: ServerMsg): m is Extract<ServerMsg, { State: unknown }> => "State" in m;

d("real-server e2e (web client <-> Go game-server)", () => {
  beforeAll(async () => {
    await startServer();
  }, 15_000);

  afterAll(() => {
    server?.kill("SIGTERM");
  });

  // Guards against in-memory-pool state leaking between the two tests (they
  // share one beforeAll server): each test's match must be a NEW game_id, so a
  // leftover seek/half-game from the prior test cannot silently pair here.
  const seenGameIds = new Set<string>();

  it("two clients match and a move reaches BOTH seats with the right uci", async () => {
    const white = new Client("alice");
    const black = new Client("bob");
    white.connect();
    black.connect();

    white.seek();
    // small gap so alice is enqueued before bob claims her
    await new Promise((r) => setTimeout(r, 50));
    black.seek();

    const wm = await white.waitFor(isMatched);
    const bm = await black.waitFor(isMatched);
    const gameId = wm.Matched.game_id;
    expect(bm.Matched.game_id).toBe(gameId);
    expect(seenGameIds.has(gameId)).toBe(false); // a NEW game, not a leaked one
    seenGameIds.add(gameId);

    // Whichever of our two clients drew seat 0 is White and moves first.
    const mover = wm.Matched.seat_idx === 0 ? white : black;
    const opponent = mover === white ? black : white;
    mover.move(gameId, 1, "e2e4");

    // §4A v1.2 / ADR-18: MOVE_ACCEPTED carries the uci and goes to BOTH seats.
    // The CONTRACT this slice exists for is that the OPPONENT (not just the
    // mover's own echo) sees the move — so assert the opponent specifically.
    const moverAck = await mover.waitFor(isMoveAccepted);
    const oppAck = await opponent.waitFor(isMoveAccepted);
    expect(moverAck.MoveAccepted.ply).toBe(1);
    expect(moverAck.MoveAccepted.uci).toBe("e2e4"); // the mover's own echo
    expect(oppAck.MoveAccepted.ply).toBe(1);
    expect(oppAck.MoveAccepted.uci).toBe("e2e4"); // the OPPONENT saw the move — the whole point

    // No client received an ERROR frame — a malformed seek (e.g. a time control
    // the server cannot resolve) would arrive here as Error "could not start
    // game" and must fail the test, not pass silently.
    const anyError = (c: Client): boolean => c.inbox.some((m) => "Error" in m);
    expect(anyError(white)).toBe(false);
    expect(anyError(black)).toBe(false);

    white.close();
    black.close();
  }, 20_000);

  it("plays a full game to a terminal (Fool's mate) — both seats, every ply + GAME_END", async () => {
    const white = new Client("carol");
    const black = new Client("dave");
    white.connect();
    black.connect();
    white.seek();
    await new Promise((r) => setTimeout(r, 50));
    black.seek();

    const wm = await white.waitFor(isMatched);
    const bm = await black.waitFor(isMatched);
    const gameId = wm.Matched.game_id;
    expect(bm.Matched.game_id).toBe(gameId);
    expect(seenGameIds.has(gameId)).toBe(false); // a NEW game, not a leaked one
    seenGameIds.add(gameId);

    // Seat 0 is White and moves on odd plies. Map our two clients to seats.
    const bySeat: Record<number, Client> = {
      [wm.Matched.seat_idx]: white,
      [bm.Matched.seat_idx]: black,
    };
    const whiteSeat = bySeat[0]!;
    const blackSeat = bySeat[1]!;

    // Fool's mate: 1. f3 e5 2. g4 Qh4#  → Black mates on ply 4.
    const script: Array<{ by: Client; uci: string }> = [
      { by: whiteSeat, uci: "f2f3" },
      { by: blackSeat, uci: "e7e5" },
      { by: whiteSeat, uci: "g2g4" },
      { by: blackSeat, uci: "d8h4" }, // mate
    ];

    for (let i = 0; i < script.length; i++) {
      const ply = i + 1;
      script[i]!.by.move(gameId, ply, script[i]!.uci);
      // BOTH seats must see each accepted move, with a monotonically increasing ply.
      const wAck = await white.waitFor(isMoveAccepted);
      const bAck = await black.waitFor(isMoveAccepted);
      expect(wAck.MoveAccepted.ply).toBe(ply);
      expect(bAck.MoveAccepted.ply).toBe(ply);
      expect(wAck.MoveAccepted.uci).toBe(script[i]!.uci);
      expect(bAck.MoveAccepted.uci).toBe(script[i]!.uci);
    }

    // The mating move ends the game: BOTH seats receive GAME_END with Mate (1).
    const wEnd = await white.waitFor(isGameEnd);
    const bEnd = await black.waitFor(isGameEnd);
    expect(wEnd.GameEnd.termination).toBe(1); // Termination.Mate
    expect(bEnd.GameEnd.termination).toBe(1);
    // result_vector: the mated side (White, seat 0) lost (0), Black won (2).
    expect(wEnd.GameEnd.result_vector[0]).toBe(0);
    expect(wEnd.GameEnd.result_vector[1]).toBe(2);

    const anyError = (c: Client): boolean => c.inbox.some((m) => "Error" in m);
    expect(anyError(white)).toBe(false);
    expect(anyError(black)).toBe(false);

    white.close();
    black.close();
  }, 20_000);

  it("takeback with consent: request, offer, accept, rewound STATE to both, then a new move (#84, GS-7)", async () => {
    const white = new Client("erin");
    const black = new Client("frank");
    white.connect();
    black.connect();
    white.seek();
    await new Promise((r) => setTimeout(r, 50));
    black.seek();

    const wm = await white.waitFor(isMatched);
    const bm = await black.waitFor(isMatched);
    const gameId = wm.Matched.game_id;
    expect(seenGameIds.has(gameId)).toBe(false);
    seenGameIds.add(gameId);
    const bySeat: Record<number, Client> = { [wm.Matched.seat_idx]: white, [bm.Matched.seat_idx]: black };
    const w = bySeat[0]!;
    const b = bySeat[1]!;

    const script: Array<[Client, string]> = [[w, "e2e4"], [b, "e7e5"], [w, "g1f3"]];
    for (let i = 0; i < script.length; i++) {
      script[i]![0].move(gameId, i + 1, script[i]![1]);
      await w.waitFor(isMoveAccepted);
      await b.waitFor(isMoveAccepted);
    }

    // White asks to take back Nf3; only Black is told, with the request's ply.
    w.requestTakeback(gameId);
    const offer = await b.waitFor(isTakebackOffered);
    expect(offer.TakebackOffered).toEqual({ game_id: gameId, seat_idx: 0, at_ply: 3 });

    // Black accepts; BOTH seats get a STATE rewound to ply 2, each with its own seat.
    b.acceptTakeback(gameId, 3);
    const ws = await w.waitFor(isState);
    const bs = await b.waitFor(isState);
    for (const [st, seat] of [[ws, 0], [bs, 1]] as const) {
      expect(st.State.state.ply).toBe(2);
      expect(st.State.state.last_move_uci).toBe("e7e5");
      expect(st.State.state.you_seat).toBe(seat);
      expect(st.State.state.takeback_by).toBe(0xff);
    }

    // The #84 scenario: a DIFFERENT move at the recurring ply is a new move,
    // acked as itself to both seats (it was re-acked as the undone Nf3 before).
    w.move(gameId, 3, "b1c3");
    const wa = await w.waitFor(isMoveAccepted);
    const ba = await b.waitFor(isMoveAccepted);
    expect(wa.MoveAccepted).toMatchObject({ ply: 3, uci: "b1c3" });
    expect(ba.MoveAccepted).toMatchObject({ ply: 3, uci: "b1c3" });

    const anyError = (c: Client): boolean => c.inbox.some((m) => "Error" in m);
    expect(anyError(white)).toBe(false);
    expect(anyError(black)).toBe(false);
    white.close();
    black.close();
  }, 20_000);
});
