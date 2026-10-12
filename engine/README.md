# engine

S1 · Chess rules engine + variants (Rust, shared core). Owns X-1/X-6/X-7.
Spec: `docs/specs/S1-engine/ENGINE-CORE.md` (SPEC-S1-ENGINE-CORE). Gate: **G-A**.

Crate: **`pgc-engine`** (`engine/Cargo.toml`). Pure, deterministic, no RNG /
clock / global state / `unsafe` (ENGINE-CORE §1.4.1, `#![forbid(unsafe_code)]`).

## Build status — chunk 2 in progress toward G-A

Implemented against the ratified spec:

- **Board representation** (§3.2–3.3): 12 piece bitboards + color/all occupancy
  aggregates + a redundant `mailbox[64]`, **LERF** indexing (`0=a1 … 63=h8`),
  and the `Position` struct carrying every FEN-encoded field (side to move,
  castling rights, en-passant target, halfmove clock, fullmove number). A debug
  invariant asserts bitboards and mailbox never disagree.
- **FEN** parse/emit with the **OPEN-D hybrid** (§3.3): lenient internal ep
  target (set after any double push, so perft matches §8.1), strict exported
  FEN (ep square emitted only when a capture to it is available — Lichess form).
- A **pseudo-legal + legality-filter** move generator (§3.5) — the simple,
  correct form.
- **make/unmake** with an `Undo` token (§3.6, stage 2a): the zero-clone perft /
  validation hot path, a perfect inverse (`unmake(make(p,m)) == p`). `perft`
  and `perft_divide` run on it over a single mutable `Position`.
- **§8.1 perft corpus** (§8.1, stage 2b): all 6 reference positions, two tiers
  (see Develop). Fast tier (≤ a few million nodes) runs every commit; the deep
  target depths (startpos d6, Kiwipete d5, …) are `#[ignore]`d to the nightly
  tier.
- **E-1..E-14 edge-case library** (§8.2, stage 2c): explicit legal-move-set
  assertions for the move-generation edge cases — ep horizontal-pin (E-2),
  ep-only-immediate (E-3), castling through/into/out of check incl. the
  queenside b-file nuance (E-4/E-5), castling-right loss by rook capture (E-6),
  all four under/promotions on push + capture (E-7), absolute pin (E-8),
  double-check king-only (E-9), halfmove-clock reset (E-13), the 218-move
  position (E-14).
- **Game-end detection + Zobrist** (§3.7, §4.1, §8.3, stage 2d): `status(p)`
  (checkmate, stalemate, insufficient material incl. the K+N+N non-draw E-12,
  50-/75-move) and `status_with_history(p, &[key])` (threefold claimable /
  fivefold automatic, by position identity E-11, not move list). A Zobrist key
  (OPEN-B, pinned seed) over exactly the identity fields backs repetition; full
  equality remains the collision guard. The engine reports *availability* only
  and never ends a game (§4.1).
- **Move encoding** (§4.2, stage 2e): `move_to_san` / `san_to_move` with
  disambiguation (`Nbd2`/`N1d2`/`Nb1d2`), `x` capture, `+`/`#`, `O-O`/`O-O-O`,
  `=Q` promotion; position-aware `uci_to_move` / `move_to_uci` (validated
  against the legal set). SAN & UCI round-trip for every legal move (§8.6).

**Gates green:** FEN round-trip (§8.6), make/unmake inverse (§8.6), the full
§8.1 perft corpus (slow tier in nightly), the §8.2 move-set edge cases, §8.3
game-end detection (incl. threefold-by-position, the castling-rights E-11
distinction, the K+N+N non-draw E-12), and the §4.2 SAN/UCI round-trips.

### Deferred to later chunk-2 stage / chunks (not yet verified)

The §8.6 full property-net consolidation [2f]; magic bitboards and the §6.1
benchmarks; full golden replay (§8.5); mobile parity (§8.8); the variant
seam (§3.9). OPEN-A resolved: **own bitboard implementation**. OPEN-B resolved:
**own Zobrist, pinned seed**. **G-A is not green yet.**

## Develop

```sh
cd engine
cargo test --all                       # unit + gate + property + FAST perft corpus
cargo test --release -- --ignored      # SLOW perft tier: §8.1 target depths (minutes)
cargo fmt --all                        # rustfmt (CI checks --check)
cargo clippy --all-targets --all-features -- -D warnings
```

The **fast** perft tier runs per commit; the **slow** `#[ignore]`d tier (deep
§8.1 depths, hundreds of millions of nodes) runs only in the nightly CI job
`engine-nightly-perft` (schedule trigger) or locally via `-- --ignored`. Do NOT
run the slow tier in a memory-tight interactive session.

CI runs the per-commit gates path-filtered on `engine/**`
(`.github/workflows/ci.yml`, job `engine`). The local post-write hook
`.kiro/hooks/engine-rust-check.sh` runs fmt + clippy after any edit to a
`*.rs` under `engine/`.
