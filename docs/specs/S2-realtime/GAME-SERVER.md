# Game Server: lifecycle, clocks, matchmaking, reconnection, codec consumption — S2 Real-time

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER |
| Owner | S2 Real-time (stream lead — fill in); co-reviewed by Architecture group (shared-contract + NFR) + S6 Trust-safety (rated play, ADR-2 veto) |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-13 |
| Implements (master IDs) | X-2; F-01 (Play Online), F-01.3/.4/.5; NFR-1 (move latency), NFR-2 (availability / no game loss), NFR-3 (scale), NFR-7 (durability), NFR-5 (one engine), NFR-10 (one codec); MASTER §VIII.1 (server-authoritative, event-sourced), §VIII.4 (wire messages), §IX.1 (latency), §IX.2 (capacity); ADR-10, ADR-12, ADR-14, ADR-15 (cgo boundary, PROPOSED) |
| Depends on | SPEC-S1-ENGINE-CORE (G-A, GREEN) — legality authority · SPEC-S2-PROTOCOL (RATIFIED §4A) — wire + event contract, cited not copied · PLAN-S2-CODEC (RATIFIED) — the `pgc-protocol` codec this server links · SPEC-S3-DATA-MODEL (event store + entities) · SPEC-PE-API-CONTRACT (REST surface, G-D sibling) |
| Gates it feeds | **G-E** (scale proven, NFR-3) — the primary gate this component carries. Also the F-01 vertical-slice exit (P1). Does NOT feed G-D (that is the protocol/API freeze; this consumes it). |

> This spec covers **server behavior** that SPEC-S2-PROTOCOL explicitly left out (its §1 non-goals: "clock algorithm, matchmaking algorithm, reconnection state machine, sharding … belong in a later GAME-SERVER.md"). It **cites** the frozen wire/event contract and the codec; it never re-defines a §4A shape or re-implements chess rules (NFR-5) or the codec (ADR-14). It also resolves **OPEN-C4** (the concrete cgo build wiring PLAN-S2-CODEC §4.5 delegated here).

## 1. Purpose & scope

`services/game-server` (X-2, Go per ADR-12) is the authoritative real-time tier for live play: it owns the WebSocket connection lifecycle, the game state machine, the clock, matchmaking, reconnection, and the durable event append — the behavior behind the frozen SPEC-S2-PROTOCOL messages. It is the first consumer of the `pgc-protocol` codec over cgo and the first consumer of the S1 engine for server-side legality.

**In scope:**
1. **Connection + session lifecycle** — accept WS, `AUTHENTICATE`, bind session→seat, heartbeat (`PING`/`PONG`), teardown.
2. **Game lifecycle state machine** — seek → match → create → play → end, as a fold over the append-only event stream (SPEC-S2-PROTOCOL §3.2).
3. **Clock model** — per-seat clocks, increment/delay, flag detection, drift budget (NFR-1).
4. **Matchmaking** — pool-based pairing by `pool_id` (rating × format × time-control), rated/unrated (F-01.3).
5. **Reconnection** — reconnect window, `STATE` resync, clock-aware forfeit (F-01.5, NFR-2).
6. **Move pipeline** — decode intent → validate via the engine → persist event (before ack, NFR-7) → encode + emit.
7. **Codec consumption (OPEN-C4)** — the concrete in-process cgo wiring to `pgc-protocol`'s static library: C-ABI surface, header generation, link flags, cross-compile, lifecycle of returned buffers.

**Non-goals (explicit):**
- **No wire/event FORMAT.** The byte layout, type codes, enum tables and event envelope are SPEC-S2-PROTOCOL §4A, frozen; this server encodes/decodes them via the codec and defines none of them.
- **No chess rules.** Legality, check/mate/draw detection, SAN/UCI parsing are the S1 engine (NFR-5). `MOVE_REJECTED{reason}` wraps the engine's verdict.
- **No codec implementation.** `pgc-protocol` (ADR-14) owns encode/decode; this server links it and must not hand-roll bytes.
- **No rating math.** Glicko-2 is X-4 (ADR-1, SPEC-S3-RATING); the server appends `RatingApplied` events the rating service produces, it does not compute ratings.
- **No persistence-engine choice.** The event store product is ADR-10 / SPEC-S3-DATA-MODEL; this spec defines how the server reads/writes the stream, not the store.
- **No REST surface.** Non-realtime endpoints (pool catalogue, history) are SPEC-PE-API-CONTRACT (X-13).
- **No spectating / variants / N-seat.** v1 is Standard, 2-seat (SPEC-S2-PROTOCOL §1); the state machine must not foreclose them (§5 extensibility) but they are later chunks.

## 2. Requirements traced

| Master ID | Requirement | Where handled |
|---|---|---|
| X-2 | Real-time game server: clocks, relay, matchmaking, reconnection | §3 (whole) |
| F-01 / F-01.3 / .4 / .5 | Play Online: matchmaking, clock/draw/resign/abort/rematch/takeback, disconnect handling | §3.4 (matchmaking), §3.3 (clock), §3.2 (lifecycle), §3.5 (reconnection) |
| NFR-1 | Move ack p50 < 50 ms / p99 < 150 ms; clock drift < 50 ms | §6.1 (move pipeline budget), §3.3 (clock sync via `PONG`) |
| NFR-2 | 99.95% play availability; zero in-progress-game loss on single-node failure | §3.5 (reconnection), §6.2 (failover; event store as truth) |
| NFR-3 | 1M conns, 200k live games, 50k moves/sec; linear horizontal scale | §6.3 (sharding model), feeds G-E |
| NFR-7 | Durable move event BEFORE ack; immutable/auditable | §3.6 (persist-before-ack), cites SPEC-S2-PROTOCOL §3.2 |
| NFR-5 | One rules engine; no reimplementation | §3.6 (engine is the only legality authority) |
| NFR-10 | One codec; byte-identical across clients + server | §4 (cgo links `pgc-protocol`; no hand-rolled bytes) |
| MASTER §VIII.1 | Server-authoritative, event-sourced | §3.1, §3.2 |
| ADR-12 | Go service, Rust engine over a thin boundary | §3, §4 (cgo in-process for the hot path) |
| ADR-14 | One schema-defined codec in the Rust core | §4 (consumed, not re-implemented) |

## 3. Design

### 3.1 Topology & authority
Clients hold WebSocket connections to a **game-server node**. The server is the sole authority for game truth: clients send **intents** (`C→S` frames); the server validates each against the S1 engine, appends the authoritative event (SPEC-S2-PROTOCOL §3.2), then emits the `S→C` frame. A client field never asserts truth (no client-set clock/result) — the wire format gives it none (SPEC-S2-PROTOCOL §7, T-1/T-2).

### 3.2 Game lifecycle state machine
A game is a fold over its append-only stream `game/{id}`. Server-held states and the events/frames that drive them:

```
(no game) --SEEK--> SEEKING --[matched]--> MATCHED --PlayerJoined x2--> LIVE
LIVE --MovePlayed--> LIVE                      (per legal move)
LIVE --DrawOffered/DrawAccepted--> ...--> ENDED (GameEnded + term=DRAW_AGREE)
LIVE --Resigned--> ENDED (term=RESIGN)
LIVE --ClockFlagged--> ENDED (term=FLAG / TIMEOUT_VS_INSUFFICIENT)
LIVE --[engine: mate/stalemate/draw]--> ENDED (term=MATE/STALEMATE/REPETITION/FIFTYMOVE/INSUFFICIENT)
MATCHED/LIVE(early) --Aborted--> ENDED (term=ABORT)
ENDED --RatingApplied (rated only, from X-4)--> (terminal)
```
Every transition is an appended event; board/PGN/clock are projections, never written directly (SPEC-S2-PROTOCOL §3.2). `TakebackReq`/`TakebackAccepted` roll the live projection back by re-folding to `ply-n` (unrated/friendly only, F-01.4) — the stream is append-only, so a takeback is a new event, not a deletion.

### 3.3 Clock model
- Per-seat remaining-ms, carried on the wire only as `clock_after_ms` in `MOVE_ACCEPTED` and `clocks[]` in `STATE` (SPEC-S2-PROTOCOL §4A.3). The server is the sole clock authority.
- Time control is a `TimeControlId` string (Q-A4); the id → `{base_ms, increment_ms, per_move_ms?}` expansion is resolved once from the pool catalogue (SPEC-PE-API-CONTRACT `GET /v1/pools`), cached per node. The server applies base/increment/delay deterministically on each `MovePlayed`.
- **Flag detection:** a seat whose remaining time reaches 0 before its move is `ClockFlagged` → `GameEnded{term=FLAG}` (or `TIMEOUT_VS_INSUFFICIENT` when the opponent has insufficient mating material — the engine answers "insufficient material", §VIII enum §4A.5). Flag is evaluated server-side on move receipt and on a per-game timer, never trusted from a client.
- **Drift (NFR-1, < 50 ms/game):** `PING{t}` / `PONG{t, server_t}` lets the client estimate offset + RTT; the authoritative clock is always the server's. §6.1 bounds the ack path so the clock the client sees tracks the server within budget.

### 3.4 Matchmaking (F-01.3)
- A `SEEK{pool_id, time_control, rated}` enters the seeker into the pool's waiting set (Redis sorted-set keyed by rating; Redis is hot/ephemeral only, never truth — ADR-10). Pairing picks the closest-rating compatible waiting seeker within a widening rating window over time.
- On a pair: allocate `game_id`, assign seats/colors, append `GameCreated` + two `PlayerJoined`, emit `MATCHED{start_state}` to both (the `STATE` body reused per §4A.6). Rated games draw from the rated pool only; unrated from unrated.
- Matchmaking state is Redis-ephemeral; a node loss drops only unmatched seekers (they re-seek), never a live game (which is in the event store).

### 3.5 Reconnection (F-01.5, NFR-2)
- A dropped client re-opens a WS, re-`AUTHENTICATE`s, and sends its last known `ply`. The server replies `STATE{fen, turn, clocks}` folded from the event stream — authoritative resync, zero game loss.
- **Reconnect window / clock-aware forfeit:** while a seat is disconnected its clock keeps running (chess rules); if it does not reconnect before its clock flags, the game ends `FLAG`. A separate (shorter, policy) abort window applies pre-first-move. Window durations are config, not hard-coded.
- Because a live game is a fold of the durable stream, any node can serve the resync — reconnection does not require landing on the original node (supports §6.3 failover).

### 3.6 Move pipeline (the hot path) & durability
Per `C MOVE{game_id, ply_expected, uci}`:
1. **Decode** the frame via the codec (cgo, §4).
2. **Authorize** — the session's bound seat must be the side to move; `ply_expected` must equal the server's current ply (else `MOVE_REJECTED{STALE_PLY}`); the game must be LIVE (`GAME_OVER`), the mover seated (`NOT_YOUR_SEAT`), the game known (`UNKNOWN_GAME`).
3. **Validate legality via the S1 engine** — the ONLY legality authority (NFR-5). Illegal → `MOVE_REJECTED{ILLEGAL}`, no event appended.
4. **Persist** `MovePlayed` to the event store **and wait for the durable commit BEFORE** emitting `MOVE_ACCEPTED` (NFR-7 persist-before-ack; ack-before-persist is a correctness bug).
5. **Encode + emit** `MOVE_ACCEPTED{ply, clock_after_ms}` to the mover and relay the move to the opponent.
Idempotency: a duplicate `(game_id, ply_expected)` whose ply is already persisted re-sends the existing `MOVE_ACCEPTED` rather than appending twice (SPEC-S2-PROTOCOL §5).

### 3.7 Anti-cheat feed (ADR-2)
The event stream is the fair-play signal source; the server must keep per-move timing (`clock_after_ms`, `server_ts`) in `MovePlayed` so S6 detection is not starved (SPEC-S2-PROTOCOL §7). The server emits no derived rating/score itself. S6 has review veto on anything weakening this (CONVENTIONS) — hence S6 is a required reviewer.

## 4. Interfaces & contracts — codec consumption over cgo (resolves OPEN-C4)

The server links the **`pgc-protocol-cgo`** sibling crate's **`staticlib`** and calls encode/decode as C-ABI functions **in-process** (PLAN-S2-CODEC §4.5, v1.1: in-process cgo, no sidecar, no network hop on the move path). Per the amended plan, `codec-cgo` is a thin delegate-only wrapper over the pure `pgc-protocol` core — the same sibling pattern as `codec-ffi` (UniFFI) and `codec-wasm` (wasm), under the uniform cgo mechanism of ADR-15 (PROPOSED) — so the unavoidable `unsafe` C-ABI seam is quarantined there and the core stays `#![forbid(unsafe_code)]`. This section fixes the Go-side consumption detail PLAN-S2-CODEC §4.5 / OPEN-C4 delegates to this spec; the plan owns the crate's existence + C-ABI shape.

**4.1 C-ABI surface (`protocol/codec-cgo`, `pgc-protocol-cgo`).** A new sibling crate: `crate-type = ["staticlib", "rlib"]`, delegate-only over `pgc-protocol`, with a `#[no_mangle] extern "C"` module (the one place `unsafe` lives — each fn carries a safety comment) and a `cbindgen`-generated C header. Surface is byte-in / byte-out (mirroring the proven shape of the other consumers — decode→re-encode and true encode/decode over frames):
```c
// returns 0 on success and fills *out (allocated by Rust) + *out_len;
// non-zero = the CodecError discriminant. Go copies *out, then calls pgc_free.
int32_t pgc_decode_client(const uint8_t* buf, size_t len, uint8_t** out, size_t* out_len);
int32_t pgc_encode_client(const uint8_t* buf, size_t len, uint8_t** out, size_t* out_len);
// ... server / event variants ...
void    pgc_free(uint8_t* ptr, size_t len);   // Rust frees what Rust allocated
```
**Buffer ownership:** Rust allocates outputs; Go copies into Go memory and calls `pgc_free` — it never frees a Rust pointer itself and never hands Rust a Go pointer Rust would retain (the cgo pointer rule). This is the single hand-written unsafe seam; it gets a dedicated fuzz + leak test (§8). The exact decode return representation is Q-GS1.

**4.2 Build wiring.** `services/game-server` builds the staticlib via a `go:generate`/Makefile step (`cargo build -p pgc-protocol-cgo --release`, dropping the `.a` + the cbindgen header into a known path), then cgo `#cgo LDFLAGS: -L<path> -lpgc_protocol_cgo`. Per deploy target (linux/amd64, linux/arm64) the Makefile parameterizes the matching Rust `--target` (mirrors the engine's mobile cross-compile discipline).

**4.3 What crosses the boundary.** Only frames (`[]byte`) and the typed Go structs the server owns. The server does NOT re-encode or inspect the §4A byte layout outside the codec call. A Go contract test decodes the committed golden vectors (`protocol/vectors/vectors.json`) and asserts byte-identical re-encode — the Go leg of NFR-10 parity (completing Rust + iOS + Android + web + **Go**).

**4.4 Change control.** The `codec-cgo` crate + its C-ABI are defined by PLAN-S2-CODEC v1.1 (§2, §4.5); this spec consumes them and adds the Go-side wiring. It does **not** change SPEC-S2-PROTOCOL §4A — the wire bytes are unchanged; cgo is a new *access path* to the same codec. Because the amendment is a change to a RATIFIED plan, it + this spec's §4 are reviewed together by the architecture group (PLAN-S2-CODEC #30).

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Stale/racing move | `MOVE_REJECTED{STALE_PLY, ply_expected=server's current}` (SPEC-S2-PROTOCOL §5). |
| Illegal move | engine rejects → `MOVE_REJECTED{ILLEGAL}`; no event. |
| Move after game end | `MOVE_REJECTED{GAME_OVER}`. |
| Duplicate MOVE (retry) | idempotent on `(game_id, ply_expected)`; re-ack, no second event. |
| Disconnect mid-game | clock keeps running; reconnect → `STATE` resync; flag if clock expires first. |
| Node failure mid-game | another node resumes from the event store (fold); no game loss (NFR-2). |
| Persist fails after validation | do NOT ack; surface `ERROR{INTERNAL}`; the move did not happen (no ack-before-persist). |
| Codec decode error on inbound frame | `ERROR{PROTOCOL_VIOLATION}` (truncated/unknown-type/bad-enum — the codec's typed `CodecError`, never a panic/trap in-process). |
| cgo boundary: Rust returns error code | mapped to the matching `ERROR`/`MOVE_REJECTED`; the Go side never dereferences a null/!0-status out-buffer. |
| Extensibility | state machine + pool model leave room for N-seat/variants/spectating without reusing a v1 event (SPEC-S2-PROTOCOL §5). |

## 6. Non-functional handling

- **6.1 Move latency (NFR-1).** Hot path = decode (cgo ~tens of ns) → authorize (O(1) in-memory) → engine legality (sub-ms, G-A §6.1) → durable append (the dominant term) → encode → emit. Budget: p50 < 50 ms / p99 < 150 ms same-region; the durability write is the thing to keep tight (co-locate the event-store write path, batch/pipeline the fsync per NFR-7's RPO). cgo overhead is negligible vs the move budget (PLAN-S2-CODEC §4.5). Measured under §8 load, proven at G-E — not asserted here.
- **6.2 Availability / no game loss (NFR-2).** Game truth is the durable event store, not node memory; a node loss loses only ephemeral Redis matchmaking state and open sockets (clients reconnect). 99.95% play availability is an operational target met by horizontal redundancy (§6.3) + the stateless-resume property (§3.5).
- **6.3 Scale (NFR-3 → G-E).** Target 1M conns / 200k live games / 50k moves/sec, linear horizontal scale. Approach: shard games across nodes by `game_id`; a connection routes to its game's current node; goroutine-per-connection (ADR-12 rationale). Redis for presence/matchmaking/clock-hot-state; event store for truth. The concrete sharding/routing/load-test is G-E work; this spec fixes the *model*, not the proven numbers.

## 7. Security & abuse

Part XI threats on this component:
- **T-1/T-2 move/clock forgery** — mitigated by design: server is the sole legality + clock authority; the wire gives clients no truth field (§3.1).
- **Spoofed seat / acting for opponent** — a `C MOVE` is authorized against the SESSION's bound seat (from `AUTHENTICATE`), never a client-supplied actor (§3.6 step 2).
- **Replay / dup injection** — idempotency + `ply_expected` race guard (§5).
- **Frame flooding** — per-connection rate limits (token bucket) at the WS edge; the frozen frame header keeps per-frame parse work bounded.
- **cgo memory safety** — the one unsafe seam (§4.1) is fuzzed + leak-checked (§8); Rust owns all codec allocations.
- **Token handling** — `AUTHENTICATE{token}` validated against X-3; never logged (SPEC-S2-PROTOCOL §7).
- **Fair play (ADR-2)** — §3.7; S6 veto; S6 is a required reviewer before RATIFIED.

## 8. Test plan

- **Unit** — clock arithmetic (base/increment/delay, flag boundary), state-machine transitions, matchmaking pairing/windowing, authorization rules.
- **cgo contract (the Go NFR-10 leg)** — decode every committed golden vector (`protocol/vectors/vectors.json`) through the cgo codec and assert byte-identical re-encode; the Go struct ↔ bytes parity completing Rust/iOS/Android/web/Go.
- **cgo safety** — fuzz the C-ABI with malformed/truncated/oversize buffers (never a crash, always a typed error code); a leak test asserting `pgc_free` balances every allocation (valgrind/asan or a counting allocator).
- **Persist-before-ack (NFR-7)** — inject a persistence stall and assert no `MOVE_ACCEPTED` is emitted until the commit; crash between validate and persist loses the move (no ack), not corrupts it.
- **Reconnection** — drop + resume mid-game; assert `STATE` fold equals pre-drop truth; clock-aware forfeit fires correctly.
- **Integration (F-01 slice)** — two clients, full Standard game: match → moves → mate/resign/draw/flag → (rated) `RatingApplied`.
- **Load / chaos (G-E)** — ramp to NFR-3 targets with node kills + reconnection storms, assert zero game loss and the §6.1 latency budget. (This is the G-E deliverable; scaffolded here, run at G-E.)

## 9. Acceptance criteria (Definition of Done)

- [ ] Spec reviewed by S2 + Architecture group (shared cgo contract + NFR budgets) + **S6** (rated-play / anti-cheat feed, ADR-2 veto); RATIFIED.
- [ ] `protocol/codec-cgo` (`pgc-protocol-cgo`) sibling crate exists — `staticlib` + `#[no_mangle] extern "C"` + cbindgen header, delegate-only over the pure core — with the §8 cgo contract + safety (fuzz + leak) tests green. **(Consumes PLAN-S2-CODEC v1.1 §4.5 / OPEN-C4; the plan owns the crate, this spec owns the Go-side wiring.)**
- [ ] `services/game-server` links it and the Go golden-vector parity test passes byte-for-byte (the Go leg of NFR-10).
- [ ] Game lifecycle, clock (incl. flag + drift), matchmaking, reconnection implemented for F-01 Standard 2-seat, with their §8 unit/integration suites green.
- [ ] Persist-before-ack (NFR-7) demonstrated by the stall/crash tests.
- [ ] Engine is the only legality authority (no chess logic in Go — a reviewer greps for it).
- [ ] The F-01 vertical-slice integration test (match→play→end→rating) green on web against this server.
- [ ] G-E load/chaos harness scaffolded (run + signed off AT G-E, not here).

## 10. Open questions

- **Q-GS1 — cgo decode return shape — DECIDED (a), owner 2026-10-13.** The C-ABI is **byte-oriented** — `decode` validates and returns the canonical frame bytes, `encode` takes a built frame — plus a small set of typed scalar accessors for the few fields the server acts on (`game_id`, `ply_expected`, `uci`, message type/direction). Not a flattened C struct per message type. Rationale: smallest `unsafe` surface at the one hand-written seam (no ~19 per-message structs with nested `Vec`/sub-struct ownership to leak or mis-free — the fuzz+leak test can cover the whole surface); matches what the server actually does (relay frames, read a few routing/authz fields); same shape as the proven wasm `reencode_*` consumer, so parity is one assertion across all four. Reversible behind the same C-ABI — if profiling ever shows a scalar accessor is a measurable cost at 50k moves/sec, add a single `MOVE`-only fast-path struct, not a general marshaller. Not an ADR (an internal access-path detail of one crate); the uniform cgo *mechanism* it rides on is ADR-15.
- **Q-GS2 — sharding/routing substrate.** Consistent-hash on `game_id` vs a routing table in Redis vs a service mesh. Deferred to the G-E design; does not block F-01 single-node bring-up. Flag if it should be an ADR.
- **Q-GS3 — matchmaking fairness policy.** Rating-window widening schedule, provisional-rating handling, anti-sniping. Needs X-4 (rating) input; parked until the rating service spec lands.
- **Q-GS4 — engine's own Go cgo seam — ROUTED TO ADR-15 (PROPOSED).** The server validates every move via the S1 engine (NFR-5), but the engine has only a UniFFI (mobile) seam today and no Go path — so this is a real gap on the F-01 critical path, not hypothetical. Rather than design a second, divergent cgo setup, **ADR-15** (MASTER-PLAN, PROPOSED) generalizes PLAN-S2-CODEC v1.1's `codec-cgo` into one uniform pattern: every Rust crate Go consumes gets a `<crate>-cgo` sibling (staticlib + extern "C" + cbindgen, delegate-only), same buffer-ownership + build + fuzz/leak discipline. So `pgc-engine → engine-cgo` reuses `codec-cgo`'s pattern. **Scope guard:** `engine-cgo` is NOT part of codec chunk 4.5 — it is built later when the game-server wires move validation; ADR-15 only ensures it does not diverge. Blocks on architecture-group DECIDED of ADR-15.

---

### Reviewer notes (not part of the ratified contract)
- This spec **consumes** the frozen SPEC-S2-PROTOCOL §4A and the ADR-14 codec; it introduces **no** new wire bytes. The only new shared contract is the cgo **access path** (§4), which needs architecture-group sign-off but does not touch G-D.
- G-D is unaffected by this spec; G-D needs SPEC-S2-PROTOCOL + SPEC-PE-API-CONTRACT frozen together (Q-P1). This spec feeds **G-E**.
- OPEN-C4 (PLAN-S2-CODEC) is resolved in §4; the codec `staticlib`/C-ABI deliverable is enumerated in §9.
