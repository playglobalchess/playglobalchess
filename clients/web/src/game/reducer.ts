// The pure game reducer — the correctness heart of the client. It is a pure
// function (state, action) -> state with NO side effects (no socket, no DOM, no
// time): the GameSocket feeds it decoded ServerMsg; the UI feeds it local move
// intents; React renders the result. Purity is what makes it exhaustively
// unit-testable (SPEC-S4-WEB-F01 §6).
//
// Server-authoritative (§VIII.1): the client holds NO truth. Every board change
// is driven by an authoritative ServerMsg (MOVE_ACCEPTED carries the uci —
// §4A v1.2 / ADR-18 — so BOTH seats advance; STATE carries the full FEN fold on
// resync). The one exception is the OPTIMISTIC local move, which is immediately
// REVERTED if the server rejects it (§3.3).

import { type Position, parseFen, applyUci } from "../board/position";
import {
  type ServerMsg,
  type ReasonCode,
  type TerminationCode,
  serverTag,
} from "../codec/messages";

export type Phase =
  | "idle" // not connected / not seeking
  | "seeking" // SEEK sent, awaiting MATCHED
  | "playing" // in a live game
  | "game_over";

export interface DrawOffer {
  /** seat index that offered the draw (the opponent, from our view). */
  by: number;
}

export interface GameState {
  phase: Phase;
  gameId: string | null;
  mySeat: number | null;
  opponent: string | null;
  position: Position | null;
  /** Authoritative current ply (server). The next MOVE sends ply_expected = ply + 1. */
  ply: number;
  /** Per-seat remaining clock ms (index = seat). */
  clocks: number[];
  /** A move shown optimistically, awaiting the server's accept/reject. */
  pendingMove: { uci: string; prevPosition: Position; prevPly: number } | null;
  /** A pending draw offer FROM the opponent (DRAW_OFFERED), awaiting our accept/decline. */
  drawOffer: DrawOffer | null;
  /** Set in game_over. */
  result: { termination: TerminationCode; resultVector: number[] } | null;
  /** Last MOVE_REJECTED reason, for the UI to surface; cleared on the next accepted move. */
  lastReject: ReasonCode | null;
}

export const initialState: GameState = {
  phase: "idle",
  gameId: null,
  mySeat: null,
  opponent: null,
  position: null,
  ply: 0,
  clocks: [],
  pendingMove: null,
  drawOffer: null,
  result: null,
  lastReject: null,
};

export type Action =
  | { kind: "seek" } // user clicked Play
  | { kind: "localMove"; uci: string } // user made a board move (optimistic)
  | { kind: "server"; msg: ServerMsg }; // a decoded ServerMsg arrived

/** Pure reducer. */
export function reduce(state: GameState, action: Action): GameState {
  switch (action.kind) {
    case "seek":
      return { ...initialState, phase: "seeking" };

    case "localMove": {
      // Optimistic: show the move now, remember how to revert. Only legal while
      // playing, it's our turn, and no move is already pending.
      if (state.phase !== "playing" || state.position == null || state.pendingMove != null) {
        return state;
      }
      const prevPosition = state.position;
      const prevPly = state.ply;
      let optimistic: Position;
      try {
        optimistic = applyUci(prevPosition, action.uci);
      } catch {
        // A malformed local move string — ignore it (the board UI should not
        // have produced it; never crash the reducer).
        return state;
      }
      return {
        ...state,
        position: optimistic,
        pendingMove: { uci: action.uci, prevPosition, prevPly },
        lastReject: null,
      };
    }

    case "server":
      return reduceServer(state, action.msg);
  }
}

function reduceServer(state: GameState, msg: ServerMsg): GameState {
  const tag = serverTag(msg);
  switch (tag) {
    case "Matched": {
      const m = (msg as { Matched: { game_id: string; seat_idx: number; opponent: string; start_state: { fen: string; turn: number; clocks: number[] } } }).Matched;
      return {
        ...initialState,
        phase: "playing",
        gameId: m.game_id,
        mySeat: m.seat_idx,
        opponent: m.opponent,
        position: parseFen(m.start_state.fen),
        ply: 0,
        clocks: m.start_state.clocks.slice(),
      };
    }

    case "MoveAccepted": {
      const a = (msg as { MoveAccepted: { game_id: string; ply: number; uci: string; clock_after_ms: number } }).MoveAccepted;
      // §4A v1.2: carries the authoritative uci and is sent to BOTH seats.
      if (state.position == null) return state;
      // If this confirms our own pending optimistic move, just clear it; the
      // board already shows the move. Otherwise (opponent's move, or our move
      // when we did not pre-apply) apply the authoritative uci now.
      const confirmsPending =
        state.pendingMove != null && state.pendingMove.uci === a.uci;
      let position = state.position;
      if (!confirmsPending) {
        try {
          position = applyUci(state.position, a.uci);
        } catch {
          // Should not happen for a server-accepted move; keep prior board.
          position = state.position;
        }
      }
      // The server echoes the ply AFTER the move and the MOVER's remaining
      // clock. The mover is the side that just moved = the opposite of the new
      // side-to-move. We derive the mover seat from the (now-advanced) position
      // turn and write clock_after_ms to that seat. STATE remains the exact
      // full-vector authority on resync; per move we update only the mover's.
      const clocks = state.clocks.slice();
      // position.turn is the side to move AFTER the move; the mover is the other.
      const moverIsWhite = position.turn === "b"; // if it's black's turn now, white just moved
      // Seat↔color: seat 0 = white, seat 1 = black (§4A.8 v1 2-seat).
      const moverSeat = moverIsWhite ? 0 : 1;
      if (moverSeat < clocks.length) {
        clocks[moverSeat] = a.clock_after_ms;
      }
      return {
        ...state,
        position,
        ply: a.ply,
        clocks,
        pendingMove: null,
        lastReject: null,
      };
    }

    case "MoveRejected": {
      const r = (msg as { MoveRejected: { game_id: string; ply_expected: number; reason: ReasonCode } }).MoveRejected;
      // Revert the optimistic move if one is pending.
      if (state.pendingMove != null) {
        return {
          ...state,
          position: state.pendingMove.prevPosition,
          ply: state.pendingMove.prevPly,
          pendingMove: null,
          lastReject: r.reason,
        };
      }
      return { ...state, lastReject: r.reason };
    }

    case "State": {
      // Authoritative resync: rebuild the board from the FEN fold. Drops any
      // optimistic/pending state — the server's STATE is the truth.
      const s = (msg as { State: { game_id: string; state: { fen: string; turn: number; clocks: number[] } } }).State;
      return {
        ...state,
        phase: state.phase === "game_over" ? "game_over" : "playing",
        gameId: s.game_id,
        position: parseFen(s.state.fen),
        clocks: s.state.clocks.slice(),
        pendingMove: null,
      };
    }

    case "DrawOffered": {
      const d = (msg as { DrawOffered: { game_id: string; by: number } }).DrawOffered;
      return { ...state, drawOffer: { by: d.by } };
    }

    case "GameEnd": {
      const g = (msg as { GameEnd: { game_id: string; result_vector: number[]; termination: TerminationCode; rating_delta: number[] } }).GameEnd;
      return {
        ...state,
        phase: "game_over",
        pendingMove: null,
        drawOffer: null,
        result: { termination: g.termination, resultVector: g.result_vector.slice() },
      };
    }

    case "Pong":
      // Clock-sync pong; no game-state change in this slice (latency display later).
      return state;

    case "Error":
      // Protocol/transport error; surfaced by the socket layer, no reducer state
      // change here (UNSUPPORTED_VERSION closes the socket in the GameSocket).
      return state;

    default:
      // Exhaustiveness: a new ServerMsg variant must be handled explicitly.
      return state;
  }
}
