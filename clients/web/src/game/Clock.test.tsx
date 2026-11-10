// @vitest-environment jsdom
//
// The clock display ticks with fake timers (#112): the failing test from the
// issue (both clocks stayed 5:00), plus the running marker and the freeze.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ClockBar, formatClock } from "./Clock";
import { reduce, initialState, type GameState } from "./reducer";
import { Status } from "../codec/messages";
import { sb } from "../test/stateBody";
import { monotonicNow } from "./monotonic";

const START_FEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";
const GID = "f47ac10b-58cc-4372-a567-0e02b2c3d479";

let root: Root | null = null;
let container: HTMLDivElement | null = null;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "performance"] });
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  vi.useRealTimers();
});

function render(s: GameState): void {
  if (container == null) {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  }
  const el = <ClockBar clocks={s.clocks} clockRun={s.clockRun} mySeat={s.mySeat} />;
  act(() => root!.render(el));
}

function timers(): HTMLElement[] {
  return Array.from(container!.querySelectorAll<HTMLElement>('[role="timer"]'));
}

function matchedAsBlack(): GameState {
  return reduce(reduce(initialState, { kind: "seek", rated: false }), {
    kind: "server",
    at: monotonicNow(),
    msg: {
      Matched: {
        game_id: GID,
        seat_idx: 1,
        opponent: "x",
        start_state: sb(START_FEN, 0, [300000, 300000], { you_seat: 1, running_seat: 0, status: Status.Live }),
      },
    },
  });
}

describe("ClockBar (#112)", () => {
  it("the side to move counts down each second; the other clock does not move", () => {
    render(matchedAsBlack());
    act(() => vi.advanceTimersByTime(5000));
    const [white, black] = timers();
    expect(white!.textContent).toContain("4:55");
    expect(black!.textContent).toContain("5:00");
  });

  it("marks the running clock with more than colour or weight", () => {
    render(matchedAsBlack());
    const [white, black] = timers();
    expect(white!.dataset.running).toBe("true");
    expect(white!.textContent).toContain("▶");
    expect(white!.textContent).toContain("(running)");
    expect(black!.textContent).not.toContain("▶");
    expect(black!.textContent).not.toContain("(running)");
    expect(black!.textContent).toContain("You");
    expect(white!.textContent).toContain("Opponent");
  });

  it("stops on GAME_END", () => {
    let s = matchedAsBlack();
    render(s);
    act(() => vi.advanceTimersByTime(3000));
    s = reduce(s, {
      kind: "server",
      at: monotonicNow(),
      msg: { GameEnd: { game_id: GID, result_vector: [0, 2], termination: 0x02, rating_delta: [] } },
    });
    render(s);
    act(() => vi.advanceTimersByTime(10000));
    const [white] = timers();
    expect(white!.textContent).toContain("4:57");
    expect(white!.dataset.running).toBe("false");
  });

  it("is not a live region (no per-tick announcements)", () => {
    render(matchedAsBlack());
    expect(container!.querySelector("[aria-live]")).toBeNull();
  });
});

describe("formatClock", () => {
  it("formats m:ss and clamps at 0", () => {
    expect(formatClock(300000)).toBe("5:00");
    expect(formatClock(59999)).toBe("0:59");
    expect(formatClock(-5)).toBe("0:00");
  });
});
