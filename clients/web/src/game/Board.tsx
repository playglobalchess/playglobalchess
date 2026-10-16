// Board — pure presentation of a Position plus a two-click move interaction.
// It renders the 8×8 grid, pieces (Unicode glyphs), and file/rank coordinates,
// oriented so the player's own seat is at the bottom. It implements NO chess
// rules (NFR-5): a click selects a from-square, a second click emits a UCI
// string to onMove; legality is the server's job (a bad move earns a reject,
// which the reducer reverts). Promotion defaults to queen in this slice
// (promotion-piece selection UI is a later polish item, noted in the spec).

import { useState } from "react";
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

export function Board(props: BoardProps): React.JSX.Element {
  const { position, mySeat, interactive, onMove } = props;
  const [selected, setSelected] = useState<number | null>(null);

  // Draw order: white (seat 0 / spectator) sees a8..h1 as given (index 0..63);
  // black (seat 1) sees it flipped so their pieces are at the bottom.
  const indices = Array.from({ length: 64 }, (_, i) => i);
  const ordered = mySeat === 1 ? indices.slice().reverse() : indices;

  function onSquareClick(idx: number): void {
    if (!interactive) return;
    const piece = position.board[idx];
    if (selected == null) {
      // First click: select only an own piece.
      if (piece != null && isOwnPiece(piece, mySeat)) setSelected(idx);
      return;
    }
    if (idx === selected) {
      setSelected(null); // click the same square to deselect
      return;
    }
    // Second click on another own piece re-selects it (convenience).
    if (piece != null && isOwnPiece(piece, mySeat)) {
      setSelected(idx);
      return;
    }
    // Otherwise: complete the move from `selected` to `idx`.
    const from = indexToSquare(selected);
    const to = indexToSquare(idx);
    const movingPiece = position.board[selected];
    let uci = `${from}${to}`;
    if (movingPiece != null && isPromotion(movingPiece, to)) {
      uci += "q"; // default-queen; selection UI deferred (spec §5 note)
    }
    setSelected(null);
    onMove(uci);
  }

  return (
    <div
      role="grid"
      aria-label={t("board.aria")}
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(8, 1fr)",
        width: "min(90vmin, 560px)",
        aspectRatio: "1 / 1",
        border: "2px solid var(--board-border, #333)",
      }}
    >
      {ordered.map((idx) => {
        const sq = indexToSquare(idx);
        const file = sq[0] ?? "";
        const rank = sq[1] ?? "";
        const piece = position.board[idx];
        const isDark = (Math.floor(idx / 8) + (idx % 8)) % 2 === 1;
        const isSel = selected === idx;
        // Light/dark squares use theme-driven CSS variables with safe fallbacks.
        const bg = isSel
          ? "var(--board-sel, #b9c66a)"
          : isDark
            ? "var(--board-dark, #7d8a99)"
            : "var(--board-light, #d8dee6)";
        return (
          <div
            key={idx}
            role="gridcell"
            aria-label={`${t("board.square.aria")} ${sq}${piece != null ? ` ${piece}` : ""}`}
            onClick={() => onSquareClick(idx)}
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
            }}
          >
            {piece != null ? <span aria-hidden="true">{GLYPH[piece] ?? piece}</span> : null}
            {/* file labels on the bottom rank, rank labels on the left file */}
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
