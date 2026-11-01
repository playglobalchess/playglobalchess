import { describe, expect, it } from "vitest";
import { announcementKey } from "./announcementKey";
import { initialState, reduce } from "./reducer";
import { ErrorCode } from "../codec/messages";

describe("announcementKey (#71.2)", () => {
  it("does not change when an error is raised then cleared", () => {
    const base = { ...initialState, phase: "playing" as const, ply: 4 };
    const withError = reduce(base, {
      kind: "server", at: 0,
      msg: { Error: { code: ErrorCode.UnsupportedVersion, msg: "x" } },
    });
    expect(withError.error).not.toBeNull();
    const cleared = { ...withError, error: null };
    expect(announcementKey(withError)).toBe(announcementKey(base));
    expect(announcementKey(cleared)).toBe(announcementKey(base));
  });

  it("changes on ply, draw offer and result", () => {
    const base = { ...initialState, phase: "playing" as const, ply: 4 };
    const k = announcementKey(base);
    expect(announcementKey({ ...base, ply: 5 })).not.toBe(k);
    expect(announcementKey({ ...base, drawOffer: { by: 1 } })).not.toBe(k);
    expect(announcementKey({ ...base, result: {} })).not.toBe(k);
  });
});
