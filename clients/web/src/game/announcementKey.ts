// The change key that gates the screen-reader announcement (NFR-8). The
// aria-live text is re-derived only when this string changes, so it must fold in
// exactly the discrete events worth reading aloud: phase, ply, a draw offer and
// the result. It deliberately EXCLUDES transport/protocol errors (they render in
// their own role="alert"), so clearing an error does not re-read the last move.

export interface AnnouncementKeyState {
  phase: string;
  ply: number;
  drawOffer: { by: number } | null;
  result: unknown;
}

export function announcementKey(state: AnnouncementKeyState): string {
  return `${state.phase}|${state.ply}|${state.drawOffer != null ? state.drawOffer.by : "-"}|${state.result != null ? "end" : "-"}`;
}
