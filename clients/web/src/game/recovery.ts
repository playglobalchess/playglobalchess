// Recovery constants — SPEC-S4-WEB-F01 §3.4.1 / Q-S4f (DECIDED). Fixed
// constants, deliberately not tunable at runtime. The reducer (pure) reads the
// two counters' bounds; useGame (the only timer owner) reads the two durations.

/** N: RESYNC attempts per episode before the forced reconnect. */
export const SYNC_MAX_ATTEMPTS = 3;
/** K: forced-reconnect cycles before FAILED. */
export const SYNC_MAX_CYCLES = 2;
/** T_reply: how long to wait (while connected) for the awaited STATE. */
export const SYNC_REPLY_TIMEOUT_MS = 1000;
/** B: backoff base; the wait after failed attempt n is B * 2^(n-1). */
export const SYNC_BACKOFF_BASE_MS = 250;

export function syncBackoffMs(failedAttempt: number): number {
  return SYNC_BACKOFF_BASE_MS * 2 ** (failedAttempt - 1);
}
