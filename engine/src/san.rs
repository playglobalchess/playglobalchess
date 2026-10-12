//! Move encoding/decoding — UCI (wire/engine) and SAN (display/PGN)
//! (ENGINE-CORE §4.2, chunk 2 stage 2e).
//!
//! - **UCI** is the wire form S2 stores (`e2e4`, `e7e8q`, `e1g1` castling).
//!   [`move_to_uci`] is pure; [`uci_to_move`] is **position-aware** — it decodes
//!   the string AND validates it is a legal move in `pos` (chunk 1's
//!   `Move::from_uci` is a bare structural parse with no legality check).
//! - **SAN** is for display/PGN: [`move_to_san`] / [`san_to_move`] with
//!   disambiguation (`Nbd2`/`N1d2`/`Nb1d2`), capture `x`, check `+`, mate `#`,
//!   castling `O-O`/`O-O-O`, promotion `=Q`. En passant renders as the normal
//!   pawn capture (`exd6`); the optional `e.p.` suffix is accepted on parse.
//!
//! Round-trip (§8.6): `uci_to_move(move_to_uci(m))` and
//! `san_to_move(move_to_san(m))` recover `m` for every legal move.

use crate::movegen::{is_in_check, legal_moves, make};
use crate::moves::Move;
use crate::position::Position;
use crate::status::{status, GameStatus};
use crate::types::{PieceType, Square};
use thiserror::Error;

/// Typed move decode failure (ENGINE-CORE §5: never panic on malformed input).
#[derive(Debug, Error, PartialEq, Eq)]
pub enum MoveError {
    /// The string is not well-formed UCI/SAN.
    #[error("malformed move string '{0}'")]
    Malformed(String),
    /// Well-formed but not a legal move in this position.
    #[error("'{0}' is not a legal move in this position")]
    Illegal(String),
    /// SAN that matches more than one legal move (caller gave too little
    /// disambiguation — should not happen for engine-generated SAN).
    #[error("ambiguous move '{0}' (matches multiple legal moves)")]
    Ambiguous(String),
}

/// UCI long-algebraic form of a move (pure; no position needed). Delegates to
/// [`Move::to_uci`]. Castling is king-to-destination (`e1g1`).
#[must_use]
pub fn move_to_uci(m: Move) -> String {
    m.to_uci()
}

/// Decode a UCI string into a **legal** move for `pos` (ENGINE-CORE §4.2).
/// Unlike [`Move::from_uci`] (bare structural parse), this validates the move
/// is actually legal — the form S2 calls on a move intent.
pub fn uci_to_move(pos: &Position, uci: &str) -> Result<Move, MoveError> {
    let parsed = Move::from_uci(uci).ok_or_else(|| MoveError::Malformed(uci.to_string()))?;
    legal_moves(pos)
        .iter()
        .copied()
        .find(|m| *m == parsed)
        .ok_or_else(|| MoveError::Illegal(uci.to_string()))
}

/// SAN for a legal move `m` in `pos` (ENGINE-CORE §4.2).
///
/// Precondition: `m` is legal in `pos`. Produces correct disambiguation, the
/// `x` capture marker, `=Q` promotion, `O-O`/`O-O-O` castling, and a trailing
/// `+` (check) / `#` (mate) computed from the resulting position.
#[must_use]
pub fn move_to_san(pos: &Position, m: Move) -> String {
    let mover = pos
        .piece_at(m.from)
        .expect("move_to_san: empty from-square");

    // Castling: king moves two files.
    if mover.kind == PieceType::King {
        let df = m.to.file() as i8 - m.from.file() as i8;
        if df == 2 {
            return decorate(pos, m, "O-O".to_string());
        }
        if df == -2 {
            return decorate(pos, m, "O-O-O".to_string());
        }
    }

    let is_capture = is_capture_move(pos, m, mover.kind);
    let dest = m.to.to_string();
    let mut san = String::new();

    if mover.kind == PieceType::Pawn {
        // Pawn: a capture carries the from-file (e.g. `exd5`); a push is just the
        // destination. Promotion appends `=Q`.
        if is_capture {
            san.push((b'a' + m.from.file()) as char);
            san.push('x');
        }
        san.push_str(&dest);
        if let Some(promo) = m.promotion {
            san.push('=');
            san.push(promo.to_char_lower().to_ascii_uppercase());
        }
    } else {
        // Piece: letter + disambiguation + optional `x` + destination.
        san.push(mover.kind.to_char_lower().to_ascii_uppercase());
        san.push_str(&disambiguation(pos, m, mover.kind));
        if is_capture {
            san.push('x');
        }
        san.push_str(&dest);
    }

    decorate(pos, m, san)
}

/// Append `+`/`#` by making the move and inspecting the opponent's status.
fn decorate(pos: &Position, m: Move, mut san: String) -> String {
    let mut next = pos.clone();
    make(&mut next, m);
    // `next.side_to_move` is now the opponent. Mate/check is about them.
    if is_in_check(&next) {
        san.push(if status(&next) == GameStatus::Checkmate {
            '#'
        } else {
            '+'
        });
    }
    san
}

/// Is `m` a capture (including en passant)?
fn is_capture_move(pos: &Position, m: Move, kind: PieceType) -> bool {
    if pos.piece_at(m.to).is_some() {
        return true;
    }
    // En passant: a pawn moving diagonally to the empty ep target.
    kind == PieceType::Pawn && pos.ep_target == Some(m.to) && m.from.file() != m.to.file()
}

/// The disambiguation substring for a piece move (empty when unambiguous).
/// SAN rule: if another piece of the same type can also move to `m.to`, add the
/// from-FILE if that alone is unique, else the from-RANK if unique, else both.
fn disambiguation(pos: &Position, m: Move, kind: PieceType) -> String {
    let rivals: Vec<Move> = legal_moves(pos)
        .iter()
        .copied()
        .filter(|other| {
            other.to == m.to
                && other.from != m.from
                && pos.piece_at(other.from).map(|p| p.kind) == Some(kind)
        })
        .collect();

    if rivals.is_empty() {
        return String::new();
    }

    let same_file = rivals.iter().any(|r| r.from.file() == m.from.file());
    let same_rank = rivals.iter().any(|r| r.from.rank() == m.from.rank());

    let file_char = ((b'a' + m.from.file()) as char).to_string();
    let rank_char = ((b'1' + m.from.rank()) as char).to_string();

    if !same_file {
        file_char // file alone disambiguates
    } else if !same_rank {
        rank_char // file clashes, rank is unique
    } else {
        format!("{file_char}{rank_char}") // need both
    }
}

/// Decode a SAN string into the unique legal move it denotes in `pos`
/// (ENGINE-CORE §4.2). Tolerant of trailing `+`/`#` and an `e.p.` suffix.
pub fn san_to_move(pos: &Position, san: &str) -> Result<Move, MoveError> {
    let raw = san.trim();
    // Strip decorations that do not affect identity: check/mate and e.p.
    let core = raw
        .trim_end_matches(['+', '#'])
        .trim_end_matches("e.p.")
        .trim_end_matches(' ')
        .trim();

    let legal = legal_moves(pos);

    // Castling.
    if core == "O-O" || core == "0-0" {
        return find_castle(pos, &legal, true).ok_or_else(|| MoveError::Illegal(raw.to_string()));
    }
    if core == "O-O-O" || core == "0-0-0" {
        return find_castle(pos, &legal, false).ok_or_else(|| MoveError::Illegal(raw.to_string()));
    }

    // Promotion suffix `=X`.
    let (body, promo) = match core.split_once('=') {
        Some((b, p)) => {
            let pt = p
                .chars()
                .next()
                .and_then(PieceType::from_char)
                .filter(|pt| !matches!(pt, PieceType::Pawn | PieceType::King))
                .ok_or_else(|| MoveError::Malformed(raw.to_string()))?;
            (b, Some(pt))
        }
        None => (core, None),
    };

    let bytes = body.as_bytes();
    if bytes.is_empty() {
        return Err(MoveError::Malformed(raw.to_string()));
    }

    // Leading uppercase letter ⇒ a piece move; otherwise a pawn move.
    let (piece_kind, rest) = match PieceType::from_char(bytes[0] as char) {
        Some(k) if (bytes[0] as char).is_ascii_uppercase() => (k, &body[1..]),
        _ => (PieceType::Pawn, body),
    };

    // `rest` is `[disambiguation][x]<dest>`. The capture marker `x`, if present,
    // must appear exactly once and immediately before the two-char destination.
    // Reject anything else (`Nxxe4`, `e4x`, `Nx`, a stray mid-string `x`) rather
    // than silently normalising it — important once PGN import (F-08) feeds
    // untrusted text through this parser.
    let x_count = rest.bytes().filter(|&b| b == b'x').count();
    if x_count > 1 {
        return Err(MoveError::Malformed(raw.to_string()));
    }
    let (hint, dest_str): (&str, &str) = if x_count == 1 {
        // Split on the single `x`: left = disambiguation, right MUST be the dest.
        let (lhs, rhs) = rest.split_once('x').expect("x_count == 1");
        if rhs.len() != 2 {
            return Err(MoveError::Malformed(raw.to_string()));
        }
        (lhs, rhs)
    } else {
        // No capture marker: destination is the trailing two chars.
        if rest.len() < 2 {
            return Err(MoveError::Malformed(raw.to_string()));
        }
        (&rest[..rest.len() - 2], &rest[rest.len() - 2..])
    };

    let dest = Square::from_coord(dest_str).ok_or_else(|| MoveError::Malformed(raw.to_string()))?;

    // Whatever precedes the destination is the disambiguation hint (file/rank).
    let (mut hint_file, mut hint_rank) = (None, None);
    for c in hint.chars() {
        if ('a'..='h').contains(&c) {
            if hint_file.is_some() {
                return Err(MoveError::Malformed(raw.to_string())); // two file hints
            }
            hint_file = Some(c as u8 - b'a');
        } else if ('1'..='8').contains(&c) {
            if hint_rank.is_some() {
                return Err(MoveError::Malformed(raw.to_string())); // two rank hints
            }
            hint_rank = Some(c as u8 - b'1');
        } else {
            return Err(MoveError::Malformed(raw.to_string()));
        }
    }

    // Find legal moves matching piece kind, destination, promotion, and hints.
    let mut matches = legal.iter().copied().filter(|mv| {
        mv.to == dest
            && mv.promotion == promo
            && pos.piece_at(mv.from).map(|p| p.kind) == Some(piece_kind)
            && hint_file.map_or(true, |f| mv.from.file() == f)
            && hint_rank.map_or(true, |r| mv.from.rank() == r)
    });

    let first = matches
        .next()
        .ok_or_else(|| MoveError::Illegal(raw.to_string()))?;
    if matches.next().is_some() {
        return Err(MoveError::Ambiguous(raw.to_string()));
    }
    Ok(first)
}

fn find_castle(pos: &Position, legal: &crate::moves::MoveList, king_side: bool) -> Option<Move> {
    legal.iter().copied().find(|m| {
        pos.piece_at(m.from).map(|p| p.kind) == Some(PieceType::King) && {
            let df = m.to.file() as i8 - m.from.file() as i8;
            if king_side {
                df == 2
            } else {
                df == -2
            }
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pos(fen: &str) -> Position {
        Position::from_fen(fen).expect("valid FEN")
    }
    fn mv(p: &Position, uci: &str) -> Move {
        legal_moves(p)
            .iter()
            .copied()
            .find(|m| m.to_uci() == uci)
            .unwrap_or_else(|| panic!("{uci} not legal"))
    }

    #[test]
    fn san_basic_pawn_and_piece() {
        let p = Position::startpos();
        assert_eq!(move_to_san(&p, mv(&p, "e2e4")), "e4");
        assert_eq!(move_to_san(&p, mv(&p, "g1f3")), "Nf3");
    }

    #[test]
    fn san_capture_pawn_and_piece() {
        // White Nxe5 and pawn dxe5 available.
        let p = pos("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3");
        // pawn ep capture e5xd6 → "exd6".
        assert_eq!(move_to_san(&p, mv(&p, "e5d6")), "exd6");
    }

    #[test]
    fn san_castling() {
        let p = pos("r3k2r/8/8/8/8/8/8/R3K2R w KQkq - 0 1");
        assert_eq!(move_to_san(&p, mv(&p, "e1g1")), "O-O");
        assert_eq!(move_to_san(&p, mv(&p, "e1c1")), "O-O-O");
    }

    #[test]
    fn san_disambiguation_by_file() {
        // Two knights (b1, f3... ) that can both reach d2: Nbd2 vs Nfd2.
        let p = pos("4k3/8/8/8/8/5N2/8/1N2K3 w - - 0 1");
        // Both Nb1 and Nf3 reach d2.
        let nb = mv(&p, "b1d2");
        let nf = mv(&p, "f3d2");
        assert_eq!(move_to_san(&p, nb), "Nbd2");
        assert_eq!(move_to_san(&p, nf), "Nfd2");
    }

    #[test]
    fn san_promotion() {
        // Black Ke4 lies on the a8–e4 diagonal, so promoting to Q or B checks
        // (a8=Q+/a8=B+); R (rank/file only) and N do not. This pins the
        // promotion suffix AND the check decoration together.
        let p = pos("8/P7/8/8/4k3/8/8/K7 w - - 0 1");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8q")), "a8=Q+");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8r")), "a8=R");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8b")), "a8=B+");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8n")), "a8=N");
    }

    #[test]
    fn san_promotion_with_check() {
        // a-file promotion to Q/R checks a king on the 8th rank (along rank 8).
        let p = pos("4k3/P7/8/8/8/8/8/4K3 w - - 0 1");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8q")), "a8=Q+");
        assert_eq!(move_to_san(&p, mv(&p, "a7a8n")), "a8=N"); // knight gives no check here
    }

    #[test]
    fn san_check_and_mate() {
        // Fool's mate position: White to move is already mated? No — build a mate.
        // Scholar's-ish: Qxf7# . Use a simple back-rank mate: Ra8#.
        let p = pos("6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1");
        assert_eq!(move_to_san(&p, mv(&p, "a1a8")), "Ra8#");
        // A checking (non-mate) rook move.
        let p2 = pos("4k3/8/8/8/8/8/8/R3K3 w - - 0 1");
        assert_eq!(move_to_san(&p2, mv(&p2, "a1a8")), "Ra8+");
    }

    #[test]
    fn uci_position_aware() {
        let p = Position::startpos();
        assert_eq!(uci_to_move(&p, "e2e4").unwrap().to_uci(), "e2e4");
        assert_eq!(
            uci_to_move(&p, "e2e5"),
            Err(MoveError::Illegal("e2e5".into()))
        );
        assert_eq!(
            uci_to_move(&p, "zz"),
            Err(MoveError::Malformed("zz".into()))
        );
    }

    #[test]
    fn san_roundtrip_startpos() {
        let p = Position::startpos();
        for m in legal_moves(&p).iter().copied() {
            let san = move_to_san(&p, m);
            assert_eq!(
                san_to_move(&p, &san),
                Ok(m),
                "SAN round-trip failed for {san}"
            );
        }
    }

    #[test]
    fn san_parse_tolerates_decorations() {
        let p = pos("6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1");
        let m = mv(&p, "a1a8");
        assert_eq!(san_to_move(&p, "Ra8#"), Ok(m));
        assert_eq!(san_to_move(&p, "Ra8"), Ok(m)); // decoration optional on parse
    }

    #[test]
    fn san_parse_rejects_malformed_capture_syntax() {
        // The `x` marker must appear once, immediately before the two-char
        // destination. These are structurally malformed and must be rejected
        // (not silently normalised by stripping every `x`).
        let p = pos("rnbqkbnr/ppp1pppp/8/3pP3/8/8/PPPP1PPP/RNBQKBNR w KQkq d6 0 3");
        for bad in ["exxd6", "ex6d", "e5xd", "exd", "Nxxd5"] {
            assert!(
                matches!(san_to_move(&p, bad), Err(MoveError::Malformed(_))),
                "expected Malformed for {bad}, got {:?}",
                san_to_move(&p, bad)
            );
        }
        // The valid capture parses.
        assert_eq!(san_to_move(&p, "exd6"), Ok(mv(&p, "e5d6")));
        // NOTE: a from-file-less pawn capture like `xd6` is accepted leniently
        // when it still resolves to a unique legal move — this is a lenient
        // internal decoder, not a strict PGN *validator* (strict validation is
        // F-08 import's job). It is not the silent x-stripping that was fixed:
        // the `x` is still required to sit immediately before the destination.
    }
}
