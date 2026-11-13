//! Zobrist hashing (ENGINE-CORE §3.7, OPEN-B) — a 64-bit key over **exactly**
//! the repetition-identity fields: piece placement, side to move, castling
//! rights, and en-passant *availability* (§3.7 / E-11). It is NOT a hash of the
//! clocks or the move number — two positions equal for repetition must share a
//! key, and the halfmove/fullmove counters are explicitly excluded (§3.7).
//!
//! # Seed stability (OPEN-B)
//! The random tables are generated at compile time from a **pinned** splitmix64
//! seed (`SEED`), so the key for a given position is identical across every
//! build and platform — required for golden tests and cross-client agreement
//! (§1.4.1, §6.2). Changing `SEED` invalidates stored keys, so it is frozen.
//!
//! Repetition is counted by this key alone, with no full-equality re-check: a
//! wrong ruling needs two different positions in one game to share a 64-bit
//! key, at most P²/2 · 2⁻⁶⁴ per game of P plies. ENGINE-CORE §5 records the
//! bound and why it is accepted.

use crate::position::Position;
use crate::types::Color;

/// Pinned seed for the table generator. **Do not change** — it fixes every
/// Zobrist key value platform-wide (OPEN-B seed stability).
const SEED: u64 = 0x9E37_79B9_7F4A_7C15;

/// splitmix64 — a tiny, high-quality `const` PRNG used only to fill the tables
/// deterministically at compile time.
const fn splitmix64(state: u64) -> (u64, u64) {
    let next = state.wrapping_add(0x9E37_79B9_7F4A_7C15);
    let mut z = next;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z = z ^ (z >> 31);
    (z, next)
}

/// All the random constants, generated once at compile time from `SEED`.
struct Keys {
    /// `[piece bb_index 0..12][square 0..64]`.
    pieces: [[u64; 64]; 12],
    /// XORed in when it is Black to move (White-to-move contributes nothing).
    black_to_move: u64,
    /// `[castling-rights bits 0..16]` — indexed by the 4-bit rights value.
    castling: [u64; 16],
    /// `[ep file 0..8]` — XORed in by the FILE of the ep-available square.
    ep_file: [u64; 8],
}

const KEYS: Keys = build_keys();

const fn build_keys() -> Keys {
    let mut st = SEED;
    let mut pieces = [[0u64; 64]; 12];
    let mut pi = 0;
    while pi < 12 {
        let mut sq = 0;
        while sq < 64 {
            let (v, s) = splitmix64(st);
            pieces[pi][sq] = v;
            st = s;
            sq += 1;
        }
        pi += 1;
    }
    let (black_to_move, s1) = splitmix64(st);
    st = s1;

    let mut castling = [0u64; 16];
    let mut ci = 0;
    while ci < 16 {
        let (v, s) = splitmix64(st);
        castling[ci] = v;
        st = s;
        ci += 1;
    }

    let mut ep_file = [0u64; 8];
    let mut fi = 0;
    while fi < 8 {
        let (v, s) = splitmix64(st);
        ep_file[fi] = v;
        st = s;
        fi += 1;
    }

    Keys {
        pieces,
        black_to_move,
        castling,
        ep_file,
    }
}

/// The Zobrist key of `pos` over the repetition-identity fields (§3.7).
///
/// Computed directly from state (not incrementally) — correctness-first; an
/// incremental update folded into make/unmake is a later-chunk optimization.
/// EP contributes only when a capture is actually available
/// ([`Position::effective_ep`]), matching the §3.3 strict/exported form that
/// repetition identity keys on.
#[must_use]
pub fn zobrist(pos: &Position) -> u64 {
    let mut h = 0u64;

    for sq in 0..64u8 {
        let square = crate::types::Square::new(sq).expect("sq < 64");
        if let Some(piece) = pos.piece_at(square) {
            h ^= KEYS.pieces[piece.bb_index()][sq as usize];
        }
    }

    if pos.side_to_move == Color::Black {
        h ^= KEYS.black_to_move;
    }

    h ^= KEYS.castling[(pos.castling_rights.bits() & 0b1111) as usize];

    if let Some(ep) = pos.effective_ep() {
        h ^= KEYS.ep_file[ep.file() as usize];
    }

    h
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::castling::CastlingRights as CR;

    #[test]
    fn equal_positions_same_key() {
        let a = Position::startpos();
        let b = Position::startpos();
        assert_eq!(zobrist(&a), zobrist(&b));
    }

    #[test]
    fn side_to_move_changes_key() {
        let mut a = Position::startpos();
        let before = zobrist(&a);
        a.side_to_move = Color::White.flip();
        assert_ne!(
            zobrist(&a),
            before,
            "flipping side to move must change the key"
        );
    }

    #[test]
    fn castling_rights_change_key_e11() {
        // E-11: identical placement, different castling rights ⇒ different key.
        let full = Position::startpos();
        let mut less = Position::startpos();
        less.castling_rights.remove(CR::WHITE_KING);
        assert_ne!(
            zobrist(&full),
            zobrist(&less),
            "E-11: castling-rights difference must change the Zobrist key"
        );
    }

    #[test]
    fn ep_availability_changes_key() {
        // ep available vs not: the strict/exported ep square is part of identity.
        let with_ep =
            Position::from_fen("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3")
                .unwrap();
        let no_ep =
            Position::from_fen("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq - 0 3")
                .unwrap();
        assert_ne!(
            zobrist(&with_ep),
            zobrist(&no_ep),
            "an available ep square must change the key"
        );
    }

    #[test]
    fn lenient_ep_without_capturer_does_not_change_key() {
        // A lenient ep target with NO legal capturer has effective_ep()==None,
        // so it must NOT perturb the key (keeps repetition identity consistent
        // with the strict/exported form, §3.3/§3.7).
        let no_capturer =
            Position::from_fen("rnbqkbnr/ppp1pppp/8/3p4/8/8/PPPPPPPP/RNBQKBNR w KQkq d6 0 3")
                .unwrap();
        let clean =
            Position::from_fen("rnbqkbnr/ppp1pppp/8/3p4/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 3")
                .unwrap();
        assert_eq!(
            zobrist(&no_capturer),
            zobrist(&clean),
            "a lenient ep square with no capturer must not change the key"
        );
    }

    #[test]
    fn clocks_do_not_change_key() {
        // halfmove/fullmove are NOT part of repetition identity (§3.7).
        let mut a = Position::startpos();
        let before = zobrist(&a);
        a.halfmove_clock = 40;
        a.fullmove_number = 25;
        assert_eq!(
            zobrist(&a),
            before,
            "clocks must not affect the Zobrist key"
        );
    }

    #[test]
    fn seed_is_pinned() {
        // Guards against an accidental SEED change: this is the startpos key
        // under the frozen seed. If this fails, SEED or the table-gen changed
        // and every stored key is invalidated (OPEN-B).
        assert_eq!(zobrist(&Position::startpos()), STARTPOS_KEY);
    }

    // Captured from the pinned seed; see `seed_is_pinned`.
    const STARTPOS_KEY: u64 = 0x63B5_CF5C_00A2_1380;
}
