// @vitest-environment jsdom
//
// Board component tests — the two-click move interaction and orientation. We
// mount with react-dom/client + act (no extra testing library) and drive real
// click events through the grid cells. The board implements NO rules (NFR-5);
// these tests assert only that a click sequence emits the right UCI string and
// that orientation/selection behave, which is the board's whole contract.

import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
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

/** Click the gridcell whose aria-label starts with the given square name. */
function clickSquare(sq: string): void {
  const cells = Array.from(container!.querySelectorAll('[role="gridcell"]'));
  const cell = cells.find((c) => (c.getAttribute("aria-label") ?? "").startsWith(`${sq},`));
  if (cell == null) throw new Error(`no cell for ${sq}`);
  act(() => {
    cell.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

const START = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

describe("Board", () => {
  it("renders 64 cells", () => {
    const moves: string[] = [];
    mount(
      <Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />,
    );
    expect(container!.querySelectorAll('[role="gridcell"]').length).toBe(64);
  });

  it("emits a UCI move on from→to click (white pawn e2e4)", () => {
    const moves: string[] = [];
    mount(
      <Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />,
    );
    clickSquare("e2"); // select own pawn
    clickSquare("e4"); // destination
    expect(moves).toEqual(["e2e4"]);
  });

  it("does not select an opponent piece as the from-square", () => {
    const moves: string[] = [];
    mount(
      <Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />,
    );
    clickSquare("e7"); // black pawn — not ours
    clickSquare("e5");
    expect(moves).toEqual([]); // no move emitted (first click selected nothing)
  });

  it("a promotion opens the picker and does NOT emit a move until a piece is chosen", () => {
    const moves: string[] = [];
    const fen = "8/P7/8/8/8/8/8/4K2k w - - 0 1"; // white pawn a7, a8 empty
    mount(<Board position={parseFen(fen)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
    clickSquare("a7");
    clickSquare("a8");
    expect(moves).toEqual([]); // nothing emitted yet
    expect(container!.querySelector('[role="dialog"]')).not.toBeNull(); // picker shown
  });

  it("promotion picker emits the chosen piece — queen AND each underpromotion", () => {
    const cases: Array<{ label: string; uci: string }> = [
      { label: "queen", uci: "a7a8q" },
      { label: "rook", uci: "a7a8r" },
      { label: "bishop", uci: "a7a8b" },
      { label: "knight", uci: "a7a8n" },
    ];
    for (const c of cases) {
      const moves: string[] = [];
      const fen = "8/P7/8/8/8/8/8/4K2k w - - 0 1";
      mount(<Board position={parseFen(fen)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
      clickSquare("a7");
      clickSquare("a8");
      // Click the picker option whose aria-label is the piece name.
      const btn = Array.from(container!.querySelectorAll('[role="dialog"] button')).find(
        (b) => (b.getAttribute("aria-label") ?? "") === c.label,
      );
      if (btn == null) throw new Error(`no promotion option for ${c.label}`);
      act(() => btn.dispatchEvent(new MouseEvent("click", { bubbles: true })));
      expect(moves).toEqual([c.uci]);
      act(() => root?.unmount());
      root = null;
      if (container != null) container.remove();
      container = null;
    }
  });

  it("is inert when not interactive", () => {
    const moves: string[] = [];
    mount(
      <Board position={parseFen(START)} mySeat={0} interactive={false} onMove={(u) => moves.push(u)} />,
    );
    clickSquare("e2");
    clickSquare("e4");
    expect(moves).toEqual([]);
  });

  it("renders for a black seat without error and emits black's move", () => {
    const moves: string[] = [];
    // Black to move after 1.e4; black plays e7e5.
    const fen = "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq - 0 1";
    mount(<Board position={parseFen(fen)} mySeat={1} interactive onMove={(u) => moves.push(u)} />);
    clickSquare("e7");
    clickSquare("e5");
    expect(moves).toEqual(["e7e5"]);
  });

  // --- NFR-8 accessibility ---

  it("exposes ARIA grid semantics: 8 rows of 8 gridcells", () => {
    const moves: string[] = [];
    mount(<Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
    expect(container!.querySelectorAll('[role="row"]').length).toBe(8);
    expect(container!.querySelectorAll('[role="gridcell"]').length).toBe(64);
  });

  it("sets aria-selected on the chosen from-square", () => {
    const moves: string[] = [];
    mount(<Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
    clickSquare("e2");
    const sel = Array.from(container!.querySelectorAll('[aria-selected="true"]'));
    expect(sel.length).toBe(1);
    expect((sel[0]!.getAttribute("aria-label") ?? "").startsWith("e2,")).toBe(true);
  });

  it("completes a move by keyboard (focus a1, arrow to the pawn, Enter, arrow, Enter)", () => {
    const moves: string[] = [];
    // Start with the cursor on a1 (white default home). Navigate to e2, select,
    // then to e4 and play — all via keydown on the grid.
    mount(<Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
    const grid = container!.querySelector('[role="grid"]')!;
    const key = (k: string): void => {
      act(() => grid.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
    };
    // a1 → e2: right 4 files, up 1 rank.
    key("ArrowRight"); key("ArrowRight"); key("ArrowRight"); key("ArrowRight");
    key("ArrowUp");
    key("Enter"); // select e2 pawn
    key("ArrowUp"); key("ArrowUp"); // e2 → e4
    key("Enter"); // play
    expect(moves).toEqual(["e2e4"]);
  });

  it("Escape cancels a keyboard selection", () => {
    const moves: string[] = [];
    mount(<Board position={parseFen(START)} mySeat={0} interactive onMove={(u) => moves.push(u)} />);
    const grid = container!.querySelector('[role="grid"]')!;
    const key = (k: string): void => {
      act(() => grid.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
    };
    key("ArrowRight"); key("ArrowRight"); key("ArrowRight"); key("ArrowRight"); key("ArrowUp");
    key("Enter"); // select e2
    expect(container!.querySelectorAll('[aria-selected="true"]').length).toBe(1);
    key("Escape");
    expect(container!.querySelectorAll('[aria-selected="true"]').length).toBe(0);
  });
});
