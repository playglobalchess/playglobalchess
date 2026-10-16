// Board — pure presentation of a Position plus move interaction by BOTH mouse
// and keyboard (NFR-8). It renders an ARIA grid (grid → row → gridcell), pieces
// (Unicode glyphs with text alternatives), and file/rank coordinates, oriented
// so the player's own seat is at the bottom. It implements NO chess rules
// (NFR-5): selecting a from-square then a to-square emits a UCI string to
// onMove; legality is the server's job (a bad move earns a reject the reducer
// reverts). Promotion defaults to queen in this slice (piece-selection UI
// deferred, spec §3.5 note).
//
// Accessibility (NFR-8):
//  - grid semantics: role=grid > 8× role=row > role=gridcell, each cell
//    aria-label'd with its square AND a human-readable piece name (not the FEN
//    char), aria-selected on the chosen from-square.
//  - keyboard move entry: a roving-tabindex focus cursor; Arrow keys move it in
//    VISUAL directions (orientation-aware), Enter/Space selects the focused own
//    piece then plays to the next focused square, Escape cancels a selection.
//    Home/End jump to the row edges. Exactly one cell is tabbable at a time.

import { useRef, useState } from "react";
import type { Position } from "../board/position";
import { indexToSquare } from "../board/position";
import { t } from "../i18n";

export interface BoardProps {
  position: Position;
  /** The player's seat: 0 = white (default orientation), 1 = black (flipped). */
  mySeat: number | null;
  /** True when it is the player's turn and moves may be initiated. */
  interactive: boolean;
  /** Emits a UCI move string (e.g. "e2e4", "e7e8q") when the player completes one. */
  onMove: (uci: string) => void;
}

// FEN piece char → Unicode chess glyph. Uppercase = white, lowercase = black.
const GLYPH: Record<string, string> = {
  K: "\u2654", Q: "\u2655", R: "\u2656", B: "\u2657", N: "\u2658", P: "\u2659",
  k: "\u265A", q: "\u265B", r: "\u265C", b: "\u265D", n: "\u265E", p: "\u265F",
};

// FEN piece char → i18n key for the human-readable piece name (for SR labels).
const PIECE_NAME_KEY: Record<string, string> = {
  k: "piece.king", q: "piece.queen", r: "piece.rook",
  b: "piece.bishop", n: "piece.knight", p: "piece.pawn",
};

const FILES = "abcdefgh";

/** Is this piece char the player's own? seat 0 = uppercase (white), 1 = lower. */
function isOwnPiece(piece: string, mySeat: number | null): boolean {
  if (mySeat === 0) return piece === piece.toUpperCase();
  if (mySeat === 1) return piece === piece.toLowerCase();
  return false;
}

/** A promotion is a pawn reaching the last rank (rank 8 for white, 1 for black). */
function isPromotion(piece: string, toSquare: string): boolean {
  const rank = toSquare[1];
  return (piece === "P" && rank === "8") || (piece === "p" && rank === "1");
}

/** A human-readable label for a square + its occupant (for the gridcell aria-label). */
function squareLabel(idx: number, piece: string | null): string {
  const sq = indexToSquare(idx);
  if (piece == null) return `${sq}, ${t("board.empty")}`;
  const colorKey = piece === piece.toUpperCase() ? "piece.white" : "piece.black";
  const nameKey = PIECE_NAME_KEY[piece.toLowerCase()];
  const name = nameKey != null ? t(nameKey as Parameters<typeof t>[0]) : piece;
  return `${sq}, ${t(colorKey)} ${name}`;
}

export function Board(props: BoardProps): React.JSX.Element {
  const { position, mySeat, interactive, onMove } = props;
  const [selected, setSelected] = useState<number | null>(null);
  // Roving focus cursor (a board index). Starts at a sensible home square.
  const [cursor, setCursor] = useState<number>(() => (mySeat === 1 ? 0 : 56)); // a1-ish for white
  const gridRef = useRef<HTMLDivElement | null>(null);

  // Visual order: white sees a8..h1 (index 0..63); black sees it flipped.
  const flipped = mySeat === 1;
  const indices = Array.from({ length: 64 }, (_, i) => i);
  const ordered = flipped ? indices.slice().reverse() : indices;

  /** Commit a move from `fromIdx` to `toIdx` (mechanical UCI, default-queen promo). */
  function commitMove(fromIdx: number, toIdx: number): void {
    const from = indexToSquare(fromIdx);
    const to = indexToSquare(toIdx);
    const movingPiece = position.board[fromIdx];
    let uci = `${from}${to}`;
    if (movingPiece != null && isPromotion(movingPiece, to)) uci += "q";
    setSelected(null);
    onMove(uci);
  }

  /** Shared select/move decision for a target square (used by click AND keyboard). */
  function activate(idx: number): void {
    if (!interactive) return;
    const piece = position.board[idx];
    if (selected == null) {
      if (piece != null && isOwnPiece(piece, mySeat)) setSelected(idx);
      return;
    }
    if (idx === selected) {
      setSelected(null);
      return;
    }
    if (piece != null && isOwnPiece(piece, mySeat)) {
      setSelected(idx); // re-select another own piece
      return;
    }
    commitMove(selected, idx);
  }

  /** Move the focus cursor by a VISUAL delta (dx, dy), orientation-aware. */
  function moveCursor(dx: number, dy: number): void {
    // Convert the current index to visual row/col (what the user sees), step,
    // clamp, convert back to a board index. For white: visual row 0 = index
    // rows 0..7 (a8 top). For black (flipped): visual row r maps to 7 - r.
    const row = Math.floor(cursor / 8);
    const col = cursor % 8;
    const visRow = flipped ? 7 - row : row;
    const visCol = flipped ? 7 - col : col;
    const nextVisRow = Math.min(7, Math.max(0, visRow + dy));
    const nextVisCol = Math.min(7, Math.max(0, visCol + dx));
    const nextRow = flipped ? 7 - nextVisRow : nextVisRow;
    const nextCol = flipped ? 7 - nextVisCol : nextVisCol;
    const next = nextRow * 8 + nextCol;
    setCursor(next);
    // Move DOM focus to the newly-focused cell (roving tabindex).
    const cell = gridRef.current?.querySelector<HTMLElement>(`[data-idx="${next}"]`);
    cell?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent): void {
    switch (e.key) {
      case "ArrowUp": e.preventDefault(); moveCursor(0, -1); break;
      case "ArrowDown": e.preventDefault(); moveCursor(0, 1); break;
      case "ArrowLeft": e.preventDefault(); moveCursor(-1, 0); break;
      case "ArrowRight": e.preventDefault(); moveCursor(1, 0); break;
      case "Home": e.preventDefault(); moveCursor(-8, 0); break; // clamps to left edge
      case "End": e.preventDefault(); moveCursor(8, 0); break; // clamps to right edge
      case "Enter":
      case " ":
        e.preventDefault();
        activate(cursor);
        break;
      case "Escape":
        e.preventDefault();
        setSelected(null);
        break;
      default:
        break;
    }
  }

  // Build the 8 rows of 8 cells in visual order.
  const rows: number[][] = [];
  for (let r = 0; r < 8; r++) rows.push(ordered.slice(r * 8, r * 8 + 8));

  return (
    <div
      ref={gridRef}
      role="grid"
      aria-label={t("board.aria")}
      onKeyDown={onKeyDown}
      style={{
        display: "grid",
        gridTemplateRows: "repeat(8, 1fr)",
        width: "min(90vmin, 560px)",
        aspectRatio: "1 / 1",
        border: "2px solid var(--board-border, #333)",
      }}
    >
      {rows.map((rowIdxs, rowNo) => (
        <div
          key={rowNo}
          role="row"
          style={{ display: "grid", gridTemplateColumns: "repeat(8, 1fr)" }}
        >
          {rowIdxs.map((idx) => {
            const sq = indexToSquare(idx);
            const file = sq[0] ?? "";
            const rank = sq[1] ?? "";
            const piece = position.board[idx] ?? null;
            const isDark = (Math.floor(idx / 8) + (idx % 8)) % 2 === 1;
            const isSel = selected === idx;
            const isCursor = cursor === idx;
            const bg = isSel
              ? "var(--board-sel, #b9c66a)"
              : isDark
                ? "var(--board-dark, #7d8a99)"
                : "var(--board-light, #d8dee6)";
            return (
              <div
                key={idx}
                role="gridcell"
                data-idx={idx}
                aria-label={squareLabel(idx, piece)}
                aria-selected={isSel}
                tabIndex={interactive && isCursor ? 0 : -1}
                onClick={() => activate(idx)}
                onFocus={() => setCursor(idx)}
                style={{
                  position: "relative",
                  background: bg,
                  color: "var(--board-piece, #111)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: "clamp(18px, 7vmin, 44px)",
                  cursor: interactive ? "pointer" : "default",
                  userSelect: "none",
                  outline: isCursor && interactive ? "3px solid var(--board-focus, #3b82f6)" : "none",
                  outlineOffset: "-3px",
                }}
              >
                {piece != null ? <span aria-hidden="true">{GLYPH[piece] ?? piece}</span> : null}
                {FILES.indexOf(file) === 0 ? (
                  <span style={coordStyle("left")} aria-hidden="true">{rank}</span>
                ) : null}
                {rank === "1" ? (
                  <span style={coordStyle("bottom")} aria-hidden="true">{file}</span>
                ) : null}
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

function coordStyle(side: "left" | "bottom"): React.CSSProperties {
  const base: React.CSSProperties = {
    position: "absolute",
    fontSize: "10px",
    opacity: 0.6,
    color: "var(--board-coord, #222)",
  };
  return side === "left"
    ? { ...base, top: 1, left: 2 }
    : { ...base, bottom: 1, right: 2 };
}
