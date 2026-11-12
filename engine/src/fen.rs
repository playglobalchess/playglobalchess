//! FEN parsing and emission (ENGINE-CORE §3.3, §4.1).
//!
//! **Parse is lenient** (OPEN-D): the ep field is stored exactly as written,
//! after any double push, so the internal state and `perft` match the §8.1
//! published counts. **Emit is strict**: the ep square is printed only when the
//! side to move actually has a pawn that could capture to it
//! ([`Position::ep_capture_available`]), matching Lichess/`scalachess`.
//!
//! A standard FEN has six space-separated fields:
//! `<placement> <stm> <castling> <ep> <halfmove> <fullmove>`.

use crate::castling::CastlingRights;
use crate::position::Position;
use crate::types::{Color, Piece, Square};
use thiserror::Error;

/// Typed FEN parse failure (ENGINE-CORE §5: never panic on malformed input).
#[derive(Debug, Error, PartialEq, Eq)]
pub enum FenError {
    #[error("FEN must have 6 space-separated fields, found {0}")]
    WrongFieldCount(usize),
    #[error("piece-placement field must have 8 ranks, found {0}")]
    WrongRankCount(usize),
    #[error("rank {rank} describes {files} files (expected 8)")]
    WrongFileCount { rank: usize, files: usize },
    #[error("invalid piece character '{0}' in placement")]
    BadPieceChar(char),
    #[error("invalid side-to-move '{0}' (expected 'w' or 'b')")]
    BadSideToMove(String),
    #[error("invalid castling field '{0}'")]
    BadCastling(String),
    #[error("invalid en-passant field '{0}'")]
    BadEnPassant(String),
    #[error("invalid halfmove clock '{0}'")]
    BadHalfmove(String),
    #[error("invalid fullmove number '{0}'")]
    BadFullmove(String),
    // Semantic validation (ENGINE-CORE §5, #120): the six fields parsed, but
    // they do not describe a position that can occur in a game.
    #[error("{color:?} must have exactly one king, found {count}")]
    KingCount { color: Color, count: u32 },
    #[error("pawn on a back rank at {0}")]
    PawnOnBackRank(Square),
    #[error("castling right '{0}' without the king and rook on their home squares")]
    CastlingWithoutPieces(char),
    #[error("en-passant square '{0}' could not follow a double pawn push")]
    ImpossibleEnPassant(Square),
    #[error("the side not to move is in check")]
    OpponentInCheck,
}

/// Parse a FEN string into a [`Position`] (lenient ep — OPEN-D, §3.3).
pub fn parse_fen(s: &str) -> Result<Position, FenError> {
    let fields: Vec<&str> = s.split_ascii_whitespace().collect();
    if fields.len() != 6 {
        return Err(FenError::WrongFieldCount(fields.len()));
    }

    let mut pos = Position::empty();

    // Field 1: piece placement, rank 8 → rank 1 (FEN top-to-bottom).
    let ranks: Vec<&str> = fields[0].split('/').collect();
    if ranks.len() != 8 {
        return Err(FenError::WrongRankCount(ranks.len()));
    }
    for (i, rank_str) in ranks.iter().enumerate() {
        // FEN lists rank 8 first; LERF rank index 7 is rank 8.
        let rank = 7 - i as u8;
        let mut file: u8 = 0;
        for c in rank_str.chars() {
            if let Some(skip) = c.to_digit(10) {
                // Bound before adding (#148): an unchecked `u8` add panics in
                // debug and wraps in release, so a long run of digits was
                // rejected or accepted depending on the build profile.
                let files = u32::from(file) + skip;
                if files > 8 {
                    return Err(FenError::WrongFileCount {
                        rank: i,
                        files: files as usize,
                    });
                }
                file = files as u8;
            } else {
                let piece = Piece::from_fen_char(c).ok_or(FenError::BadPieceChar(c))?;
                let sq = Square::from_file_rank(file, rank).ok_or(FenError::WrongFileCount {
                    rank: i,
                    files: file as usize + 1,
                })?;
                pos.put_piece(sq, piece);
                file += 1;
            }
        }
        if file != 8 {
            return Err(FenError::WrongFileCount {
                rank: i,
                files: file as usize,
            });
        }
    }

    // Field 2: side to move.
    pos.side_to_move = match fields[1] {
        "w" => Color::White,
        "b" => Color::Black,
        other => return Err(FenError::BadSideToMove(other.to_string())),
    };

    // Field 3: castling rights.
    pos.castling_rights = CastlingRights::from_fen(fields[2])
        .ok_or_else(|| FenError::BadCastling(fields[2].to_string()))?;

    // Field 4: en-passant target (stored leniently — exactly as written).
    pos.ep_target = if fields[3] == "-" {
        None
    } else {
        Some(
            Square::from_coord(fields[3])
                .ok_or_else(|| FenError::BadEnPassant(fields[3].to_string()))?,
        )
    };

    // Field 5: halfmove clock.
    pos.halfmove_clock = fields[4]
        .parse::<u16>()
        .map_err(|_| FenError::BadHalfmove(fields[4].to_string()))?;

    // Field 6: fullmove number.
    pos.fullmove_number = fields[5]
        .parse::<u32>()
        .map_err(|_| FenError::BadFullmove(fields[5].to_string()))?;

    validate(&pos)?;
    Ok(pos)
}

/// Semantic checks after all six fields parsed (ENGINE-CORE §5, #120). A
/// position that cannot occur in a game would make move generation and `make`
/// misbehave (castling moving an enemy piece, ep deleting an arbitrary piece,
/// a missing king read as stalemate), so it is a typed error instead.
///
/// An ep square with no pawn able to capture is still accepted: the internal
/// target is lenient by design (OPEN-D, §3.3), and the perft corpus relies on it.
fn validate(pos: &Position) -> Result<(), FenError> {
    use crate::types::PieceType;
    for color in [Color::White, Color::Black] {
        let king = Piece::new(color, PieceType::King);
        let count = (0..64u8)
            .filter_map(Square::new)
            .filter(|&sq| pos.piece_at(sq) == Some(king))
            .count() as u32;
        if count != 1 {
            return Err(FenError::KingCount { color, count });
        }
    }
    for file in 0..8u8 {
        for rank in [0u8, 7] {
            let sq = Square::from_file_rank(file, rank).expect("on board");
            if matches!(pos.piece_at(sq), Some(p) if p.kind == PieceType::Pawn) {
                return Err(FenError::PawnOnBackRank(sq));
            }
        }
    }
    for (ch, color, rook_file) in [
        ('K', Color::White, 7u8),
        ('Q', Color::White, 0),
        ('k', Color::Black, 7),
        ('q', Color::Black, 0),
    ] {
        let right = match ch {
            'K' | 'k' => CastlingRights::king_side(color),
            _ => CastlingRights::queen_side(color),
        };
        if !pos.castling_rights.has(right) {
            continue;
        }
        let rank = if color == Color::White { 0 } else { 7 };
        let king_home = Square::from_file_rank(4, rank).expect("on board");
        let rook_home = Square::from_file_rank(rook_file, rank).expect("on board");
        if pos.piece_at(king_home) != Some(Piece::new(color, PieceType::King))
            || pos.piece_at(rook_home) != Some(Piece::new(color, PieceType::Rook))
        {
            return Err(FenError::CastlingWithoutPieces(ch));
        }
    }
    if let Some(ep) = pos.ep_target {
        // White to move: Black just pushed from rank 7 to 5 through rank 6.
        let (ep_rank, pawn_rank, origin_rank, pusher) = match pos.side_to_move {
            Color::White => (5u8, 4u8, 6u8, Color::Black),
            Color::Black => (2, 3, 1, Color::White),
        };
        let at = |r| Square::from_file_rank(ep.file(), r).expect("on board");
        if ep.rank() != ep_rank
            || pos.piece_at(ep).is_some()
            || pos.piece_at(at(origin_rank)).is_some()
            || pos.piece_at(at(pawn_rank)) != Some(Piece::new(pusher, PieceType::Pawn))
        {
            return Err(FenError::ImpossibleEnPassant(ep));
        }
    }
    let them = pos.side_to_move.flip();
    let their_king = pos.king_square(them).expect("one king checked above");
    if crate::movegen::is_square_attacked(pos, their_king, pos.side_to_move) {
        return Err(FenError::OpponentInCheck);
    }
    Ok(())
}

/// Emit a position as FEN — **strict ep** per OPEN-D (§3.3): the ep square is
/// printed only when the side to move can actually capture to it.
#[must_use]
pub fn to_fen(pos: &Position) -> String {
    let mut out = String::with_capacity(80);

    // Field 1: placement, rank 8 → rank 1.
    for rank in (0..8u8).rev() {
        let mut empties: u8 = 0;
        for file in 0..8u8 {
            let sq = Square::from_file_rank(file, rank).expect("file,rank < 8");
            match pos.piece_at(sq) {
                Some(piece) => {
                    if empties > 0 {
                        out.push(char::from(b'0' + empties));
                        empties = 0;
                    }
                    out.push(piece.to_fen_char());
                }
                None => empties += 1,
            }
        }
        if empties > 0 {
            out.push(char::from(b'0' + empties));
        }
        if rank > 0 {
            out.push('/');
        }
    }

    // Field 2: side to move.
    out.push(' ');
    out.push(match pos.side_to_move {
        Color::White => 'w',
        Color::Black => 'b',
    });

    // Field 3: castling.
    out.push(' ');
    out.push_str(&pos.castling_rights.to_fen());

    // Field 4: en passant — STRICT (OPEN-D): emit only if a legal-ish ep
    // capture is available to the side to move. `effective_ep` is the single
    // source of this rule, shared with position equality (§3.3, §3.7).
    out.push(' ');
    match pos.effective_ep() {
        Some(ep) => out.push_str(&ep.to_string()),
        None => out.push('-'),
    }

    // Fields 5, 6: clocks.
    out.push(' ');
    out.push_str(&pos.halfmove_clock.to_string());
    out.push(' ');
    out.push_str(&pos.fullmove_number.to_string());

    out
}

#[cfg(test)]
mod tests {
    use super::*;

    const STARTPOS: &str = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1";

    #[test]
    fn startpos_roundtrip_text() {
        let p = parse_fen(STARTPOS).unwrap();
        assert_eq!(to_fen(&p), STARTPOS);
    }

    #[test]
    fn kiwipete_roundtrip_text() {
        // Kiwipete carries no ep square, so strict emit == input text.
        let fen = "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1";
        let p = parse_fen(fen).unwrap();
        assert_eq!(to_fen(&p), fen);
    }

    #[test]
    fn strict_ep_dropped_when_no_capturer() {
        // Lenient parse keeps the ep square internally; strict emit drops it
        // because no White pawn can capture to d6 (OPEN-D).
        let lenient = "rnbqkbnr/ppp1pppp/8/3p4/8/8/PPPPPPPP/RNBQKBNR w KQkq d6 0 3";
        let p = parse_fen(lenient).unwrap();
        assert_eq!(p.ep_target, Square::from_coord("d6")); // internal: present
        let emitted = to_fen(&p);
        assert!(emitted.ends_with(" w KQkq - 0 3"), "got: {emitted}"); // exported: '-'
    }

    #[test]
    fn strict_ep_kept_when_capturer_present() {
        // White pawn on e5 can capture to d6 → strict emit keeps d6.
        let fen = "rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3";
        let p = parse_fen(fen).unwrap();
        assert_eq!(to_fen(&p), fen);
    }

    #[test]
    fn rejects_wrong_field_count() {
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 w"),
            Err(FenError::WrongFieldCount(_))
        ));
    }

    #[test]
    fn rejects_wrong_rank_count() {
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8 w - - 0 1"),
            Err(FenError::WrongRankCount(7))
        ));
    }

    #[test]
    fn rejects_bad_piece_char() {
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/7X w - - 0 1"),
            Err(FenError::BadPieceChar('X'))
        ));
    }

    #[test]
    fn rejects_short_rank() {
        // rank describes only 7 files
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/7 w - - 0 1"),
            Err(FenError::WrongFileCount { .. })
        ));
    }

    #[test]
    fn rejects_bad_side_castling_ep_clocks() {
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 x - - 0 1"),
            Err(FenError::BadSideToMove(_))
        ));
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 w Z - 0 1"),
            Err(FenError::BadCastling(_))
        ));
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 w - z9 0 1"),
            Err(FenError::BadEnPassant(_))
        ));
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 w - - x 1"),
            Err(FenError::BadHalfmove(_))
        ));
        assert!(matches!(
            parse_fen("8/8/8/8/8/8/8/8 w - - 0 x"),
            Err(FenError::BadFullmove(_))
        ));
    }

    /// #148: a rank made of many digits is rejected the same way in debug
    /// and release (it used to panic in debug and wrap to a valid rank in
    /// release).
    #[test]
    fn digit_run_overflow_is_rejected_not_wrapped() {
        let fen = format!("{}/8/8/8/8/8/8/8 w - - 0 1", "8".repeat(33));
        assert!(matches!(
            parse_fen(&fen),
            Err(FenError::WrongFileCount { rank: 0, .. })
        ));
        // The overflow is caught at the first digit that passes the 8th file.
        assert!(matches!(
            parse_fen("44p/8/8/8/8/8/8/8 w - - 0 1"),
            Err(FenError::WrongFileCount { rank: 0, .. })
        ));
        assert!(matches!(
            parse_fen("9/8/8/8/8/8/8/8 w - - 0 1"),
            Err(FenError::WrongFileCount { rank: 0, files: 9 })
        ));
    }

    /// #120: positions that cannot occur in a game are rejected with a
    /// distinct error, after all six fields have parsed.
    #[test]
    fn semantically_invalid_positions_are_rejected() {
        use crate::types::Color;
        let sq = |c: &str| Square::from_coord(c).unwrap();
        for (why, fen, want) in [
            // The issue's cases (a) to (f).
            (
                "right without own rook",
                "4k3/8/8/8/8/8/8/4K2n w K - 0 1",
                FenError::CastlingWithoutPieces('K'),
            ),
            (
                "no white king",
                "4k3/8/8/8/8/8/8/8 w - - 0 1",
                FenError::KingCount {
                    color: Color::White,
                    count: 0,
                },
            ),
            (
                "two black kings",
                "4k2k/8/8/8/8/8/8/4K3 w - - 0 1",
                FenError::KingCount {
                    color: Color::Black,
                    count: 2,
                },
            ),
            (
                "pawn on rank 1",
                "4k3/8/8/8/8/8/8/P3K3 w - - 0 1",
                FenError::PawnOnBackRank(sq("a1")),
            ),
            (
                "side not to move in check",
                "4k3/8/8/8/8/8/8/4K2r b - - 0 1",
                FenError::OpponentInCheck,
            ),
            (
                "ep on the wrong rank",
                "4k3/8/8/8/4P3/8/8/4K3 w - e4 0 1",
                FenError::ImpossibleEnPassant(sq("e4")),
            ),
            // Boundaries.
            (
                "pawn on rank 8",
                "3pk3/8/8/8/8/8/8/4K3 w - - 0 1",
                FenError::PawnOnBackRank(sq("d8")),
            ),
            (
                "queen-side right, rook moved",
                "r3k3/8/8/8/8/8/8/1R2K3 w Q - 0 1",
                FenError::CastlingWithoutPieces('Q'),
            ),
            (
                "black right, king off e8",
                "3k3r/8/8/8/8/8/8/4K3 w k - 0 1",
                FenError::CastlingWithoutPieces('k'),
            ),
            (
                "ep without the pushed pawn",
                "4k3/8/8/4P3/8/8/8/4K3 w - d6 0 1",
                FenError::ImpossibleEnPassant(sq("d6")),
            ),
            (
                "ep with the origin occupied",
                "4k3/3p4/8/3pP3/8/8/8/4K3 w - d6 0 1",
                FenError::ImpossibleEnPassant(sq("d6")),
            ),
            (
                "ep target occupied",
                "4k3/8/3n4/3pP3/8/8/8/4K3 w - d6 0 1",
                FenError::ImpossibleEnPassant(sq("d6")),
            ),
        ] {
            assert_eq!(parse_fen(fen).unwrap_err(), want, "{why}");
        }
    }

    /// The rules accept every legitimate case: the start position, all four
    /// rights with pieces home, a lenient ep square with no capturer (OPEN-D),
    /// the side to move in check, and the Black-to-move ep mirror.
    #[test]
    fn valid_positions_are_accepted() {
        for fen in [
            "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
            "r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1",
            "rnbqkbnr/ppp1pppp/8/3p4/8/8/PPPPPPPP/RNBQKBNR w KQkq d6 0 2",
            "4k3/8/8/8/8/8/8/4K2r w - - 0 1",
            "4k3/8/8/8/3Pp3/8/8/4K3 b - d3 0 1",
        ] {
            assert!(parse_fen(fen).is_ok(), "{fen}");
        }
    }
}
