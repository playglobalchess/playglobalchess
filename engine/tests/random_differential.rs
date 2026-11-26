//! #202: random-position differential against `shakmaty` for every engine
//! query the server and clients use (ENGINE-CORE §8.7). DEV-ONLY: the file
//! compiles only under `--features difftest`, so shakmaty is never a runtime
//! dependency (OPEN-A).
//!
//! The fixed corpora (six perft positions, E-1..E-14, golden games) only cover
//! the position classes someone thought of. #119 (en passant ignoring pins),
//! #120 (impossible FENs) and #121 (mating material) all sat outside them.
//! This test plays seeded random games, places random sparse endgames, and
//! compares, position by position: the legal-move set, check / checkmate /
//! stalemate, insufficient material, `has_mating_material` per colour, the FEN
//! round trip, SAN of every legal move, a sampled perft(2), and Zobrist
//! identity (our keys are equal exactly when shakmaty's are). A second arm
//! mutates FENs and requires accept/reject agreement, apart from the
//! documented deliberate leniencies.
//!
//! Run (per commit, fixed seed, 6000 positions):
//!   `cargo test --release --features difftest --test random_differential`
//! Reproduce a failure (the failing seed and FEN are printed):
//!   `PGC_DIFF_SEED=<seed> cargo test --release --features difftest --test random_differential -- --nocapture`
//! Nightly: `PGC_DIFF_POSITIONS=1000000 PGC_DIFF_SEED=<rotating>`.

#![cfg(feature = "difftest")]

use pgc_engine::{
    has_mating_material, is_in_check, is_insufficient_material, legal_moves, make, move_to_san,
    parse_fen, perft, status, to_fen, uci_to_move, Color, GameStatus, Position,
};
use shakmaty::{
    fen::Fen, san::SanPlus, uci::UciMove, zobrist::Zobrist64, CastlingMode, Chess, EnPassantMode,
    Position as _,
};
use std::collections::HashMap;

const DEFAULT_SEED: u64 = 0x2025_0202_C0FF_EE01;
const DEFAULT_POSITIONS: u64 = 6_000;
const DEFAULT_INVALID_FENS: u64 = 3_000;

const CORPUS: [&str; 6] = [
    "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
    "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
    "8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1",
    "r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1",
    "rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8",
    "r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10",
];

/// xorshift64*: tiny, seedable, no extra dependency.
struct Rng(u64);
impl Rng {
    fn new(seed: u64) -> Self {
        Rng(seed | 1)
    }
    fn next(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.0 = x;
        x.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }
    fn below(&mut self, n: u64) -> u64 {
        self.next() % n
    }
}

fn env_u64(name: &str, default: u64) -> u64 {
    match std::env::var(name) {
        Ok(v) => {
            let v = v.trim();
            let parsed = match v.strip_prefix("0x") {
                Some(h) => u64::from_str_radix(h, 16),
                None => v.parse(),
            };
            parsed.unwrap_or_else(|_| panic!("{name}={v:?} is not a number"))
        }
        Err(_) => default,
    }
}

/// shakmaty position for a FEN. Material-count and impossible-check rules are
/// ignored: we deliberately do not check them (ENGINE-CORE §5).
fn shak(fen: &str) -> Result<Chess, ()> {
    let setup: Fen = fen.parse().expect("shakmaty: FEN syntax");
    setup
        .into_position::<Chess>(CastlingMode::Standard)
        .or_else(shakmaty::PositionError::ignore_too_much_material)
        .or_else(shakmaty::PositionError::ignore_impossible_check)
        .map_err(|_| ())
}

fn fail(seed: u64, fen: &str, what: &str) -> ! {
    panic!(
        "differential mismatch: {what}\n  FEN: {fen}\n  reproduce: PGC_DIFF_SEED={seed:#x} cargo test --release --features difftest --test random_differential -- --nocapture"
    );
}

/// Zobrist identity: our key and shakmaty's must induce the same equality
/// relation over every position seen in the run.
#[derive(Default)]
struct KeyIdentity {
    ours_to_ref: HashMap<u64, u64>,
    ref_to_ours: HashMap<u64, u64>,
}

impl KeyIdentity {
    fn check(&mut self, ours: u64, reference: u64) -> bool {
        *self.ours_to_ref.entry(ours).or_insert(reference) == reference
            && *self.ref_to_ours.entry(reference).or_insert(ours) == ours
    }
}

/// Compare every query on one position. `fen` is the position's FEN as the
/// reference sees it (used for reporting and for the shakmaty side).
fn check_position(seed: u64, ours: &Position, reference: &Chess, ids: &mut KeyIdentity, n: u64) {
    let fen = to_fen(ours);

    // FEN round trip: exported text must equal shakmaty's (strict ep, #E-1).
    let ref_fen = Fen::from_position(reference, EnPassantMode::Legal).to_string();
    if fen != ref_fen {
        fail(
            seed,
            &fen,
            &format!("FEN export: ours={fen} shakmaty={ref_fen}"),
        );
    }
    match parse_fen(&fen) {
        Ok(p) if to_fen(&p) == fen => {}
        other => fail(seed, &fen, &format!("FEN round trip failed: {other:?}")),
    }

    // Legal move set (UCI).
    let mut our_moves: Vec<String> = legal_moves(ours).iter().map(|m| m.to_uci()).collect();
    let ref_list = reference.legal_moves();
    let mut ref_moves: Vec<String> = ref_list
        .iter()
        .map(|m| UciMove::from_move(*m, CastlingMode::Standard).to_string())
        .collect();
    our_moves.sort();
    ref_moves.sort();
    if our_moves != ref_moves {
        fail(
            seed,
            &fen,
            &format!("legal moves differ: ours={our_moves:?} shakmaty={ref_moves:?}"),
        );
    }

    // SAN of every legal move, including check / mate suffixes.
    for rm in ref_list.iter() {
        let uci = UciMove::from_move(*rm, CastlingMode::Standard).to_string();
        let our_move =
            uci_to_move(ours, &uci).unwrap_or_else(|e| fail(seed, &fen, &format!("{uci}: {e:?}")));
        let ours_san = move_to_san(ours, our_move);
        let ref_san = SanPlus::from_move(reference.clone(), *rm).to_string();
        if ours_san != ref_san {
            fail(
                seed,
                &fen,
                &format!("SAN of {uci}: ours={ours_san} shakmaty={ref_san}"),
            );
        }
    }

    // Check, mate, stalemate, insufficient material.
    if is_in_check(ours) != reference.is_check() {
        fail(seed, &fen, "is_in_check differs");
    }
    let st = status(ours);
    if (st == GameStatus::Checkmate) != reference.is_checkmate() {
        fail(seed, &fen, &format!("checkmate differs: ours={st:?}"));
    }
    if (st == GameStatus::Stalemate) != reference.is_stalemate() {
        fail(seed, &fen, &format!("stalemate differs: ours={st:?}"));
    }
    if is_insufficient_material(ours) != reference.is_insufficient_material() {
        fail(
            seed,
            &fen,
            &format!(
                "is_insufficient_material: ours={} shakmaty={}",
                is_insufficient_material(ours),
                reference.is_insufficient_material()
            ),
        );
    }
    for (color, sc) in [
        (Color::White, shakmaty::Color::White),
        (Color::Black, shakmaty::Color::Black),
    ] {
        let o = has_mating_material(ours, color);
        let r = !reference.has_insufficient_material(sc);
        if o != r {
            fail(
                seed,
                &fen,
                &format!("has_mating_material({color:?}): ours={o} shakmaty={r}"),
            );
        }
    }

    // Zobrist identity (repetition equality).
    let ref_key: u64 = reference
        .zobrist_hash::<Zobrist64>(EnPassantMode::Legal)
        .into();
    if !ids.check(ours.zobrist(), ref_key) {
        fail(seed, &fen, "Zobrist identity differs from shakmaty's");
    }

    // Sampled perft(2): catches make/unmake state errors the move set alone hides.
    if n % 8 == 0 {
        let mut p = ours.clone();
        let (a, b) = (perft(&mut p, 2), shakmaty::perft(reference, 2));
        if a != b {
            fail(seed, &fen, &format!("perft(2): ours={a} shakmaty={b}"));
        }
    }
}

/// Random game from `start`; every visited position is compared. Moves are
/// taken from the (already verified equal) move lists, biased to captures so
/// games thin out into sparse endings.
fn playout(
    rng: &mut Rng,
    seed: u64,
    start: &str,
    budget: &mut u64,
    total: &mut u64,
    ids: &mut KeyIdentity,
) {
    let mut ours = parse_fen(start)
        .unwrap_or_else(|e| fail(seed, start, &format!("corpus FEN rejected: {e:?}")));
    let mut reference =
        shak(start).unwrap_or_else(|e| fail(seed, start, &format!("shakmaty rejected: {e:?}")));
    let max_plies = 20 + rng.below(160);
    for _ in 0..max_plies {
        if *budget == 0 {
            return;
        }
        check_position(seed, &ours, &reference, ids, *total);
        *budget -= 1;
        *total += 1;
        let moves = reference.legal_moves();
        if moves.is_empty() {
            return;
        }
        let captures: Vec<_> = moves.iter().filter(|m| m.is_capture()).collect();
        let pick = if !captures.is_empty() && rng.below(100) < 35 {
            *captures[rng.below(captures.len() as u64) as usize]
        } else {
            moves[rng.below(moves.len() as u64) as usize]
        };
        let uci = UciMove::from_move(pick, CastlingMode::Standard).to_string();
        let our_move = uci_to_move(&ours, &uci)
            .unwrap_or_else(|e| fail(seed, &to_fen(&ours), &format!("{uci}: {e:?}")));
        make(&mut ours, our_move);
        reference.play_unchecked(pick);
    }
}

/// Random sparse placement: two kings plus 0..=6 other pieces on random squares.
fn random_endgame(rng: &mut Rng) -> String {
    let mut board = [' '; 64];
    let put = |rng: &mut Rng, c: char, pawn: bool, board: &mut [char; 64]| {
        for _ in 0..64 {
            let sq = rng.below(64) as usize;
            let rank = sq / 8;
            if board[sq] == ' ' && !(pawn && (rank == 0 || rank == 7)) {
                board[sq] = c;
                return;
            }
        }
    };
    put(rng, 'K', false, &mut board);
    put(rng, 'k', false, &mut board);
    let extra = rng.below(7);
    for _ in 0..extra {
        let kinds = ['p', 'n', 'b', 'r', 'q', 'n', 'b', 'b', 'n'];
        let mut c = kinds[rng.below(kinds.len() as u64) as usize];
        if rng.below(2) == 0 {
            c = c.to_ascii_uppercase();
        }
        put(rng, c, c.eq_ignore_ascii_case(&'p'), &mut board);
    }
    let mut out = String::new();
    for rank in (0..8).rev() {
        let mut empty = 0;
        for file in 0..8 {
            let c = board[rank * 8 + file];
            if c == ' ' {
                empty += 1;
            } else {
                if empty > 0 {
                    out.push_str(&empty.to_string());
                    empty = 0;
                }
                out.push(c);
            }
        }
        if empty > 0 {
            out.push_str(&empty.to_string());
        }
        if rank > 0 {
            out.push('/');
        }
    }
    let side = if rng.below(2) == 0 { 'w' } else { 'b' };
    format!("{out} {side} - - 0 1")
}

#[test]
fn random_positions_agree_with_shakmaty() {
    let seed = env_u64("PGC_DIFF_SEED", DEFAULT_SEED);
    let target = env_u64("PGC_DIFF_POSITIONS", DEFAULT_POSITIONS);
    println!("random differential: seed={seed:#x} positions={target}");
    let mut rng = Rng::new(seed);
    let mut ids = KeyIdentity::default();
    let (mut total, mut endgames, mut games) = (0u64, 0u64, 0u64);
    while total < target {
        let mut budget = target - total;
        if rng.below(100) < 50 {
            // Batch of sparse endings: where material and mate rules live.
            for _ in 0..25 {
                if total >= target {
                    break;
                }
                let fen = random_endgame(&mut rng);
                match (parse_fen(&fen), shak(&fen)) {
                    (Ok(ours), Ok(reference)) => {
                        check_position(seed, &ours, &reference, &mut ids, total);
                        total += 1;
                        endgames += 1;
                    }
                    (Err(_), Err(_)) => {}
                    (o, r) => fail(
                        seed,
                        &fen,
                        &format!(
                            "accept/reject differs: ours={:?} shakmaty_ok={}",
                            o.map(|_| ()),
                            r.is_ok()
                        ),
                    ),
                }
            }
        } else {
            let start = CORPUS[rng.below(CORPUS.len() as u64) as usize];
            playout(&mut rng, seed, start, &mut budget, &mut total, &mut ids);
            games += 1;
        }
    }
    println!("checked {total} positions ({games} playouts, {endgames} random endgames)");
}

/// Mutate a FEN field-wise. Returns a possibly-invalid FEN.
fn mutate(rng: &mut Rng, fen: &str) -> String {
    let mut f: Vec<String> = fen.split(' ').map(str::to_string).collect();
    match rng.below(6) {
        0 => {
            // swap two squares of the board
            let mut cells: Vec<char> = expand(&f[0]);
            let (a, b) = (rng.below(64) as usize, rng.below(64) as usize);
            cells.swap(a, b);
            f[0] = compress(&cells);
        }
        1 => {
            // overwrite one square with a random piece (or empty)
            let mut cells = expand(&f[0]);
            let pieces = [
                ' ', 'K', 'k', 'P', 'p', 'N', 'n', 'B', 'b', 'R', 'r', 'Q', 'q',
            ];
            cells[rng.below(64) as usize] = pieces[rng.below(pieces.len() as u64) as usize];
            f[0] = compress(&cells);
        }
        2 => {
            let rights = ["-", "K", "Q", "k", "q", "KQ", "kq", "KQkq", "Kq", "Qk"];
            f[2] = rights[rng.below(rights.len() as u64) as usize].to_string();
        }
        3 => {
            let file = (b'a' + rng.below(8) as u8) as char;
            let rank = [3, 6, 3, 6, 1, 4][rng.below(6) as usize];
            f[3] = format!("{file}{rank}");
        }
        4 => f[1] = if f[1] == "w" { "b".into() } else { "w".into() },
        _ => {
            // put a pawn on a back rank
            let mut cells = expand(&f[0]);
            let idx = if rng.below(2) == 0 {
                rng.below(8)
            } else {
                56 + rng.below(8)
            } as usize;
            cells[idx] = if rng.below(2) == 0 { 'P' } else { 'p' };
            f[0] = compress(&cells);
        }
    }
    f.join(" ")
}

/// Board field to 64 cells, rank 8 first (FEN order).
fn expand(placement: &str) -> Vec<char> {
    let mut v = Vec::with_capacity(64);
    for c in placement.chars() {
        match c {
            '/' => {}
            '1'..='8' => v.extend(std::iter::repeat(' ').take(c.to_digit(10).unwrap() as usize)),
            _ => v.push(c),
        }
    }
    v.resize(64, ' ');
    v
}

fn compress(cells: &[char]) -> String {
    let mut out = String::new();
    for (r, row) in cells.chunks(8).enumerate() {
        let mut empty = 0;
        for &c in row {
            if c == ' ' {
                empty += 1;
            } else {
                if empty > 0 {
                    out.push_str(&empty.to_string());
                    empty = 0;
                }
                out.push(c);
            }
        }
        if empty > 0 {
            out.push_str(&empty.to_string());
        }
        if r < 7 {
            out.push('/');
        }
    }
    out
}

#[test]
fn mutated_fens_agree_on_accept_reject() {
    let seed = env_u64("PGC_DIFF_SEED", DEFAULT_SEED) ^ 0xFE17;
    let count = env_u64("PGC_DIFF_INVALID_FENS", DEFAULT_INVALID_FENS);
    println!("invalid-FEN arm: seed={seed:#x} count={count}");
    let mut rng = Rng::new(seed);
    let (mut accepted, mut rejected) = (0u64, 0u64);
    for _ in 0..count {
        let base = CORPUS[rng.below(CORPUS.len() as u64) as usize];
        let mut fen = mutate(&mut rng, base);
        if rng.below(3) == 0 {
            fen = mutate(&mut rng, &fen);
        }
        let ours = parse_fen(&fen);
        let reference = match fen.parse::<Fen>() {
            Ok(setup) => setup
                .into_position::<Chess>(CastlingMode::Standard)
                .or_else(shakmaty::PositionError::ignore_too_much_material)
                .or_else(shakmaty::PositionError::ignore_impossible_check)
                .map_err(|_| ()),
            Err(_) => Err(()),
        };
        match (&ours, &reference) {
            (Ok(_), Ok(_)) => accepted += 1,
            (Err(_), Err(_)) => rejected += 1,
            _ => fail(
                seed,
                &fen,
                &format!(
                    "accept/reject differs: ours={:?} shakmaty_ok={}",
                    ours.as_ref().map(|_| ()),
                    reference.is_ok()
                ),
            ),
        }
        // Every accepted FEN must also pass the full position comparison.
        if let (Ok(o), Ok(r)) = (&ours, &reference) {
            check_position(seed, o, r, &mut KeyIdentity::default(), 1);
        }
    }
    println!("accepted {accepted}, rejected {rejected}");
    assert!(
        accepted > 100 && rejected > 100,
        "mutator must hit both outcomes"
    );
}
