// useGame — the React glue that binds the GameSocket (I/O) to the pure reducer
// (truth), and nothing else. It is the ONE place the two load-bearing units
// meet, so it is deliberately thin and side-effect-disciplined:
//
//   • It owns the reducer state via useReducer(reduce, initialState).
//   • It constructs ONE GameSocket whose onServerMsg dispatches {kind:"server"}
//     into the reducer — the socket feeds the reducer, never the other way.
//   • It wires the step-4 DEFERRED seam: when the reducer sets needsResync
//     (a ply gap it detected), this hook calls sock.resync(gameId). GameSocket
//     never observes reducer output itself (§ its own doc); that observation
//     lives here, because only this layer sees both the state and the socket.
//   • It exposes a small command surface (connect/seek/move/resign/offerDraw/
//     acceptDraw) the UI calls — each of which is a socket send plus, for a
//     local move, the optimistic reducer dispatch.
//
// Correctness notes that an independent reviewer should check:
//   - The GameSocket is created ONCE (useRef + an effect that connects on mount
//     and close()s on unmount), NOT re-created per render — a per-render socket
//     would reconnect-storm. React 19 StrictMode double-invokes effects in dev;
//     the effect's cleanup close() + GameSocket.connect()'s own teardown make
//     the double-mount safe.
//   - needsResync is consumed exactly once per raised edge: after calling
//     resync() we must not call it again until the reducer clears the flag
//     (STATE clears it). We dispatch nothing to clear it from here — STATE does
//     — so we guard on the flag transition with a ref, not on every render.
//   - move() applies the optimistic reducer dispatch BEFORE the socket send is
//     irrelevant to ordering (both are local), but we send with ply+1 computed
//     from the CURRENT reducer ply, read at call time, not from a stale closure.

import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { reduce, initialState, canOfferDraw, type GameState } from "./reducer";
import {
  GameSocket,
  type ConnStatus,
  type SocketFactory,
} from "../net/GameSocket";
import type { ServerMsg } from "../codec/messages";

export interface UseGameOptions {
  url: string;
  token: string;
  /** Injected socket factory for tests; real WebSocket in the browser. */
  factory?: SocketFactory;
}

export interface GameCommands {
  seek: (poolId: string, timeControl: string, rated: boolean) => void;
  move: (uci: string) => void;
  resign: () => void;
  offerDraw: () => void;
  /** Accept a pending draw offer (sends OFFER_DRAW back — mutual agreement). */
  acceptDraw: () => void;
  /** Decline the opponent's draw offer (§4A v1.3 DECLINE_DRAW). */
  declineDraw: () => void;
  /** Request a takeback (unrated only; the opponent must accept). */
  requestTakeback: () => void;
  /** Accept / decline the opponent's pending takeback request. */
  acceptTakeback: () => void;
  declineTakeback: () => void;
}

export interface UseGameResult {
  state: GameState;
  status: ConnStatus;
  commands: GameCommands;
}

/**
 * Bind a GameSocket to the pure reducer for the lifetime of the component.
 * Connects on mount, closes on unmount.
 */
export function useGame(opts: UseGameOptions): UseGameResult {
  const [state, dispatch] = useReducer(reduce, initialState);

  // The socket is a stable instance across renders. useRef (not useState) so
  // mutating it never triggers a render; it is created lazily on first mount.
  const sockRef = useRef<GameSocket | null>(null);
  // A plain React state slot for the connection status line.
  const [status, setStatus] = useStatus();

  // Keep the latest reducer state readable from stable callbacks WITHOUT
  // re-creating them each render (which would re-run the connect effect). The
  // ref mirrors `state` after each commit (in an effect, never during render —
  // reading/writing a ref during render is a React anti-pattern); callbacks
  // read sockRef/stateRef, so their identity is stable and the mount effect
  // runs exactly once.
  const stateRef = useRef(state);
  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  // Guards the once-per-edge resync: true while we have already asked for a
  // resync and are waiting for the clearing STATE. Prevents re-sending RESYNC
  // on every render while needsResync is still set.
  const resyncInFlight = useRef(false);

  // --- construct + connect the socket exactly once (mount) -----------------
  useEffect(() => {
    const sock = new GameSocket({
      url: opts.url,
      token: opts.token,
      // Only include `factory` when provided — exactOptionalPropertyTypes
      // forbids passing an explicit `undefined` to an optional field.
      ...(opts.factory != null ? { factory: opts.factory } : {}),
      onServerMsg: (msg: ServerMsg) => dispatch({ kind: "server", msg }),
      onStatus: (s) => setStatus(s),
    });
    sockRef.current = sock;
    // DEFER the connect by a tick in the REAL-WebSocket path. React StrictMode
    // (dev) double-invokes this effect: mount → cleanup → mount, synchronously.
    // An eager connect on the discarded first mount opens a socket and closes it
    // immediately — and through a dev-server WS proxy that half-open upgrade
    // orphans the proxy's upstream pipe (server write → EPIPE, killing the real
    // second socket too). Deferring to a 0ms timer and cancelling it in cleanup
    // means the discarded mount never opens a socket; only the surviving mount
    // connects. Tests inject a `factory` (a synchronous fake with no proxy and
    // no real upgrade) and want connect to happen now — so defer only when there
    // is no injected factory (i.e. the real browser WebSocket).
    if (opts.factory != null) {
      sock.connect();
      return () => {
        sock.close();
        sockRef.current = null;
      };
    }
    let connected = false;
    const timer = setTimeout(() => {
      connected = true;
      sock.connect();
    }, 0);
    return () => {
      clearTimeout(timer);
      if (connected) sock.close();
      sockRef.current = null;
    };
    // url/token/factory identify the connection; changing them re-connects.
  }, [opts.url, opts.token, opts.factory, setStatus]);

  // --- wire the deferred needsResync → resync seam -------------------------
  // When the reducer raises needsResync (a ply gap), ask the server for a fresh
  // STATE exactly once. STATE clears needsResync, which drops resyncInFlight so
  // a LATER gap can resync again.
  useEffect(() => {
    const sock = sockRef.current;
    if (state.needsResync && state.gameId != null && sock != null) {
      if (!resyncInFlight.current) {
        resyncInFlight.current = true;
        sock.resync(state.gameId);
      }
    } else if (!state.needsResync) {
      // Flag cleared (by STATE) — re-arm for the next gap.
      resyncInFlight.current = false;
    }
  }, [state.needsResync, state.gameId]);

  // --- command surface the UI calls ----------------------------------------
  const seek = useCallback((poolId: string, timeControl: string, rated: boolean) => {
    sockRef.current?.seek(poolId, timeControl, rated);
    dispatch({ kind: "seek", rated });
  }, []);

  const move = useCallback((uci: string) => {
    const s = stateRef.current;
    if (s.phase !== "playing" || s.gameId == null || s.position == null) return;
    // Enforce the reducer's OWN preconditions here, not just in the view: a MOVE
    // frame must never go to the wire without a corresponding optimistic apply.
    //  - a move is already pending (awaiting accept/reject) → the reducer would
    //    drop the localMove, so sending another frame risks two frames with the
    //    same ply_expected. Refuse.
    //  - it is not our turn → the reducer drops the localMove; a send would only
    //    earn a NOT_YOUR_TURN reject. Refuse.
    // This closes the double-send/off-turn window the view's board gate also
    // guards, so a direct commands.move() cannot bypass the contract.
    if (s.pendingMove != null) return;
    const myTurn =
      (s.mySeat === 0 && s.position.turn === "w") ||
      (s.mySeat === 1 && s.position.turn === "b");
    if (!myTurn) return;
    // Optimistic local apply, then the send with ply+1 from the CURRENT reducer
    // ply (read at call time — move() runs in a click handler, after the mirror
    // effect has committed the latest state).
    dispatch({ kind: "localMove", uci });
    sockRef.current?.move(s.gameId, s.ply + 1, uci);
  }, []);

  const resign = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId != null) sockRef.current?.resign(s.gameId);
  }, []);

  const offerDraw = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId == null || s.phase !== "playing") return;
    // Accepting the opponent's offer is always allowed; a new offer only when
    // the server would accept it (canOfferDraw).
    if (s.drawOffer == null && !canOfferDraw(s)) return;
    dispatch({ kind: "localOfferDraw" });
    sockRef.current?.offerDraw(s.gameId);
  }, []);

  const declineDraw = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId == null || s.drawOffer == null) return;
    dispatch({ kind: "localDeclineDraw" });
    sockRef.current?.declineDraw(s.gameId);
  }, []);

  const requestTakeback = useCallback(() => {
    const s = stateRef.current;
    if (!canRequestTakeback(s)) return;
    dispatch({ kind: "localRequestTakeback" });
    sockRef.current?.requestTakeback(s.gameId as string);
  }, []);

  const acceptTakeback = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId == null || s.incomingTakeback == null) return;
    // The server answers with STATE to both seats (the rewound position).
    sockRef.current?.acceptTakeback(s.gameId, s.incomingTakeback.atPly);
  }, []);

  const declineTakeback = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId == null || s.incomingTakeback == null) return;
    const at = s.incomingTakeback.atPly;
    dispatch({ kind: "localDeclineTakeback" });
    sockRef.current?.declineTakeback(s.gameId, at);
  }, []);

  const acceptDraw = useCallback(() => {
    // Accepting a draw = sending our own OFFER_DRAW (mutual agreement, §4A v1):
    // there is no distinct ACCEPT frame in the frozen set — a matching offer
    // from both seats is the agreement. The server resolves it to GAME_END.
    const s = stateRef.current;
    if (s.gameId != null && s.drawOffer != null) sockRef.current?.offerDraw(s.gameId);
  }, []);

  return {
    state,
    status,
    commands: { seek, move, resign, offerDraw, acceptDraw, declineDraw, requestTakeback, acceptTakeback, declineTakeback },
  };
}

// Small local hook so status lives in React state (triggers a re-render of the
// status line) while the socket itself stays in a ref. Split out only for
// readability; its identity (setStatus) is stable across renders.
function useStatus(): [ConnStatus, (s: ConnStatus) => void] {
  const [status, setStatus] = useState<ConnStatus>("connecting");
  return [status, setStatus];
}

/** A takeback can be requested in a live UNRATED game once both seats have
 * moved (ply >= 2), with no request pending either way (GS-7 §5.2 step 1). */
export function canRequestTakeback(s: GameState): boolean {
  return (
    s.phase === "playing" &&
    s.gameId != null &&
    s.rated === false &&
    s.ply >= 2 &&
    s.myTakebackSent == null &&
    s.incomingTakeback == null
  );
}
