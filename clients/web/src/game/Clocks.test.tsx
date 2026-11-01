// @vitest-environment jsdom
//
// SPEC-S4-WEB-F01 §3.3.1 C3/C6/C10/C11 for the Clocks component, with a fake
// performance.now and vi fake timers (the injected NowContext reads the faked
// performance clock, so timers and the time source advance together).

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Profiler, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Clocks } from "./Clocks";
import { parseFen } from "../board/position";
import type { ClockView } from "./clock";

const START = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let commits = 0;

function view(clocks: number[], phase = "playing", anchor = performance.now()): ClockView {
  return { phase, position: parseFen(START), clocks, clockAt: clocks.map(() => anchor) };
}
function mount(clock: ClockView): void {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  commits = 0;
  act(() => {
    root!.render(
      <Profiler id="clocks" onRender={() => commits++}>
        <Clocks clock={clock} mySeat={0} />
      </Profiler>,
    );
  });
}
// Step in 10 ms acts: the next tick is armed by an effect that only runs when an
// act scope exits, so one big act would collapse many ticks into one render.
const advance = (ms: number): void => {
  for (let i = 0; i < ms; i += 10) {
    act(() => {
      vi.advanceTimersByTime(10);
    });
  }
};
const seatEls = (): HTMLElement[] =>
  Array.from(container!.querySelectorAll('[role="group"][tabindex="0"]')) as HTMLElement[];

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "performance"] });
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  vi.useRealTimers();
});

describe("Clocks (§3.3.1)", () => {
  it("C3: shows the running seat counting down and the other frozen", () => {
    mount(view([65_000, 65_000]));
    expect(seatEls().map((e) => e.textContent)).toEqual(["1:05", "1:05"]);
    advance(5000);
    expect(seatEls().map((e) => e.textContent)).toEqual(["1:00", "1:05"]);
  });

  it("C3: a tab that was throttled for 60 s shows the right value on its next render", () => {
    mount(view([300_000, 300_000]));
    // A throttled tab: the clock jumps 60 s and the single pending tick fires once.
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(seatEls()[0]!.textContent).toBe("4:00");
  });

  it("C6: stops at 0:00.0, arms no further timer, never removes itself", () => {
    mount(view([1_500, 300_000]));
    advance(5000);
    expect(seatEls()[0]!.textContent).toBe("0:00.0");
    expect(vi.getTimerCount()).toBe(0);
    advance(10_000);
    expect(seatEls()[0]!.textContent).toBe("0:00.0");
  });

  it("C10: re-renders about 10 times a second under 10 s", () => {
    mount(view([9_000, 300_000]));
    const before = commits;
    advance(1000);
    expect(commits - before).toBeGreaterThanOrEqual(9);
    expect(commits - before).toBeLessThanOrEqual(11);
  });

  it("C10: re-renders once a second at or above 10 s", () => {
    mount(view([60_000, 300_000]));
    const before = commits;
    advance(3000);
    expect(commits - before).toBe(3);
  });

  it("C10: switches from 1 Hz to 10 Hz when the running clock drops under 10 s", () => {
    mount(view([12_000, 300_000]));
    advance(3000); // now 9 s
    const before = commits;
    advance(1000);
    expect(commits - before).toBeGreaterThanOrEqual(9);
  });

  it("C10: unmount clears the tick timer", () => {
    mount(view([60_000, 300_000]));
    expect(vi.getTimerCount()).toBe(1);
    act(() => root!.unmount());
    root = null;
    expect(vi.getTimerCount()).toBe(0);
  });

  it("C2: no timer is armed when no clock runs (not playing)", () => {
    mount(view([60_000, 60_000], "game_over"));
    expect(vi.getTimerCount()).toBe(0);
    advance(5000);
    expect(seatEls().map((e) => e.textContent)).toEqual(["1:00", "1:00"]);
  });

  it("C11: no live region; each seat's accessible name carries its value and tracks ticks", () => {
    mount(view([65_000, 65_000]));
    expect(container!.querySelector("[aria-live]")).toBeNull();
    expect(container!.querySelector('[role="timer"], [role="status"], [role="alert"], [role="log"]')).toBeNull();
    const names = (): string[] => seatEls().map((e) => e.getAttribute("aria-label") ?? "");
    expect(names()).toEqual(["Your clock: 1:05", "Opponent's clock: 1:05"]);
    advance(5000);
    expect(names()).toEqual(["Your clock: 1:00", "Opponent's clock: 1:05"]);
  });
});
