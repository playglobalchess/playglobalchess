// Pure clock model for SPEC-S4-WEB-F01 §3.3.1 (C1-C8, C10). The server is the
// only authority on time and on flagging (GS-3 §3.4); this is a display model.
//
// State shape (lives in GameState): per seat a last authoritative `value` (ms)
// and the local receipt time `anchor` of the event that set it. Nothing is ever
// decremented per tick: the displayed value is derived from performance.now()
// at render (C3), so it cannot drift and a throttled background tab shows the
// right value when it wakes.

import type { Position } from "../board/position";

export interface ClockView {
  phase: string;
  position: Position | null;
  /** Last authoritative (or frozen) value per seat, ms. */
  clocks: number[];
  /** Local receipt time (performance.now) each value is measured from. */
  clockAt: number[];
}

/** C2: exactly one clock runs while phase = playing: the side to move of the
 * current position (seat 0 = white). None before MATCHED or after GAME_END.
 * The position includes an un-confirmed optimistic move, which is what makes
 * C4 fall out: the mover's clock stops and the opponent's starts. */
export function runningSeat(s: Pick<ClockView, "phase" | "position">): number | null {
  if (s.phase !== "playing" || s.position == null) return null;
  return s.position.turn === "w" ? 0 : 1;
}

/** C3 + C6: last value minus time since its anchor for the running seat,
 * floored at 0. The client never flags; it waits for the server's GAME_END. */
export function displayedMs(s: ClockView, seat: number, now: number): number {
  const value = s.clocks[seat] ?? 0;
  if (runningSeat(s) !== seat) return value;
  return Math.max(0, value - (now - (s.clockAt[seat] ?? now)));
}

/** Re-anchor every seat at `at`, folding the time elapsed so far into the
 * values (the running seat is charged, the others are frozen). Called before an
 * event that may change which seat runs, so the previous runner is frozen at its
 * displayed value and the next one starts counting from `at`. */
export function settleClocks(s: ClockView, at: number): { clocks: number[]; clockAt: number[] } {
  return {
    clocks: s.clocks.map((_, seat) => displayedMs(s, seat, at)),
    clockAt: s.clocks.map(() => at),
  };
}

/** C10 format: h:mm:ss at or above 1 h; m:ss otherwise; m:ss.t below 10 s.
 * Values are floored (a clock never shows time it no longer has). */
export function formatClock(ms: number): string {
  const total = Math.max(0, ms);
  const sec = Math.floor(total / 1000);
  const mm = Math.floor(sec / 60) % 60;
  const ss = String(sec % 60).padStart(2, "0");
  if (total >= 3_600_000) return `${Math.floor(sec / 3600)}:${String(mm).padStart(2, "0")}:${ss}`;
  if (total < 10_000) return `${mm}:${ss}.${Math.floor(total / 100) % 10}`;
  return `${mm}:${ss}`;
}

/** C10 refresh cadence: 10 Hz below 10 s, otherwise once per second. */
export function refreshIntervalMs(displayed: number): number {
  return displayed < 10_000 ? 100 : 1000;
}
