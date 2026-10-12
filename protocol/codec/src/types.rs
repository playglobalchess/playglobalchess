//! The SPEC-S2-PROTOCOL §4A value types: id newtypes, the frozen u8 enum tables
//! (§4A.4/5/7/9), and the shared value structs used by both the wire messages
//! (`message.rs`) and the event-store bodies (`event.rs`).
//!
//! ## Why the frozen enums are `u8`-backed newtypes, not `#[repr(u8)]` enums
//!
//! postcard serializes a serde fieldless enum by its **variant index**
//! (0,1,2,…), *not* by any `#[repr(u8)]` value. The §4A tables are NOT
//! contiguous from zero — `reason` reserves `0x00`, `termination` starts at
//! `0x01` and skips, `error code` starts at `0x01`. Relying on serde's variant
//! index would silently emit the wrong byte and break the frozen contract.
//!
//! So every §4A "enum8" is a thin `u8`-backed newtype whose serialized form is
//! exactly that one byte. Named constants give the frozen values; `is_known()`
//! lets the decode path reject an out-of-range discriminant with a typed error
//! instead of accepting garbage. This makes "the byte IS the frozen value" true
//! by construction and is what the golden-vector freeze guard locks (§4.2).
//!
//! ## Field order is the contract
//!
//! Every struct's field order below is copied verbatim from §4A.3 / §4A.8.
//! postcard is positional — reordering a field silently changes the wire format
//! — so the golden-vector corpus (`protocol/vectors/`) exists to make any such
//! reorder a loud byte-mismatch test failure (PLAN-CODEC §3).

use serde::{Deserialize, Serialize};

// --- id newtypes (§3.3, §4A.3) ---------------------------------------------
// Ids travel as strings on the wire (postcard length-prefixed UTF-8). Newtypes
// keep them from being mixed up with each other or with free text at the type
// level, while serializing transparently as the bare string (so the wire byte
// shape is identical to a plain `str`, and the golden vectors are unaffected).

macro_rules! str_newtype {
    ($(#[$m:meta])* $name:ident) => {
        $(#[$m])*
        #[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
        #[serde(transparent)]
        pub struct $name(pub String);

        impl $name {
            /// Borrow the inner string.
            pub fn as_str(&self) -> &str {
                &self.0
            }
        }

        impl From<&str> for $name {
            fn from(s: &str) -> Self {
                Self(s.to_string())
            }
        }

        impl From<String> for $name {
            fn from(s: String) -> Self {
                Self(s)
            }
        }
    };
}

str_newtype!(
    /// A game id — UUID text (`str36` on the wire, §4A.3).
    GameId
);
str_newtype!(
    /// A rating-pool id (`format × variant`, §3.3).
    PoolId
);
str_newtype!(
    /// A time-control id string per Q-A4 (e.g. `"blitz-5+3"`, `"daily-3"`,
    /// `"custom-300000+3000"`). NOT an inlined base/increment struct — the
    /// id → `{base_ms, increment_ms, per_move_ms?}` expansion lives once in
    /// REST `GET /v1/pools` (§3.3), keeping the hot path tiny.
    TimeControlId
);
str_newtype!(
    /// An opaque auth token minted by X-3 (`str`, ≤ 4096). Carried opaquely;
    /// never logged (§7).
    AuthToken
);
str_newtype!(
    /// A user id — UUID text (`str36`, §4A.8).
    UserId
);

// --- frozen u8 enum tables (serialized as exactly one byte) -----------------

/// Macro for a §4A "enum8": a `u8`-backed newtype that serializes as its single
/// byte, with named frozen constants and a known-value check for decode.
macro_rules! enum8 {
    (
        $(#[$m:meta])*
        $name:ident { $( $(#[$cm:meta])* $cname:ident = $cval:expr ),+ $(,)? }
    ) => {
        $(#[$m])*
        #[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
        #[serde(transparent)]
        pub struct $name(pub u8);

        impl $name {
            // The §4A.9 event-type constants keep the spec's exact PascalCase
            // names (GameCreated, PlayerJoined, …) so the code reads 1:1 against
            // the frozen table; the per-const allow silences the upper-case lint
            // for those (the reason/termination/error tables use UPPER names).
            $( $(#[$cm])* #[allow(non_upper_case_globals)] pub const $cname: $name = $name($cval); )+

            /// The exact frozen byte value (§4A).
            pub fn value(self) -> u8 {
                self.0
            }

            /// True iff this is one of the frozen v1 values. The decode path
            /// uses this to reject an unknown discriminant with a typed error
            /// rather than silently accepting it (SPEC-S2-PROTOCOL §8).
            ///
            /// The values are enumerated one-by-one on purpose: §4A reserves
            /// unused codes for additive v1 events/variants, so a future gap
            /// must NOT be silently accepted. Collapsing them to a numeric range
            /// (which clippy suggests only because the v1 values happen to be
            /// contiguous today) would be exactly that latent bug.
            #[allow(clippy::manual_range_patterns)]
            pub fn is_known(self) -> bool {
                matches!(self.0, $( $cval )|+)
            }
        }
    };
}

enum8!(
    /// `MOVE_REJECTED.reason` — §4A.4. `0x00` is reserved/unused.
    Reason {
        /// 0x01 — the move is illegal per the engine.
        ILLEGAL = 0x01,
        /// 0x02 — `ply_expected` did not match the server's current ply.
        STALE_PLY = 0x02,
        /// 0x03 — the game is already over.
        GAME_OVER = 0x03,
        /// 0x04 — it is not this seat's turn.
        NOT_YOUR_TURN = 0x04,
        /// 0x05 — the mover is not seated in this game.
        NOT_YOUR_SEAT = 0x05,
        /// 0x06 — no such game.
        UNKNOWN_GAME = 0x06,
    }
);

enum8!(
    /// `GAME_END.termination` and `GameEnded.termination` — §4A.5. Matches
    /// `Game.termination` in SPEC-S3-DATA-MODEL §3.4.
    Termination {
        /// 0x01 — checkmate.
        MATE = 0x01,
        /// 0x02 — a player resigned.
        RESIGN = 0x02,
        /// 0x03 — a player flagged (ran out of time).
        FLAG = 0x03,
        /// 0x04 — the game was aborted.
        ABORT = 0x04,
        /// 0x05 — draw by agreement.
        DRAW_AGREE = 0x05,
        /// 0x06 — stalemate.
        STALEMATE = 0x06,
        /// 0x07 — threefold repetition.
        REPETITION = 0x07,
        /// 0x08 — fifty-move rule.
        FIFTYMOVE = 0x08,
        /// 0x09 — insufficient material.
        INSUFFICIENT = 0x09,
        /// 0x0A — timeout vs insufficient material.
        TIMEOUT_VS_INSUFFICIENT = 0x0A,
    }
);

enum8!(
    /// `ERROR.code` — §4A.7.
    ErrorCode {
        /// 0x01 — the frame's `ver` is unsupported; socket closes.
        UNSUPPORTED_VERSION = 0x01,
        /// 0x02 — wrong-direction or malformed frame.
        PROTOCOL_VIOLATION = 0x02,
        /// 0x03 — no such game.
        UNKNOWN_GAME = 0x03,
        /// 0x04 — not authenticated.
        UNAUTHENTICATED = 0x04,
        /// 0x05 — rate limited.
        RATE_LIMITED = 0x05,
        /// 0x06 — internal server error.
        INTERNAL = 0x06,
    }
);

enum8!(
    /// Event `type` — §4A.9. Carried in the event-store envelope.
    EventType {
        /// 0x01
        GameCreated = 0x01,
        /// 0x02
        PlayerJoined = 0x02,
        /// 0x03
        MovePlayed = 0x03,
        /// 0x04
        DrawOffered = 0x04,
        /// 0x05
        DrawAccepted = 0x05,
        /// 0x06
        Resigned = 0x06,
        /// 0x07
        Aborted = 0x07,
        /// 0x08
        TakebackReq = 0x08,
        /// 0x09
        TakebackAccepted = 0x09,
        /// 0x0A
        ClockFlagged = 0x0A,
        /// 0x0B
        GameEnded = 0x0B,
        /// 0x0C
        RatingApplied = 0x0C,
    }
);

// --- shared value structs (§4A.3, §4A.6, §4A.8) -----------------------------

/// A single game-result entry per seat (`result_vector` element). Encoded as a
/// raw `u8`: `0 = loss, 1 = draw, 2 = win` (§4A.3). Kept as a `u8` field (not a
/// Rust enum) so the wire byte is exactly the frozen value and an N-seat vector
/// serializes as the postcard `[u8]` the spec names.
pub type SeatResult = u8;

/// `loss` result-vector value (§4A.3).
pub const RESULT_LOSS: SeatResult = 0;
/// `draw` result-vector value (§4A.3).
pub const RESULT_DRAW: SeatResult = 1;
/// `win` result-vector value (§4A.3).
pub const RESULT_WIN: SeatResult = 2;

/// True iff `v` is a frozen `result_vector` value (`0`/`1`/`2`, §4A.3). The
/// decode path uses this to reject an out-of-domain result byte — the same
/// "reject rather than accept garbage" rule the enum8 tables get (§8), applied
/// to this closed-domain `[u8]` field (a mis-decoded result mis-rules a game).
pub fn is_known_result(v: SeatResult) -> bool {
    matches!(v, RESULT_LOSS | RESULT_DRAW | RESULT_WIN)
}

/// `color` value inside `GameCreated.seats` (§4A.8): `0 = white, 1 = black`
/// (N-seat variants extend). Raw `u8` to pin the wire byte.
pub type Color = u8;

/// White color value (§4A.8).
pub const COLOR_WHITE: Color = 0;
/// Black color value (§4A.8).
pub const COLOR_BLACK: Color = 1;

/// True iff `c` is a frozen v1 `color` value (`0`/`1`, §4A.8). Validated on
/// decode for the same reason as [`is_known_result`]. Note: §4A.8 says "N-seat
/// variants extend" the color space — but v1 is Standard 2-seat (§1), and a
/// variant that adds colors is a versioned change (§5), so v1 decode rejects
/// anything outside {0,1} rather than silently accepting a value no v1 consumer
/// can interpret.
pub fn is_known_color(c: Color) -> bool {
    matches!(c, COLOR_WHITE | COLOR_BLACK)
}

/// `Aborted.by` sentinel for a system-initiated abort (§4A.8): `0xFF`.
pub const ABORT_BY_SYSTEM: u8 = 0xFF;

/// The `STATE` body from `fen` onward (§4A.6). Reused verbatim as
/// `MATCHED.start_state` (Q-P4 DECIDED) — one state format, no second encoding.
/// Field order: `fen, turn, clocks`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct StateBody {
    /// Full-position FEN (`str`, ≤ 90). Only ever carried in STATE resync,
    /// never on the move hot path (§6).
    pub fen: String,
    /// Side-to-move seat index (`turn:u8`).
    pub turn: u8,
    /// Per-seat remaining clock in ms (`[u32; seat_count]`). A `Vec` so v1's
    /// 2-seat and ADR-3 N-seat both encode as the postcard length-prefixed
    /// sequence the spec names (`[u32; seat_count]`).
    pub clocks: Vec<u32>,
}

/// One seat descriptor inside `GameCreated.seats` (§4A.8): `{seat_idx, color}`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SeatColor {
    /// 0-based seat index.
    pub seat_idx: u8,
    /// `COLOR_WHITE` / `COLOR_BLACK` (§4A.8).
    pub color: Color,
}

/// One per-seat rating change inside `RatingApplied.per_seat` (§4A.8):
/// `{seat_idx, before, after, rd, vol}`. `vol` is the only float in the whole
/// contract (postcard encodes `f32` as 4 fixed LE bytes).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SeatRating {
    /// 0-based seat index.
    pub seat_idx: u8,
    /// Rating before (`i16`).
    pub before: i16,
    /// Rating after (`i16`).
    pub after: i16,
    /// Rating deviation (`u16`).
    pub rd: u16,
    /// Volatility (`f32`).
    pub vol: f32,
}
