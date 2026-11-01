// @vitest-environment jsdom
//
// SPEC-S4-WEB-F01 §3.3.1 C10: a clock tick must not re-render the board. The
// real App and Clocks run; Board is replaced by a render counter.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./codec/seam", () => ({ codecProtocolVersion: () => 1 }));
const boardRenders = vi.fn();
vi.mock("./game/Board", () => ({
  Board: () => {
    boardRenders();
    return <div data-testid="board" />;
  },
}));
let mockResult: UseGameResult;
vi.mock("./game/useGame", () => ({ useGame: () => mockResult }));

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { App } from "./App";
import { initialState, IN_SYNC } from "./game/reducer";
import { parseFen } from "./board/position";
import type { UseGameResult } from "./game/useGame";

let root: Root | null = null;
let container: HTMLDivElement | null = null;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "performance"] });
  const t0 = performance.now();
  mockResult = {
    state: {
      ...initialState,
      phase: "playing",
      gameId: "g1",
      mySeat: 0,
      position: parseFen("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"),
      clocks: [20_000, 20_000],
      clockAt: [t0, t0],
      sync: IN_SYNC,
    },
    status: "connected",
    commands: { seek: vi.fn(), move: vi.fn(), resign: vi.fn(), offerDraw: vi.fn(), acceptDraw: vi.fn() },
  };
  boardRenders.mockClear();
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container?.remove();
  vi.useRealTimers();
});

describe("clock ticks vs the board (C10)", () => {
  it("the clock text changes on ticks while the Board does not re-render", () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root!.render(<App />);
    });
    const input = container.querySelector("input")!;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      setter.call(input, "tok");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    act(() => {
      container!.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    const own = (): string =>
      container!.querySelector('[aria-label^="Your clock"]')!.getAttribute("aria-label") ?? "";
    expect(own()).toBe("Your clock: 0:20");
    const rendersBefore = boardRenders.mock.calls.length;
    expect(rendersBefore).toBeGreaterThan(0);
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(own()).toBe("Your clock: 0:15");
    expect(boardRenders.mock.calls.length).toBe(rendersBefore);
  });
});
