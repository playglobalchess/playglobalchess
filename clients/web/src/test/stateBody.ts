import { NO_SEAT, Status, type StateBody } from "../codec/messages";

/** Test helper: a valid v1.3 STATE body with nothing pending and no result.
 * `ply` defaults to the FEN's ply (fullmove and side to move), so a fixture
 * written for v1.2 keeps its meaning. ply 0 is MATCHED; otherwise LIVE with
 * the side to move's clock running. Override any field with `extra`. */
export function sb(fen: string, turn: number, clocks: number[], extra: Partial<StateBody> = {}): StateBody {
  const parts = fen.split(" ");
  const full = Number(parts[5] ?? "1");
  const ply = (Number.isFinite(full) && full >= 1 ? full - 1 : 0) * 2 + (parts[1] === "b" ? 1 : 0);
  const live = ply > 0;
  return {
    fen,
    turn,
    clocks,
    you_seat: 0,
    ply,
    status: live ? Status.Live : Status.Matched,
    result_vector: [],
    termination: 0,
    last_move_uci: live ? "e2e4" : "",
    pending_draw_by: NO_SEAT,
    takeback_by: NO_SEAT,
    takeback_at_ply: 0,
    running_seat: live ? turn : NO_SEAT,
    server_t: 1728000000500n,
    ...extra,
  };
}
