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
                file += skip as u8;
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

    Ok(pos)
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
}
