# Engine Chunk 2 — full move generation + perft/edge-case conformance (plan)

| Field | Value |
|---|---|
| Doc type | **Implementation plan** (NOT a new spec — implements the already-RATIFIED `ENGINE-CORE.md`) |
| Plan ID | PLAN-S1-ENGINE-CHUNK-2 |
| Owner | S1 stream lead — *fill in* |
| Status | **APPROVED** (owner-approved 2026-10-12; decisions D1–D4 settled, see §7) |
| Version | 1.0 |
| Last updated | 2026-10-12 |
| Lifecycle note | Disposable build doc — **delete this file once chunk 2 is DONE** (owner instruction 2026-10-12). Kept during the build as the cross-PR/cross-session stage checklist. |
| Implements (spec sections) | SPEC-S1-ENGINE-CORE §3.5, §3.6, §3.7, §4.1, §4.2, §8.1, §8.2, §8.3, §8.6; partial §6.1, §8.5 |
| Builds on | chunk 1 ([PR #5](https://github.com/playglobalchess/playglobalchess/pull/5)) — board rep §3.2–3.3, FEN §3.3, perft skeleton §4.4, minimal generator |
| Gate it advances | **G-A** (this is the bulk of it) |

> This is **not** a spec and does not redefine anything. `ENGINE-CORE.md` is RATIFIED and is the single source of truth; this plan only sequences which of its already-agreed sections chunk 2 implements and in what order. If a genuine ambiguity surfaces during the build, I STOP and ask rather than deciding (per build manual §0).

---

## 1. Where chunk 1 left off

Chunk 1 (merged / in [PR #5](https://github.com/playglobalchess/playglobalchess/pull/5)) delivered:
- Board representation (§3.2–3.3): 12 bitboards + mailbox, LERF, `Position`, debug invariant.
- FEN parse/emit with the OPEN-D hybrid (§3.3).
- Perft harness **skeleton** (§4.4): `perft`, `perft_divide`.
- A **minimal, correct-but-unoptimized** pseudo-legal + copy-make legality generator (§3.5) — built only so `perft(startpos,1)==20` was met honestly. It is verified at depth 1 (gate) + a shallow smoke check (depth 2=400, depth 3=8902).

**Chunk 1 is not proof the generator is correct.** It passes depth-1/3 startpos only. The whole point of chunk 2 is to prove — against the published reference corpus — that the generator is right at depth, in every tricky position, and across every edge case E-1..E-14.

## 2. Chunk 2 objective (one sentence)

Make the move generator **conformance-correct** — pass the complete §8.1 perft corpus (6 positions to their stated depths), the full §8.2 edge-case library (E-1..E-14), §8.3 game-end detection, and the §8.6 property suite — turning the chunk-1 skeleton into the real G-A deliverable, with make/unmake (§3.6) as the perft hot path.

## 3. What chunk 2 explicitly does NOT include (deferred to chunk 3+)

So scope stays tight and reviewable:
- **Magic bitboards / PEXT attack tables (§3.2) and the §6.1 performance gate** (`legal_moves` < 1 ms, `is_legal` < 10 µs). Chunk 2 proves **correctness**; the performance tier is chunk 3. (A correct simple generator can be slow; the spec explicitly orders "correctness before speed," §1.4.3.) *Rationale for the split:* magics are an optimization that must produce the identical move set — doing them before correctness is nailed down means debugging two things at once.
- **SAN encode/decode (§4.2 `move_to_san` / `san_to_move`)** — needed for PGN/display (F-08, F-17), not for perft or legality. Candidate for chunk 2 **stretch** or chunk 3; see §7 decision point D1.
- **Golden-game replay library (§8.5)** full build-out — chunk 2 wires the *harness* and seeds it with the regression positions it discovers, but the curated master-game library is chunk 3.
- **Mobile parity / UniFFI (§8.8, §6.2)** — chunk 4.
- **Zobrist hashing (OPEN-B)** — needed by repetition (§3.7). Chunk 2 implements repetition via **full position-identity equality** first (correct, simpler); Zobrist is the optimization layered in chunk 2 *only if* the game-end tests need the hash-keyed occurrence count to be fast. See D2.
- **Differential testing vs `shakmaty`/`cozy-chess` (§8.7)** — valuable as a *bring-up accelerator*; proposed as an optional dev-only `--features difftest` path (D3), not a shipped dependency (OPEN-A already decided: own implementation).

## 4. Build order (sub-stages, each with its own exit check)

Sequenced so each stage produces a provable signal and localizes bugs before the next. **I propose landing these as separate PRs** (reviewable units), or as one branch with staged commits — your call (D4).

### Stage 2a — make/unmake (§3.6) replaces copy-make
- `make(p,&mut) -> Undo` + `unmake(p, m, undo)`; `Undo` captures captured piece, prior castling rights, prior ep target, prior halfmove clock, moved-piece provenance.
- Keep the chunk-1 copy-make `apply` as the immutable convenience path (clients, §3.6 / OPEN-C).
- **Exit:** property test `unmake(make(p,m)) == p` green over a large random-legal sample (§8.6); perft re-pointed at make/unmake and still gives depth-1/2/3 startpos = 20/400/8902.

### Stage 2b — perft corpus §8.1 (the backbone)
- Wire the 6 reference positions as a conformance suite, fast tier ≤ d5 per commit, slow tier d6–d7 nightly/pre-gate (§8.8).
- Use `perft_divide` to localize every mismatch to a root move.
- This is where the generator's real bugs surface — Kiwipete and Position 3 are specifically chosen to break ep/pin/castling handling.
- **Exit:** all 6 positions **exact** to their stated depths (startpos d6=119,060,324; Kiwipete d5=193,690,690; Pos3 d6=11,030,083; Pos4 d5=15,833,292; Pos5 d5=89,941,194; Pos6 d5=164,075,551). Zero nodes off.

### Stage 2c — edge-case library §8.2 (E-1..E-14)
- ≥1 explicit assertion on the **exact legal-move set** (not just a count) per edge case. Non-negotiable members called out in §8.2: ep-exposes-king horizontal pin (E-2, `8/8/8/K2pP2r/8/8/8/7k`), castling through/into/out of check (E-4/E-5), castling right lost by rook **capture** (E-6), all four under/promotions on push *and* capture (E-7), double-check→king-only (E-9), the 218-move position (E-14, `R6R/3Q4/1Q4Q1/4Q3/2Q4Q/Q4Q2/pp1Q4/kBNN1KB1 w - - 0 1`).
- The chunk-1 known gap — **strict-ep is pseudo-legal adjacency, not pin-aware** — is closed here by E-2.
- **Exit:** every E-1..E-14 case green.

### Stage 2d — game-end & draw detection §3.7 / §8.3 + `status()` §4.1 + Zobrist (OPEN-B)
- Implement `status() -> GameStatus` (Ongoing | Checkmate | Stalemate | DrawInsufficientMaterial | DrawFiftyMove(claimable|auto) | DrawRepetition(threefold|fivefold)); **report availability only, never auto-end** (§4.1).
- **Zobrist key (D2, OPEN-B)** over exactly the repetition-identity fields (placement + side + castling rights + ep *availability*), seeds pinned in code; backs the occurrence count. **Full position-identity equality is the collision guard** (§3.7) — never trust the hash alone on a claim.
- Repetition by **position identity**, NOT move list; threefold claimable, fivefold auto. Castling-rights distinction (E-11) and K+N+N-is-not-a-draw (E-12) explicitly tested.
- 50-move (≥100 plies claimable) / 75-move (≥150 auto); clock reset on capture/pawn move (E-13).
- **Exit:** §8.3 positions each assert the exact `GameStatus`, including threefold-by-position reached via different move orders, the castling-rights non-match, and every insufficient-material class; Zobrist equal-key ⟺ equal-identity property green.

### Stage 2e — move encoding completeness §4.2 (UCI + SAN, D1=IN)
- `uci_to_move(p, uci)` / `move_to_uci` as **position-aware** functions (chunk 1 has only structural `Move::from_uci`); validate against the legal set.
- **SAN (D1=IN):** `move_to_san` / `san_to_move` with correct disambiguation (`Nbd2`/`Nfd2`), capture `x`, check `+`, mate `#`, castling `O-O`/`O-O-O`, promotion `=Q`.
- **Exit:** `uci_to_move(move_to_uci(m)) == m` and `san_to_move(move_to_san(m)) == m` for all legal moves (§8.6).

### Stage 2f — property suite §8.6 wired as the fast per-commit net
- make/unmake inverse, UCI round-trip (SAN if present), FEN round-trip (already green from chunk 1), `perft(p,1) == legal_moves(p).len()` on a large sample, and (since we keep both a pseudo-legal path and will not add a separate pin-aware generator unless perf needs it) the pseudo-legal==pin-aware assertion is **N/A unless a second generator exists** (§3.5).
- **Exit:** all property tests green, running in the fast tier.

## 5. Acceptance for chunk 2 (subset of §9 that this chunk owns)

Chunk 2 is DONE when:
- [ ] Perft §8.1: **100% exact** on all 6 positions to stated depths.
- [ ] Edge-case library §8.2: **100% green**, E-1..E-14 each ≥1 exact-move-set assertion.
- [ ] Game-end §8.3: **100% green**, incl. threefold-by-position, castling-rights repetition distinction, K+N+N non-draw.
- [ ] Property suite §8.6: **100% green** (make/unmake inverse, UCI round-trip, **SAN round-trip**, FEN round-trip, perft(·,1)==len).
- [ ] No panics on malformed UCI **or SAN** (fuzz), typed `MoveError` (§5).
- [ ] CI engine job runs the fast tier every commit; slow tier (d6–d7) wired for nightly/pre-gate.
- [ ] `#![forbid(unsafe_code)]` preserved, `clippy -D warnings` + `rustfmt` clean.

**NOT in chunk 2's acceptance** (chunk 3/4, tracked): §6.1 performance gate, §8.5 full golden library, §8.7 differential tier *as a standing gate* (it exists in chunk 2 only as the dev-only `--features difftest` bring-up aid, D3), §8.8 mobile parity, magic bitboards. (SAN and Zobrist are now IN chunk 2 per D1/D2.)

G-A is **not** declared green at the end of chunk 2 — it needs chunks 3 (perf + golden + SAN) and 4 (mobile parity) too. Chunk 2 gets the engine **correctness-complete for Standard**, which is the hardest part.

## 6. Risks

| Risk | Mitigation |
|---|---|
| The ep horizontal-pin (E-2) and the castling-attacked-square matrix are the classic perft-breakers | They have dedicated §8.2 cases AND Kiwipete/Pos3 in §8.1 exercise them at depth — two independent nets |
| Deep perft (d6–d7) is slow on this memory-tight host | Fast tier ≤ d5 locally/per-commit; d6–d7 only nightly/pre-gate (§8.8); do NOT run d6+ in the interactive session |
| make/unmake state-restore bugs are subtle | The `unmake(make(p,m))==p` property test (large sample) is the guard, landed in stage 2a *before* the generator leans on it |
| Scope creep (magics, SAN, golden, mobile all tempting) | Explicitly fenced to chunk 3/4 in §3; D1/D2/D3 are the only "pull-forward" decisions and they're yours |

## 7. Decisions — SETTLED (owner-approved 2026-10-12)

- **D1 — SAN: IN chunk 2.** `move_to_san` / `san_to_move` (§4.2) land in stage 2e, with correct disambiguation (`Nbd2`/`Nfd2`), `x`/`+`/`#`, `O-O`/`O-O-O`, `=Q`. SAN round-trip (`san_to_move(move_to_san(m))==m` for all legal moves) joins the §8.6 property net.
- **D2 — Zobrist: NOW (chunk 2), not deferred.** OPEN-B is implemented in stage 2d: a Zobrist key over **exactly** the §3.7 repetition-identity fields (placement + side + castling rights + ep *availability*), seeds pinned in code for cross-build stability (golden tests). The hash backs the occurrence count; the **full position-identity equality check is retained as the collision guard** the spec mandates (§3.7 — "a full-equality check guards hash collisions on claim"). So it is Zobrist *plus* equality, never Zobrist alone.
- **D3 — Differential testing: dev-only `--features difftest`.** A reference crate (`shakmaty`/`cozy-chess`) is wired behind an optional dev feature to accelerate 2b/2c bring-up (§8.7). **Not a shipped/runtime dependency** — OPEN-A (own implementation) stands; the published §8.1 reference counts remain the real acceptance bar.
- **D4 — A PR per sub-stage.** 2a, 2b, 2c, 2d, 2e, 2f land as separate reviewable PRs off `main`, each green on its own exit check before the next opens.

## 8. What I will do once approved

Start **stage 2a** (make/unmake + the inverse property test) on a fresh feature branch off `main`, keep each sub-stage's exit check green before moving on, and NOT run deep perft (d6+) in-session (memory). I will not touch `ENGINE-CORE.md` (RATIFIED) except to flip its status to `BUILDING` once you approve this plan, per CONVENTIONS lifecycle, if you want that recorded.
