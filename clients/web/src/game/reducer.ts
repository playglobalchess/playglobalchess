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

import { type Position, parseFen, applyUci, plyFromFen } from "../board/position";
import {
  type ServerMsg,
  type ReasonCode,
  type TerminationCode,
  type ErrorCodeValue,
  Reason,
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
  /** Set when the reducer detects a ply gap (a MOVE_ACCEPTED skipped ahead): the
   * GameSocket reads this and sends RESYNC to pull a fresh STATE. Cleared by STATE. */
  needsResync: boolean;
  /** A protocol/transport Error the UI must surface (e.g. UNSUPPORTED_VERSION →
   * "please refresh"). null when there is no active error. */
  error: ErrorCodeValue | null;
  /** The most recent applied move (for screen-reader announcements). uci is the
   * move, moverSeat the seat that made it. null before any move / after a fold. */
  lastMove: { uci: string; moverSeat: number } | null;
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
  needsResync: false,
  error: null,
  lastMove: null,
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
      // Defense in depth: only apply optimistically on OUR turn. seat 0 = white,
      // seat 1 = black; position.turn is the side to move. The board UI should
      // not offer a move off-turn, but a stray action must not optimistically
      // apply (it would only earn a NOT_YOUR_TURN reject).
      const myTurn =
        (state.mySeat === 0 && state.position.turn === "w") ||
        (state.mySeat === 1 && state.position.turn === "b");
      if (!myTurn) {
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
        ply: plyFromFen(m.start_state.fen),
        clocks: m.start_state.clocks.slice(),
      };
    }

    case "MoveAccepted": {
      const a = (msg as { MoveAccepted: { game_id: string; ply: number; uci: string; clock_after_ms: number } }).MoveAccepted;
      // §4A v1.2: carries the authoritative uci and is sent to BOTH seats.
      if (state.position == null) return state;

      // BASE = the board BEFORE any optimistic move. We always reconcile against
      // the server from this base, never on top of an un-confirmed optimistic
      // move — so an accept whose uci differs from our pending move (a race, a
      // stale frame, the opponent's move arriving while ours is pending) can
      // never leave the optimistic move AND the server move both on the board.
      const base = state.pendingMove != null ? state.pendingMove.prevPosition : state.position;
      const basePly = state.pendingMove != null ? state.pendingMove.prevPly : state.ply;

      // Ply-contiguity / idempotency guard. a.ply is the ply AFTER the move, so
      // a fresh move advances basePly by exactly one. Anything else is a
      // duplicate (replay / at-least-once delivery) or an out-of-order/gap frame
      // — do NOT apply it a second time (that corrupts the board) and do NOT
      // jump the ply. A gap flips needsResync so the socket can request STATE.
      if (a.ply !== basePly + 1) {
        // Clear a now-moot pending move (it was superseded or already applied),
        // and ask for a resync when we are behind (a gap), not on a stale replay.
        return {
          ...state,
          pendingMove: null,
          needsResync: a.ply > basePly + 1 ? true : state.needsResync,
        };
      }

      // Apply exactly ONE authoritative move onto the base position.
      let position: Position;
      try {
        position = applyUci(base, a.uci);
      } catch {
        // A server-accepted move that we cannot apply mechanically should never
        // happen; ask for a resync rather than show a wrong board.
        return { ...state, pendingMove: null, needsResync: true };
      }

      // Mover seat from PLY PARITY (race-proof, independent of board state):
      // ply 1 = white's first move, so odd ply ⇒ white (seat 0) just moved.
      // Seat↔color: seat 0 = white, seat 1 = black (§4A.8 v1 2-seat).
      const clocks = state.clocks.slice();
      const moverSeat = a.ply % 2 === 1 ? 0 : 1;
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
        lastMove: { uci: a.uci, moverSeat },
      };
    }

    case "MoveRejected": {
      const r = (msg as { MoveRejected: { game_id: string; ply_expected: number; reason: ReasonCode } }).MoveRejected;
      // A StalePly reject is precisely the signal that our ply is behind the
      // server — raise needsResync so the hook pulls a fresh STATE (which
      // restores ply). Without this, a reject with no pending move leaves us
      // wedged at a stale ply with no recovery trigger.
      const stale = r.reason === Reason.StalePly;
      // Revert the optimistic move if one is pending.
      if (state.pendingMove != null) {
        return {
          ...state,
          position: state.pendingMove.prevPosition,
          ply: state.pendingMove.prevPly,
          pendingMove: null,
          lastReject: r.reason,
          needsResync: stale ? true : state.needsResync,
        };
      }
      return { ...state, lastReject: r.reason, needsResync: stale ? true : state.needsResync };
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
        // CRITICAL: restore ply from the FEN. StateBody carries no ply, but ply
        // is exactly what a gap desynchronized — if we leave it stale, the next
        // MOVE sends ply_expected = stale+1 and the server rejects it StalePly
        // forever (the resync would "succeed" visually yet wedge the player).
        ply: plyFromFen(s.state.fen),
        clocks: s.state.clocks.slice(),
        pendingMove: null,
        needsResync: false,
        // A fresh authoritative STATE means the connection is healthy again.
        lastReject: null,
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
        needsResync: false,
        lastReject: null,
        result: { termination: g.termination, resultVector: g.result_vector.slice() },
      };
    }

    case "Pong":
      // Clock-sync pong; no game-state change in this slice (latency display later).
      return state;

    case "Error": {
      // Surface the error to the UI (e.g. UNSUPPORTED_VERSION → "please
      // refresh"). The GameSocket also closes the socket on UNSUPPORTED_VERSION;
      // here we record the code so GameView can render the right message rather
      // than only a generic "disconnected" status.
      const e = (msg as { Error: { code: ErrorCodeValue; msg: string } }).Error;
      return { ...state, error: e.code };
    }

    default:
      // Exhaustiveness: a new ServerMsg variant must be handled explicitly.
      return state;
  }
}
