# Engine Core — board representation, move generation, conformance corpus — S1 Core Chess Engine

| Field | Value |
|---|---|
| Spec ID | SPEC-S1-ENGINE-CORE |
| Owner | S1 stream lead — *fill in* |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-10-12 |
| Implements (master IDs) | X-1 (rules engine), NFR-5 (correctness), NFR-1 (sub-ms move gen contributes to move-latency budget), partially NFR-10 (one engine, three consumers via ADR-9) |
| Depends on | ADR-11 (Rust), ADR-10 (event store = game truth), ADR-3 (N-seat container — Standard uses 2 seats), MASTER §VIII.3 (game-event model), MASTER §X (test strategy) |
| Gates it feeds | **G-A · Engine correct** (critical path; blocks S2 live play, S1 variants, all puzzle/analysis work) |

> Scope note: this spec covers **Standard chess only**. Variants (Chess960, Crazyhouse, Atomic, Fog of War, Duck, Spell Chess, N-player/Chaturaji, …) are specified in `VARIANTS.md` (ADR-4) and layered as data-driven overlays on the core defined here. Analysis/engine integration (Stockfish/NNUE, tablebases, X-6) is `ANALYSIS-INTEGRATION.md`. Neither is in scope here.

---

## 1. Purpose & scope

### 1.1 Purpose
Define the authoritative, server-side Standard-chess rules engine (X-1): how a position is represented, how legal moves are generated and validated, how game-end conditions are detected, and the exact test corpus that proves it correct. This engine is the single source of truth for move legality across the server and all three clients (ADR-9 / ADR-12 boundary rule: *no service reimplements chess logic*). It is the highest-risk component in the platform (MASTER §III, X-1) and its correctness corpus **is** the G-A gate (MASTER §X.1).

### 1.2 In scope
- Standard FIDE chess: the 6 piece types, all special moves (castling, en passant, promotion incl. underpromotion), check/checkmate/stalemate, and all draw conditions.
- Board representation and the position model (including the hidden state that FEN carries: side to move, castling rights, en-passant target, halfmove clock, fullmove number).
- Pseudo-legal and legal move generation; move make/unmake; perft.
- Move encoding/decoding: UCI (wire + engine) and SAN (display/PGN), FEN parse/emit.
- Game-end detection: checkmate, stalemate, threefold/fivefold repetition, 50-/75-move, insufficient material.
- The full conformance corpus (§8) that gates G-A.
- The public API surface the server (X-2, Go, via FFI/gRPC — ADR-12) and the shared client core (ADR-9, UniFFI) call.

### 1.3 Explicit non-goals
- **No search / evaluation / AI.** Move *selection* for bots (F-02/F-03) and analysis (X-6) is Stockfish/NNUE in `ANALYSIS-INTEGRATION.md`. This engine only knows what is *legal*, never what is *good*.
- **No variant rules.** See `VARIANTS.md`. The core must be factored so variants overlay it (ADR-4), but the overlays themselves are specified elsewhere.
- **No network, clock, or matchmaking.** Clocks, time controls, draw-offer/resign/abort negotiation, reconnection are S2 (`GAME-SERVER.md`). The engine is given a position and a move; it does not own wall-clock time. (It *does* define the `halfmove`/`fullmove` counters that are part of position state, which is different from a chess clock.)
- **No persistence.** The event store (ADR-10) is the game's truth; the engine is a pure, deterministic function over position + move. It emits no events and reads no store; S2 projects engine output into `MovePlayed` events (MASTER §VIII.3).
- **No PGN tag/metadata model** beyond SAN move text — full PGN headers are an S2/S3 concern.

### 1.4 Design tenets
1. **Pure and deterministic.** Same position + same move → same result, bit-for-bit, on every platform and build. No RNG, no clock, no global state, no allocation on the hot path. This is what lets the *same* Rust engine run on the server and inside iOS/Android (ADR-9) and lets the event log replay identically (ADR-10, MASTER §VIII.1).
2. **Legality is the engine's only authority** (threat T-1, MASTER §XI): clients send *intents*; the engine says legal/illegal and produces the resulting position. Nothing downstream re-derives legality.
3. **Correctness before speed, but speed is a requirement.** Target sub-millisecond full legal move generation for a typical middlegame position (MASTER §V.2 G-A "sub-ms move gen", NFR-1). Measured, not assumed (§6).
4. **Factored for variants.** Core move-gen + a documented extension seam so ADR-4 overlays (drops, duck placement, spell casts, N-seat turn order) attach without forking the generator.

---

## 2. Requirements traced

| Master ID | Requirement | Where satisfied |
|---|---|---|
| X-1 | Correct, fast, server-authoritative move generation & validation | §3 (design), §4 (API), §8 (corpus) |
| NFR-5 | Rules engine passes full perft corpus + conformance at 100%; 0 known legal-move defects at any gate | §8 (test plan), §9 (acceptance) |
| NFR-1 | Sub-ms move gen (engine's slice of the move-latency budget) | §3.2–3.3 (bitboards), §6.1 (perf targets) |
| NFR-10 | One engine, three consumers; shared protocol | §1.4, §3.1 (Rust core via ADR-9), §4 (stable API) |
| G-A | Engine correct — the gate | §8 **is** the gate; §9 acceptance = G-A green |
| ADR-11 | Engine in Rust | §3.1 |
| ADR-3 | N-seat container; Standard = 2 seats | §3.4 (position carries side-to-move only; seat mapping is S2's) |
| ADR-10 | Game truth is the event log; engine is a pure fold step | §1.3, §3.6 (make/unmake), §4.3 |
| MASTER §VIII.3 | `MovePlayed {ply, seat_idx, uci, …}` | §4.2 (engine emits uci+san; S2 wraps as events) |
| MASTER §X.1 | The named corpus (perft startpos+Kiwipete+tricky, edge-case library, game-end, golden replay) | §8.1–8.5 |
| T-1 (MASTER §XI) | Move injection / illegal move | §3.5 (legal-only validation), §4.1 (intent→accept/reject) |

Not traced here (owned elsewhere): T-2 clock manipulation (S2), T-3 engine assistance (S6), rating (S3), the WS frame layout (PE/S2).

---

## 3. Design

### 3.1 Language & crate shape (ADR-11)
Rust, `#![no_std]`-friendly core where practical so it binds cleanly via UniFFI into Swift/Kotlin (ADR-9) and compiles into the Go server boundary (ADR-12) as a static lib / sidecar. Candidate reference crates for *validation and differential testing* (not necessarily as the shipped dependency): `shakmaty`, `cozy-chess` (MASTER ADR-11). Decision on "wrap a crate vs. own implementation" is **OPEN-A** (§10) — the spec is written so either satisfies it, and either way our corpus (§8) is the acceptance bar, not the crate's.

### 3.2 Board representation — bitboards
Primary representation is **bitboards**: a `u64` per `(color, piece-type)` plus occupancy aggregates. Rationale: bitboards give branch-light, SIMD-friendly move generation and the sub-ms target (NFR-1); they are the standard for correct, fast engines and interoperate with the reference crates for differential testing.

- 12 piece bitboards (`white_pawns … black_king`) + `white_occ`, `black_occ`, `all_occ` derived.
- Square indexing **0 = a1 … 63 = h8** (little-endian rank-file / LERF), the de-facto standard — fixed here so SAN/UCI/FEN/magic tables agree.
- Sliding attacks via **magic bitboards** (or PEXT where available) for rook/bishop; precomputed attack tables for knight/king/pawn. The table-generation strategy is an implementation detail, but the *chosen* scheme is pinned in code docs because perft timing depends on it.
- A redundant `mailbox[64]` (piece-on-square array) is kept alongside for O(1) "what is on this square" during SAN disambiguation and make/unmake. Bitboards and mailbox must never disagree — a debug-build invariant check asserts this.

### 3.3 Position state (everything FEN carries)
A `Position` is the complete, self-contained game state — the fold target of the event log (ADR-10):
```
Position {
  bitboards[12], mailbox[64],
  side_to_move:        White | Black,
  castling_rights:     { WK, WQ, BK, BQ } bitset,
  ep_target:           Option<Square>,   // the square a pawn may capture *to* via en passant, set the move after a double push (see EP convention below)
  halfmove_clock:      u16,              // plies since last capture or pawn move (50-/75-move rule)
  fullmove_number:     u32,              // starts 1, increments after Black's move
  // repetition detection needs position identity incl. rights+ep — see §3.7
}
```

**En-passant target convention — resolved as a hybrid (OPEN-D).** Two conventions exist and they are *not* interchangeable:
- **Lenient (classic):** the ep square is set after *any* double pawn push. This is the convention the published perft counts in §8.1 (CPW) were generated under — e.g. the Kiwipete and Position-3 reference FENs both carry an ep square with no legal ep capture available.
- **Strict:** the ep square is set only when a pawn could *legally* capture en passant on the reply (an enemy pawn is adjacent and the capture doesn't leave the king pinned). This is what **Lichess** does: `scalachess` emits the ep square only when the capture is actually possible, a documented, deliberate deviation from the FEN spec ([lichess-org/lila #6827](https://github.com/lichess-org/lila/issues/6827); [lichess open database](https://database.lichess.org/)). Chess.com is closed-source and observably follows the standard/lenient form.

The two produce **different FENs and different perft node counts at shallow depths**, so the choice directly affects whether the engine matches §8.1. The resolution separates the two *uses* of the ep square rather than picking one global convention:

- **Internal state + perft/corpus → lenient.** Store the ep square after any double push and count perft under this convention, so the §8.1 published numbers hold **unchanged** (lowest G-A risk).
- **Exported FEN** (PGN headers, REST/analysis hand-off, anything leaving the engine) **→ strict.** Emit the ep square only when a legal ep capture exists, matching Lichess (our ADR-4 "pin to Lichess" authority) and the modern FEN standard.

EP *legality* (E-2) is enforced at move-gen time regardless of convention, so the hybrid changes only what the square *looks like* in internal state vs. exported text, never which moves are legal. Repetition identity (§3.7) keys on EP *availability* (whether a legal ep exists) — which under this split is exactly "what the strict/exported form would show," so it is well-defined and consistent. Tracked as OPEN-D (§10).

### 3.4 Side-to-move vs. seats (ADR-3)
The engine models **side to move** (White/Black) only. The mapping of color → seat → user/bot (`GameSeat`, MASTER §VIII.2) and N-seat turn order is **S2/S3's** container concern. For Standard chess seats = 2 and the mapping is trivial; the engine stays color-centric so variants with >2 seats (Chaturaji) extend the container, not the Standard core.

### 3.5 Move generation — pseudo-legal then filter
Two-stage generation (the clearest correct design; optimize later only if perft timing demands):
1. **Pseudo-legal generation** — all moves per piece ignoring whether they leave the king in check: pawn pushes/double-pushes/captures/promotions/en-passant, knight, bishop/rook/queen slides, king moves, castling candidates.
2. **Legality filter** — a pseudo-legal move is legal iff, after making it, the mover's king is not attacked. Implemented via make/unmake + `is_square_attacked(king_sq, by=opponent)`. (A pin-aware generator that produces only legal moves is a permitted optimization, but it must produce the *identical set* as filter — asserted by a differential test in debug builds.)

Special-move rules enforced exactly (FIDE):
- **Castling**: king and chosen rook unmoved (rights present); squares between empty; king not currently in check; king does not pass through or land on an attacked square (the rook may pass through attacked squares; the b1/b8 square for queenside may be attacked). Edge cases E-4..E-6.
- **En passant**: only on the move immediately following the opponent's double push; capturing pawn must be on the correct rank; the capture is illegal if it leaves the mover's king in check — including the rare "horizontal pin along the 5th/4th rank through *both* pawns" case (E-2). This is the single most common perft-failing bug — it has a dedicated test (§8.2).
- **Promotion**: a pawn reaching the last rank *must* promote; all four targets (Q/R/B/N) are distinct legal moves; underpromotion is a first-class move, not an afterthought (E-7).

### 3.6 Make / unmake
`make(move) -> Undo` mutates the position and returns an `Undo` token capturing everything needed to restore it (captured piece, prior castling rights, prior ep target, prior halfmove clock, moved-piece provenance). `unmake(move, undo)` restores exactly. Make/unmake must be a perfect inverse — a property test (§8) asserts `unmake(make(p,m)) == p` for a large random/legal move sample. Make/unmake (not copy-on-move) is the hot path for perft and for S2's "validate this intent" call; copy-make may back a convenience `apply(&self, move) -> Position` for callers that want immutability (clients).

### 3.7 Game-end & draw detection
- **Checkmate**: side to move is in check and has zero legal moves.
- **Stalemate**: side to move is *not* in check and has zero legal moves → draw.
- **Threefold / fivefold repetition**: repetition is **by position identity**, where identity = (piece placement + side to move + castling rights + en-passant *availability*), explicitly **not** the move list (MASTER §X.1.3). A Zobrist hash keyed over exactly those fields backs an occurrence count; a full-equality check guards hash collisions on claim. Threefold is claimable; fivefold is automatic.
- **50-move / 75-move**: halfmove_clock ≥ 100 plies ⇒ 50-move draw claimable; ≥ 150 ⇒ 75-move automatic. Clock resets on any capture or pawn move.
- **Insufficient material** (automatic draw): K vs K; K+B vs K; K+N vs K; K+B vs K+B with both bishops on the **same color** complex (MASTER §X.1.3). (K+N+N vs K is *not* auto-draw under FIDE — it is not forced mate but mate is possible, so it is handled as "no auto-draw"; documented so it is not "fixed" by mistake.)
- **Per-side mating capacity (FIDE Art. 6.9 — the timeout rule)**: a *distinct* question from the symmetric dead-position rule above. On a clock flag, the game is a draw instead of a loss iff the OPPONENT of the flagging side "cannot checkmate … by any possible series of legal moves" — i.e. that side lacks mating material. `has_mating_material(p, color)` (§4.1) answers it **per side**: a side CAN mate unless it holds only a lone king, K+one knight, or K+one bishop; any pawn/rook/queen or two-or-more minors (NN/BN/BB) can. Note the deliberate divergence from the dead-position rule: **K+N+N returns `true` here** (two knights *can* mate a cooperating king, which "any series of legal moves" permits — the settled Lichess/chess.com timeout ruling), even though K+N+N is *not* an **automatic** draw via `is_insufficient_material`. Same material, two different questions — do not unify them. The engine reports capacity; S2 governs whether a flag ends as `FLAG` or `TIMEOUT_VS_INSUFFICIENT` (SPEC-S2-GAME-SERVER §3.3), consistent with detect-don't-govern.

### 3.8 Determinism & move ordering
Generated move lists have a **defined, stable order** (by from-square, then to-square, then promotion piece). Perft counts are order-independent, but a stable order makes golden tests, FFI, and client/server agreement reproducible (tenet §1.4.1). No ordering heuristic lives in the core — ordering for search is a §X-6 (analysis) concern.

### 3.9 Variant seam (forward-looking, not built here)
The generator exposes extension points — a per-piece move source list, a post-generation hook, and a position-extension slot — so ADR-4 overlays (Crazyhouse drops, Duck placement as a second half-ply, Spell `SpellCast` events, Fog visibility filter, N-seat turn order) attach without editing Standard move-gen. This seam is *declared* here (so the Standard core is built with it in mind) and *specified* in `VARIANTS.md`.

---

## 4. Interfaces & contracts

The engine is a library, not a service. Its public API is a **shared contract** consumed by S2 (Go server, via FFI/gRPC — ADR-12) and the client core (ADR-9). Changes to it need S1 + consumer + architecture-group review (CONVENTIONS §Cross-stream contracts). Signatures below are illustrative Rust, normative in *shape*, not final syntax.

### 4.1 Core legality API (what S2 calls on every move intent — T-1)
```rust
fn parse_fen(s: &str) -> Result<Position, FenError>;
fn to_fen(p: &Position) -> String;

fn legal_moves(p: &Position) -> MoveList;            // all legal moves
fn is_legal(p: &Position, m: Move) -> bool;          // single-move validation
fn make(p: &mut Position, m: Move) -> Undo;          // precondition: m is legal
fn unmake(p: &mut Position, m: Move, u: Undo);

fn status(p: &Position) -> GameStatus;               // Ongoing | Checkmate | Stalemate
                                                     //  | DrawInsufficientMaterial
                                                     //  | DrawFiftyMove(claimable|auto)
                                                     //  | DrawRepetition(threefold|fivefold)
fn is_in_check(p: &Position) -> bool;
fn has_mating_material(p: &Position, color: Color) -> bool;  // per-side FIDE-6.9 timeout capacity (§3.7)
```
S2's move path: receive `MOVE {game_id, ply_expected, uci}` intent → decode uci → `is_legal` → if legal, `make` + project a `MovePlayed` event (MASTER §VIII.3) + reply `MOVE_ACCEPTED`; else `MOVE_REJECTED {reason}`. The engine supplies the legal/illegal verdict and the resulting position; S2 owns ply/clock/events.

**Claimable vs automatic draws:** `status()` distinguishes *claimable* conditions (threefold, 50-move) from *automatic* ones (checkmate, stalemate, fivefold, 75-move, insufficient material). The engine only **reports availability** — it never auto-ends a game on a claimable condition. Acting on a claim (offering the draw UI, honoring an in-protocol claim, auto-drawing on the automatic thresholds) is S2's policy via the `DrawOffered`/`GameEnded` events (MASTER §VIII.3). This keeps rule *detection* (engine) separate from game *governance* (server).

### 4.2 Move encoding
```rust
fn uci_to_move(p: &Position, uci: &str) -> Result<Move, MoveError>;  // "e2e4", "e7e8q", "e1g1" castling
fn move_to_uci(m: Move) -> String;
fn move_to_san(p: &Position, m: Move) -> String;    // "Nf3", "exd5", "O-O", "e8=Q+", "Qh4#"
fn san_to_move(p: &Position, san: &str) -> Result<Move, MoveError>;
```
- **UCI** is the wire + engine form (MASTER §VIII.3 stores `uci`; §VIII.4 `MOVE` carries `uci`). Castling is encoded king-to-destination (`e1g1`); promotion suffix is lowercase piece.
- **SAN** is for display/PGN (F-08.4 download, F-17), with correct disambiguation (`Nbd2` vs `Nfd2`), capture `x`, check `+`, mate `#`, castling `O-O`/`O-O-O`, promotion `=Q`.
- Round-trip property: `move_to_uci` → `uci_to_move` and `move_to_san` → `san_to_move` are inverses for all legal moves (§8).

### 4.3 Relationship to the event store (ADR-10)
The engine neither reads nor writes the event store. It is the **fold step**: given the current `Position` and the next `MovePlayed.uci`, it produces the next `Position`. Replaying `game/{id}`'s `MovePlayed` events through `make` reconstructs any historical state bit-for-bit (MASTER §VIII.1, §XIII versioning). The engine's determinism (§1.4.1) is what makes this replay sound.

### 4.4 Perft entry point (test + benchmark surface)
```rust
fn perft(p: &Position, depth: u32) -> u64;                 // leaf node count
fn perft_divide(p: &Position, depth: u32) -> Vec<(Move, u64)>;  // per-root-move, for debugging mismatches
```
`perft_divide` is required, not optional — it is the primary tool for localizing a move-gen bug to a specific root move during G-A bring-up.

---

## 5. Edge cases & failure modes

The non-happy paths (CONVENTIONS: "state the edge cases, not just the happy path"). Each has a corresponding corpus entry in §8.2.

| ID | Edge case | Correct behavior |
|---|---|---|
| E-1 | EP target convention — hybrid: lenient internally/perft, strict in exported FEN (OPEN-D, §3.3) | Internal ep set after any double push (perft matches §8.1); exported FEN sets ep only when a legal ep capture exists (matches Lichess); EP legality (E-2) enforced regardless |
| E-2 | En passant that exposes own king (horizontal pin through both pawns, e.g. `8/8/8/K2pP2r/8/8/8/7k`) | **Illegal** — rejected by the legality filter |
| E-3 | En passant legal only on the immediate reply | A double push not answered immediately clears the ep right permanently |
| E-4 | Castling through an attacked square | **Illegal**; king may not pass through check |
| E-5 | Castling out of or into check | **Illegal** (out: king currently in check; into: destination attacked) |
| E-6 | Castling rights lost by rook **capture** (not just rook move) | Capturing a rook on its home square removes that side's corresponding castling right |
| E-7 | Promotion incl. all underpromotions | Q/R/B/N each a distinct legal move; a pawn on the last rank *must* promote |
| E-8 | Absolute pin | A pinned piece may not move off the pin ray if doing so exposes the king; may move *along* the ray / capture the pinner |
| E-9 | Discovered check & double check | Legal moves must resolve check; under **double check only king moves** are legal |
| E-10 | Checkmate vs stalemate boundary | 0 legal moves + in check = mate; 0 legal moves + not in check = stalemate (draw) |
| E-11 | Repetition identity includes castling rights & ep availability | Two positions with identical piece placement but different rights are **not** the same for threefold |
| E-12 | Insufficient-material classification | K vs K, K+B, K+N, K+B vs K+B same-color = draw; K+N+N vs K = **not** auto-draw |
| E-13 | 50/75-move clock reset | Any capture or pawn move resets `halfmove_clock` to 0 |
| E-14 | Max-ish legal move counts | Positions engineered for very high branching (e.g. the "218 legal moves" position) generate the full set without overflow/truncation |

Failure modes to defend against:
- **Malformed FEN / UCI / SAN** → typed error (`FenError`, `MoveError`), never a panic. S2 treats a parse error as a protocol error, not a legal-move rejection.
- **A FEN that parses but describes an impossible position** → typed error from `parse_fen` (#120), checked after all six fields parse. Otherwise move generation and `make` misbehave: castling could move an enemy piece, en passant could delete an arbitrary piece, and a missing king reads as stalemate. A FEN is rejected (`FenError` variant in brackets) when:
  1. a side does not have exactly one king (`KingCount`);
  2. a pawn stands on rank 1 or 8 (`PawnOnBackRank`);
  3. a castling right is set without that side's king on e1/e8 and its rook on the matching corner, h1/a1 or h8/a8 (`CastlingWithoutPieces`);
  4. the en-passant square could not follow a double push (`ImpossibleEnPassant`). That means it is on the wrong rank for the side to move (rank 6 when White moves, rank 3 when Black moves), or it is occupied, or the pushed pawn is not beside it, or that pawn's start square is not empty;
  5. the side not to move is in check (`OpponentInCheck`).

  An en-passant square that passes rule 4 but has no pawn able to capture is still **accepted**: the internal target is lenient by design (OPEN-D, §3.3), and the perft corpus relies on it. As defence in depth, castling is never generated without that side's rook on its corner, even for a position built without `parse_fen`. Piece counts other than kings (for example nine pawns, or more pieces than promotions allow) are deliberately **not** checked: they cannot make move generation misbehave.
- **`make` called with an illegal move** → debug-build assertion; release-build contract is "callers must `is_legal` first" (S2 always does). The convenience `apply` returns `Result`.
- **Bitboard/mailbox divergence** → debug invariant check (§3.2).
- **Hash collision on repetition** → full-equality guard (§3.7) so a Zobrist collision can never cause a wrong draw/not-draw ruling.

---

## 6. Non-functional handling

### 6.1 Performance (NFR-1, G-A "sub-ms move gen")
| Metric | Target | How measured |
|---|---|---|
| `legal_moves` on a typical middlegame position | **< 1 ms** (goal: tens of µs) | `criterion` micro-benchmark, median of N, release build |
| `is_legal` single-move validation (S2 hot path) | **< 10 µs** | `criterion`; this, not full gen, is the per-intent server cost |
| perft(startpos, 6) = 119,060,324 | completes in CI time budget | wall-clock in the perft harness |
| Allocation on the move-gen hot path | **zero** | heap-profiler / `#[no_std]` discipline in the core |

Numbers are measured and recorded per build; a regression beyond threshold fails CI (ties to MASTER §X.4). The engine's slice of NFR-1 is validation latency (`is_legal`); the rest of the move-latency budget (network, clock, event durability) is S2's.

### 6.2 Portability (NFR-10, ADR-9)
The same crate compiles to: the server target (consumed by Go via FFI/gRPC — ADR-12), `aarch64-apple-ios` (Swift via UniFFI), and `aarch64-linux-android` (Kotlin via UniFFI). Determinism (§1.4.1) means the move set and resulting FEN are identical on all three — asserted by running a slice of the perft + golden corpus in the mobile core's own test target (contributes to NFR-10's "shared protocol suite passes on all three").

### 6.3 Memory
Precomputed attack/magic tables are a fixed, bounded footprint (hundreds of KB, acceptable on mobile). `Position` is a small, `Copy`-able-or-cheap-to-clone value so clients can hold history cheaply.

---

## 7. Security & abuse

| Threat (MASTER §XI) | Relevance here | Control in this spec |
|---|---|---|
| **T-1 Move injection / illegal move** | *Primary.* The engine is the authority that makes client-supplied moves safe | §3.5 legal-only validation; §4.1 intent→accept/reject; clients never trusted to compute legality (MASTER §VIII.1) |
| T-3 Engine assistance (cheating) | Out of scope (S6/ADR-2), but the engine's determinism + stable move order makes replay-based signals (centipawn-loss, engine-match) reproducible | §3.8, §4.3 replayability |
| T-13 Anti-cheat evasion | Keep detection private (S6); the rules engine itself is public-safe (it only knows legality) | n/a — no secret logic lives here |

The engine handles no PII, no secrets, no network. Its only abuse surface is malformed input, covered by typed errors (§5) rather than panics (a panic in an FFI boundary on mobile is a crash — hence the no-panic contract).

---

## 8. Test plan — the conformance corpus (this section **is** G-A, MASTER §X.1)

Correctness is a hard gate; the corpus is versioned with the spec and lives beside the engine. **100% green is the merge bar** (MASTER §X.4). Each subsection maps 1:1 to MASTER §X.1's five pillars.

### 8.1 Perft suite (MASTER §X.1.1) — the backbone
Node-count verification against published reference counts. Minimum set, each to the stated depth, exact match required:

| Position | FEN | Depth | Reference nodes (spot) |
|---|---|---|---|
| Startpos | `rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1` | 6 | d6 = 119,060,324 |
| Kiwipete | `r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1` | 5 | d5 = 193,690,690 |
| Position 3 (ep/pins) | `8/2p5/3p4/KP5r/1R3p1k/8/4P1P1/8 w - - 0 1` | 6 | d6 = 11,030,083 (d7 = 178,633,661) |
| Position 4 | `r3k2r/Pppp1ppp/1b3nbN/nP6/BBP1P3/q4N2/Pp1P2PP/R2Q1RK1 w kq - 0 1` | 5 | d5 = 15,833,292 |
| Position 5 | `rnbq1k1r/pp1Pbppp/2p5/8/2B5/8/PPP1NnPP/RNBQK2R w KQ - 1 8` | 5 | d5 = 89,941,194 |
| Position 6 (Steven Edwards) | `r4rk1/1pp1qppp/p1np1n2/2b1p1B1/2B1P1b1/P1NP1N2/1PP1QPPP/R4RK1 w - - 0 10` | 5 | d5 = 164,075,551 |

Depths 6–7 run in a nightly/pre-gate tier (slow); a fast tier (depth ≤ 5) runs every commit. `perft_divide` (§4.4) is used to localize any mismatch. These are the canonical "tricky FENs" MASTER §X.1.1 names.

**Perft counts move paths, not game outcomes:** terminal nodes that happen to be checkmate or stalemate are still counted as leaves, and perft never stops early at a mate/draw. Draw-by-repetition, 50-move, and insufficient-material rules are ignored by perft entirely. So `status()` (§3.7) must **not** short-circuit move generation — a reference counts mismatch at depth ≥ 2 is often a generator that stopped at a "game over" node.

### 8.2 Legal-move edge-case library (MASTER §X.1.2)
One or more explicit cases per edge case E-1..E-14 (§5), each asserting the exact legal-move set (not just a count) for a hand-built position. Non-negotiable members:
- **En passant exposing the king** (E-2): position `8/8/8/K2pP2r/8/8/8/7k` style — assert the ep capture is **absent** from `legal_moves`.
- **Castling through / into / out of check** (E-4,E-5): three positions, assert the castling move is absent.
- **Castling right lost by rook capture** (E-6): make a move capturing a home-square rook, assert the right is gone from the resulting FEN.
- **Underpromotion** (E-7): assert all four promotion moves present on a promoting push *and* capture.
- **Double check → king moves only** (E-9).
- **218-move position** (E-14): `R6R/3Q4/1Q4Q1/4Q3/2Q4Q/Q4Q2/pp1Q4/kBNN1KB1 w - - 0 1` — assert count = 218.

### 8.3 Game-end detection (MASTER §X.1.3)
Explicit positions asserting the exact `GameStatus`:
- Checkmate (back-rank, smothered, Fool's/Scholar's end states), stalemate (classic K+P vs K stalemate), threefold **by position** (a repetition reached by *different* move orders must still count; a position differing only in castling rights must **not**), fivefold auto, 50-move claimable + 75-move auto, each insufficient-material class in E-12 incl. the K+N+N-is-not-a-draw case.

### 8.4 Per-variant conformance (MASTER §X.1.4)
*Declared here, specified in `VARIANTS.md`.* Each variant ships only when its own corpus is 100% green (ADR-4). The Standard corpus (§8.1–8.3) is the shared base every variant corpus extends. No variant merges against this spec.

### 8.5 Golden-game replay + regression net (MASTER §X.1.5)
A library of complete real games (incl. games exercising en passant, promotion, castling both sides, threefold claims) replayed move-by-move through `make`, asserting the FEN at **every ply**. This doubles as the regression net: **every bug found becomes a new golden case** before its fix merges (MASTER §X.1.5). Source games: a curated set of annotated master games + the engine's own historical bug repro positions.

### 8.6 Property-based tests (MASTER §X.2 "property" layer)
Run every commit (fast):
- `unmake(make(p,m)) == p` for random legal `m` (make/unmake inverse — §3.6).
- `uci_to_move(move_to_uci(m)) == m` and SAN round-trip for all legal moves (§4.2).
- `parse_fen(to_fen(p)) == p` (FEN round-trip).
- Pseudo-legal-filter set == pin-aware set, if both generators exist (§3.5).
- `perft(p,1) == legal_moves(p).len()` for a large random-position sample (cheap cross-check that catches most gen bugs before the slow perft tier runs).

### 8.7 Differential testing
During G-A bring-up, run positions/perft against a reference crate (`shakmaty`/`cozy-chess`, MASTER ADR-11) and assert agreement. This is a *development* gate (finds our bugs fast), retained in the nightly tier; it is not a substitute for the published-reference perft counts in §8.1.

### 8.8 Test tiers / where they run (MASTER §X.2, §X.4)
| Tier | Contents | Cadence |
|---|---|---|
| Fast (per commit) | perft ≤ d5, all edge-case & game-end asserts, all property tests, `is_legal`/`legal_moves` micro-bench threshold | every commit / PR |
| Slow (nightly + pre-gate) | perft d6–d7, full golden replay, differential vs reference crate | nightly + before G-A sign-off |
| Mobile parity (NFR-10) | perft startpos d5 + a golden slice, run in the UniFFI core's iOS/Android test target | nightly |

---

## 9. Acceptance criteria (Definition of Done → G-A)

A reviewer/QA can call this engine DONE — and declare **G-A green** (MASTER §V.2, §X.4, §XII) — when **all** hold:

- [ ] **Perft (§8.1): 100% exact match** on all 6 positions to their stated depths. No position off by a single node.
- [ ] **Edge-case library (§8.2): 100% green**, every case E-1..E-14 covered by ≥1 explicit assertion on the exact move set.
- [ ] **Game-end detection (§8.3): 100% green**, including threefold-by-position (not move list), the castling-rights repetition distinction, and the K+N+N non-draw.
- [ ] **Golden replay (§8.5): 100% green**, FEN asserted at every ply; the regression net is wired so a new bug *must* add a golden case to merge its fix.
- [ ] **Property tests (§8.6): 100% green** (make/unmake inverse, UCI/SAN/FEN round-trips, pseudo-legal==pin-aware, perft(·,1)==legal_moves len).
- [ ] **Performance (§6.1)**: `legal_moves` < 1 ms and `is_legal` < 10 µs on the reference position, recorded; CI fails on regression beyond threshold.
- [ ] **No panics on malformed input** — fuzz FEN/UCI/SAN parsers; all errors are typed (§5).
- [ ] **Mobile parity (§8.8)**: the perft+golden slice passes in the iOS and Android UniFFI core test targets, byte-identical results (NFR-10, ADR-9).
- [ ] **No known legal-move defect** at sign-off (NFR-5: "0 known legal-move defects at any gate").
- [ ] **Public API (§4) reviewed** by S2 + architecture group as a shared contract (CONVENTIONS §Cross-stream contracts), since S2 and the client core build against it.
- [ ] Spec status advanced `REVIEW → RATIFIED` with those reviews recorded before implementation tickets are cut (`BUILDING`).

G-A being green **unblocks** S2 live play, S1 variant work, and all puzzle/analysis work (MASTER §V.2).

---

## 10. Open questions

| ID | Question | Needs | Proposed resolution |
|---|---|---|---|
| OPEN-A | **Wrap a reference crate (`shakmaty`/`cozy-chess`) vs. own bitboard implementation?** | S1 lead + architecture-group call | Either satisfies this spec; §8 corpus is the acceptance bar regardless. Recommendation: prototype against a crate for differential testing (§8.7) and decide on own-vs-wrap from the measured perf (§6.1) and FFI/mobile-footprint fit (ADR-9). **Not an architecture decision requiring a new ADR** unless it changes the ADR-11 language stance (it does not). |
| OPEN-B | Zobrist key scheme (which fields, seed stability across versions) | S1 | Must cover exactly the repetition-identity fields (§3.7) and be stable across builds for golden tests; pin the seeds in code. Low-risk, decide in implementation. |
| OPEN-C | Expose `apply(&self)->Result<Position>` (copy-make) in the FFI surface for clients, or UniFFI make/unmake? | S1 + S5 | Lean copy-make for clients (immutability over FFI is simpler), make/unmake for the server hot path. Confirm with S5 when the client core spec lands (ADR-9). |
| OPEN-D | ~~En-passant target convention: lenient vs strict (§3.3)~~ **CLOSED (owner-approved 2026-10-12)** | — | **Resolved: hybrid** (§3.3) — lenient internally + for perft (so §8.1 counts hold unchanged), strict in exported FEN (matches Lichess/`scalachess` [lila #6827], satisfies ADR-4 "pin to Lichess", and makes threefold identity consistent). Approved at ratification. |

OPEN-A, -B, -C remain implementation-level and open to the S1 lead; OPEN-D is **closed** (hybrid, owner-approved). *None is architecture-blocking; all are resolvable within S1 (CONVENTIONS: local specs implement decisions, they don't make architecture). No new ADR is proposed by this spec — it implements ADR-3/-4/-9/-10/-11/-12 as written.*
