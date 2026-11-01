import { describe, it, expect } from "vitest";
import { boardInteractive } from "./boardInteractive";

describe("boardInteractive (§3.4.1 read-only while recovering)", () => {
  const ok = { pendingMove: null, sync: { phase: "IN_SYNC" as const } };
  it("is interactive only on our turn, no pending move, IN_SYNC", () => {
    expect(boardInteractive(ok, true)).toBe(true);
    expect(boardInteractive(ok, false)).toBe(false);
    expect(boardInteractive({ ...ok, pendingMove: {} }, true)).toBe(false);
  });
  it("is read-only in SYNCING, RECONNECTING and FAILED", () => {
    for (const phase of ["SYNCING", "RECONNECTING", "FAILED"] as const) {
      expect(boardInteractive({ ...ok, sync: { phase } }, true)).toBe(false);
    }
  });
});
