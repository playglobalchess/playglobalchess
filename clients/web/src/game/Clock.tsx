// The game clocks (#112). Display only: the server is authoritative, so these
// count the running seat down from the last server value and never flag; every
// MOVE_ACCEPTED / STATE re-anchors them, and GAME_END freezes them.
//
// Accessibility: the running clock is marked by a visible ▶ and the word
// "running" (not by colour or weight alone; bold already means "yours"). Each
// clock is role="timer", which is not a live region, so assistive tech is not
// made to read every tick; low-time announcements are #142.

import { useEffect, useState } from "react";
import { t } from "../i18n";
import { type ClockRun, displayClocks } from "./reducer";
import { monotonicNow } from "./monotonic";

/** How often a running clock repaints. Seconds are shown, so 250 ms keeps the
 * shown second at most a quarter-second late without costly re-renders. */
const TICK_MS = 250;

/** Local monotonic time, refreshed every TICK_MS while `running`. A stale value
 * is harmless: displayClocks ignores it when nothing runs, and treats a time
 * before the anchor as no elapsed time, so the first tick corrects it. */
function useNow(running: boolean): number {
  const [now, setNow] = useState(monotonicNow);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => setNow(monotonicNow()), TICK_MS);
    return () => clearInterval(id);
  }, [running]);
  return now;
}

export function ClockBar({
  clocks,
  clockRun,
  mySeat,
}: {
  clocks: number[];
  clockRun: ClockRun | null;
  mySeat: number | null;
}): React.JSX.Element {
  const now = useNow(clockRun != null);
  const shown = displayClocks({ clocks, clockRun }, now);
  return (
    <div aria-label={t("game.clock.aria")} style={{ display: "flex", gap: "1rem" }}>
      {shown.map((ms, seat) => {
        const running = clockRun?.seat === seat;
        const who = seat === mySeat ? t("game.clock.you") : t("game.clock.opponent");
        return (
          <span
            key={seat}
            role="timer"
            data-seat={seat}
            data-running={running ? "true" : "false"}
            style={{ fontWeight: seat === mySeat ? 700 : 400 }}
          >
            <span aria-hidden="true">{running ? "▶ " : ""}</span>
            {who} {formatClock(ms)}
            {running ? <span style={visuallyHidden}> {t("game.clock.running")}</span> : null}
          </span>
        );
      })}
    </div>
  );
}

const visuallyHidden: React.CSSProperties = {
  position: "absolute",
  width: 1,
  height: 1,
  overflow: "hidden",
  clip: "rect(0 0 0 0)",
  whiteSpace: "nowrap",
};

/** m:ss, whole seconds rounded down, never negative. */
export function formatClock(ms: number): string {
  const totalSec = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
