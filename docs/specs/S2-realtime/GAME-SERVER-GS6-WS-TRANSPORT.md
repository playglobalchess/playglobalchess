# Game-server GS-6 — WebSocket transport & connection lifecycle (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS6 (implementation slice of SPEC-S2-GAME-SERVER §3.5/§3.6/§3.1) |
| Owner | S2 Real-time |
| Status | RATIFIED |
| Version | 0.1 |
| Last updated | 2026-10-15 |
| Implements (master IDs) | X-2, F-01 (Play Online end-to-end), F-01.5 (reconnection), NFR-7 (persist-before-ack), NFR-5, ADR-12, ADR-14/15 (codec over cgo) |
| Depends on | GS-1/2/3 (session + pipeline + engine legality), GS-4 (durable store), GS-5 (matchmaking), GS-3b (resign/abort/draw session commands), the codec seam incl. the GS-6a message seam. **All merged** (GS-6a = PR #51; GS-3b = PR #53). |
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
  (proposed `nhooyr.io/websocket` — Q-GS6a, behind a `Conn` interface so the
  choice is reversible) with one goroutine per connection (ADR-12),
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
  if it flags before the seat reconnects.
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

**Write pump (#91).** `Handle` wraps every connection in a `pumpedConn`
(`pump.go`), and the seat/user maps store that wrapper, so every outbound frame,
from the connection's own handler, the other seat's fan-out, or the flag ticker,
goes through it. `Write` only enqueues on a bounded queue (64 frames) and never
blocks; one writer goroutine per connection drains it in order, each frame under
a 5 s write deadline. A slow consumer is disconnected, not waited on: a full
queue, or a write that fails or passes its deadline, closes the connection. The
game keeps running under the clock and the player reconnects (STATE resyncs), so
a peer that stops reading can never stall the opponent's handler or the flag
ticker. Closing never blocks the caller: the connection is marked closed at
once and the socket's closing handshake runs on its own goroutine. On `Handle`
return the queue is flushed before the close, so a final frame such as the
auth-failure `ERROR` still reaches the client; the frame in flight keeps its own
deadline and the rest of the flush shares one, so a stalled peer's teardown
takes at most two deadlines.

### 3.2 Dispatch & matchmaking

- `SEEK` → `matchmaker.Seek(ctx, seeker)`. On a `Match`, the transport delivers
  `MATCHED{start_state}` to BOTH seats by looking up each player's connection in
  a `userID → conn` map (bound at AUTHENTICATE) — NOT a seat→conn map, because
  the already-waiting seeker's handler has already returned (enqueued), so it must
  be reached by user id, not through its own goroutine. The transport then
  `GetOrCreate`s the game's live `Session` via the **`Registry`** (the node's live
  set of sessions keyed by game id; builds from the durable stream on first touch,
  §6.3 no node affinity).
- `MOVE` → the seat's `Session.SubmitMove`; the session's outcome
  (`MoveAccepted`/`MoveRejected`/flag→`GameEnd`) is encoded and sent. Persist
  happens inside the pipeline BEFORE the ack (NFR-7) — the transport sends only
  after `SubmitMove` returns success.
- `RESIGN`/`ABORT`/`OFFER_DRAW`/`TAKEBACK` → the matching **GS-3/GS-3b `Session`
  command** (`Resign`/`Abort`/`OfferDraw`/`Takeback`; `AcceptDraw` is reached via
  a second `OFFER_DRAW` from the other seat — §4A has no distinct accept code, so
  the session's mutual-offer path turns it into the agreement). A command's
  typed error (`ErrAbortTooLate`/`ErrNoDrawToAccept`/`ErrGameNotLive`) maps to a
  `MOVE_REJECTED`/`ERROR` reply.

### 3.3 Move hot path & persist-before-ack

The transport does NO game logic (NFR-5): it decodes the intent, calls the
session, encodes the authoritative result. The pipeline already guarantees
persist-before-ack and seq-conflict recovery (GS-4); the transport must not ack a
MOVE until `SubmitMove` returns, and must send the EXACT authoritative fields the
session returns (never a client-asserted clock/result).

### 3.4 Reconnection & clock-aware forfeit (§3.5) — server-push, Option A

§4A has NO client resync/state-request message (`STATE` is server→client only),
so reconnection is **server-initiated**: on a successful (re-)`AUTHENTICATE`, the
server looks up any live game the user is seated in (a `userID → game` index,
populated at MATCHED), rebinds that seat to the NEW connection, and pushes
`STATE{fen,turn,clocks}` folded from the durable stream (authoritative; any node,
GS-3 folds from GS-4). The client sends nothing to trigger this — the earlier
"sends its last ply" wording was vestigial and is corrected here. The
disconnected seat's clock keeps running; the timer-driven `TickFlagCheck` ends the
game `FLAG` (or `TIMEOUT_VS_INSUFFICIENT`) if it flags first. The
MATCHED-but-never-joined case (GS-5 ghost-claim residual) uses the join-timeout →
abort path.

### 3.5 Heartbeat, flag-forfeit & teardown

The transport drives the idle/disconnect **forfeit** path, which GS-3 delegates
to the caller ("the session does not own a ticker", Q-GS3C): a per-game flag
ticker calls `Session.TickFlagCheck` every `flagInterval` (config, default 1s),
charging elapsed-since-last-move against the side to move; if that flags, the
session ends the game (`FLAG` / `TIMEOUT_VS_INSUFFICIENT`) and the transport
broadcasts `GAME_END` to both seats. The ticker is armed at MATCHED and exits
when the game is gone (ended by any path), so there is exactly one driver per
live game and it stops with the game — this is also what reclaims a game whose
players both disconnect (the session + goroutines + indices are freed, not
leaked). Without this driver a disconnected or stalled player's clock would run
forever and the game would hang; it is NOT a deferred limitation.

`PING{t}` → `PONG{t, server_t}` (liveness + RTT). On close, the connection's
seat is unbound (via `unbindAll`, which only drops bindings still pointing at
that connection, so a reconnect's rebind survives) but the game persists and its
clock keeps running under the flag ticker (reconnect window applies).

### 3.6 Client-mirror codec seam (added in this slice)

The GS-6a seam gave Go `DecodeClient`/`EncodeServer` (the server's directions).
This slice adds the mirror — `codec.EncodeClient` / `codec.DecodeServer`, backed
by new cgo `pgc_encode_client` / `pgc_decode_server` — which the SERVER never
uses (it only receives client frames and sends server frames) but a CLIENT and
the transport's own end-to-end test (which acts as a client) do. Additive C-ABI,
§4A bytes unchanged.

### 3.8 Observability (deferred to X-14) & dependency notes

The transport is deliberately logger-free, like the GS-1 pipeline (GS-4 F2): the
server-side failure points — `deliverMatched` session-build failure,
`handleMove` persist failure, `send`'s write error, the flag ticker's
`TickFlagCheck` error (now bounded-retry), and `dropGame` — each produce a client
`ERROR` but NO operator signal today. These are the X-14 punch-list: wire a
log/metric at each when the observability stack lands, rather than scattering
ad-hoc logging now that X-14 would have to unify. Named here so the list is not
lost.

WS library: `github.com/coder/websocket` (the maintained successor to the
archived `nhooyr.io/websocket` — same codebase, API-compatible; migrated in this
slice while the import is isolated to one file behind the `Conn` interface,
Q-GS6a).

### 3.7 Known v1 limitations — RESOLVED by §4A v1.1 (ADR-17)

> **Update (§4A v1.1, ADR-17, SPEC-S2-PROTOCOL-V1.1):** both gaps below were
> closed by the owner-approved additive amendment — `RESYNC` (client 0x0A) makes
> reconnection client-requestable, and `DRAW_OFFERED` (server 0x87) push-notifies
> a pending offer. The transport now routes `RESYNC`→`STATE` and pushes
> `DRAW_OFFERED` to the opponent on a first offer. The text below is retained as
> the original v1.0 record of why the amendment was made.

Two gaps fell out of the frozen §4A v1.0 message set, recorded honestly rather
than worked around with an invented frame; both were slated for (and are now
closed by) the §4A v1.1 amendment:

- **Reconnection was server-push-on-auth, not client-requestable** (§3.4): §4A
  v1.0 had no `RESYNC` client code. (Closed: RESYNC 0x0A.)
- **A pending (non-ending) draw OFFER was not push-notified**: §4A v1.0 had no
  `DRAW_OFFERED` server code, so when a first `OFFER_DRAW` did not immediately
  end the game, the opponent was not proactively told. The offer WAS recorded
  (GS-3b `drawOfferBy` + the `DrawOffered` event) and acceptable — only the
  notification lacked a v1.0 frame. (Closed: DRAW_OFFERED 0x87.)

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
| Peer stops reading (slow consumer) | its writes never block other handlers or the flag ticker: queue full (64) or one write past 5 s → that connection is closed; reconnect → `STATE` (#91). |
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

> **Resolution status (this slice is RATIFIED + implemented):** Q-GS6a =
> nhooyr.io/websocket behind the `Conn` interface. Q-GS6b = GS-6a landed as its
> own PR (#51). Q-GS6c = `Authenticator` seam + `DevAuthenticator`. Q-GS6d =
> web client DEFERRED to S4 (GS-6 e2e uses in-process fake clients). Q-GS6e =
> reconnect/abort windows are config (`Server.joinTTL`, session `AbortBeforePly`).
> NEW seam added: a `TimeControlResolver` (dev `ConventionResolver` parses
> `blitz-5+3`/`custom-…`/`daily-…`) stands in until the REST `GET /v1/pools`
> expansion (X-13) lands — same placeholder-behind-a-seam pattern as the others.
>
> **QUEUED next: §4A v1.1 amendment slice.** The two v1 limitations in §3.7 (no
> client `RESYNC`, no server `DRAW_OFFERED`) are closed by a deliberate,
> owner-approved §4A amendment (new ADR, two message codes, codec + GS-6a-style
> seam + golden/parity) — timed BEFORE the web client freezes behavior against
> v1, so the freeze window (open while there are zero deployed clients) is used
> once and deliberately rather than eroded.


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
