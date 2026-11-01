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

import { type Position, parseFen, applyUci, plyFromFen, hasValidFullmove } from "../board/position";
import { SYNC_MAX_ATTEMPTS, SYNC_MAX_CYCLES } from "./recovery";
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

/** Recovery phase (SPEC-S4-WEB-F01 §3.4.1 table S). */
export type SyncPhase = "IN_SYNC" | "SYNCING" | "RECONNECTING" | "FAILED";

/** Which detection trigger started the current recovery episode (§3.4.1 T1-T5). */
export type SyncTrigger = "T1_GAP" | "T2_MISMATCH" | "T3_UNAPPLIABLE" | "T4_STALE" | "T5_INCONSISTENT";

/** Recovery state. OWNER: the reducer holds and transitions it (pure); useGame
 * reads it to drive every timer and every send. */
export interface SyncState {
  phase: SyncPhase;
  /** SYNCING attempt n, 1..N (0 while IN_SYNC). */
  attempt: number;
  /** RECONNECTING cycle k, 1..K (0 outside RECONNECTING/FAILED). */
  cycle: number;
  /** Raise sequence: bumps on every episode start and every new attempt/cycle.
   * useGame keys its once-per-attempt RESYNC guard on (socket, gameId, seq). */
  seq: number;
  /** "await_reply": the current attempt waits for a STATE (useGame arms T_reply
   * while connected). "backoff": the attempt failed; useGame waits B*2^(n-1)
   * and then dispatches syncBackoffElapsed. */
  step: "await_reply" | "backoff";
  /** The trigger that started the episode (kept in FAILED for the error log). */
  trigger: SyncTrigger | null;
}

export const IN_SYNC: SyncState = { phase: "IN_SYNC", attempt: 0, cycle: 0, seq: 0, step: "await_reply", trigger: null };

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
  /** Consistency/recovery state (§3.4.1). The reducer detects and transitions;
   * useGame owns the timers and the RESYNC/reconnect sends. */
  sync: SyncState;
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
  sync: IN_SYNC,
  error: null,
  lastMove: null,
};

export type Action =
  | { kind: "seek" } // user clicked Play
  | { kind: "localMove"; uci: string } // user made a board move (optimistic)
  | { kind: "server"; msg: ServerMsg } // a decoded ServerMsg arrived
  // Timer-driven recovery actions. useGame owns the timers; the reducer only
  // transitions. Each carries the raise seq it was armed for so a stale timer
  // (one that raced a STATE or a newer attempt) is a no-op.
  | { kind: "syncReplyTimeout"; seq: number } // T_reply elapsed with no STATE
  | { kind: "syncBackoffElapsed"; seq: number }; // backoff after a failed attempt elapsed

/** A server FEN frame is trustworthy only if its side-to-move agrees with the
 * wire turn (u8, 0 = white) and its move-number is well-formed (T5). */
function frameConsistent(fen: string, wireTurn: number, position: Position): boolean {
  return position.turn === (wireTurn === 0 ? "w" : "b") && hasValidFullmove(fen);
}

/** Start a recovery episode (T1-T5). Only for a live game, only from IN_SYNC:
 * a trigger during SYNCING/RECONNECTING is coalesced (recovery is already in
 * progress) and FAILED is terminal. */
function raise(state: GameState, trigger: SyncTrigger): GameState {
  if (state.phase !== "playing" || state.sync.phase !== "IN_SYNC") return state;
  return {
    ...state,
    sync: { phase: "SYNCING", attempt: 1, cycle: 0, seq: state.sync.seq + 1, step: "await_reply", trigger },
  };
}

/** The current attempt/cycle got no consistent STATE (timeout or inconsistent
 * reply). SYNCING(n<N) -> backoff; SYNCING(N) -> RECONNECTING(1);
 * RECONNECTING(k<K) -> RECONNECTING(k+1); RECONNECTING(K) -> FAILED. */
function failAttempt(sync: SyncState): SyncState {
  if (sync.phase === "SYNCING") {
    if (sync.attempt < SYNC_MAX_ATTEMPTS) return { ...sync, step: "backoff" };
    return { ...sync, phase: "RECONNECTING", cycle: 1, seq: sync.seq + 1, step: "await_reply" };
  }
  if (sync.phase === "RECONNECTING") {
    if (sync.cycle < SYNC_MAX_CYCLES) return { ...sync, cycle: sync.cycle + 1, seq: sync.seq + 1, step: "await_reply" };
    return { ...sync, phase: "FAILED" };
  }
  return sync;
}

function recovering(sync: SyncState): boolean {
  return sync.phase === "SYNCING" || sync.phase === "RECONNECTING";
}

/** Pure reducer. */
export function reduce(state: GameState, action: Action): GameState {
  switch (action.kind) {
    case "seek":
      return { ...initialState, sync: { ...IN_SYNC, seq: state.sync.seq }, phase: "seeking" };

    case "localMove": {
      // Optimistic: show the move now, remember how to revert. Only legal while
      // playing, it's our turn, and no move is already pending.
      // Also read-only while recovering (§3.4.1): the board is not trustworthy.
      if (
        state.phase !== "playing" ||
        state.position == null ||
        state.pendingMove != null ||
        state.sync.phase !== "IN_SYNC"
      ) {
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

    case "syncReplyTimeout":
      if (!recovering(state.sync) || state.sync.step !== "await_reply" || action.seq !== state.sync.seq) return state;
      return { ...state, sync: failAttempt(state.sync) };

    case "syncBackoffElapsed":
      if (state.sync.phase !== "SYNCING" || state.sync.step !== "backoff" || action.seq !== state.sync.seq) return state;
      return {
        ...state,
        sync: { ...state.sync, attempt: state.sync.attempt + 1, seq: state.sync.seq + 1, step: "await_reply" },
      };
  }
}

function reduceServer(state: GameState, msg: ServerMsg): GameState {
  const tag = serverTag(msg);
  switch (tag) {
    case "Matched": {
      const m = (msg as { Matched: { game_id: string; seat_idx: number; opponent: string; start_state: { fen: string; turn: number; clocks: number[] } } }).Matched;
      const startPos = parseFen(m.start_state.fen);
      // §3.4.1: MATCHED for a different game resets recovery to IN_SYNC; T5 then
      // applies to its start_state. (Same game id: recovery state is kept.)
      const sync = m.game_id === state.gameId ? state.sync : { ...IN_SYNC, seq: state.sync.seq };
      const next: GameState = {
        ...initialState,
        sync,
        phase: "playing",
        gameId: m.game_id,
        mySeat: m.seat_idx,
        opponent: m.opponent,
        position: startPos,
        ply: plyFromFen(m.start_state.fen),
        clocks: m.start_state.clocks.slice(),
      };
      return frameConsistent(m.start_state.fen, m.start_state.turn, startPos) ? next : raise(next, "T5_INCONSISTENT");
    }

    case "MoveAccepted": {
      const a = (msg as { MoveAccepted: { game_id: string; ply: number; uci: string; clock_after_ms: number } }).MoveAccepted;
      // §4A v1.2: carries the authoritative uci and is sent to BOTH seats.
      if (state.position == null) return state;

      // §3.4.1: while SYNCING/RECONNECTING (or FAILED) a MOVE_ACCEPTED that
      // arrives before the awaited STATE is not applied: the stream is ordered
      // and that STATE already folds it. Frames after the STATE apply normally.
      if (state.sync.phase !== "IN_SYNC") return state;

      // BASE = the board BEFORE any optimistic move. We always reconcile against
      // the server from this base, never on top of an un-confirmed optimistic
      // move — so an accept whose uci differs from our pending move (a race, a
      // stale frame, the opponent's move arriving while ours is pending) can
      // never leave the optimistic move AND the server move both on the board.
      const base = state.pendingMove != null ? state.pendingMove.prevPosition : state.position;
      const basePly = state.pendingMove != null ? state.pendingMove.prevPly : state.ply;

      // T1 gap: the stream skipped ahead.
      if (a.ply > basePly + 1) return raise(state, "T1_GAP");
      // T2 mismatch: at or behind local. The ONE exact match (same ply AND the
      // last applied move) is a duplicate and is ignored silently.
      if (a.ply <= basePly) {
        const duplicate = a.ply === basePly && state.lastMove != null && state.lastMove.uci === a.uci;
        return duplicate ? state : raise(state, "T2_MISMATCH");
      }

      // Apply exactly ONE authoritative move onto the base position.
      let position: Position;
      try {
        position = applyUci(base, a.uci);
      } catch {
        // T3 unappliable: a server-accepted move the binding cannot apply.
        return raise(state, "T3_UNAPPLIABLE");
      }

      // Mover seat from PLY PARITY (race-proof, independent of board state):
      // ply 1 = white's first move, so odd ply => white (seat 0) just moved.
      // Seat/color: seat 0 = white, seat 1 = black (§4A.8 v1 2-seat).
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
      // Revert the optimistic move if one is pending.
      const reverted: GameState =
        state.pendingMove != null
          ? {
              ...state,
              position: state.pendingMove.prevPosition,
              ply: state.pendingMove.prevPly,
              pendingMove: null,
              lastReject: r.reason,
            }
          : { ...state, lastReject: r.reason };
      // T4 stale: STALE_PLY means our ply is behind the server.
      return r.reason === Reason.StalePly ? raise(reverted, "T4_STALE") : reverted;
    }

    case "State": {
      // Authoritative resync: rebuild the board from the FEN fold. Drops any
      // optimistic/pending state — the server's STATE is the truth.
      const s = (msg as { State: { game_id: string; state: { fen: string; turn: number; clocks: number[] } } }).State;
      const position = parseFen(s.state.fen);
      if (!frameConsistent(s.state.fen, s.state.turn, position)) {
        // T5: not trusted, never applied. From IN_SYNC it starts an episode;
        // as the reply to an outstanding attempt it fails that attempt (the
        // next one runs after backoff, or the next cycle). During backoff/FAILED
        // there is no outstanding attempt, so it is simply dropped.
        if (state.phase !== "playing") return state;
        if (state.sync.phase === "IN_SYNC") return raise(state, "T5_INCONSISTENT");
        if (recovering(state.sync) && state.sync.step === "await_reply") {
          return { ...state, sync: failAttempt(state.sync) };
        }
        return state;
      }
      // A consistent STATE is ALWAYS authoritative — including a LOWER ply than
      // local (Takeback: ply is not monotonic) and from FAILED. Resets counters.
      return {
        ...state,
        phase: state.phase === "game_over" ? "game_over" : "playing",
        gameId: s.game_id,
        position,
        // CRITICAL: restore ply from the FEN. StateBody carries no ply, but ply
        // is exactly what a gap desynchronized — if we leave it stale, the next
        // MOVE sends ply_expected = stale+1 and the server rejects it StalePly.
        ply: plyFromFen(s.state.fen),
        clocks: s.state.clocks.slice(),
        pendingMove: null,
        sync: { ...IN_SYNC, seq: state.sync.seq },
        lastReject: null,
        // A fold has no meaningful "most recent move" — clear it so the
        // screen-reader announcement does not re-read a stale pre-resync move.
        lastMove: null,
      };
    }

    case "DrawOffered": {
      // Shown in every recovery phase (§3.4.1).
      const d = (msg as { DrawOffered: { game_id: string; by: number } }).DrawOffered;
      return { ...state, drawOffer: { by: d.by } };
    }

    case "GameEnd": {
      // Applied immediately in every phase and ends recovery (§3.4.1): the
      // result is authoritative and needs no position.
      const g = (msg as { GameEnd: { game_id: string; result_vector: number[]; termination: TerminationCode; rating_delta: number[] } }).GameEnd;
      return {
        ...state,
        phase: "game_over",
        pendingMove: null,
        drawOffer: null,
        sync: { ...IN_SYNC, seq: state.sync.seq },
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
