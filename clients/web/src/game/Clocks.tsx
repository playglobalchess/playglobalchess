// Clock display — SPEC-S4-WEB-F01 §3.3.1 C2/C3/C6/C10/C11.
//
// OWNER of ticking. The tick state lives HERE and nowhere above, so a tick
// re-renders only this component, never the board (C10). Nothing is decremented
// per tick: every render derives the value from the injected time source and the
// reducer's (value, anchor) pairs (C3), so a throttled or backgrounded tab shows
// the right value the moment it wakes.

import { createContext, useContext, useEffect, useState } from "react";
import { t } from "../i18n";
import { type ClockView, displayedMs, formatClock, refreshIntervalMs, runningSeat } from "./clock";

/** The monotonic time source. performance.now in the browser; tests inject a
 * fake. The same source stamps the reducer's anchors (useGame `now`). */
export const NowContext = createContext<() => number>(() => performance.now());

export function Clocks({ clock, mySeat }: { clock: ClockView; mySeat: number | null }): React.JSX.Element {
  const now = useContext(NowContext);
  const [, setTick] = useState(0);
  const at = now();
  const running = runningSeat(clock);

  // (Re)arm after EVERY render: the interval depends on the displayed value of
  // the running seat (10 Hz below 10 s, 1 Hz otherwise). At 0 the display has
  // stopped (C6) so nothing is armed; only the server's GAME_END ends the game.
  useEffect(() => {
    const ms = running == null ? 0 : displayedMs(clock, running, now());
    if (ms <= 0) return; // nothing runs (not playing) or the display is at 0
    const id = setTimeout(() => setTick((n) => n + 1), refreshIntervalMs(ms));
    return () => clearTimeout(id);
  });

  return (
    // C11: NOT a live region (no aria-live): ticks are never announced.
    <div role="group" aria-label={t("game.clock.aria")} style={{ display: "flex", gap: "1rem" }}>
      {clock.clocks.map((_, seat) => {
        const text = formatClock(displayedMs(clock, seat, at));
        const who = seat === mySeat ? t("game.clock.you") : t("game.clock.opponent");
        return (
          // C11: each seat's value is exposed through its accessible name.
          <span
            key={seat}
            role="group"
            tabIndex={0}
            aria-label={`${who}: ${text}`}
            style={{ fontWeight: seat === mySeat ? 700 : 400 }}
          >
            {text}
          </span>
        );
      })}
    </div>
  );
}
