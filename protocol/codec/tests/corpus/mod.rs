//! The canonical golden corpus: the single list of named `(name, value)` pairs
//! that the committed `protocol/vectors/vectors.json` is generated from and the
//! golden-freeze test checks against. Shared by the `gen_vectors` example (which
//! writes the JSON) and the `golden_freeze` test (which asserts the live codec
//! reproduces every committed hex byte-for-byte).
//!
//! Coverage obligation (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.2): EVERY §4A message
//! type and EVERY event type appears here at least once, plus the named edge
//! values — max-length FEN in STATE, 5-char promotion UCI (`e7e8q`), an
//! N-seat-ready result_vector, a daily per-move time-control id, and a
//! GameEnded carrying a full termination enum.

use pgc_protocol::{
    ClientMsg, ErrorCode, EventEnvelope, GameEvent, GameId, PoolId, Reason, SeatColor, SeatRating,
    ServerMsg, StateBody, Termination, TimeControlId,
};

/// One encodable item in the corpus, tagged by its codec direction/kind.
pub enum Item {
    Client(ClientMsg),
    Server(ServerMsg),
    Event(EventEnvelope),
}

/// Encode an item with the matching codec entry point.
pub fn encode_item(item: &Item) -> Vec<u8> {
    match item {
        Item::Client(m) => pgc_protocol::encode_client(m).expect("corpus client encodes"),
        Item::Server(m) => pgc_protocol::encode_server(m).expect("corpus server encodes"),
        Item::Event(e) => pgc_protocol::encode_event(e).expect("corpus event encodes"),
    }
}

/// A representative UUID-text game id used throughout the corpus (36 chars).
pub const GAME_UUID: &str = "f47ac10b-58cc-4372-a567-0e02b2c3d479";
/// A representative user id (36 chars).
pub const USER_UUID: &str = "7c9e6679-7425-40de-944b-e07fc1f90ae7";

/// A long, VALID standard FEN for the STATE resync max-length edge value
/// (`fen` ≤ 90 chars, §4A.3; PLAN-CODEC §4.2). A dense all-pieces-on-board
/// position with an en-passant target square and multi-digit halfmove/fullmove
/// counters — ~70 chars, the realistic upper end for a standard FEN (the §4A.3
/// 90-char cap is headroom). The codec does not validate FEN (the engine does,
/// §3.1.1), so this only needs to be a realistic max-length string.
pub const MAX_FEN: &str = "rnbqkbnr/pp1ppppp/8/2p5/4P3/5N2/PPPP1PPP/RNBQKB1R b KQkq e3 100 199";

/// Build the full ordered corpus. Order is stable so the JSON diff stays
/// readable when a vector is added (new entries append).
pub fn corpus() -> Vec<(&'static str, Item)> {
    let gid = || GameId::from(GAME_UUID);
    let state = || StateBody {
        fen: "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1".to_string(),
        turn: 1,
        clocks: vec![300_000, 300_000],
    };

    vec![
        // --- client → server messages (every §4A.2 C code) ---
        (
            "client/authenticate",
            Item::Client(ClientMsg::Authenticate {
                token: "eyJhbGciOiJIUzI1Ni9.header.sig".into(),
            }),
        ),
        (
            "client/seek",
            Item::Client(ClientMsg::Seek {
                pool_id: PoolId::from("blitz-standard"),
                time_control: TimeControlId::from("blitz-5+3"),
                rated: true,
            }),
        ),
        (
            "client/move",
            Item::Client(ClientMsg::Move {
                game_id: gid(),
                ply_expected: 17,
                uci: "e2e4".to_string(),
            }),
        ),
        (
            // Edge: 5-char promotion UCI (PLAN-CODEC §4.2).
            "client/move_promotion_5char",
            Item::Client(ClientMsg::Move {
                game_id: gid(),
                ply_expected: 103,
                uci: "e7e8q".to_string(),
            }),
        ),
        (
            "client/offer_draw",
            Item::Client(ClientMsg::OfferDraw { game_id: gid() }),
        ),
        (
            "client/resign",
            Item::Client(ClientMsg::Resign { game_id: gid() }),
        ),
        (
            "client/abort",
            Item::Client(ClientMsg::Abort { game_id: gid() }),
        ),
        (
            "client/takeback",
            Item::Client(ClientMsg::Takeback { game_id: gid() }),
        ),
        (
            "client/rematch",
            Item::Client(ClientMsg::Rematch { game_id: gid() }),
        ),
        (
            "client/ping",
            Item::Client(ClientMsg::Ping {
                t: 1_728_000_000_123,
            }),
        ),
        (
            // §4A v1.1 (ADR-17).
            "client/resync",
            Item::Client(ClientMsg::Resync { game_id: gid() }),
        ),
        // --- server → client messages (every §4A.2 S code) ---
        (
            "server/matched",
            Item::Server(ServerMsg::Matched {
                game_id: gid(),
                seat_idx: 0,
                opponent: "grandmaster_jane".to_string(),
                start_state: StateBody {
                    fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1".to_string(),
                    turn: 0,
                    clocks: vec![300_000, 300_000],
                },
            }),
        ),
        (
            "server/move_accepted",
            Item::Server(ServerMsg::MoveAccepted {
                game_id: gid(),
                ply: 18,
                uci: "e2e4".to_string(),
                clock_after_ms: 297_500,
            }),
        ),
        (
            "server/move_rejected",
            Item::Server(ServerMsg::MoveRejected {
                game_id: gid(),
                ply_expected: 18,
                reason: Reason::STALE_PLY,
            }),
        ),
        (
            // Edge: max-length FEN in STATE (PLAN-CODEC §4.2).
            "server/state_max_fen",
            Item::Server(ServerMsg::State {
                game_id: gid(),
                state: StateBody {
                    fen: MAX_FEN.to_string(),
                    turn: 0,
                    clocks: vec![1_000, 2_000],
                },
            }),
        ),
        (
            "server/state",
            Item::Server(ServerMsg::State {
                game_id: gid(),
                state: state(),
            }),
        ),
        (
            // Edge: a GameEnded (GAME_END) carrying a full termination enum +
            // signed negative rating_delta (PLAN-CODEC §4.2).
            "server/game_end",
            Item::Server(ServerMsg::GameEnd {
                game_id: gid(),
                result_vector: vec![pgc_protocol::RESULT_WIN, pgc_protocol::RESULT_LOSS],
                termination: Termination::TIMEOUT_VS_INSUFFICIENT,
                rating_delta: vec![12, -12],
            }),
        ),
        (
            "server/pong",
            Item::Server(ServerMsg::Pong {
                t: 1_728_000_000_123,
                server_t: 1_728_000_000_456,
            }),
        ),
        (
            // §4A v1.1 (ADR-17).
            "server/draw_offered",
            Item::Server(ServerMsg::DrawOffered {
                game_id: gid(),
                by: 1,
            }),
        ),
        (
            "server/error",
            Item::Server(ServerMsg::Error {
                code: ErrorCode::RATE_LIMITED,
                msg: "slow down".to_string(),
            }),
        ),
        // --- events (every §4A.9 event type) ---
        (
            "event/game_created",
            Item::Event(ev(
                1,
                0,
                GameEvent::GameCreated {
                    pool_id: PoolId::from("blitz-standard"),
                    rated: true,
                    seats: vec![
                        SeatColor {
                            seat_idx: 0,
                            color: pgc_protocol::COLOR_WHITE,
                        },
                        SeatColor {
                            seat_idx: 1,
                            color: pgc_protocol::COLOR_BLACK,
                        },
                    ],
                    start_fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
                        .to_string(),
                    time_control: TimeControlId::from("blitz-5+3"),
                },
            )),
        ),
        (
            // Edge: a daily per-move time-control id (PLAN-CODEC §4.2).
            "event/game_created_daily",
            Item::Event(ev(
                7,
                0,
                GameEvent::GameCreated {
                    pool_id: PoolId::from("daily-standard"),
                    rated: false,
                    seats: vec![
                        SeatColor {
                            seat_idx: 0,
                            color: pgc_protocol::COLOR_WHITE,
                        },
                        SeatColor {
                            seat_idx: 1,
                            color: pgc_protocol::COLOR_BLACK,
                        },
                    ],
                    start_fen: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
                        .to_string(),
                    time_control: TimeControlId::from("daily-3"),
                },
            )),
        ),
        (
            "event/player_joined",
            Item::Event(ev(
                2,
                0,
                GameEvent::PlayerJoined {
                    seat_idx: 1,
                    user_id: pgc_protocol::UserId::from(USER_UUID),
                },
            )),
        ),
        (
            "event/move_played",
            Item::Event(ev(
                3,
                0,
                GameEvent::MovePlayed {
                    ply: 1,
                    seat_idx: 0,
                    uci: "e2e4".to_string(),
                    clock_after_ms: 299_000,
                    server_ts: 1_728_000_000_000,
                },
            )),
        ),
        (
            "event/move_played_promotion",
            Item::Event(ev(
                55,
                0,
                GameEvent::MovePlayed {
                    ply: 103,
                    seat_idx: 1,
                    uci: "e7e8q".to_string(),
                    clock_after_ms: 42_000,
                    server_ts: 1_728_000_600_000,
                },
            )),
        ),
        (
            "event/draw_offered",
            Item::Event(ev(4, 0, GameEvent::DrawOffered { seat_idx: 0 })),
        ),
        (
            "event/draw_accepted",
            Item::Event(ev(5, 0, GameEvent::DrawAccepted { seat_idx: 1 })),
        ),
        (
            "event/resigned",
            Item::Event(ev(6, 0, GameEvent::Resigned { seat_idx: 0 })),
        ),
        (
            "event/aborted_by_seat",
            Item::Event(ev(7, 0, GameEvent::Aborted { by: 1 })),
        ),
        (
            // Edge: system-initiated abort sentinel 0xFF (§4A.8).
            "event/aborted_by_system",
            Item::Event(ev(
                7,
                0,
                GameEvent::Aborted {
                    by: pgc_protocol::ABORT_BY_SYSTEM,
                },
            )),
        ),
        (
            "event/takeback_req",
            Item::Event(ev(8, 0, GameEvent::TakebackReq { seat_idx: 0 })),
        ),
        (
            "event/takeback_accepted",
            Item::Event(ev(9, 0, GameEvent::TakebackAccepted { seat_idx: 1 })),
        ),
        (
            "event/clock_flagged",
            Item::Event(ev(10, 0, GameEvent::ClockFlagged { seat_idx: 0 })),
        ),
        (
            "event/game_ended",
            Item::Event(ev(
                11,
                0,
                GameEvent::GameEnded {
                    result_vector: vec![pgc_protocol::RESULT_DRAW, pgc_protocol::RESULT_DRAW],
                    termination: Termination::REPETITION,
                },
            )),
        ),
        (
            "event/rating_applied",
            Item::Event(ev(
                12,
                0,
                GameEvent::RatingApplied {
                    pool_id: PoolId::from("blitz-standard"),
                    per_seat: vec![
                        SeatRating {
                            seat_idx: 0,
                            before: 1500,
                            after: 1512,
                            rd: 60,
                            vol: 0.06,
                        },
                        SeatRating {
                            seat_idx: 1,
                            before: 1500,
                            after: 1488,
                            rd: 60,
                            vol: 0.06,
                        },
                    ],
                },
            )),
        ),
    ]
}

/// Build an event envelope with a fixed `ts`/`schema_ver` so vectors are
/// deterministic.
fn ev(seq: u32, schema_ver: u16, event: GameEvent) -> EventEnvelope {
    EventEnvelope {
        stream_id: format!("game/{GAME_UUID}"),
        seq,
        schema_ver,
        ts: 1_728_000_000_000,
        event,
    }
}
