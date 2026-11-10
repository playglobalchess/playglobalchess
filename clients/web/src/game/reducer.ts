// The pure game reducer — the correctness heart of the client. It is a pure
// function (state, action) -> state with NO side effects (no socket, no DOM, no
// clock read: the time an action happened arrives IN the action, as `at`): the GameSocket feeds it decoded ServerMsg; the UI feeds it local move
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
  type StateBody,
  type ReasonCode,
  type TerminationCode,
  type ErrorCodeValue,
  Reason,
  Status,
  NO_SEAT,
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

export interface ClockRun {
  seat: number;
  /** Local monotonic ms at which clocks[seat] was valid. */
  since: number;
}

export interface GameState {
  phase: Phase;
  gameId: string | null;
  mySeat: number | null;
  opponent: string | null;
  position: Position | null;
  /** Authoritative current ply (server). The next MOVE sends ply_expected = ply + 1. */
  ply: number;
  /** Per-seat remaining clock ms (index = seat), as last set by the server (or
   * frozen locally, see clockRun). For the running seat the value is valid at
   * clockRun.since; use displayClocks() to read what to show now. */
  clocks: number[];
  /** The clock that is running (#112): `seat` ticks down from clocks[seat],
   * which was valid at local monotonic time `since` (ms, the action's `at`).
   * null when no clock runs (before the server starts it, after our own move
   * until it is accepted, and once the game is over). Display only: the server
   * stays authoritative and the client never flags. */
  clockRun: ClockRun | null;
  /** A move shown optimistically, awaiting the server's accept/reject. Holds
   * the clocks from before it, so a reject can restart our clock. */
  pendingMove: {
    uci: string;
    prevPosition: Position;
    prevPly: number;
    prevClocks: number[];
    prevClockRun: ClockRun | null;
  } | null;
  /** A pending draw offer FROM the opponent (DRAW_OFFERED), awaiting our accept/decline. */
  drawOffer: DrawOffer | null;
  /** Our own draw offer is pending (GS-7 §6.4). */
  myDrawOffered: boolean;
  /** Ply at which we last offered a draw (the server's 20-ply cooldown, §6.3),
   * or null. Lets the UI disable "Offer draw" when the server would refuse. */
  myLastDrawOfferPly: number | null;
  /** A pending takeback request FROM the opponent, awaiting our accept/decline. */
  incomingTakeback: { by: number; atPly: number } | null;
  /** Our own takeback request is pending. */
  myTakebackSent: { atPly: number } | null;
  /** Whether this game is rated, as we asked in our own SEEK; null when unknown
   * (e.g. after a reload, where only STATE arrives). Takeback is unrated-only. */
  rated: boolean | null;
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
  clockRun: null,
  pendingMove: null,
  drawOffer: null,
  myDrawOffered: false,
  myLastDrawOfferPly: null,
  incomingTakeback: null,
  myTakebackSent: null,
  rated: null,
  result: null,
  lastReject: null,
  needsResync: false,
  error: null,
  lastMove: null,
};

export type Action =
  | { kind: "seek"; rated?: boolean } // user clicked Play
  // Local echoes of our own prompt actions: the server tells only the OTHER seat,
  // so the sender records its own pending/declined state here. A STATE push
  // (the server's answer to an ignored action) overwrites them.
  | { kind: "localOfferDraw" }
  | { kind: "localDeclineDraw" }
  | { kind: "localRequestTakeback" }
  | { kind: "localDeclineTakeback" }
  // `at` is the local monotonic time (ms) the action happened: the clock anchor
  // (#112). REQUIRED, so a dispatch path that forgets it is a type error rather
  // than an anchor at 0 that silently shows the running clock as 0:00.
  | { kind: "localMove"; uci: string; at: number } // user made a board move (optimistic)
  | { kind: "server"; msg: ServerMsg; at: number }; // a decoded ServerMsg arrived

/** Pure reducer. */
export function reduce(state: GameState, action: Action): GameState {
  switch (action.kind) {
    case "seek":
      return { ...initialState, phase: "seeking", rated: action.rated ?? null };

    case "localOfferDraw":
      // An offer while the opponent's is pending IS the accept (GAME_END follows).
      // An accept of the opponent's offer, or an offer the server would refuse
      // (canOfferDraw false), records nothing: no false "waiting" line.
      if (state.drawOffer != null || !canOfferDraw(state)) return state;
      return { ...state, myDrawOffered: true, myLastDrawOfferPly: state.ply };
    case "localDeclineDraw":
      return { ...state, drawOffer: null, myDrawOffered: false };
    case "localRequestTakeback":
      if (state.phase !== "playing" || state.incomingTakeback != null) return state;
      return { ...state, myTakebackSent: { atPly: state.ply } };
    case "localDeclineTakeback":
      return { ...state, incomingTakeback: null };

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
      // Our clock stops the moment we move, as on chess.com (#112): freeze the
      // shown value until MOVE_ACCEPTED sets the authoritative one.
      return {
        ...state,
        position: optimistic,
        clocks: displayClocks(state, action.at),
        clockRun: null,
        pendingMove: {
          uci: action.uci,
          prevPosition,
          prevPly,
          prevClocks: state.clocks,
          prevClockRun: state.clockRun,
        },
        lastReject: null,
      };
    }

    case "server":
      return reduceServer(state, action.msg, action.at);
  }
}

/** The clocks to SHOW at local monotonic time `now`: the running seat's value
 * minus the time since its anchor, clamped at 0. Display only (#112). */
export function displayClocks(s: Pick<GameState, "clocks" | "clockRun">, now: number): number[] {
  const out = s.clocks.slice();
  const run = s.clockRun;
  if (run != null && run.seat < out.length) {
    out[run.seat] = Math.max(0, (out[run.seat] ?? 0) - Math.max(0, now - run.since));
  }
  return out;
}

/** The anchor a server body (MATCHED / STATE) gives: running_seat from `at`. */
function runFromBody(b: StateBody, at: number): ClockRun | null {
  if (b.running_seat === NO_SEAT || b.running_seat >= b.clocks.length) return null;
  return { seat: b.running_seat, since: at };
}

function reduceServer(state: GameState, msg: ServerMsg, at: number): GameState {
  const tag = serverTag(msg);
  switch (tag) {
    case "Matched": {
      const m = (msg as { Matched: { game_id: string; seat_idx: number; opponent: string; start_state: StateBody } }).Matched;
      return {
        ...initialState,
        rated: state.rated, // from our own SEEK; MATCHED does not carry it
        phase: "playing",
        gameId: m.game_id,
        mySeat: m.seat_idx,
        opponent: m.opponent,
        position: parseFen(m.start_state.fen),
        // v1.3: the server's ply is in the body (no FEN inference).
        ply: m.start_state.ply,
        clocks: m.start_state.clocks.slice(),
        clockRun: runFromBody(m.start_state, at),
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
          // A moot optimistic move (a duplicate or gap accept superseded it)
          // froze our clock; restart it from its anchor, as on a reject, so it
          // does not stand still until the resync STATE arrives.
          ...(state.pendingMove != null
            ? { clocks: state.pendingMove.prevClocks, clockRun: state.pendingMove.prevClockRun }
            : {}),
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
      const clocks = (state.pendingMove != null ? state.pendingMove.prevClocks : state.clocks).slice();
      const moverSeat = a.ply % 2 === 1 ? 0 : 1;
      if (moverSeat < clocks.length) {
        clocks[moverSeat] = a.clock_after_ms;
      }
      // The mover's clock stops; the other seat's starts now (#112). Not after
      // the game is over (a GAME_END that overtook this frame, #92).
      const nextSeat = (moverSeat + 1) % 2;
      const clockRun: ClockRun | null =
        state.phase === "playing" && nextSeat < clocks.length ? { seat: nextSeat, since: at } : null;
      // GS-7 §6.4: a NEW move by the opponent declines our pending prompts; a
      // new move by us declines theirs. (A duplicate MOVE_ACCEPTED, a re-ack,
      // returned above without clearing anything.)
      const mine = moverSeat === state.mySeat;
      return {
        ...state,
        position,
        ply: a.ply,
        clocks,
        clockRun,
        pendingMove: null,
        lastReject: null,
        lastMove: { uci: a.uci, moverSeat },
        ...(mine
          ? { drawOffer: null, incomingTakeback: null }
          : { myDrawOffered: false, myTakebackSent: null }),
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
          // Our clock never stopped on the server: restart it from its anchor.
          clocks: state.pendingMove.prevClocks,
          clockRun: state.pendingMove.prevClockRun,
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
      const s = (msg as { State: { game_id: string; state: StateBody } }).State;
      const b = s.state;
      // v1.3: STATE alone is enough to resume (#101) — seat, ply, status,
      // result and every pending prompt come from the body, not from memory.
      const mySeat = b.you_seat === NO_SEAT ? null : b.you_seat;
      const ended = b.status === Status.Ended;
      const seatOrNull = (v: number) => (v === NO_SEAT ? null : v);
      const drawBy = seatOrNull(b.pending_draw_by);
      const tbBy = seatOrNull(b.takeback_by);
      return {
        ...state,
        phase: ended ? "game_over" : "playing",
        gameId: s.game_id,
        mySeat,
        result: ended ? { termination: b.termination as TerminationCode, resultVector: b.result_vector.slice() } : null,
        drawOffer: drawBy != null && drawBy !== mySeat ? { by: drawBy } : null,
        myDrawOffered: drawBy != null && drawBy === mySeat,
        incomingTakeback: tbBy != null && tbBy !== mySeat ? { by: tbBy, atPly: b.takeback_at_ply } : null,
        myTakebackSent: tbBy != null && tbBy === mySeat ? { atPly: b.takeback_at_ply } : null,
        // A rewind drops cooldown entries above the new ply (§5.5).
        myLastDrawOfferPly:
          state.myLastDrawOfferPly != null && state.myLastDrawOfferPly > b.ply ? null : state.myLastDrawOfferPly,
        // A STATE with a LOWER ply than ours (after a takeback rewind) is valid:
        // the fold below replaces the board, ply and clocks wholesale.
        position: parseFen(s.state.fen),
        // CRITICAL: restore ply — it is exactly what a gap desynchronized. If it
        // stayed stale, the next MOVE would send ply_expected = stale+1 and be
        // rejected StalePly forever. v1.3 STATE carries the server ply directly.
        ply: s.state.ply,
        clocks: s.state.clocks.slice(),
        clockRun: ended ? null : runFromBody(b, at),
        pendingMove: null,
        needsResync: false,
        // A fresh authoritative STATE means the connection is healthy again.
        lastReject: null,
        // A fold has no meaningful "most recent move" — clear it so the
        // screen-reader announcement does not re-read a stale pre-resync move.
        lastMove: null,
      };
    }

    case "DrawOffered": {
      const d = (msg as { DrawOffered: { game_id: string; by: number } }).DrawOffered;
      return { ...state, drawOffer: { by: d.by } };
    }

    case "DrawDeclined":
      // The other seat declined our offer, or withdrew theirs (§6.3).
      return { ...state, drawOffer: null, myDrawOffered: false };

    case "TakebackOffered": {
      const o = (msg as { TakebackOffered: { game_id: string; seat_idx: number; at_ply: number } }).TakebackOffered;
      return { ...state, incomingTakeback: { by: o.seat_idx, atPly: o.at_ply } };
    }

    case "TakebackDeclined":
      // The other seat declined our request, or cancelled theirs (§5.2).
      return { ...state, incomingTakeback: null, myTakebackSent: null };

    case "GameEnd": {
      const g = (msg as { GameEnd: { game_id: string; result_vector: number[]; termination: TerminationCode; rating_delta: number[] } }).GameEnd;
      return {
        ...state,
        phase: "game_over",
        // Freeze the shown clocks where they are; nothing runs any more.
        clocks: displayClocks(state, at),
        clockRun: null,
        pendingMove: null,
        drawOffer: null,
        myDrawOffered: false,
        incomingTakeback: null,
        myTakebackSent: null,
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

/** The server's §6.3 draw-offer gate as far as the client can see it: a live
 * game at ply >= 2, no offer of ours pending, and none of ours in the last 20
 * plies. (After a reload the last-offer ply is unknown; a refused offer is then
 * answered by a STATE push, which keeps the UI honest.) */
export function canOfferDraw(s: GameState): boolean {
  if (s.phase !== "playing" || s.gameId == null || s.myDrawOffered || s.ply < 2) return false;
  return s.myLastDrawOfferPly == null || s.myLastDrawOfferPly < s.ply - 20;
}
