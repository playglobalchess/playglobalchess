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
    this.sock.seek("blitz", "blitz-5+3", false);
  }
  move(gameId: string, ply: number, uci: string): void {
    this.sock.move(gameId, ply, uci);
  }
  close(): void {
    this.sock.close();
  }
  /** Wait until a predicate matches an inbox message, or time out. */
  async waitFor<T extends ServerMsg>(pred: (m: ServerMsg) => m is T, ms = 5000): Promise<T> {
    const deadline = Date.now() + ms;
    for (;;) {
      const hit = this.inbox.find(pred);
      if (hit != null) return hit;
      if (Date.now() > deadline) throw new Error("timed out waiting for a server message");
      await new Promise((r) => setTimeout(r, 20));
    }
  }
}

const isMatched = (m: ServerMsg): m is Extract<ServerMsg, { Matched: unknown }> => "Matched" in m;
const isMoveAccepted = (m: ServerMsg): m is Extract<ServerMsg, { MoveAccepted: unknown }> =>
  "MoveAccepted" in m;

d("real-server e2e (web client <-> Go game-server)", () => {
  beforeAll(async () => {
    await startServer();
  }, 15_000);

  afterAll(() => {
    server?.kill("SIGTERM");
  });

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

    white.close();
    black.close();
  }, 20_000);
});
