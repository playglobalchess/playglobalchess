# G-A acceptance scorecard — S1 Standard chess engine

Maps every box of ENGINE-CORE §9 (Definition of Done → **G-A**) to its evidence
and status. G-A turns green when the three HUMAN/INFRA boxes below are signed
off; **all engine CODE is complete and merged to `main`.**

| State | Meaning |
|---|---|
| ✅ DONE | Implemented, tested, merged to `main` |
| 🟡 HOST-PROVEN | Code done + proven on this host; a device/human step remains |
| ⬜ HUMAN | Not code — needs a person (review, sign-off, a booted device) |

## §9 acceptance boxes

| # | §9 box | Status | Evidence (on `main`) |
|---|---|---|---|
| 1 | **Perft §8.1** — 100% exact, all 6 positions to stated depths | ✅ | `engine/tests/perft_corpus.rs`: fast tier per-commit; slow tier (startpos d6 = 119,060,324; Kiwipete d5 = 193,690,690; Pos3 d6; Pos4 d5; Pos5 d5; Pos6 d5) all verified exact |
| 2 | **Edge-case library §8.2** — E-1..E-14, exact move-set asserts | ✅ | `engine/tests/edge_cases.rs` (12 tests): ep horizontal-pin E-2, castling E-4/E-5, rook-capture rights E-6, under/promotion E-7, pin E-8, double-check E-9, clock E-13, 218-move E-14 |
| 3 | **Game-end §8.3** — mate/stalemate, repetition-by-position, K+N+N non-draw | ✅ | `engine/tests/game_end.rs` + `src/status.rs`: threefold via shuffles (E-11), castling-rights distinction, K+N+N-not-draw (E-12), Zobrist (OPEN-B) |
| 4 | **Golden replay §8.5** — FEN at every ply; regression net | ✅ | `engine/tests/golden_games.rs` (5 games): external final-FEN oracle + per-ply regression lock |
| 5 | **Property tests §8.6** — make/unmake, UCI/SAN/FEN round-trips, perft(·,1)==len | ✅ | `engine/tests/properties.rs` (single generator), + `is_legal`==`legal_moves.contains` |
| 6 | **Performance §6.1** — `legal_moves` < 1 ms, `is_legal` < 10 µs | ✅ | `engine/benches/movegen.rs` + `PERF.md`: legal_moves ~1.9 µs, dedicated `is_legal` ~0.28 µs; magics skipped by decision (D5), nightly threshold gate `platform/ci/check_bench.py` |
| 7 | **No panics on malformed input** — typed errors (§5) | ✅ | `FenError`, `MoveError` (thiserror); fuzz in `gate.rs`; FFI boundary returns `FfiError`, never panics |
| 8 | **Mobile parity §8.8** — perft+golden slice byte-identical on iOS/Android | 🟡 | `engine/ffi` (UniFFI, ADR-9) cross-compiles for iOS/iOS-sim/Android; Swift parity test RUNS byte-identical to the oracle; Kotlin binding compiles. **Device run remains** (⬜ below) |
| 9 | **No known legal-move defect** (NFR-5) | ✅ | §8.1 corpus exact to deep depths + §8.7 differential vs `shakmaty` agrees on all 6 positions — two independent correctness nets, zero known defects |
| 10 | **Public API §4 reviewed** by S2 + architecture group | ⬜ HUMAN | The surface is final & stable on `main`; needs S2 + arch review as a shared contract |
| 11 | **Spec → RATIFIED** with reviews recorded | ⬜ HUMAN | ENGINE-CORE is RATIFIED for the spec itself; this box is the DoD review-record step |

## What remains for G-A — three items, none are agent code

1. **⬜ Mobile device run (completes box 8).** Execute the scaffolded targets on a booted sim/emulator:
   - iOS: `platform/mobile/build_mobile.sh && cd clients/ios/EngineParityTests && xcodebuild test -scheme EngineParityTests -destination 'platform=iOS Simulator,name=iPhone 15'`
   - Android: `platform/mobile/build_mobile.sh && ./gradlew :engine-parity:connectedAndroidTest`
   Engine + binding correctness is already host-proven byte-identical; this is device confirmation.
2. **⬜ §4 public API review (box 10)** — S2 + architecture group sign off on the engine as the shared contract they bind.
3. **⬜ Spec DoD review-record (box 11)** — record those reviews against the spec.

## Decisions on record

- **OPEN-A** — own bitboard implementation (no wrapped crate). ✅ ratified.
- **OPEN-B** — own Zobrist, pinned seed. ✅ resolved in `src/zobrist.rs`.
- **OPEN-D** — en-passant hybrid (lenient internal / strict exported). ✅ ratified.
- **D5** — magic bitboards NOT built for G-A (measured ~500× under target). See `PERF.md`.
- **ADR-13 (magics skip)** — drafted, **HELD** per owner; the spec↔decision drift is documented as a known gap in `PERF.md`, not reconciled into ENGINE-CORE.
- **D3** — §8.7 differential tier built, dev-only behind `--features difftest`.

## Chunk map (what was built, in order)

Chunk 1 (board rep + FEN + perft skeleton) → Chunk 2 (make/unmake · §8.1 corpus
· E-1..E-14 · game-end+Zobrist · SAN/UCI · property net) → Chunk 3 (perf+bench ·
golden replay · mobile parity · differential tier). 17 PRs, each reviewed and
gate-checked.

> **G-A is NOT green yet** — it is code-complete; the three boxes above are the
> human/infra sign-offs that flip it.
