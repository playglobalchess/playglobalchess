// App — the F-01 first-playable UI shell. Flow: a dev-token connect gate
// (accounts have not landed; the token is a temporary dev credential), then the
// GameView, which binds useGame (GameSocket ↔ reducer) and renders the board,
// clocks, controls, and status. All display text comes from t() (NFR-11); the
// board/engine rules stay server-side (NFR-5).

import { useEffect, useRef, useState } from "react";
import { t } from "./i18n";
import { codecProtocolVersion } from "./codec/seam";
import { useGame, canRequestTakeback } from "./game/useGame";
import { canOfferDraw } from "./game/reducer";
import { Board } from "./game/Board";
import { ClockBar } from "./game/Clock";
import { Reason, ErrorCode, type ReasonCode } from "./codec/messages";
import { DEV_POOL, DEV_TIME_CONTROL } from "./devConfig";

// The dev matchmaking pool + time control for first-playable live in a shared
// module (src/devConfig) so the real-server e2e sends the SAME values the UI
// does — see devConfig.ts for why (the "300+0" game-creation bug).

export function App(): React.JSX.Element {
  const [token, setToken] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  if (token == null) {
    return (
      <main>
        <h1>{t("app.title")}</h1>
        <p>{t("app.tagline")}</p>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (draft.trim().length > 0) setToken(draft.trim());
          }}
        >
          <label>
            {t("connect.token.label")}
            <input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder={t("connect.token.placeholder")}
            />
          </label>
          <button type="submit">{t("connect.action")}</button>
        </form>
        <p aria-label={t("codec.version.aria")}>
          {t("codec.version.label")} <code>{codecProtocolVersion()}</code>
        </p>
      </main>
    );
  }

  return <GameView token={token} />;
}

// The WebSocket URL for the game server. In dev, Vite proxies /ws; a real
// deploy supplies this via build config (deferred). Derived from the page
// origin so it works behind the dev proxy without hard-coding a host.
function gameSocketUrl(): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/ws`;
}

function GameView({ token }: { token: string }): React.JSX.Element {
  const { state, status, commands } = useGame({ url: gameSocketUrl(), token });

  const statusText =
    state.phase === "seeking"
      ? t("status.seeking")
      : state.phase === "playing"
        ? t("status.in_game")
        : state.phase === "game_over"
          ? t("status.game_over")
          : status === "connected"
            ? t("status.connected")
            : status === "connecting"
              ? t("status.connecting")
              : t("status.disconnected");

  const myTurn =
    state.phase === "playing" &&
    state.position != null &&
    ((state.mySeat === 0 && state.position.turn === "w") ||
      (state.mySeat === 1 && state.position.turn === "b"));

  // Screen-reader announcement (NFR-8): the latest move / offer / result,
  // rendered into an aria-live region so assistive tech reads it aloud. Gated
  // on a change key (ply / draw offer / result / error) so an UNRELATED
  // re-render (a clock tick, a status change) does not re-emit the same text
  // and make the screen reader read a move or result twice.
  const announcement = useGatedAnnouncement(state, Boolean(myTurn));

  return (
    <main>
      <h1>{t("app.title")}</h1>
      <p role="status">{statusText}</p>

      {/* Polite live region — visually hidden, read aloud by assistive tech. */}
      <p
        aria-live="polite"
        style={{
          position: "absolute",
          width: 1,
          height: 1,
          overflow: "hidden",
          clip: "rect(0 0 0 0)",
          whiteSpace: "nowrap",
        }}
      >
        {announcement}
      </p>

      {state.error != null ? (
        <p role="alert">
          {state.error === ErrorCode.UnsupportedVersion
            ? t("error.unsupported_version")
            : t("error.generic")}
        </p>
      ) : null}

      {state.phase === "idle" || state.phase === "seeking" ? (
        <button
          type="button"
          disabled={state.phase === "seeking"}
          onClick={() => commands.seek(DEV_POOL, DEV_TIME_CONTROL, false)}
        >
          {t("seek.action")}
        </button>
      ) : null}

      {state.needsResync ? <p role="status">{t("game.resync")}</p> : null}

      {state.position != null ? (
        <>
          <ClockBar clocks={state.clocks} clockRun={state.clockRun} mySeat={state.mySeat} />
          <p>{myTurn ? t("game.turn.yours") : t("game.turn.theirs")}</p>
          <Board
            position={state.position}
            mySeat={state.mySeat}
            interactive={Boolean(myTurn) && state.pendingMove == null}
            onMove={(uci) => commands.move(uci)}
          />
          {state.lastReject != null ? (
            <p role="alert">{rejectText(state.lastReject)}</p>
          ) : null}

          {state.phase === "playing" ? (
            <div>
              <button type="button" onClick={() => commands.resign()}>
                {t("game.resign")}
              </button>
              <button
                type="button"
                disabled={!canOfferDraw(state)}
                onClick={() => commands.offerDraw()}
              >
                {t("game.offerDraw")}
              </button>
              {state.rated === false ? (
                <button
                  type="button"
                  disabled={!canRequestTakeback(state)}
                  onClick={() => commands.requestTakeback()}
                >
                  {t("game.takeback")}
                </button>
              ) : null}
            </div>
          ) : null}

          {state.drawOffer != null && state.phase === "playing" ? (
            <div role="alert">
              <span>{t("game.draw.offered")}</span>
              <button type="button" onClick={() => commands.acceptDraw()}>
                {t("game.draw.accept")}
              </button>
              <button type="button" onClick={() => commands.declineDraw()}>
                {t("game.draw.decline")}
              </button>
            </div>
          ) : null}
          {state.myDrawOffered && state.phase === "playing" ? (
            <p role="status">{t("game.draw.sent")}</p>
          ) : null}

          {state.incomingTakeback != null && state.phase === "playing" ? (
            <div role="alert">
              <span>{t("game.takeback.requested")}</span>
              <button type="button" onClick={() => commands.acceptTakeback()}>
                {t("game.takeback.accept")}
              </button>
              <button type="button" onClick={() => commands.declineTakeback()}>
                {t("game.takeback.decline")}
              </button>
            </div>
          ) : null}
          {state.myTakebackSent != null && state.phase === "playing" ? (
            <p role="status">{t("game.takeback.sent")}</p>
          ) : null}

          {state.phase === "game_over" && state.result != null ? (
            <p role="status">{resultText(state)}</p>
          ) : null}
        </>
      ) : null}
    </main>
  );
}

/** Derive the screen-reader announcement from the latest meaningful state. */
function buildAnnouncement(
  state: {
    phase: string;
    mySeat: number | null;
    lastMove: { uci: string; moverSeat: number } | null;
    drawOffer: { by: number } | null;
    incomingTakeback: { by: number; atPly: number } | null;
    result: unknown;
  },
  myTurn: boolean,
): string {
  if (state.phase === "game_over") return t("announce.game_over");
  // Both prompts can be pending at once (STATE carries both): announce both.
  const prompts = [
    state.drawOffer != null ? t("announce.draw_offered") : "",
    state.incomingTakeback != null ? t("announce.takeback_requested") : "",
  ].filter((p) => p !== "");
  if (prompts.length > 0) return prompts.join(" ");
  if (state.lastMove != null) {
    const mine = state.lastMove.moverSeat === state.mySeat;
    const who = mine ? t("announce.you_moved") : t("announce.opponent_moved");
    const turn = myTurn ? ` ${t("announce.your_move")}` : "";
    return `${who} ${state.lastMove.uci}.${turn}`;
  }
  if (myTurn) return t("announce.your_move");
  return "";
}

/** Gate buildAnnouncement so the aria-live region only changes when a NEW event
 * occurs (a new ply, a draw offer, a result, or an error) — not on every
 * re-render. On an unrelated render it keeps the region empty so assistive tech
 * is not made to re-read the previous move/result. */
function useGatedAnnouncement(
  state: {
    phase: string;
    ply: number;
    mySeat: number | null;
    lastMove: { uci: string; moverSeat: number } | null;
    drawOffer: { by: number } | null;
    incomingTakeback: { by: number; atPly: number } | null;
    result: unknown;
    error: number | null;
  },
  myTurn: boolean,
): string {
  // Key the announcement to the discrete events worth reading aloud.
  const key = `${state.phase}|${state.ply}|${state.drawOffer != null ? state.drawOffer.by : "-"}|${state.incomingTakeback != null ? state.incomingTakeback.atPly : "-"}|${state.result != null ? "end" : "-"}|${state.error ?? "-"}`;
  const [text, setText] = useState("");
  const lastKey = useRef<string | null>(null);
  useEffect(() => {
    if (key === lastKey.current) return; // nothing new → leave the region as-is
    lastKey.current = key;
    setText(buildAnnouncement(state, myTurn));
    // We intentionally key only on the change-key; the message is derived from
    // the state at the moment the key advanced.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return text;
}

function rejectText(reason: ReasonCode): string {
  switch (reason) {
    case Reason.Illegal:
      return t("game.reject.illegal");
    case Reason.StalePly:
      return t("game.reject.stale");
    case Reason.GameOver:
      return t("game.reject.gameover");
    case Reason.NotYourTurn:
      return t("game.reject.notyourturn");
    case Reason.NotYourSeat:
      return t("game.reject.notyourseat");
    default:
      return t("game.reject.unknown");
  }
}

function resultText(state: {
  mySeat: number | null;
  result: { termination: number; resultVector: number[] } | null;
}): string {
  const r = state.result;
  if (r == null || state.mySeat == null) return t("game.result.over");
  const mine = r.resultVector[state.mySeat];
  // result_vector element: 0 loss / 1 draw / 2 win (§4A.3).
  if (mine === 2) return t("game.result.win");
  if (mine === 0) return t("game.result.loss");
  if (mine === 1) return t("game.result.draw");
  // No seat result (e.g. an abort) — a neutral game-over message.
  return t("game.result.over");
}
