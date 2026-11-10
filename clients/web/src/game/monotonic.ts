// The one local time source for clock display (#112). Monotonic, so a wall-clock
// change (NTP, the user editing the time) never jumps a running clock. Every
// reducer action that anchors a clock is stamped with it, and the clock display
// reads it, so both sides of the subtraction use the same timeline.
export function monotonicNow(): number {
  return performance.now();
}
