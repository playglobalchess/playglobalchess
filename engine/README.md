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

**Gates green:** FEN round-trip (§8.6), make/unmake inverse (§8.6), and the
**full §8.1 perft corpus** — every position exact to its stated depth (slow tier
run in nightly / pre-G-A).

### Deferred to later chunk-2 stages / chunks (not yet verified)

E-1..E-14 edge-case library (§8.2) [2c]; game-end & draw detection + Zobrist
(§3.7, OPEN-B) [2d]; SAN (§4.2) [2e]; the §8.6 full property net [2f]; magic
bitboards and the §6.1 benchmarks; full golden replay (§8.5); mobile parity
(§8.8); the variant seam (§3.9). OPEN-A resolved: **own bitboard
implementation** (no wrapped crate). **G-A is not green yet.**

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
