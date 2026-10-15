// TS mirror of the §4A wire message types (protocol/codec/src/message.rs +
// types.rs). These are the SHAPES the shared codec's serde boundary produces and
// consumes — pinned byte-for-byte by the S4a node-parity + hand-built tests.
// This file defines ONLY the shapes; it encodes/decodes nothing (that is the
// Rust codec, via the wasm seam — NFR-5). A mismatch here is caught at runtime
// by the seam throwing, and the shapes are regression-guarded by the seam's
// hand-built golden tests on the Rust side.
//
// Conventions the §4A serde boundary uses (do not change without the codec):
//  - Messages are EXTERNALLY TAGGED: `{ Seek: { ... } }`, `{ Move: { ... } }`.
//  - Id newtypes (GameId/PoolId/TimeControlId/AuthToken) are #[serde(transparent)]
//    → bare `string`.
//  - enum8 fields (Reason/Termination/ErrorCode) are #[serde(transparent)] over
//    u8 → bare `number` (NOT a string or a tagged object).
//  - u64 fields (Ping.t, Pong.t, Pong.server_t) cross as `bigint` (the seam sets
//    serialize_large_number_types_as_bigints).
//  - u32/u16/u8/i16 fields are `number`.

// --- id strings (§4A.3, transparent newtypes) ------------------------------
export type GameId = string;
export type PoolId = string;
export type TimeControlId = string;
export type AuthToken = string;

// --- frozen enum8 value tables (§4A.4/5/7), as bare u8 numbers --------------
// MOVE_REJECTED.reason (§4A.4; 0x00 reserved).
export const Reason = {
  Illegal: 1,
  StalePly: 2,
  GameOver: 3,
  NotYourTurn: 4,
  NotYourSeat: 5,
  UnknownGame: 6,
} as const;
export type ReasonCode = (typeof Reason)[keyof typeof Reason];

// GAME_END.termination (§4A.5).
export const Termination = {
  Mate: 1,
  Resign: 2,
  Flag: 3,
  Abort: 4,
  DrawAgree: 5,
  Stalemate: 6,
  Repetition: 7,
  FiftyMove: 8,
  Insufficient: 9,
  TimeoutVsInsufficient: 10,
} as const;
export type TerminationCode = (typeof Termination)[keyof typeof Termination];

// ERROR.code (§4A.7).
export const ErrorCode = {
  UnsupportedVersion: 1,
  ProtocolViolation: 2,
  UnknownGame: 3,
  Unauthenticated: 4,
  RateLimited: 5,
  Internal: 6,
} as const;
export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

// result_vector element (§4A.3): 0 loss / 1 draw / 2 win.
export type SeatResult = 0 | 1 | 2;

// --- shared value structs (§4A.6) ------------------------------------------
/** STATE body / MATCHED.start_state (§4A.6): fen, turn, clocks. */
export interface StateBody {
  fen: string;
  turn: number; // side-to-move seat index (u8)
  clocks: number[]; // per-seat remaining ms ([u32; seat_count])
}

// --- client → server messages (§4A.2, externally tagged) -------------------
export type ClientMsg =
  | { Authenticate: { token: AuthToken } }
  | { Seek: { pool_id: PoolId; time_control: TimeControlId; rated: boolean } }
  | { Move: { game_id: GameId; ply_expected: number; uci: string } }
  | { OfferDraw: { game_id: GameId } }
  | { Resign: { game_id: GameId } }
  | { Abort: { game_id: GameId } }
  | { Takeback: { game_id: GameId } }
  | { Rematch: { game_id: GameId } }
  | { Ping: { t: bigint } }
  | { Resync: { game_id: GameId } }; // §4A v1.1 (ADR-17)

// --- server → client messages (§4A.2, externally tagged) -------------------
export type ServerMsg =
  | {
      Matched: {
        game_id: GameId;
        seat_idx: number;
        opponent: string;
        start_state: StateBody;
      };
    }
  | { MoveAccepted: { game_id: GameId; ply: number; clock_after_ms: number } }
  | { MoveRejected: { game_id: GameId; ply_expected: number; reason: ReasonCode } }
  | { State: { game_id: GameId; state: StateBody } }
  | {
      GameEnd: {
        game_id: GameId;
        result_vector: SeatResult[];
        termination: TerminationCode;
        rating_delta: number[];
      };
    }
  | { Pong: { t: bigint; server_t: bigint } }
  | { DrawOffered: { game_id: GameId; by: number } } // §4A v1.1 (ADR-17)
  | { Error: { code: ErrorCodeValue; msg: string } };

// --- discriminant helpers (externally-tagged tag extraction) ---------------
/** The single variant key of an externally-tagged message, e.g. "Move". */
export type ServerMsgTag = keyof UnionToIntersection<ServerMsg> & string;

// Utility: turn a union of single-key objects into the intersection of keys so
// `keyof` yields every variant tag. (TS-standard UnionToIntersection.)
type UnionToIntersection<U> = (U extends unknown ? (k: U) => void : never) extends (
  k: infer I,
) => void
  ? I
  : never;

/** Narrowing accessor: the variant tag present on a decoded ServerMsg. */
export function serverTag(msg: ServerMsg): ServerMsgTag {
  // Exactly one key by construction (externally tagged).
  return Object.keys(msg)[0] as ServerMsgTag;
}
