# Game-server GS-6 — WebSocket transport & connection lifecycle (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS6 (implementation slice of SPEC-S2-GAME-SERVER §3.5/§3.6/§3.1) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-15 |
| Implements (master IDs) | X-2, F-01 (Play Online end-to-end), F-01.5 (reconnection), NFR-7 (persist-before-ack), NFR-5, ADR-12, ADR-14/15 (codec over cgo) |
| Depends on | GS-1/2/3 (session + pipeline + engine legality), GS-4 (durable store), GS-5 (matchmaking), the codec seam (frames + the GS-6a message seam below). All merged except the GS-6a prerequisite. |
| Gates it feeds | **The first chunk after which a move travels browser→server→browser** — the F-01 vertical slice's transport. Does NOT by itself make a lettered gate green (a *rated* game still needs G-B anti-cheat + G-C rating). |

> **The milestone chunk — but it carries a codec-seam prerequisite (GS-6a).**
> GS-6 is the WebSocket edge that finally connects the merged pieces (engine,
> codec, session, store, matchmaking) to a real client socket. Like GS-4 needed
> GS-4a (a `game.Event ⇄ §4A bytes` seam), GS-6 needs a **client/server MESSAGE
> seam**: the Go codec today exposes only frame→frame reencode
> (`ReencodeClient/Server`) and the event seam — it has NO way to decode a
> `ClientMsg` to typed Go fields (to read `Move{game_id, ply_expected, uci}`) or
> encode a `ServerMsg` from Go fields (to build `MoveAccepted{…}`). That seam
> (**GS-6a**) must land first, keeping §4A encoding in Rust (NFR-5). This slice
> specifies BOTH the seam prerequisite (§3.0) and the transport (§3.1+).

## 1. Purpose & scope

Accept WebSocket connections, authenticate them, and wire the frozen
SPEC-S2-PROTOCOL message flow to the already-built server behavior: `SEEK` →
GS-5 matchmaking → `MATCHED`; `MOVE` → GS-1 pipeline/GS-3 session → `MOVE_ACCEPTED`
/`MOVE_REJECTED`; `STATE` resync on reconnect; `PING`/`PONG` heartbeat; the clock
`TickFlagCheck` driven on a timer; and graceful teardown. The server stays the
sole authority — clients send intents, never truth (§3.1 of the parent spec).

**In scope (GS-6):**
- **GS-6a — the client/server message seam** (prerequisite, §3.0): `DecodeClient`
  (frame → typed `ClientMsg` fields) and `EncodeServer` (typed `ServerMsg` fields
  → frame) across the `codec-cgo` C-ABI, mirroring the GS-4a event seam, with its
  own golden-vector parity + ABI-fuzz tests. §4A stays in Rust (NFR-5).
- A `services/game-server/internal/transport` (or `cmd/`) package: a WS server
  (gorilla/nhooyr-style — Q-GS6a) with one goroutine per connection (ADR-12),
  `AUTHENTICATE` handshake, and a connection↔(game, seat) registry.
- **Message dispatch:** decode each inbound frame via the seam, route by type to
  the matchmaker (`SEEK`) or the seat's live session (`MOVE`, `RESIGN`, `ABORT`,
  `OFFER_DRAW`, `TAKEBACK`), encode and send the authoritative `S→C` reply.
- **MATCHED delivery:** on a GS-5 pair, send `MATCHED{start_state}` to BOTH seats'
  connections (the transport owns the seat→connection map; the matchmaker returns
  the `Match`).
- **Reconnection (§3.5):** a dropped client re-opens, re-`AUTHENTICATE`s, sends
  its last `ply`; the server replies `STATE{fen,turn,clocks}` folded from the
  durable stream (any node can serve it — GS-3 rebuilds from GS-4). The
  disconnected seat's clock keeps running; `TickFlagCheck` ends the game `FLAG`
  if it flags before reconnats.
- **The ghost-claim / join-timeout (GS-5 follow-up):** a seat that is `MATCHED`
  but whose connection never joins within a window is aborted — this is where the
  GS-5 "a game created from a just-expired seeker" residual is resolved.
- **Heartbeat + teardown:** `PING`/`PONG` liveness; on close, unbind the seat
  (the game persists in the store; the seat may reconnect).
- Config: listen addr, auth, reconnect/abort windows, heartbeat interval — all
  config, not hard-coded (§3.5).

**Out of scope (later chunks — do NOT start):**
- Rating (X-4, G-C) and anti-cheat (X-5, G-B) — a *rated* game needs these; GS-6
  carries unrated/rated frames but the rating event is appended by X-4.
- The web **client** (S4 `clients/web`) — GS-6 is the server edge; a minimal
  client to exercise it end-to-end is Q-GS6d (scope in or defer).
- Sharding/routing across nodes (§6.3, Q-GS2) — G-E.
- Rematch flow, spectating.

## 2. Requirements traced

| Requirement | Where |
|---|---|
| §3.1 connection + session lifecycle | §3.1 — WS accept, AUTHENTICATE, bind, teardown. |
| §3.6 persist-before-ack (NFR-7) | §3.3 — MOVE is acked only after the pipeline's durable append returns. |
| §3.5 reconnection + clock-aware forfeit (F-01.5, NFR-2) | §3.4 — STATE resync from the durable fold; clock runs while disconnected. |
| NFR-5 (one rules engine / one codec) | §3.0 — the message seam keeps §4A in Rust; the session owns legality via the engine. |
| F-01 end-to-end | §3.1-3.4 — SEEK→MATCHED→MOVE→…→GAME_END over the socket. |

## 3. Design

### 3.0 GS-6a — client/server message seam (prerequisite)

Add to `codec-cgo` (mirroring GS-4a's `pgc_encode_event`/`pgc_decode_event`):
- `pgc_decode_client(frame) → postcard(ClientMsg intermediate)` — the Go side
  reads typed fields (Seek pool/time-control/rated; Move game_id/ply/uci; etc.).
- `pgc_encode_server(postcard(ServerMsg intermediate)) → frame` — the Go side
  builds Matched/MoveAccepted/MoveRejected/State/GameEnd/Pong/Error.
The intermediate reuses the postcard-of-struct approach proven in GS-4a (derive
`Serialize/Deserialize` on `ClientMsg`/`ServerMsg`, which the Go mirror matches,
pinned by golden vectors). §4A frame bytes are unchanged (freeze test). Go gains
`codec.DecodeClient`/`codec.EncodeServer`. (Could be its own PR like GS-4a; see
Q-GS6b.)

### 3.1 Connection lifecycle

Accept WS → first frame MUST be `AUTHENTICATE{token}` (X-3 validates; a seed/stub
auth until X-3, Q-GS6c, behind an `Authenticator` interface like GS-5's
`RatingSource`). On success the connection is a known `user_id`; it may then
`SEEK` or (reconnect) resync. One goroutine per connection reads frames; a
per-connection write pump serializes outbound frames (no concurrent writes to one
WS). Teardown unbinds the seat; the game is untouched (it lives in the store).

### 3.2 Dispatch & matchmaking

- `SEEK` → `matchmaker.Seek(ctx, seeker)`. On a `Match`, the transport looks up
  BOTH seats' connections (its seat→conn registry) and sends `MATCHED{start_state}`
  to each, then constructs/attaches the GS-3 live `Session` for the game.
- `MOVE` → the seat's `Session.SubmitMove`; the session's outcome
  (`MoveAccepted`/`MoveRejected`/flag→`GameEnd`) is encoded and sent. Persist
  happens inside the pipeline BEFORE the ack (NFR-7) — the transport sends only
  after `SubmitMove` returns success.
- `RESIGN`/`ABORT`/`OFFER_DRAW`/`TAKEBACK` → the matching `Session` command.

### 3.3 Move hot path & persist-before-ack

The transport does NO game logic (NFR-5): it decodes the intent, calls the
session, encodes the authoritative result. The pipeline already guarantees
persist-before-ack and seq-conflict recovery (GS-4); the transport must not ack a
MOVE until `SubmitMove` returns, and must send the EXACT authoritative fields the
session returns (never a client-asserted clock/result).

### 3.4 Reconnection & clock-aware forfeit (§3.5)

A reconnecting client re-`AUTHENTICATE`s and sends its last `ply`; the server
builds (or finds) the `Session` from the durable stream and replies
`STATE{fen,turn,clocks}` — authoritative, any node (GS-3 folds from GS-4). The
disconnected seat's clock keeps running; the timer-driven `TickFlagCheck` ends the
game `FLAG` (or `TIMEOUT_VS_INSUFFICIENT`, the engine's call) if it flags first. A
shorter pre-first-move abort window applies (config). The MATCHED-but-never-joined
case (GS-5 ghost-claim residual) uses the same join-timeout → abort path.

### 3.5 Heartbeat & teardown

`PING{t}` → `PONG{t, server_t}` (liveness + RTT). A connection silent past the
heartbeat window is closed; its seat is unbound but the game persists and the
clock keeps running (reconnect window applies).

## 4. Interfaces & contracts

- GS-6a adds `codec.DecodeClient`/`codec.EncodeServer` (additive C-ABI, no §4A
  byte change). The transport consumes them + the existing `Session`/`Matchmaker`
  Go APIs — no change to those contracts.
- No new §4A shapes; no chess logic in the transport.
- The WS library choice (Q-GS6a) is isolated behind a small `Conn` interface so
  the dispatch logic is testable without a real socket.

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| First frame is not AUTHENTICATE | reject + close (no anonymous play). |
| MOVE on a game this conn is not seated in | `MOVE_REJECTED{NOT_YOUR_SEAT}` (the session/pipeline already enforces; the transport routes by its seat map). |
| Persist fails (pipeline error) | NO ack; `ERROR{INTERNAL}` — the move did not happen (NFR-7). |
| Disconnect mid-game | seat unbound, clock keeps running; reconnect → `STATE`; flag if the clock expires first. |
| MATCHED seat never joins (ghost-claim) | join-timeout → abort the game (resolves the GS-5 residual). |
| Concurrent writes to one WS | serialized by the per-conn write pump. |
| Reconnect to a different node | works — the Session folds from the durable stream (no node affinity). |
| Oversized / malformed frame | the codec seam rejects it (typed error) → `ERROR{...}`, connection may be closed; never a panic (the seam's catch_unwind). |

## 6. Non-functional handling

- **6.1 move latency (NFR-1):** decode → session (which does the durable append)
  → encode; the append dominates (GS-4). One goroutine per conn, a write pump; no
  extra copies on the hot path.
- **6.2 no game loss (NFR-2):** truth is the store; a dropped socket or node loss
  loses only the ephemeral connection — the game resumes on reconnect from the
  fold.
- **6.3 scale (G-E):** goroutine-per-conn (ADR-12); sharding/routing by game_id is
  G-E (Q-GS2).

## 7. Security & abuse

- `AUTHENTICATE` first, always; the token is validated by X-3 (stub until then,
  Q-GS6c). A client field never carries truth (no client clock/result — the wire
  gives it none, T-1/T-2).
- Seek-flood / move-flood rate-limiting (Redis counters, ADR-10) — noted; a basic
  per-conn rate limit is in scope, tuning is later.
- WS origin check / TLS termination — config/infra; the server binds per ADR-12.

## 8. Test plan

- **GS-6a seam:** golden-vector parity (every `ClientMsg`/`ServerMsg` variant
  round-trips byte-identically through Go via the seam), ABI-fuzz, §4A-frame
  freeze — the GS-4a pattern.
- **Dispatch (no real socket):** a fake `Conn` feeds frames; assert SEEK→MATCHED
  to both seats, MOVE→MoveAccepted with the session's authoritative fields,
  illegal MOVE→MoveRejected, persist-fail→Error-no-ack.
- **Reconnection:** drop a conn mid-game, reconnect, assert `STATE` matches the
  folded truth; assert a seat that stays gone past its clock flags `FLAG`.
- **Join-timeout:** a MATCHED seat that never joins → the game aborts.
- **Heartbeat:** PING→PONG; silent conn past the window is closed.
- **End-to-end (the F-01 proof):** two in-process fake clients SEEK, get MATCHED,
  play a short game to a real GAME_END — all over the transport + the real
  session/store (DynamoDB Local) + matchmaking (Redis), gated like the other
  real-dependency tiers.
- `make test` green; the seam + e2e run in CI with the redis/dynamodb containers.

## 9. Acceptance criteria (Definition of Done)

- [ ] GS-6a message seam lands (DecodeClient/EncodeServer) with golden + fuzz +
      freeze tests green (may be its own PR — Q-GS6b).
- [ ] WS server: AUTHENTICATE handshake, goroutine-per-conn + write pump,
      seat↔conn registry, teardown.
- [ ] SEEK→MATCHED (both seats), MOVE→MoveAccepted/Rejected with the session's
      authoritative fields, persist-before-ack honored, RESIGN/ABORT/DRAW/TAKEBACK
      routed to the session.
- [ ] Reconnection: STATE resync from the durable fold; clock-aware forfeit;
      MATCHED-never-joined → abort (GS-5 ghost-claim residual closed).
- [ ] No chess logic in the transport (reviewer greps); no new §4A shapes.
- [ ] The end-to-end test (fake clients, real session/store/matchmaking) plays a
      full game SEEK→…→GAME_END over the transport, green in CI.
- [ ] `make test` green; CI services job green with redis + dynamodb containers.
- [ ] Makes no lettered gate green; it is the F-01 transport. A *rated* game still
      needs G-B (anti-cheat) + G-C (rating). Stated in the PR.

## 10. Open questions

- **Q-GS6a — WS library.** `nhooyr.io/websocket` (modern, context-aware, smaller)
  vs `gorilla/websocket` (ubiquitous, battle-tested). **Proposed:
  nhooyr.io/websocket** (context.Context-native, cleaner read/write API fitting
  the goroutine-per-conn + write-pump model). Isolated behind a `Conn` interface
  so the choice is reversible and dispatch is socket-free testable.
- **Q-GS6b — GS-6a as its own PR?** The message seam is a `protocol/codec-cgo`
  change with its own fuzz/golden gate, like GS-4a. **Proposed: land GS-6a as a
  separate PR first** (reviewed as a codec change), then GS-6 the transport builds
  on it — keeps the codec change isolated and the transport PR focused.
- **Q-GS6c — auth before X-3.** The transport needs an authenticated `user_id`,
  but accounts/auth (X-3) is not built. **Proposed:** an `Authenticator` interface
  with a seed/dev impl (accepts a signed dev token → user_id) until X-3, mirroring
  GS-5's `RatingSource` seam. No transport change when X-3 lands.
- **Q-GS6d — minimal web client?** To exercise the transport end-to-end a human
  can *see*, a tiny web client (board + WS) helps. **Proposed: DEFER to an S4
  `clients/web` slice**; GS-6's own e2e proof uses in-process fake clients (no
  browser needed to prove the server edge). Flag because "first-playable by a
  human in a browser" needs this client, so it is the very next thing after GS-6
  if the goal is a visible demo.
- **Q-GS6e — reconnect/abort window durations.** Config, not frozen (§3.5).
  **Proposed:** sensible F-01 defaults (e.g. a short pre-first-move abort window;
  post-start, the clock itself is the forfeit timer) in config, tunable later.
