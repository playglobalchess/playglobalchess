// @vitest-environment jsdom
//
// Automated accessibility check (NFR-8 / spec §6 "automated axe checks on the
// board"). We mount the Board and run axe-core against the rendered DOM,
// asserting zero violations. This complements the hand-written ARIA assertions
// in Board.test.tsx with a real accessibility engine's ruleset (WCAG 2.1 A/AA),
// so a regression in grid semantics, labelling, or roles is caught structurally
// rather than only by a bespoke query.

import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import axe from "axe-core";
import { Board } from "./Board";
import { parseFen } from "../board/position";

let root: Root | null = null;
let container: HTMLDivElement | null = null;

function mount(el: React.JSX.Element): void {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => root!.render(el));
}

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  if (container != null) container.remove();
  container = null;
});

const START = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

/** Run axe against the mounted container; return the violations array. */
async function violations(): Promise<axe.Result[]> {
  const results = await axe.run(container!, {
    // WCAG 2.1 A + AA rule tags — the baseline NFR-8 targets.
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] },
  });
  return results.violations;
}

/** A readable summary so a failure names the rule, not just a count. */
function describeViolations(v: axe.Result[]): string {
  return v.map((r) => `${r.id} (${r.impact}): ${r.help}`).join("\n");
}

describe("Board accessibility (axe-core)", () => {
  it("has no WCAG A/AA violations when interactive (white seat)", async () => {
    mount(<Board position={parseFen(START)} mySeat={0} interactive onMove={() => {}} />);
    const v = await violations();
    expect(v, describeViolations(v)).toEqual([]);
  });

  it("has no WCAG A/AA violations when not interactive", async () => {
    mount(<Board position={parseFen(START)} mySeat={0} interactive={false} onMove={() => {}} />);
    const v = await violations();
    expect(v, describeViolations(v)).toEqual([]);
  });

  it("has no WCAG A/AA violations for a flipped black-seat board", async () => {
    const fen = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1";
    mount(<Board position={parseFen(fen)} mySeat={1} interactive onMove={() => {}} />);
    const v = await violations();
    expect(v, describeViolations(v)).toEqual([]);
  });
});
