# Protocol: WS binary frame + event-store schema + entity model — S2 Real-time

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-PROTOCOL |
| Owner | S2 Real-time (stream lead — fill in); co-reviewed by S3 Platform + PE Platform-Eng + Architecture group |
| Status | RATIFIED |
| Version | 1.3 (wire) |
| Last updated | 2026-11-10 (§4A v1.3 per ADR-19 folded in: new codes, event types, §4A.10 status and the v1.3 STATE body. The v1.3 §5 behaviour rows fold in with the GS-7 server reducer; see PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md §7.) |
| Implements (master IDs) | X-2, X-13, ADR-10, NFR-5, NFR-7, NFR-10; MASTER §VIII (VIII.1–VIII.4), §IX.1 |
| Depends on | SPEC-S1-ENGINE-CORE (G-A, GREEN) — the server validates intents via the engine |
| Gates it feeds | **G-D** (API/protocol contract frozen v1). Per owner decision Q-P1=(b), G-D is green only when **this spec AND `PE/API-CONTRACT.md` (REST surface, X-13) are frozen together** — this is the realtime half of the v1 surface, not the whole gate. |

## 1. Purpose & scope

Define the **`protocol/` contract** — the one place the server, web, iOS and Android cite (never copy) for how a live chess game is transported and stored. It is the second critical-path item after the engine (build manual §2) and the foundation `services/game-server` (X-2, ADR-12) and every client build against.

This spec covers exactly **three artifacts**, matching the `protocol/` folder's remit (MASTER §VIII):

1. **Wire protocol** — the WebSocket binary frame layout for live play: message type codes, the byte-level encoding of each `C→S` / `S→C` message in MASTER §VIII.4, versioning, and the framing envelope.
2. **Event-store schema** — the append-only game-event model (MASTER §VIII.3): the canonical event set, each event's field schema, ordering/versioning rules, and the serialization used for durable persistence (NFR-7).
3. **Entity model** — the relational entities (MASTER §VIII.2) **as a cited contract shape** that the wire and event schemas reference (e.g. `pool_id`, `seat_idx`, `result_vector`), to the depth the protocol needs.

**In scope:** the on-the-wire and on-disk *shapes*, their encodings, their version/compatibility rules, and the conformance corpus that freezes them (G-D).

**Non-goals (explicit):**
- **No server behavior/implementation.** Clock algorithm, matchmaking algorithm, reconnection state machine, sharding — those are `services/game-server` (X-2) and belong in a later `GAME-SERVER.md`. This spec defines only the *messages and schemas* they exchange.
- **No full REST/JSON endpoint catalog.** MASTER §VIII.5 REST surface is owned by PE `API-CONTRACT.md` (X-13); this spec defines only the realtime WS + event-store contracts. Per owner decision Q-P1=(b), that PE spec is a **required parallel P0 artifact** and G-D is green only when both freeze together (§10, §9).
- **No variant extensions.** Crazyhouse drops, Duck placement, N-seat turn order (MASTER §VIII.6 "still owed") are deferred to a `PROTOCOL-VARIANTS.md`; v1 is **Standard, 2-seat** only. The frame/event formats must be *extensible* to them (see §5), but their payloads are out of scope.
- **No persistence engine choice.** "Which event store product" is ADR-10 (Postgres + event store); this spec defines the schema that store holds, not the store.
- **No auth/token issuance.** `AUTHENTICATE {token}` carries a token minted by X-3; token format/issuance is X-3's spec. This spec defines only that the frame carries it and when.

## 2. Requirements traced

| Master ID | Requirement | Where handled |
|---|---|---|
| X-2 | Real-time game server transport | §3.1, §4.1 (wire frame) |
| X-13 | Shared API/protocol contract | §4 (the whole contract); §9 freeze = G-D |
| ADR-10 | Event store = game truth; WS binary transport | §3.2 (event schema), §3.1 (binary frame) |
| NFR-5 | One rules engine, no reimplementation | §3.1 — clients send **intents**; only the server validates via the engine and emits events (no client-side legality on the authoritative path) |
| NFR-7 | Durable move event **before** ack; immutable, auditable | §3.2 (append-only, immutable, compensating-event rule), §5 (ordering/idempotency) |
| NFR-10 | Platform parity, one protocol, shared test suite on all 3 clients | §8 (contract conformance corpus runs on web/iOS/Android), §3.3 (shared Rust codec via ADR-9) |
| MASTER §VIII.1 | Design principles (event log, server-authoritative, one protocol, binary hot path) | §3 design |
| MASTER §VIII.2–4 | Entities, event model, wire protocol | §3.1/§3.2/§3.3, §4 |
| MASTER §IX.1 | Latency budget (frame must be tiny/fast) | §6 (encoding size budget) |

## 3. Design

### 3.1 Wire protocol — WebSocket binary frame (MASTER §VIII.4)

**Envelope.** Every WS message is one binary frame:
```
┌────────┬────────┬───────────────────────┐
│ ver(1) │ type(1)│ payload (type-specific)│
└────────┴────────┴───────────────────────┘
```
- `ver` (u8): protocol major version. v1 = `0x01`. A frame whose `ver` the peer does not support → `S ERROR {code=UNSUPPORTED_VERSION}` and the socket closes. (Compatibility rules: §5.)
- `type` (u8): message type code (table below). Direction is implied by type (a server never receives an `S` type and vice-versa; receiving a wrong-direction type → `ERROR {code=PROTOCOL_VIOLATION}`).
- `payload`: the message body, encoded per §3.1.1.

**Message type codes (v1).** Direction `C` = client→server, `S` = server→client. Codes mirror MASTER §VIII.4 exactly; the dotted sub-IDs below are internal to this spec, not new master IDs.

| Code | Dir | Message | Payload fields |
|---|---|---|---|
| `0x01` | C | AUTHENTICATE | `token: str` |
| `0x02` | C | SEEK | `pool_id, time_control, rated: bool` |
| `0x03` | C | MOVE | `game_id, ply_expected: u32, uci: str` |
| `0x04` | C | OFFER_DRAW | `game_id` |
| `0x05` | C | RESIGN | `game_id` |
| `0x06` | C | ABORT | `game_id` |
| `0x07` | C | TAKEBACK | `game_id` |
| `0x08` | C | REMATCH | `game_id` |
| `0x09` | C | PING | `t: u64` (client monotonic ms) |
| `0x0A` | C | RESYNC | `game_id` (v1.1, ADR-17: ask for a fresh STATE) |
| `0x0B` | C | TAKEBACK_ACCEPT | `game_id, at_ply: u32` (v1.3, ADR-19) |
| `0x0C` | C | TAKEBACK_DECLINE | `game_id, at_ply: u32` (v1.3: decline, or cancel by the proposer) |
| `0x0D` | C | DECLINE_DRAW | `game_id` (v1.3: decline, or withdraw by the offerer) |
| `0x81` | S | MATCHED | `game_id, seat_idx: u8, opponent, start_state` |
| `0x82` | S | MOVE_ACCEPTED | `game_id, ply: u32, uci: str, clock_after_ms: u32` (v1.2, ADR-18: carries `uci`, sent to both seats) |
| `0x83` | S | MOVE_REJECTED | `game_id, ply_expected: u32, reason: enum` |
| `0x84` | S | STATE | `game_id, STATE-body` (full resync; v1.3 body in §4A.6, built per recipient) |
| `0x85` | S | GAME_END | `game_id, result_vector, termination: enum, rating_delta` |
| `0x86` | S | PONG | `t: u64, server_t: u64` (clock sync, NFR-1 drift budget) |
| `0x87` | S | DRAW_OFFERED | `game_id, by: u8` (v1.1, ADR-17: pushed to the opponent on a new offer) |
| `0x88` | S | TAKEBACK_OFFERED | `game_id, seat_idx: u8, at_ply: u32` (v1.3, ADR-19) |
| `0x89` | S | TAKEBACK_DECLINED | `game_id, seat_idx: u8` (v1.3) |
| `0x8A` | S | DRAW_DECLINED | `game_id, seat_idx: u8` (v1.3) |
| `0x8F` | S | ERROR | `code: enum, msg: str` |

**Payload encoding — DECIDED (ADR-14).** One schema-defined binary codec in the shared Rust core (`protocol/codec`), bound into every client and the Go server. Payloads are **postcard**-encoded; the two header bytes are written raw (§4A.1). The concrete byte rules are in §4A.

#### 3.1.1 Payload encoding (ADR-14)
- One schema (the Rust types in `protocol/codec`) defines every payload; the shared Rust core (ADR-9) exposes `encode(msg) -> bytes` / `decode(bytes) -> msg` and is the single codec all three clients + the Go server call. Web uses the wasm binding (`protocol/codec-wasm`), iOS/Android the UniFFI binding (`protocol/codec-ffi`), and the Go server the in-process cgo C-ABI (`protocol/codec-cgo`, ADR-15). No consumer re-implements the codec.
- `uci`: ASCII move string (`e2e4`, `e7e8q`), ≤ 5 bytes; validated by the engine, not the codec.
- `fen` only appears in `STATE` (resync), never on the per-move hot path (§6).
- `result_vector`: per-seat result (win/loss/draw) indexed by `seat_idx`, sized for N-seat (ADR-3) but v1 carries 2 seats.
- Enums (`reason`, `termination`, `code`) are u8 with a fixed value table frozen at G-D (§4.3).

### 3.2 Event-store schema — append-only game events (MASTER §VIII.3)

A game is the stream `game/{id}`: an ordered, append-only sequence of immutable events. Board state, PGN and the `Move` projection are **folds** of this stream (never written directly). Anti-cheat (ADR-2) subscribes to the same stream.

**Canonical event set (v1).** Exactly MASTER §VIII.3 as amended by ADR-19 (v1.3 adds the last five rows; a v1.3 server never writes `TakebackReq`/`TakebackAccepted`, which stay decodable):

| Event | Schema |
|---|---|
| `GameCreated` | `{pool_id, rated: bool, seats: [{seat_idx, color}], start_fen, time_control}` |
| `PlayerJoined` | `{seat_idx, user_id}` |
| `MovePlayed` | `{ply: u32, seat_idx, uci, clock_after_ms: u32, server_ts: u64}` |
| `DrawOffered` | `{seat_idx}` |
| `DrawAccepted` | `{seat_idx}` |
| `Resigned` | `{seat_idx}` |
| `Aborted` | `{by: seat_idx \| system}` |
| `TakebackReq` | `{seat_idx}` |
| `TakebackAccepted` | `{seat_idx}` |
| `ClockFlagged` | `{seat_idx}` |
| `GameEnded` | `{result_vector, termination}` |
| `RatingApplied` | `{pool_id, per_seat: [{seat_idx, before, after, rd, vol}]}` |
| `TakebackProposed` | `{seat_idx}` (v1.3) |
| `TakebackRewound` | `{seat_idx, to_ply: u32, server_ts: u64}` (v1.3) |
| `TakebackDeclined` | `{seat_idx}` (v1.3) |
| `DrawDeclined` | `{seat_idx}` (v1.3) |
| `ClockStarted` | `{server_ts: u64}` (v1.3) |

**Event envelope.** Each persisted event carries metadata around the body:
```
{ stream_id: "game/{id}", seq: u32, type: str, ts: u64, schema_ver: u16, body: {…} }
```
- `seq`: 0-based position in the stream; strictly increasing, no gaps; the ordering authority (distinct from chess `ply`, since non-move events have no ply).
- `schema_ver`: per-event schema version for forward evolution (§5).
- `ts`: server-assigned persist time.
- Byte order on disk (§4A.8): `stream_id`, `seq`, `type` (one raw byte), `schema_ver`, `ts`, then the body.

**Durability contract (NFR-7).** The server MUST durably persist `MovePlayed` (envelope committed to the event store) **before** emitting the `S MOVE_ACCEPTED` wire ack. Ack-before-persist is a correctness bug. RPO ≤ 5 s / RTO ≤ 15 min (NFR-7, §XIII) are the store's targets, cited not defined here.

**Immutability (NFR-7).** Events are never edited or deleted in place. A correction is a new compensating event appended later (e.g. a mistaken `RatingApplied` is corrected by a subsequent compensating `RatingApplied`, not by mutation). Results and rating changes are immutable once written.

### 3.3 Entity model — relational shape as cited contract (MASTER §VIII.2)

The protocol references relational entities by the fields it puts on the wire / in events; the **full relational schema (DDL, indexes, constraints) is owned by S3 `DATA-MODEL.md`** (specs README), not redefined here. This spec pins only the contract-visible fields and their types so wire/event schemas are unambiguous:

- `pool_id` → `RatingPool.id` (a `format × variant` pool; MASTER §VIII.2).
- `seat_idx: u8` → `GameSeat.seat_idx`, 0-based; v1 games have seats `{0,1}` (ADR-3 N-seat generalized, 2-seat instance).
- `user_id`, `bot_id` → `GameSeat.user_id | bot_id`.
- `result_vector` → `Game.result_vector`, per-seat outcome.
- `termination` → `Game.termination` enum (mate/resign/flag/abort/…; frozen table §4.3).
- `time_control` → `Game.time_control`, carried as a **stable id string** (e.g. `"blitz-5+3"`, `"daily-3"`, or `"custom-<base>+<inc>"`), NOT an inlined base/increment struct (Q-P3 / cross-spec DECIDED, SPEC-PE-API-CONTRACT Q-A4). The id → `{base_ms, increment_ms, per_move_ms?}` expansion lives once, in REST `GET /v1/pools`. The wire/event carry only the id, keeping the hot path tiny (NFR-1).
- `start_fen` → `Game.start_fen`.

Anything the protocol does **not** carry (email, credentials, profile, entitlements) is out of this contract — owned by S3/X-3/ADR-8 specs.

## 4. Interfaces & contracts

This spec **is** a shared cross-stream contract (CONVENTIONS "Cross-stream contracts"): it spans S2 (server), S4/S5 (clients), S3 (entity model + event store), PE (API surface). Therefore:

- **4.1 Produced/consumed.** Server consumes `C*` frames, produces `S*` frames + appends events. Clients produce `C*`, consume `S*`. Anti-cheat (ADR-2) consumes the event stream read-only. Rating service (X-4) appends `RatingApplied`.
- **4.2 Change control.** Per CONVENTIONS, any change to this contract requires review by **both affected streams + the architecture group**. After G-D freeze, a wire/event change is a *new protocol version* (§5), not an in-place edit.
- **4.3 Frozen value tables.** The u8 enum tables (`reason`, `termination`, `error code`), the frame field widths, and the event-envelope layout are enumerated in **Appendix 4A** below and frozen as part of G-D (locked at RATIFIED, against the ADR-14 codec). `time_control` is an id string per Q-A4, not an enum.
- **4.4 Relationship to G-A.** The server's only legality authority is the S1 engine (GREEN). `MOVE_REJECTED {reason}` wraps the engine's verdict; this spec defines the *reason enum*, not the legality logic.

## 4A. Appendix — frozen byte layout & enum tables (v1)

Encoded by the ADR-14 codec (`protocol/codec`). **Physical encoding:** the frame header bytes `ver` and `type` are single raw bytes; the payload is the postcard encoding of the fields below, in the listed order. Postcard rules that matter for implementers:
- `u8` and `bool` are one byte (`bool` ∈ {0,1}); `enum8` is a `u8` from the tables below.
- `u16`, `u32`, `u64` are **varints** (LEB128-style), not fixed-width little-endian.
- `str` (and `strN`) is a varint byte-length prefix followed by UTF-8 bytes; `N` is a maximum length the codec enforces, not a fixed width.
- `[T; seat_count]` and other sequences are a varint element count followed by the elements.
- A payload must be consumed exactly: trailing bytes are a decode error.

The tables give field **order and logical type**; the byte-level truth is the golden-vector corpus in `protocol/vectors/`, which every consumer must reproduce byte-for-byte (§8).

### 4A.1 Frame envelope
| Offset | Field | Type | Notes |
|---|---|---|---|
| 0 | `ver` | u8 | `0x01` for v1 |
| 1 | `type` | u8 | message type code (§4A.2) |
| 2.. | `payload` | per type | §4A.3 field order |

Max frame size (v1): **4 KB** (`MAX_FRAME_BYTES = 4096` in the codec; a resync `STATE` with a long FEN is the largest; the move hot path is ≤ ~40 B). Over-size → `ERROR{code=PROTOCOL_VIOLATION}`. Note: the game-server's WebSocket read limit is currently 16 KiB, above this cap (tracked in #104).

### 4A.2 Message type codes (u8) — frozen
Client→server `0x01–0x7F`; server→client `0x80–0xFF` (high bit = direction, a cheap wrong-direction check).

| Code | Dir | Message |   | Code | Dir | Message |
|---|---|---|---|---|---|---|
| 0x01 | C | AUTHENTICATE | | 0x81 | S | MATCHED |
| 0x02 | C | SEEK | | 0x82 | S | MOVE_ACCEPTED |
| 0x03 | C | MOVE | | 0x83 | S | MOVE_REJECTED |
| 0x04 | C | OFFER_DRAW | | 0x84 | S | STATE |
| 0x05 | C | RESIGN | | 0x85 | S | GAME_END |
| 0x06 | C | ABORT | | 0x86 | S | PONG |
| 0x07 | C | TAKEBACK | | 0x8F | S | ERROR |
| 0x08 | C | REMATCH | | 0x87 | S | DRAW_OFFERED |
| 0x09 | C | PING | | 0x88 | S | TAKEBACK_OFFERED |
| 0x0A | C | RESYNC | | 0x89 | S | TAKEBACK_DECLINED |
| 0x0B | C | TAKEBACK_ACCEPT | | 0x8A | S | DRAW_DECLINED |
| 0x0C | C | TAKEBACK_DECLINE | | | | |
| 0x0D | C | DECLINE_DRAW | | | | |

v1.1 (ADR-17) added `0x0A RESYNC` and `0x87 DRAW_OFFERED`; v1.3 (ADR-19) added `0x0B–0x0D` and `0x88–0x8A`. All in reserved slots; no existing code moved. In v1.3 `0x07 TAKEBACK` means "request" only.

### 4A.3 Payload field layouts (field order fixed)
`game_id` = `str36` (UUID text); `uci` = `str5` max; `token` = `str` (≤ 4096); `time_control` = `str` id (Q-A4); `fen` = `str` (≤ 90).

```
AUTHENTICATE   token:str
SEEK           pool_id:str, time_control:str, rated:bool
MOVE           game_id:str36, ply_expected:u32, uci:str5
OFFER_DRAW/RESIGN/ABORT/TAKEBACK/REMATCH/RESYNC/DECLINE_DRAW   game_id:str36
TAKEBACK_ACCEPT/TAKEBACK_DECLINE   game_id:str36, at_ply:u32
PING           t:u64
MATCHED        game_id:str36, seat_idx:u8, opponent:str(handle), start_state:STATE-body(§4A.6)
MOVE_ACCEPTED  game_id:str36, ply:u32, uci:str5, clock_after_ms:u32
MOVE_REJECTED  game_id:str36, ply_expected:u32, reason:enum8(§4A.4)
STATE          game_id:str36, STATE-body(§4A.6)
GAME_END       game_id:str36, result_vector:[u8; seat_count], termination:enum8(§4A.5), rating_delta:[i16; seat_count]
PONG           t:u64, server_t:u64
DRAW_OFFERED   game_id:str36, by:u8
TAKEBACK_OFFERED   game_id:str36, seat_idx:u8, at_ply:u32
TAKEBACK_DECLINED/DRAW_DECLINED   game_id:str36, seat_idx:u8
ERROR          code:enum8(§4A.7), msg:str
```
`MOVE_ACCEPTED.uci` (v1.2, ADR-18) is the move just applied, in the same position as in the `MovePlayed` event body; the server sends it to BOTH seats for every accepted move (and then `GAME_END` if that move ended the game). On a duplicate `MOVE` it echoes the persisted move's `uci`. `RESYNC` shares the bare `game_id` payload with the ending commands.
`result_vector` entry: `0=loss, 1=draw, 2=win` (per seat_idx). `rating_delta` is `i16` signed.

### 4A.4 `reason` enum (MOVE_REJECTED) — u8, frozen
| 0x00 OK-unused | 0x01 ILLEGAL | 0x02 STALE_PLY | 0x03 GAME_OVER | 0x04 NOT_YOUR_TURN | 0x05 NOT_YOUR_SEAT | 0x06 UNKNOWN_GAME |

### 4A.5 `termination` enum (GAME_END) — u8, frozen
| 0x01 MATE | 0x02 RESIGN | 0x03 FLAG(timeout) | 0x04 ABORT | 0x05 DRAW_AGREE | 0x06 STALEMATE | 0x07 REPETITION | 0x08 FIFTYMOVE | 0x09 INSUFFICIENT | 0x0A TIMEOUT_VS_INSUFFICIENT |

(Matches `Game.termination` enum in SPEC-S3-DATA-MODEL §3.4.)

### 4A.6 `STATE`-body (reused by MATCHED.start_state — Q-P4 DECIDED; v1.3)
The `start_state` field of `MATCHED` is **exactly the STATE payload after `game_id`** — one state format, no second encoding (resolves Q-P4). v1.3 (ADR-19) body:
```
fen:str, turn:u8, clocks:[u32; seat_count],
you_seat:u8, ply:u32, status:enum8(§4A.10),
result_vector:[u8], termination:u8,
last_move_uci:str5,
pending_draw_by:u8, takeback_by:u8, takeback_at_ply:u32,
running_seat:u8, server_t:u64
```
| Field | Meaning |
|---|---|
| `you_seat` | the recipient's seat; `0xFF` = none |
| `ply` | server ply; the next `MOVE.ply_expected` is `ply + 1` |
| `result_vector` | §4A.3 result codes when `ENDED`, empty otherwise |
| `termination` | §4A.5 code when `ENDED`, `0x00` otherwise |
| `last_move_uci` | the last live move; empty iff `ply == 0` |
| `pending_draw_by` | seat with a pending draw offer, `0xFF` none |
| `takeback_by`, `takeback_at_ply` | pending takeback request; `0xFF` / `0` when none |
| `running_seat` | seat whose clock runs, `0xFF` if none |
| `server_t` | server epoch ms at which `clocks` are valid; the running seat's clock runs from it |

**Decode-time invariants** (a frame violating one is malformed; enforced on encode and decode by the Rust codec, so every binding and the Go seams inherit them):
1. `result_vector` non-empty iff `status == ENDED`, and then `len == clocks.len()`.
2. `termination != 0x00` iff `status == ENDED`.
3. `you_seat`, `pending_draw_by`, `takeback_by`, `running_seat` each `< clocks.len()` or `0xFF`.
4. `takeback_at_ply == 0` iff `takeback_by == 0xFF`.
5. `last_move_uci` ≤ 5 bytes, empty iff `ply == 0`.
6. `running_seat == 0xFF` when `status == ENDED`.

`STATE` is built **per recipient** (`you_seat` differs per connection). An old (v1.2) client cannot decode this body, so v1.3 server and clients ship together.

### 4A.7 `error code` enum (ERROR) — u8, frozen
| 0x01 UNSUPPORTED_VERSION | 0x02 PROTOCOL_VIOLATION | 0x03 UNKNOWN_GAME | 0x04 UNAUTHENTICATED | 0x05 RATE_LIMITED | 0x06 INTERNAL |

### 4A.8 Event-store envelope & body layout (persisted, §3.2)
Events are persisted via the **same codec** (one encoder for wire + store). Envelope:
```
{ stream_id:str("game/{uuid}"), seq:u32, type:enum8(§4A.9), schema_ver:u16, ts:u64, body:... }
```
`seq` strictly increasing, no gaps. Body field order:
```
GameCreated      pool_id:str, rated:bool, seats:[{seat_idx:u8,color:u8}], start_fen:str, time_control:str
PlayerJoined     seat_idx:u8, user_id:str36
MovePlayed       ply:u32, seat_idx:u8, uci:str5, clock_after_ms:u32, server_ts:u64
DrawOffered/DrawAccepted/Resigned/TakebackReq/TakebackAccepted/ClockFlagged   seat_idx:u8
Aborted          by:u8   (0xFF = system)
GameEnded        result_vector:[u8], termination:enum8(§4A.5)
RatingApplied    pool_id:str, per_seat:[{seat_idx:u8, before:i16, after:i16, rd:u16, vol:f32}]
TakebackProposed/TakebackDeclined/DrawDeclined   seat_idx:u8   (v1.3)
TakebackRewound  seat_idx:u8, to_ply:u32, server_ts:u64   (v1.3)
ClockStarted     server_ts:u64   (v1.3)
```
`color` u8: `0=white, 1=black` (N-seat variants extend).

### 4A.9 Event `type` enum — u8, frozen
| 0x01 GameCreated | 0x02 PlayerJoined | 0x03 MovePlayed | 0x04 DrawOffered | 0x05 DrawAccepted | 0x06 Resigned | 0x07 Aborted | 0x08 TakebackReq | 0x09 TakebackAccepted | 0x0A ClockFlagged | 0x0B GameEnded | 0x0C RatingApplied | 0x0D TakebackProposed | 0x0E TakebackRewound | 0x0F TakebackDeclined | 0x10 DrawDeclined | 0x11 ClockStarted |

`0x08`/`0x09` are frozen: decodable, never written by a v1.3 server.

### 4A.10 `status` enum (STATE) — u8, frozen (v1.3)
| 0x01 MATCHED | 0x02 LIVE | 0x03 ENDED |

`0x00` and unlisted values are rejected on decode.

All enum spaces leave unused codes reserved for v1-additive events/variants (§5 extensibility); a code is never reused for a different meaning across a version.

## 5. Edge cases & failure modes

| Case | Contract behavior |
|---|---|
| **Stale/racing move** (`ply_expected` ≠ server ply) | `S MOVE_REJECTED {ply_expected = server's current, reason=STALE_PLY}`. `ply_expected` on the `C MOVE` is the race guard (MASTER §VIII.4). |
| **Illegal move** | Engine rejects → `MOVE_REJECTED {reason=ILLEGAL}`. No event appended. |
| **Move arrives after game ended** | `MOVE_REJECTED {reason=GAME_OVER}`. |
| **Reconnect** | Client re-auths; the server pushes `S STATE` (§4A.6: seat, ply, status, result, last move, pending draw, clock anchor) for the user's live game after `AUTHENTICATE` (NFR-2 no game loss). A client can also ask with `C RESYNC` (v1.1). STATE is a fold of the event stream, authoritative. |
| **Duplicate MOVE (client retry)** | Idempotent on `(game_id, ply_expected)`: if that ply is already persisted, re-send `MOVE_ACCEPTED` for it (echoing the persisted `uci`, v1.2) rather than appending a second event. (Protects the NFR-7 persist-before-ack window on a flaky socket.) |
| **Unknown/zero `game_id`** | `ERROR {code=UNKNOWN_GAME}`. |
| **Unsupported protocol version** | `ERROR {code=UNSUPPORTED_VERSION}`, socket closed. |
| **Wrong-direction / malformed frame** | `ERROR {code=PROTOCOL_VIOLATION}`. |
| **Event schema evolution** | Readers tolerate unknown higher `schema_ver` of a known event type by schema rules; a *new event type* or a breaking field change bumps the stream's compatibility and is a G-D-versioned change (§4.2). **Implementation gap (not a contract change):** today the codec reads `schema_ver` but no reader branches on it, and the event store rejects any encoding version other than its current one, so this tolerance rule is not implemented or tested (#150). |
| **Extensibility (future variants)** | Frame `type` code space and event `type` set are open-ended; v1 reserves Standard/2-seat codes and leaves room for variant/N-seat events (MASTER §VIII.6) without reusing a v1 code. |

## 6. Non-functional handling

- **Latency / size (NFR-1, §IX.1).** The move hot path (`C MOVE` / `S MOVE_ACCEPTED`) must be a tiny binary frame: type byte + `game_id` + `ply` (u32) + `uci` (≤5 B) + `clock_after_ms` (u32) — target well under the 50 ms p50 / 300 ms hard-ceiling intent→ack budget leaves no room for a fat payload. `fen` is **never** on the move path (only `STATE` resync). This spec defines the *shape*; the measured latency is proven by `services/game-server` under G-E, not here.
- **Parity (NFR-10).** One codec in the shared Rust core → byte-identical encode/decode on all three clients; proven by the §8 contract corpus running on web/iOS/Android (the same parity discipline already proven for the engine FFI).
- **Durability (NFR-7).** Persist-before-ack (§3.2); numbers (RPO/RTO) cited from NFR-7/§XIII.

## 7. Security & abuse

Part XI threats touching this contract:
- **T-1/T-2 move/clock forgery.** Mitigated by design: clients send intents only; the server is the sole legality + clock authority (NFR-5, §VIII.1). The wire format gives a client no field that asserts game truth (no client-set clock, no client-set result).
- **Spoofed `seat_idx` / acting for the opponent.** The server binds the authenticated session (from `AUTHENTICATE`) to its `seat_idx`; a `C MOVE` is authorized against the session's seat, never a client-supplied one. (Authorization logic lives in game-server; the contract simply does not trust a client-asserted actor.)
- **Replay / dup injection.** Idempotency on `(game_id, ply_expected)` (§5) + `ply_expected` race guard.
- **Resource abuse (frame flooding).** Per-connection rate limits are a game-server concern (Part XI), cited not defined; the frame format keeps parsing cheap (fixed header) to bound work per frame.
- **Token handling.** `AUTHENTICATE {token}` — token issuance/validation is X-3; this contract carries it opaquely and never logs it.
- **Fair-play (ADR-2).** The event stream is the anti-cheat signal source; this spec must keep the per-move timing (`clock_after_ms`, `server_ts`) in the event so S6 detection is not starved. S6 has review veto (CONVENTIONS) → S6 is a required reviewer before RATIFIED.

## 8. Test plan

The contract's conformance corpus is part of this spec (CONVENTIONS "the test corpus is part of the spec"):

- **Codec round-trip (unit + property).** For every message type and every event type: `decode(encode(x)) == x`; random-value property tests. Run in the Rust core.
- **Golden vectors (contract).** A frozen set of `(message, bytes)` and `(event, bytes)` golden pairs committed under `protocol/`. **The same vectors are decoded on web/iOS/Android** and must match byte-for-byte (NFR-10 — the shared protocol test suite "passes on all three"). This is the parity gate.
- **Cross-decoder matrix.** Rust-encoded → TS/Swift/Kotlin-decoded (and the reverse where a client encodes) must agree.
- **Negative/edge decode.** Malformed frames, unknown `type`, unsupported `ver`, truncated payloads, wrong-direction types → defined `ERROR`/reject, never a panic or silent accept. (Mirrors the engine lesson: external/untrusted input needs dedicated per-branch decode tests, not only round-trip.)
- **Event ordering/immutability.** `seq` strictly increasing no-gap; append-only enforced; compensating-event correction path tested.
- **Schema evolution.** A v1 reader tolerating a higher `schema_ver` per the rules; a breaking change forced to bump the protocol version.

**Not tested here:** latency, throughput, reconnection timing (G-E, game-server).

## 9. Acceptance criteria (Definition of Done)

Frozen = **G-D for live play**. A reviewer can call this DONE when:

- [x] §4A enum tables and field widths enumerated with concrete frozen u8 values **in this spec** (resolves Q-P3); codec-library framing locks at RATIFIED with ADR-14.
- [x] `MATCHED.start_state` shape pinned (reuse `STATE` body, §4A.6) (resolves Q-P4).
- [x] Codec written as **ADR-14** in MASTER-PLAN, now **DECIDED** (postcard payloads, raw header bytes); §3.1.1 cites it (resolves Q-P2).
- [ ] Q-P1 is DECIDED (b): `../PE-platform-eng/API-CONTRACT.md` (REST surface, X-13) exists and freezes **together with** this spec — G-D is not green on this spec alone. ✓ exists.
- [ ] The codec is implemented in the shared Rust core and bound into all three clients.
- [ ] The §8 golden-vector corpus exists under `protocol/` and passes on Rust + web + iOS + Android (NFR-10).
- [ ] Round-trip + negative-decode + ordering/immutability suites green.
- [ ] S3 (`../S3-platform/DATA-MODEL.md`) and this spec agree on every contract-visible entity field (§3.3) — reconciled: termination enum (§4A.5) matches, `time_control` id matches, color/result encodings match.
- [ ] Reviewed by S2 + S3 + PE + architecture group; **S6 reviewed** the event stream carries sufficient fair-play signal (ADR-2 veto).
- [ ] Spec RATIFIED; the frozen version is tagged as part of the G-D baseline (jointly with the PE + S3 specs).

## 10. Decisions & open items

Owner directive (2026-10-12): **correctness and highest quality over speed.** The resolutions below are set to the no-compromise reading accordingly.

- **Q-P1 — G-D scope / ownership boundary — DECIDED (b), owner, 2026-10-12.** G-D (API/protocol contract frozen v1) is green only when **both** this realtime contract (WS frame + event store + entity shape) **and** a PE `API-CONTRACT.md` (the REST/JSON surface, MASTER §VIII.5, X-13) are frozen together as one coherent v1 client-facing surface. Rationale: clients bind both halves; freezing one and declaring the gate green would let the two drift — the exact compromise the owner ruled out. Matches MASTER §V.2's own wording ("X-13 API contract frozen (v1)"). **Consequence:** a sibling spec `PE/API-CONTRACT.md` is a required, parallel P0 artifact; this spec is the realtime half and does not freeze alone. This spec stays scoped to the three realtime artifacts.
- **Q-P2 — binary payload codec — DECIDED (ADR-14).** One schema-defined binary codec in the shared Rust core, bound into all three clients and the Go server; payloads are postcard, header bytes raw (§4A). (ADR-13 is the held magic-bitboards skip, unrelated.)
- **Q-P3 — exact byte values & enum tables — RESOLVED (Appendix 4A).** Concrete u8 codes for `type`, `reason` (§4A.4), `termination` (§4A.5), `error code` (§4A.7), event `type` (§4A.9), the frame envelope + payload field layouts (§4A.1–4A.3), and the event-store envelope (§4A.8) are all pinned in-spec. The codec framing is now locked: postcard varints and length prefixes (§4A); field order and logical types are as tabled. `time_control` is a str id (Q-A4), not an enum.
- **Q-P4 — `start_state` payload of `MATCHED` — RESOLVED (§4A.6).** `MATCHED.start_state` is exactly the `STATE` body from `fen` onward — one state format, no second encoding.

## 11. Version history

| Version | Date | Change | Decision |
|---|---|---|---|
| 1.0 | 2026-10-12 | Initial frozen contract | ADR-10, ADR-14 |
| 1.1 | 2026-10-16 | `C RESYNC 0x0A`, `S DRAW_OFFERED 0x87` (additive) | ADR-17; slice `PROTOCOL-V1.1-RESYNC-DRAWOFFERED.md`, merged #56 |
| 1.2 | 2026-10-16 | `MOVE_ACCEPTED` gains `uci` and goes to both seats (mutates a frozen frame) | ADR-18; slice `PROTOCOL-V1.2-MOVEACCEPTED-UCI.md`, merged #61 |
| text | 2026-11-09 | v1.1/v1.2 folded into this document; §4A physical encoding corrected to postcard; stale Q-P2/ADR-14 PROPOSED text removed (#106). No wire change. | none (doc correction) |
| 1.3 | 2026-11-10 | Takeback accept/decline, draw decline, five new events, §4A.10 status, v1.3 STATE body (mutates `0x81` and `0x84`). Wire and tables folded in; the §5 behaviour rows fold in with the GS-7 reducer. | ADR-19; slice `PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md` |

The major `ver` byte stays `0x01` for v1.x. Version numbers here are document versions of the v1 wire.

---

### Reviewer note (not part of the frozen contract)

Parked per owner, **not reopened here:** ADR-13 (magic-bitboards skip) and the §3.3/§6.3 magic language — engine-internal, no protocol surface. This spec touches neither.

The three "still owed" items from MASTER §VIII.6 that are **in** this spec's remit (binary frame byte-layout, event schema) are addressed to contract depth; the two that are **deferred** (variant-specific event extensions, matchmaking algorithm) are explicit non-goals (§1) with extensibility preserved (§5).
