# engine

S1 · Chess rules engine + variants (Rust, shared core). Owns X-1/X-6/X-7.
Spec: `docs/specs/S1-engine/ENGINE-CORE.md` (SPEC-S1-ENGINE-CORE). Gate: **G-A**.

Crate: **`pgc-engine`** (`engine/Cargo.toml`). Pure, deterministic, no RNG /
clock / global state / `unsafe` (ENGINE-CORE §1.4.1, `#![forbid(unsafe_code)]`).

## Build status — chunk 1 of 4 toward G-A

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
  correct form; present only so the chunk gate is met honestly.
- The **perft harness skeleton** (§4.4): `perft` and `perft_divide`.

**Chunk-1 gate (green):** the FEN round-trip property `parse_fen(to_fen(p)) == p`
(§8.6) and `perft(startpos, 1) == 20` (§8.1).

### Deferred to later chunks (not yet implemented / verified)

Full §8.1 perft corpus (Kiwipete, Positions 3–6, deep perft); the E-1..E-14
edge-case library (§8.2); game-end & draw detection (§3.7); SAN (§4.2);
`make -> Undo` / `unmake` hot path (§3.6); Zobrist / repetition (§3.7, OPEN-B);
magic bitboards; the §6.1 benchmarks; mobile parity (§8.8); the variant
seam (§3.9). OPEN-A resolved: **own bitboard implementation** (no wrapped crate).

## Develop

```sh
cd engine
cargo test --all          # unit + gate + property tests
cargo fmt --all           # rustfmt (CI checks --check)
cargo clippy --all-targets --all-features -- -D warnings
```

CI runs the same gates, path-filtered on `engine/**`
(`.github/workflows/ci.yml`, job `engine`). The local post-write hook
`.kiro/hooks/engine-rust-check.sh` runs fmt + clippy after any edit to a
`*.rs` under `engine/`.
