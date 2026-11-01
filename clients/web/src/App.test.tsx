// @vitest-environment jsdom
//
// Recovery UI tests (SPEC-S4-WEB-F01 §3.4.1 "UX while recovering" / "Terminal
// state"). useGame is mocked so each test pins sync.phase directly; the real App,
// Board and i18n render. The codec seam is mocked for the same reason as in
// useGame.test.tsx (the wasm asset is not loadable under jsdom; not under test).

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./codec/seam", () => ({ codecProtocolVersion: () => 1 }));

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { App } from "./App";
import { initialState, IN_SYNC, type GameState, type SyncState } from "./game/reducer";
import { parseFen } from "./board/position";
import type { UseGameResult } from "./game/useGame";

let mockResult: UseGameResult;
vi.mock("./game/useGame", () => ({ useGame: () => mockResult }));

const START = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const move = vi.fn();

function gameState(sync: SyncState): GameState {
  return {
    ...initialState,
    phase: "playing",
    gameId: "g1",
    mySeat: 0,
    opponent: "bob",
    position: parseFen(START),
    clocks: [300000, 300000],
    sync,
  };
}
function setSync(sync: SyncState, status: UseGameResult["status"] = "connected"): void {
  mockResult = {
    state: gameState(sync),
    status,
    commands: {
      seek: vi.fn(),
      move,
      resign: vi.fn(),
      offerDraw: vi.fn(),
      acceptDraw: vi.fn(),
    },
  };
}

let root: Root | null = null;
let container: HTMLDivElement | null = null;

function renderApp(): void {
  act(() => {
    root!.render(<App />);
  });
}

/** Mount App past the dev-token gate. */
function mountApp(): void {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  renderApp();
  const input = container.querySelector("input")!;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(input, "tok");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  act(() => {
    container!.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
}
function rerender(): void {
  renderApp();
}
function cell(sq: string): HTMLElement {
  const cells = Array.from(container!.querySelectorAll<HTMLElement>('[role="gridcell"]'));
  const c = cells.find((x) => (x.getAttribute("aria-label") ?? "").startsWith(`${sq},`));
  if (c == null) throw new Error(`no cell for ${sq}`);
  return c;
}
function click(sq: string): void {
  act(() => {
    cell(sq).dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}
const buttons = (): string[] =>
  Array.from(container!.querySelectorAll("button")).map((b) => b.textContent ?? "");

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  move.mockClear();
});

const syncing: SyncState = { ...IN_SYNC, phase: "SYNCING", attempt: 1, seq: 1, trigger: "T1_GAP" };
const reconnecting: SyncState = { ...syncing, phase: "RECONNECTING", attempt: 3, cycle: 1, seq: 4 };
const failed: SyncState = { ...syncing, phase: "FAILED", attempt: 3, cycle: 2, seq: 5 };

describe("recovery UI (§3.4.1)", () => {
  it("control: IN_SYNC on our turn lets the board move and shows no sync notice", () => {
    setSync(IN_SYNC);
    mountApp();
    click("e2");
    click("e4");
    expect(move).toHaveBeenCalledWith("e2e4");
    expect(container!.textContent).not.toContain("Syncing game");
  });

  for (const [name, sync] of [["SYNCING", syncing], ["RECONNECTING", reconnecting]] as const) {
    it(`${name}: board visible but read-only, polite status "Syncing game…", no recovery buttons or modal`, () => {
      setSync(sync);
      mountApp();
      expect(container!.querySelectorAll('[role="gridcell"]').length).toBe(64);
      click("e2");
      click("e4");
      expect(move).not.toHaveBeenCalled();
      const status = Array.from(container!.querySelectorAll('[role="status"]')).find((n) =>
        (n.textContent ?? "").includes("Syncing game…"),
      );
      expect(status).toBeDefined();
      // Only the normal in-game controls: nothing added by recovery, no dialog.
      expect(buttons()).toEqual(["Resign", "Offer draw"]);
      expect(container!.querySelector('[role="dialog"], [role="alertdialog"]')).toBeNull();
      expect(container!.querySelector('[role="alert"]')).toBeNull();
    });
  }

  it("FAILED: a role=alert with exactly ONE recovery action (Refresh); board read-only", () => {
    setSync(failed);
    mountApp();
    const alert = container!.querySelector('[role="alert"]')!;
    expect(alert).not.toBeNull();
    expect(Array.from(alert.querySelectorAll("button")).map((b) => b.textContent)).toEqual(["Refresh"]);
    click("e2");
    click("e4");
    expect(move).not.toHaveBeenCalled();
  });

  it("FAILED: Refresh reloads the page", () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { protocol: "http:", host: "test", reload });
    try {
      setSync(failed);
      mountApp();
      const btn = Array.from(container!.querySelectorAll("button")).find((b) => b.textContent === "Refresh")!;
      act(() => btn.dispatchEvent(new MouseEvent("click", { bubbles: true })));
      expect(reload).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("recovery never moves keyboard focus (board focus survives IN_SYNC -> SYNCING -> FAILED -> IN_SYNC)", () => {
    setSync(IN_SYNC);
    mountApp();
    const target = cell("e2");
    act(() => target.focus());
    const focused = document.activeElement;
    expect(focused).toBe(target);
    for (const sync of [syncing, reconnecting, failed, IN_SYNC]) {
      setSync(sync);
      rerender();
      expect(document.activeElement).toBe(focused);
    }
  });

  it("in-game status line: Game in progress when connected; Reconnecting… when the socket is down (§3.4.1)", () => {
    setSync(IN_SYNC, "connected");
    mountApp();
    const first = (): string => container!.querySelector('[role="status"]')!.textContent ?? "";
    expect(first()).toBe("Game in progress");
    for (const down of ["disconnected", "connecting"] as const) {
      setSync(IN_SYNC, down);
      rerender();
      expect(first()).toBe("Reconnecting…");
    }
    setSync(IN_SYNC, "connected");
    rerender();
    expect(first()).toBe("Game in progress");
  });
});
