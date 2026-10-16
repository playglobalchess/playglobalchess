// Board position model for RENDERING ONLY.
//
// NFR-5 boundary (read this before editing): this module does NOT implement
// chess rules. It parses a FEN into a square array and APPLIES an authoritative
// UCI move mechanically — it moves the piece the server already validated from
// one square to another, handles the mechanical side effects of a move the
// server accepted (promotion piece swap, en-passant capture, castling rook
// shift), and nothing else. It never decides legality, never generates moves,
// never detects check/mate. All of that is the server + the Rust engine. The
// client only needs to DRAW the position that results from a move the server
// has already accepted. If you find yourself adding "is this legal" logic here,
// stop — that belongs to the engine (NFR-5: one rules engine).

export type Piece = string; // FEN piece char: PNBRQK / pnbrqk
export type Square = Piece | null;
/** 64 squares, index 0 = a8 … 63 = h1 (FEN rank order, a8 first). */
export type Board = Square[];

export interface Position {
  board: Board;
  /** 'w' | 'b' — side to move, from the FEN. */
  turn: "w" | "b";
  /** The raw FEN this position was parsed from (kept for resync/debug). */
  fen: string;
}

const FILES = "abcdefgh";

/** Square name (e.g. "e4") → board index (0 = a8 … 63 = h1). */
export function squareToIndex(sq: string): number {
  const file = FILES.indexOf(sq[0] ?? "");
  const rank = Number(sq[1]);
  if (file < 0 || rank < 1 || rank > 8) {
    throw new Error(`invalid square: ${sq}`);
  }
  return (8 - rank) * 8 + file;
}

/** Board index → square name. */
export function indexToSquare(i: number): string {
  const file = i % 8;
  const rank = 8 - Math.floor(i / 8);
  return `${FILES[file]}${rank}`;
}

/** Parse the piece-placement + side-to-move fields of a FEN into a Position. */
export function parseFen(fen: string): Position {
  const parts = fen.split(/\s+/);
  const placement = parts[0] ?? "";
  const turnField = parts[1] ?? "w";
  const board: Board = new Array<Square>(64).fill(null);
  let i = 0;
  for (const ch of placement) {
    if (ch === "/") continue;
    if (ch >= "1" && ch <= "8") {
      i += Number(ch);
    } else {
      if (i >= 64) throw new Error(`FEN overflows the board: ${fen}`);
      board[i] = ch;
      i += 1;
    }
  }
  if (i !== 64) throw new Error(`FEN does not fill the board (${i}/64): ${fen}`);
  return { board, turn: turnField === "b" ? "b" : "w", fen };
}

/**
 * Derive the authoritative half-move ply from a FEN. The server's STATE body
 * carries only {fen, turn, clocks} — NO ply — yet ply is exactly what a resync
 * must restore (SPEC-S4-WEB-F01 §3.4 / the needsResync seam). A FEN's 6th field
 * is the full-move number (1-based, increments after Black's move); combined
 * with the side to move this fixes the half-move count:
 *
 *   ply = (fullmove - 1) * 2 + (black to move ? 1 : 0)
 *
 * This matches the server's ply convention by construction: startpos (fullmove
 * 1, White) ⇒ ply 0; after 1.e4 (fullmove 1, Black) ⇒ ply 1; after 1...e5
 * (fullmove 2, White) ⇒ ply 2. A missing/garbled move-number field falls back
 * to 1 (ply derived from side-to-move only) rather than throwing — a resync
 * should never crash the client.
 */
export function plyFromFen(fen: string): number {
  const parts = fen.split(/\s+/);
  const turnField = parts[1] ?? "w";
  const fullmoveRaw = Number(parts[5]);
  const fullmove = Number.isFinite(fullmoveRaw) && fullmoveRaw >= 1 ? Math.floor(fullmoveRaw) : 1;
  return (fullmove - 1) * 2 + (turnField === "b" ? 1 : 0);
}

/**
 * Apply an authoritative UCI move to a position MECHANICALLY, returning a NEW
 * position. The move is assumed already legal (the server validated it); this
 * only relocates pieces so the board can be drawn. Handles the mechanical side
 * effects of an accepted move: promotion (5th char), en-passant capture (pawn
 * diagonal to an empty square), and castling (king two files → shift the rook).
 * It does NOT validate anything.
 */
export function applyUci(pos: Position, uci: string): Position {
  const from = squareToIndex(uci.slice(0, 2));
  const to = squareToIndex(uci.slice(2, 4));
  const promo = uci.length >= 5 ? uci[4] : null;
  const board = pos.board.slice();
  const piece = board[from];
  if (piece == null) throw new Error(`no piece on ${uci.slice(0, 2)} to move`);

  const isPawn = piece === "P" || piece === "p";
  const fromFile = from % 8;
  const toFile = to % 8;

  // En-passant: a pawn moving diagonally to an EMPTY square captures the pawn
  // that sits on the mover's own rank, in the destination file.
  if (isPawn && fromFile !== toFile && board[to] == null) {
    const capturedIdx = from - fromFile + toFile; // same rank as 'from', file of 'to'
    board[capturedIdx] = null;
  }

  // Move the piece (promotion swaps the piece type, preserving color case).
  if (promo != null) {
    board[to] = piece === "P" ? promo.toUpperCase() : promo.toLowerCase();
  } else {
    board[to] = piece;
  }
  board[from] = null;

  // Castling: king moves two files → relocate the rook. (King is on its home
  // rank; the server already validated the castle, we only move the rook.)
  if ((piece === "K" || piece === "k") && Math.abs(toFile - fromFile) === 2) {
    const rank = to - toFile; // index of the a-file square on this rank
    if (toFile === 6) {
      // king-side: rook h→f
      board[rank + 5] = board[rank + 7] ?? null;
      board[rank + 7] = null;
    } else if (toFile === 2) {
      // queen-side: rook a→d
      board[rank + 3] = board[rank + 0] ?? null;
      board[rank + 0] = null;
    }
  }

  return { board, turn: pos.turn === "w" ? "b" : "w", fen: "" }; // fen recomputed only on STATE
}
