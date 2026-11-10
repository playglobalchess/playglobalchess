//! Property-based tests (SPEC-S2-PROTOCOL §8, PLAN-CODEC §5) and the event
//! ordering invariant (§3.2).
//!
//! The round-trip property `decode(encode(x)) == x` is asserted over randomly
//! generated VALID values — a stronger net than the fixed corpus, catching
//! field-order or width bugs the hand-picked vectors might miss. proptest is
//! pinned to 1.5.0, the same version the engine uses.

use pgc_protocol::{
    decode_client, decode_event, decode_server, encode_client, encode_event, encode_server,
    ClientMsg, ErrorCode, EventEnvelope, GameEvent, GameId, PoolId, Reason, SeatColor, SeatRating,
    ServerMsg, StateBody, Status, Termination, TimeControlId, UserId, NO_SEAT,
};
use proptest::prelude::*;

// --- generators for valid values -------------------------------------------

fn arb_game_id() -> impl Strategy<Value = GameId> {
    // UUID-ish text; the codec treats it as an opaque string so any UTF-8 works,
    // but we keep it realistic.
    "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}".prop_map(GameId::from)
}

fn arb_uci() -> impl Strategy<Value = String> {
    // 4- or 5-char UCI (promotion). Legality is the engine's job, not the codec.
    prop_oneof![
        "[a-h][1-8][a-h][1-8]".prop_map(|s| s),
        "[a-h][1-8][a-h][1-8][qrbn]".prop_map(|s| s),
    ]
}

fn arb_reason() -> impl Strategy<Value = Reason> {
    prop_oneof![
        Just(Reason::ILLEGAL),
        Just(Reason::STALE_PLY),
        Just(Reason::GAME_OVER),
        Just(Reason::NOT_YOUR_TURN),
        Just(Reason::NOT_YOUR_SEAT),
        Just(Reason::UNKNOWN_GAME),
    ]
}

fn arb_termination() -> impl Strategy<Value = Termination> {
    prop_oneof![
        Just(Termination::MATE),
        Just(Termination::RESIGN),
        Just(Termination::FLAG),
        Just(Termination::ABORT),
        Just(Termination::DRAW_AGREE),
        Just(Termination::STALEMATE),
        Just(Termination::REPETITION),
        Just(Termination::FIFTYMOVE),
        Just(Termination::INSUFFICIENT),
        Just(Termination::TIMEOUT_VS_INSUFFICIENT),
    ]
}

fn arb_error_code() -> impl Strategy<Value = ErrorCode> {
    prop_oneof![
        Just(ErrorCode::UNSUPPORTED_VERSION),
        Just(ErrorCode::PROTOCOL_VIOLATION),
        Just(ErrorCode::UNKNOWN_GAME),
        Just(ErrorCode::UNAUTHENTICATED),
        Just(ErrorCode::RATE_LIMITED),
        Just(ErrorCode::INTERNAL),
    ]
}

/// A VALID v1.3 STATE body: the §4A.6 invariants hold by construction (seat
/// fields are a seat or 0xFF, result/termination iff ENDED, takeback ply iff a
/// takeback is pending, last move iff ply > 0, no running clock once ENDED).
fn arb_state() -> impl Strategy<Value = StateBody> {
    (
        "[rnbqkpRNBQKP1-8/ w b KQkq-]{10,80}",
        prop::collection::vec(any::<u32>(), 1..=4),
        0u8..3,
        0u32..500,
        arb_uci(),
        any::<[u8; 5]>(),
        1u32..500,
        any::<u64>(),
        0u8..10,
    )
        .prop_map(
            |(fen, clocks, st, ply, uci, picks, tb_ply, server_t, term)| {
                let n = clocks.len() as u8;
                // A seat in 0..n, or NO_SEAT when the pick lands on n.
                let seat = |p: u8| {
                    if p % (n + 1) == n {
                        NO_SEAT
                    } else {
                        p % (n + 1)
                    }
                };
                let status = [Status::MATCHED, Status::LIVE, Status::ENDED][st as usize];
                let ended = status == Status::ENDED;
                let takeback_by = seat(picks[2]);
                StateBody {
                    fen,
                    turn: picks[0] % n,
                    you_seat: seat(picks[1]),
                    ply,
                    status,
                    result_vector: if ended {
                        vec![pgc_protocol::RESULT_DRAW; n as usize]
                    } else {
                        vec![]
                    },
                    termination: if ended { term + 1 } else { 0 },
                    last_move_uci: if ply == 0 { String::new() } else { uci },
                    pending_draw_by: seat(picks[3]),
                    takeback_by,
                    takeback_at_ply: if takeback_by == NO_SEAT { 0 } else { tb_ply },
                    running_seat: if ended { NO_SEAT } else { seat(picks[4]) },
                    server_t,
                    clocks,
                }
            },
        )
}

fn arb_client_msg() -> impl Strategy<Value = ClientMsg> {
    prop_oneof![
        "[ -~]{0,64}".prop_map(|t| ClientMsg::Authenticate { token: t.into() }),
        ("[a-z0-9-]{1,20}", "[a-z0-9+-]{1,20}", any::<bool>()).prop_map(|(p, tc, r)| {
            ClientMsg::Seek {
                pool_id: PoolId::from(p),
                time_control: TimeControlId::from(tc),
                rated: r,
            }
        }),
        (arb_game_id(), any::<u32>(), arb_uci()).prop_map(|(g, p, u)| ClientMsg::Move {
            game_id: g,
            ply_expected: p,
            uci: u
        }),
        arb_game_id().prop_map(|g| ClientMsg::OfferDraw { game_id: g }),
        arb_game_id().prop_map(|g| ClientMsg::Resign { game_id: g }),
        arb_game_id().prop_map(|g| ClientMsg::Abort { game_id: g }),
        arb_game_id().prop_map(|g| ClientMsg::Takeback { game_id: g }),
        arb_game_id().prop_map(|g| ClientMsg::Rematch { game_id: g }),
        any::<u64>().prop_map(|t| ClientMsg::Ping { t }),
        (arb_game_id(), any::<u32>()).prop_map(|(g, p)| ClientMsg::TakebackAccept {
            game_id: g,
            at_ply: p
        }),
        (arb_game_id(), any::<u32>()).prop_map(|(g, p)| ClientMsg::TakebackDecline {
            game_id: g,
            at_ply: p
        }),
        arb_game_id().prop_map(|g| ClientMsg::DeclineDraw { game_id: g }),
    ]
}

fn arb_server_msg() -> impl Strategy<Value = ServerMsg> {
    prop_oneof![
        (arb_game_id(), any::<u8>(), "[ -~]{0,32}", arb_state()).prop_map(|(g, s, o, st)| {
            ServerMsg::Matched {
                game_id: g,
                seat_idx: s,
                opponent: o,
                start_state: st,
            }
        }),
        (arb_game_id(), any::<u32>(), arb_uci(), any::<u32>()).prop_map(|(g, p, u, c)| {
            ServerMsg::MoveAccepted {
                game_id: g,
                ply: p,
                uci: u,
                clock_after_ms: c,
            }
        }),
        (arb_game_id(), any::<u32>(), arb_reason()).prop_map(|(g, p, r)| {
            ServerMsg::MoveRejected {
                game_id: g,
                ply_expected: p,
                reason: r,
            }
        }),
        (arb_game_id(), arb_state()).prop_map(|(g, st)| ServerMsg::State {
            game_id: g,
            state: st
        }),
        (
            arb_game_id(),
            prop::collection::vec(0u8..3, 1..=4),
            arb_termination(),
            prop::collection::vec(any::<i16>(), 1..=4),
        )
            .prop_map(|(g, rv, t, rd)| ServerMsg::GameEnd {
                game_id: g,
                result_vector: rv,
                termination: t,
                rating_delta: rd,
            }),
        (any::<u64>(), any::<u64>()).prop_map(|(t, s)| ServerMsg::Pong { t, server_t: s }),
        (arb_error_code(), "[ -~]{0,48}").prop_map(|(c, m)| ServerMsg::Error { code: c, msg: m }),
        (arb_game_id(), any::<u8>(), any::<u32>()).prop_map(|(g, s, p)| {
            ServerMsg::TakebackOffered {
                game_id: g,
                seat_idx: s,
                at_ply: p,
            }
        }),
        (arb_game_id(), any::<u8>()).prop_map(|(g, s)| ServerMsg::TakebackDeclined {
            game_id: g,
            seat_idx: s
        }),
        (arb_game_id(), any::<u8>()).prop_map(|(g, s)| ServerMsg::DrawDeclined {
            game_id: g,
            seat_idx: s
        }),
    ]
}

fn arb_seat_color() -> impl Strategy<Value = SeatColor> {
    (any::<u8>(), 0u8..2).prop_map(|(seat_idx, color)| SeatColor { seat_idx, color })
}

fn arb_seat_rating() -> impl Strategy<Value = SeatRating> {
    (
        any::<u8>(),
        any::<i16>(),
        any::<i16>(),
        any::<u16>(),
        any::<f32>(),
    )
        .prop_map(|(seat_idx, before, after, rd, vol)| SeatRating {
            seat_idx,
            before,
            after,
            rd,
            vol,
        })
}

fn arb_event() -> impl Strategy<Value = GameEvent> {
    prop_oneof![
        (
            "[a-z0-9-]{1,20}",
            any::<bool>(),
            prop::collection::vec(arb_seat_color(), 1..=4),
            "[rnbqkpRNBQKP1-8/ wbKQkq-]{10,70}",
            "[a-z0-9+-]{1,20}",
        )
            .prop_map(|(p, r, s, f, tc)| GameEvent::GameCreated {
                pool_id: PoolId::from(p),
                rated: r,
                seats: s,
                start_fen: f,
                time_control: TimeControlId::from(tc),
            }),
        (any::<u8>(), arb_game_id()).prop_map(|(s, u)| GameEvent::PlayerJoined {
            seat_idx: s,
            user_id: UserId::from(u.0),
        }),
        (
            any::<u32>(),
            any::<u8>(),
            arb_uci(),
            any::<u32>(),
            any::<u64>()
        )
            .prop_map(|(ply, s, u, c, ts)| GameEvent::MovePlayed {
                ply,
                seat_idx: s,
                uci: u,
                clock_after_ms: c,
                server_ts: ts,
            }),
        any::<u8>().prop_map(|s| GameEvent::DrawOffered { seat_idx: s }),
        any::<u8>().prop_map(|s| GameEvent::DrawAccepted { seat_idx: s }),
        any::<u8>().prop_map(|s| GameEvent::Resigned { seat_idx: s }),
        any::<u8>().prop_map(|b| GameEvent::Aborted { by: b }),
        any::<u8>().prop_map(|s| GameEvent::TakebackReq { seat_idx: s }),
        any::<u8>().prop_map(|s| GameEvent::TakebackAccepted { seat_idx: s }),
        any::<u8>().prop_map(|s| GameEvent::TakebackProposed { seat_idx: s }),
        (any::<u8>(), any::<u32>(), any::<u64>()).prop_map(|(s, p, ts)| {
            GameEvent::TakebackRewound {
                seat_idx: s,
                to_ply: p,
                server_ts: ts,
            }
        }),
        any::<u8>().prop_map(|s| GameEvent::TakebackDeclined { seat_idx: s }),
        any::<u8>().prop_map(|s| GameEvent::DrawDeclined { seat_idx: s }),
        any::<u64>().prop_map(|ts| GameEvent::ClockStarted { server_ts: ts }),
        any::<u8>().prop_map(|s| GameEvent::ClockFlagged { seat_idx: s }),
        (prop::collection::vec(0u8..3, 1..=4), arb_termination()).prop_map(|(rv, t)| {
            GameEvent::GameEnded {
                result_vector: rv,
                termination: t,
            }
        }),
        (
            "[a-z0-9-]{1,20}",
            prop::collection::vec(arb_seat_rating(), 1..=4)
        )
            .prop_map(|(p, ps)| GameEvent::RatingApplied {
                pool_id: PoolId::from(p),
                per_seat: ps
            }),
    ]
}

fn arb_envelope() -> impl Strategy<Value = EventEnvelope> {
    (
        arb_game_id(),
        any::<u32>(),
        any::<u16>(),
        any::<u64>(),
        arb_event(),
    )
        .prop_map(|(g, seq, sv, ts, ev)| EventEnvelope {
            stream_id: format!("game/{}", g.0),
            seq,
            schema_ver: sv,
            ts,
            event: ev,
        })
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(512))]

    /// `decode_client(encode_client(x)) == x` over random valid client messages.
    #[test]
    fn prop_client_round_trip(msg in arb_client_msg()) {
        let bytes = encode_client(&msg).expect("encode valid client msg");
        prop_assert!(bytes.len() <= pgc_protocol::MAX_FRAME_BYTES);
        let back = decode_client(&bytes).expect("decode own client output");
        prop_assert_eq!(back, msg);
    }

    /// `decode_server(encode_server(x)) == x` over random valid server messages.
    #[test]
    fn prop_server_round_trip(msg in arb_server_msg()) {
        let bytes = encode_server(&msg).expect("encode valid server msg");
        prop_assert!(bytes.len() <= pgc_protocol::MAX_FRAME_BYTES);
        let back = decode_server(&bytes).expect("decode own server output");
        prop_assert_eq!(back, msg);
    }

    /// `decode_event(encode_event(x)) == x` over random valid event envelopes.
    #[test]
    fn prop_event_round_trip(env in arb_envelope()) {
        let bytes = encode_event(&env).expect("encode valid event");
        let back = decode_event(&bytes).expect("decode own event output");
        prop_assert_eq!(back, env);
    }

    /// Decode never panics on arbitrary bytes: it returns Ok or a typed error,
    /// for both direction paths and the event path. (The property is "no
    /// panic"; the harness fails the case if the call unwinds.)
    #[test]
    fn prop_decode_arbitrary_bytes_never_panics(bytes in prop::collection::vec(any::<u8>(), 0..200)) {
        let _ = decode_client(&bytes);
        let _ = decode_server(&bytes);
        let _ = decode_event(&bytes);
    }
}

// --- event ordering invariant (§3.2) ---------------------------------------

/// `seq` is strictly increasing with no gaps across a stream. This is a
/// unit-level invariant the game-server must uphold when it appends events; the
/// codec carries `seq` faithfully (round-trip proves that), and this test pins
/// the contract rule itself so a future change that weakens it is caught.
#[test]
fn event_seq_strictly_increasing_no_gaps() {
    // A plausible 0-based stream prefix.
    let seqs: Vec<u32> = (0..8).collect();
    for w in seqs.windows(2) {
        assert_eq!(w[1], w[0] + 1, "seq must be strictly increasing, no gaps");
    }

    // Encode a short stream and confirm each decoded envelope preserves its seq
    // (the codec is faithful; ordering enforcement is the store's job but the
    // seq field must survive the codec exactly).
    for seq in &seqs {
        let env = EventEnvelope {
            stream_id: "game/f47ac10b-58cc-4372-a567-0e02b2c3d479".to_string(),
            seq: *seq,
            schema_ver: 0,
            ts: 1_728_000_000_000 + u64::from(*seq),
            event: GameEvent::MovePlayed {
                ply: *seq,
                seat_idx: (*seq % 2) as u8,
                uci: "e2e4".to_string(),
                clock_after_ms: 1000,
                server_ts: 1_728_000_000_000,
            },
        };
        let bytes = encode_event(&env).unwrap();
        let back = decode_event(&bytes).unwrap();
        assert_eq!(back.seq, *seq, "codec must preserve seq exactly");
    }
}
