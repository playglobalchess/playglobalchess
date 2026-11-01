// useGame — the React glue that binds the GameSocket (I/O) to the pure reducer
// (truth), and nothing else. It is the ONE place the two load-bearing units
// meet, so it is deliberately thin and side-effect-disciplined:
//
//   • It owns the reducer state as a synchronously-updated store: every action
//     goes through apply(), which runs the pure reduce() on a ref-held state
//     and publishes the result to React. The ref is therefore never behind the
//     reducer, so a decision made from it (move gating) cannot lag a render.
//   • It constructs ONE GameSocket whose onServerMsg applies {kind:"server"}
//     into the reducer — the socket feeds the reducer, never the other way.
//   • It OWNS the recovery policy's I/O (SPEC-S4-WEB-F01 §3.4.1): every timer
//     (T_reply, backoff), every RESYNC send, the forced GameSocket.reconnect(),
//     and the terminal error log. The reducer (pure) detects triggers and holds
//     sync.phase/attempt/cycle/seq; GameSocket is transport only. This hook is
//     the ONLY layer that sends RESYNC.
//   • It exposes a small command surface (connect/seek/move/resign/offerDraw/
//     acceptDraw) the UI calls — each of which is a socket send plus, for a
//     local move, the optimistic reducer action.
//
// Correctness notes that an independent reviewer should check:
//   - The GameSocket is created ONCE (useRef + an effect that connects on mount
//     and close()s on unmount), NOT re-created per render.
//   - T_reply is armed only while the React `status` is "connected" and is
//     cleared by that effect's cleanup whenever status or the connection epoch
//     changes, so an outage (including the new transport opened by reconnect())
//     pauses it. A timer armed in the one commit where `status` still lags is
//     cleared by the very next render.
//   - move() asks the REDUCER (not a mirror, not its own copy of the rules)
//     whether the move is accepted: it runs reduce(localMove) and sends only if
//     that produced a pending optimistic move. A trigger and a move in the same
//     commit therefore cannot both win: the trigger is applied first, the
//     reducer then refuses the move while recovering (§3.4.1).
//   - The once-per-attempt RESYNC is keyed by the effect's own dependencies:
//     (connection epoch, game id, raise seq). The epoch bumps on every
//     (re)connect, so a drop and reconnect inside one React batch still re-sends.

import { useCallback, useEffect, useRef, useState } from "react";
import { reduce, initialState, type Action, type GameState } from "./reducer";
import { SYNC_REPLY_TIMEOUT_MS, syncBackoffMs } from "./recovery";
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
  // The synchronous store (see header). stateRef is the single source of truth;
  // React state only republishes it for rendering.
  const [state, setState] = useState<GameState>(initialState);
  const stateRef = useRef<GameState>(initialState);
  const apply = useCallback((action: Action): GameState => {
    const next = reduce(stateRef.current, action);
    stateRef.current = next;
    setState(next);
    return next;
  }, []);

  // The socket is a stable instance across renders. useRef (not useState) so
  // mutating it never triggers a render.
  const sockRef = useRef<GameSocket | null>(null);
  const [status, setStatus] = useStatus();
  // Bumps on every transition to "connected": part of the RESYNC key, so a new
  // connection is a new key space even if React batches away the status change.
  const [connEpoch, setConnEpoch] = useState(0);

  // --- construct + connect the socket exactly once (mount) -----------------
  useEffect(() => {
    const sock = new GameSocket({
      url: opts.url,
      token: opts.token,
      // Only include `factory` when provided — exactOptionalPropertyTypes
      // forbids passing an explicit `undefined` to an optional field.
      ...(opts.factory != null ? { factory: opts.factory } : {}),
      onServerMsg: (msg: ServerMsg) => apply({ kind: "server", msg }),
      onStatus: (s) => {
        if (s === "connected") setConnEpoch((n) => n + 1);
        setStatus(s);
      },
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
  }, [opts.url, opts.token, opts.factory, setStatus, apply]);

  // --- recovery driver (§3.4.1) ---------------------------------------------
  // OWNER: this hook (timers + sends). The reducer only transitions.
  const { phase: syncPhase, step: syncStep, attempt: syncAttempt, seq: syncSeq } = state.sync;
  const gameId = state.gameId;
  const recovering = syncPhase === "SYNCING" || syncPhase === "RECONNECTING";

  // RECONNECTING(k): force a fresh transport once per cycle (this effect re-runs
  // only when phase/step/seq change, and seq is new for every cycle).
  useEffect(() => {
    if (syncPhase !== "RECONNECTING" || syncStep !== "await_reply") return;
    sockRef.current?.reconnect();
  }, [syncPhase, syncStep, syncSeq]);

  // await_reply: T_reply runs ONLY while connected (an outage pauses recovery
  // and consumes neither N nor K). In SYNCING send exactly one RESYNC per
  // attempt per connection; in RECONNECTING the server's auto-STATE after
  // re-AUTH is the reply, so nothing is sent.
  useEffect(() => {
    if (!recovering || syncStep !== "await_reply" || gameId == null) return;
    if (status !== "connected") return;
    // The key (epoch, game, seq) is this effect's dependency list: it runs once
    // per attempt per connection. Leaving "connected" only runs the cleanup and
    // returns above, so the next connection (new epoch) sends again (#69).
    if (syncPhase === "SYNCING") sockRef.current?.resync(gameId);
    const timer = setTimeout(
      () => apply({ kind: "syncReplyTimeout", seq: syncSeq }),
      SYNC_REPLY_TIMEOUT_MS,
    );
    return () => clearTimeout(timer);
  }, [recovering, syncPhase, syncStep, syncSeq, gameId, status, connEpoch, apply]);

  // backoff after a failed SYNCING attempt n: B * 2^(n-1), then the next attempt.
  useEffect(() => {
    if (syncPhase !== "SYNCING" || syncStep !== "backoff") return;
    const timer = setTimeout(
      () => apply({ kind: "syncBackoffElapsed", seq: syncSeq }),
      syncBackoffMs(syncAttempt),
    );
    return () => clearTimeout(timer);
  }, [syncPhase, syncStep, syncAttempt, syncSeq, apply]);

  // FAILED: send nothing more; log ONE client error with the game id and the
  // trigger that started the episode (console until the X-14 sink exists).
  const failedTrigger = state.sync.trigger;
  useEffect(() => {
    if (syncPhase !== "FAILED") return;
    console.error("web: game recovery exhausted (FAILED)", { gameId, trigger: failedTrigger });
  }, [syncPhase, gameId, failedTrigger]);

  // --- command surface the UI calls ----------------------------------------
  const seek = useCallback((poolId: string, timeControl: string, rated: boolean) => {
    sockRef.current?.seek(poolId, timeControl, rated);
    apply({ kind: "seek" });
  }, [apply]);

  const move = useCallback((uci: string) => {
    // The REDUCER decides (§3.4.1 clarification): run the local move through it
    // and send only if it produced a pending optimistic move. Its guards cover
    // phase, pending move, turn, malformed uci and recovery (SYNCING,
    // RECONNECTING, FAILED). stateRef is updated synchronously by apply(), so a
    // trigger earlier in the same commit has already been applied here.
    const before = stateRef.current;
    if (before.gameId == null) return;
    const after = apply({ kind: "localMove", uci });
    // The reducer returns the SAME state object when it refuses a move.
    if (after === before || after.pendingMove == null) return;
    sockRef.current?.move(before.gameId, after.pendingMove.prevPly + 1, uci);
  }, [apply]);

  const resign = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId != null) sockRef.current?.resign(s.gameId);
  }, []);

  const offerDraw = useCallback(() => {
    const s = stateRef.current;
    if (s.gameId != null) sockRef.current?.offerDraw(s.gameId);
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
    commands: { seek, move, resign, offerDraw, acceptDraw },
  };
}

// Small local hook so status lives in React state (triggers a re-render of the
// status line) while the socket itself stays in a ref. Split out only for
// readability; its identity (setStatus) is stable across renders.
function useStatus(): [ConnStatus, (s: ConnStatus) => void] {
  const [status, setStatus] = useState<ConnStatus>("connecting");
  return [status, setStatus];
}
