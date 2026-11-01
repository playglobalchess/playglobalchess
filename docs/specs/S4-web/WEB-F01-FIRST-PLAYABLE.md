# S4 Web Client — F-01 first-playable slice (implementation spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S4-WEB-F01 (first slice of the S4 Web Client stream; implements F-01 on web) |
| Owner | S4 Web |
| Status | RATIFIED — v1.1 owner-approved 2026-11-01 (§3.4.1 client consistency & recovery policy; Q-S4f and Q-S4g decided; Q-S4h deferred to an S2 protocol issue). v1.0 owner-approved 2026-10-16 (Q-S4a–e decided). |
| Version | 1.1 |
| Last updated | 2026-11-01 |
| Implements (master IDs) | F-01 (Play Online, Standard 2-seat), X-2 (via the GS-6 WS edge), X-1 (engine, via the shared codec/wasm), NFR-5 (one codec — the client binds it, never re-implements §4A), NFR-8 (accessibility), NFR-11 (i18n), ADR-9 (web = TypeScript + React), ADR-14 (one shared binary codec), §4A v1.1 (RESYNC/DRAW_OFFERED, ADR-17) |
| Depends on | GS-6 WS transport (merged), §4A v1.1 (merged), the `codec-wasm` binding (exists). **Carries a prerequisite: a wasm MESSAGE seam (S4a) — see §3.0.** |
| Gates it feeds | **The first time a HUMAN plays a move in a browser** — F-01's visible client. Makes no lettered gate green by itself (a *rated* game still needs G-B anti-cheat + G-C rating); it is the client half of the F-01 vertical slice whose server half is live. |

> **The milestone chunk — but it carries a codec-binding prerequisite (S4a).**
> Like GS-6 needed GS-6a, the web client needs a typed message seam the current
> `codec-wasm` does not expose. Today `codec-wasm` exports only
> `reencode_{client,server,event}` (frame→frame) + `protocol_version` — there is
> NO way for JS/TS to BUILD a `SEEK`/`MOVE` frame from typed fields or READ a
> `MATCHED`/`STATE`/`MOVE_ACCEPTED`/`DRAW_OFFERED` into typed fields. The client
> cannot speak the protocol without that. **S4a** adds it (the wasm analog of the
> GS-6a/GS-6 Go seam), keeping §4A encoding in Rust (NFR-5). This slice specifies
> BOTH the seam prerequisite (§3.0) and the minimal F-01 client (§3.1+).

## 1. Purpose & scope

Build the minimal browser client that lets a human play a Standard game end to
end against the live server: connect, authenticate, seek, get matched, see a
board, make legal moves, and reach a game end (mate/resign/draw/flag) — the
client half of the F-01 loop whose server half (engine, codec, session, store,
matchmaking, WS transport) is merged.

**In scope (this slice):**
- **S4a — the wasm message seam** (prerequisite, §3.0): typed
  `encodeClient`/`decodeServer` (and `encodeServer`/`decodeClient` for tests)
  over the `codec-wasm` binding, so TS builds/reads §4A messages without
  hand-rolling a byte (NFR-5). Mirrors the GS-6a/GS-6 Go seam.
- A `clients/web` app shell: TypeScript + React + Vite (ADR-9), strict mode, no
  `any`, i18n-externalized strings (NFR-11).
- A **WS client**: open a binary WebSocket to the GS-6 edge, AUTHENTICATE
  (dev token until X-3), SEEK, and drive the frozen §4A flow — a goroutine-free
  single read loop dispatching `ServerMsg` to UI state; a send path building
  `ClientMsg` frames via the seam.
- A **board component**: render a position from a FEN, show the clock, accept a
  move by click/drag, and present legal-move affordances. **Built from scratch**
  (Q-S4d, owner-decided) — no third-party board renderer, so the web client takes
  on NO third-party UI dependency and there is zero risk of pulling in a JS
  chess-rules library (NFR-5). Legal-move UX is driven by the **shared engine via
  the codec/wasm**, NOT a re-implementation (NFR-5) — the client proposes a UCI
  move and the server is authoritative; the client may pre-validate for UX only
  via the engine binding.
- The F-01 loop: SEEK→MATCHED (render board + clocks), MOVE→MOVE_ACCEPTED (apply)
  /MOVE_REJECTED (revert + show reason), opponent moves applied, RESIGN/
  OFFER_DRAW/DRAW_OFFERED/accept, STATE resync on reconnect (RESYNC on demand),
  PING/PONG, GAME_END (show result).
- Accessibility baseline (NFR-8): keyboard move entry, ARIA on the board,
  screen-reader move announcements.

**Out of scope (later S4 slices — do NOT start):**
- Puzzles, Learn/Train, profiles, chat, spectating, variants, Chess960 — later
  F-modules.
- A design system / full visual polish — a functional board, not a themed one.
- Account UI / real auth (X-3) — a dev-token field until X-3 lands.
- Rating/clock-fairness display beyond the raw clocks (ratings = X-4).
- Native sharing with S5 (the shared board LIB extraction is a later refactor;
  this slice ships a web board, S5 reuses it when both exist).

## 2. Requirements traced

| Requirement | Where |
|---|---|
| F-01 end-to-end on web | §3.1–3.4 — connect→seek→match→play→end. |
| NFR-5 one codec / one engine | §3.0 (wasm seam, no hand-rolled §4A), §3.3 (engine binding for legal-move UX, server authoritative). |
| §4A v1.1 RESYNC/DRAW_OFFERED | §3.4 (RESYNC on reconnect), §3.3 (DRAW_OFFERED render). |
| NFR-2 (no game loss) — client side | §3.4.1 (bounded, automatic recovery to the server's authoritative STATE; never a dead end). |
| NFR-8 accessibility / NFR-11 i18n | §3.5. |
| ADR-9 web stack | §3.1 (TS+React+Vite). |

## 3. Design

### 3.0 S4a — wasm message seam (prerequisite)

Add to `codec-wasm` (mirroring the GS-6a/GS-6 Go seam `DecodeClient`/
`EncodeServer`/`EncodeClient`/`DecodeServer`):
- `encodeClient(msg) → Uint8Array` and `decodeServer(frame) → ServerMsg` — the
  two directions a CLIENT needs (build C→S frames, read S→C frames).
- `encodeServer`/`decodeClient` — the mirrors, for the client's own tests and a
  future JS test harness, exactly as GS-6 added the client-mirror for its e2e.
Typed via `wasm-bindgen` to a TS-shaped object (serde→JsValue, or a thin hand-
typed boundary matching the §4A payload fields). §4A bytes stay produced in Rust;
the TS side never encodes a §4A byte. Proven by a node parity test (the
`web_parity.rs`/node harness pattern already used for `codec-wasm`): every
`ClientMsg`/`ServerMsg` round-trips byte-identically to the committed golden
vectors (the same corpus the Rust/Go seams freeze — NFR-10).

### 3.1 App shell & WS client

`clients/web`: Vite + React + TypeScript (strict). A single `GameSocket` wraps a
binary `WebSocket`: one `onmessage` → `decodeServer` → a typed `ServerMsg` →
reducer updating game state; a `send(msg)` → `encodeClient` → `ws.send(bytes)`.
AUTHENTICATE is the first frame (dev token from an input until X-3, Q-S4c). The
client holds NO game truth — it renders what the server sends and proposes
intents.

### 3.2 Seek → matched

A "Play" action sends `SEEK{pool_id, time_control, rated:false}`; on `MATCHED`
the client stores `{game_id, my_seat, opponent, start_state}` and renders the
board + both clocks from `start_state`.

### 3.3 Move hot path & social actions

- A board move → `MOVE{game_id, ply_expected, uci}`. Optimistic UX is allowed
  (show the move immediately) but the server is authoritative: on `MOVE_ACCEPTED`
  confirm (apply the authoritative `ply`/`clock_after_ms`); on `MOVE_REJECTED`
  REVERT the optimistic move and surface the reason. Legal-move highlighting uses
  the engine binding for UX only (NFR-5 — the server still validates).
- Opponent `MOVE_ACCEPTED` (for the other seat) applies their move + clock.
- `RESIGN`/`OFFER_DRAW` buttons send the client messages; a received
  `DRAW_OFFERED{by}` shows an accept/decline affordance (accept = send
  `OFFER_DRAW`, the mutual-offer agreement); `GAME_END` shows the result.

### 3.4 Reconnection

On socket close, reconnect + re-AUTHENTICATE; the server auto-pushes `STATE`
(Option A) and the client may also send `RESYNC{game_id}` to force a fresh
`STATE` (§4A v1.1). The board rebuilds from the `STATE` fold — the client never
reconstructs truth locally.

### 3.4.1 Client consistency & recovery policy (v1.1 amendment — RATIFIED 2026-11-01)

**Model.** Modelled on lichess's open-source client (`ui/lib/src/socket.ts`):
duplicates are ignored silently, any other inconsistency triggers a short
automatic retry, and if that fails the client rebuilds from the server (lichess
reloads the page; we reconnect, which makes the server push `STATE`). The player
is never asked to act during recovery and the board is never locked behind a
user action. The server is authoritative for position, clocks and result
throughout; the client only decides *when its view is untrustworthy*.

**Layer ownership (one owner per action).**
- **Reducer** (pure): detects every trigger below and holds the recovery state
  (`sync.phase`, attempt and cycle counters, a raise sequence number). No I/O.
- **`useGame`** (hook): owns every timer and every send — RESYNC, the forced
  reconnect, and the terminal action. It is the ONLY layer that sends RESYNC.
- **`GameSocket`** (transport): framing, auth, backoff on socket drops, and a
  `reconnect()` the hook calls to force a fresh transport. It never decides to
  resync.

**T — detection triggers (reducer).** Each one raises a resync request:
- **T1 gap:** `MOVE_ACCEPTED.ply > local ply + 1`.
- **T2 mismatch:** `MOVE_ACCEPTED.ply <= local ply` and NOT (`ply == local ply`
  and `uci == the last applied move`). The one exact match is a **duplicate and
  is ignored silently** — no resync.
- **T3 unappliable:** a `MOVE_ACCEPTED` whose `uci` the engine binding cannot
  apply to the current position.
- **T4 stale:** `MOVE_REJECTED{STALE_PLY}`.
- **T5 inconsistent frame:** a `STATE`, or `MATCHED.start_state`, whose FEN
  side-to-move differs from the wire `turn` (0 = white), or whose fullmove field
  is missing or not a positive integer (#68, #71.4).

**S — recovery state machine (reducer state, driven by `useGame`).**

| Phase | Entered when | Behaviour | Leaves when |
|---|---|---|---|
| `IN_SYNC` | start; any consistent `STATE` | normal play | any trigger T1–T5 → `SYNCING(1)` |
| `SYNCING(n)`, n = 1..N | trigger, or retry | `useGame` sends ONE `RESYNC` on the current socket once it is `connected`, then waits up to `T_reply` for a `STATE` | consistent `STATE` → `IN_SYNC`; inconsistent `STATE` or timeout → `SYNCING(n+1)` after backoff `B·2^(n-1)`; after attempt N fails → `RECONNECTING(1)` |
| `RECONNECTING(k)`, k = 1..K | resyncs exhausted | `useGame` calls `GameSocket.reconnect()`; on re-AUTH the server auto-pushes `STATE` (Option A), which counts as the reply; waits up to `T_reply` after `connected` | consistent `STATE` → `IN_SYNC`; otherwise → `RECONNECTING(k+1)`; after cycle K fails → `FAILED` |
| `FAILED` | recovery exhausted | terminal: see "Terminal state" | page refresh; or a later consistent `STATE` (e.g. the socket's own reconnect) → `IN_SYNC` |

Bounds (Q-S4f, DECIDED): **N = 3** resyncs, **K = 2** reconnect cycles,
**`T_reply` = 1000 ms**, **B = 250 ms**. A healthy resync completes in one round
trip. Worst case before `FAILED` is about 7–8 s, during which the player's clock
may run. That cost is why the bounds were an owner decision; they are fixed
constants, not tuned at runtime.

**Recovery of the recovery.**
- **Coalescing:** a new trigger while `SYNCING`/`RECONNECTING` does not restart
  or add attempts; recovery is already in progress.
- **Socket drop mid-recovery:** the in-flight RESYNC is treated as lost and is
  re-sent once the socket is `connected` again (the guard is keyed on socket +
  game + raise sequence, and cleared when status leaves `connected`; #69). The
  server's auto-`STATE` on re-AUTH also counts as a reply. A drop does not
  consume an attempt.
- **Prolonged outage:** while the socket is not `connected`, recovery is
  PAUSED, not failed. `T_reply` only runs while connected, and the existing
  connection status is shown. This matches lichess's "Reconnecting" banner. A
  pure network outage is the transport's concern (`GameSocket` backoff). It
  does not count toward N or K, so it cannot drive the client to `FAILED`.
- **Frames that arrive while recovering:**
  - `MOVE_ACCEPTED` frames that arrive before the awaited `STATE` are not
    applied, because the stream is ordered and that `STATE` already folds them.
    Frames after it apply normally.
  - An optimistic local move still pending is discarded when the `STATE` lands;
    the board shows exactly the `STATE`.
- **Game ends mid-recovery:** `GAME_END` is applied immediately in every phase
  (the result is authoritative and needs no position) and ends recovery.
  `DRAW_OFFERED` is shown in every phase.
- **New game:** `MATCHED` for a different game resets recovery to `IN_SYNC`
  (then T5 applies to its `start_state`).
- **A consistent `STATE` is always authoritative**, including one with a LOWER
  ply than local (ply is not monotonic under Takeback, GS-3 §3.2; see the
  ordering note below). It resets all counters.

**UX while recovering.**
- In `SYNCING`/`RECONNECTING` the board stays visible but read-only: no move
  initiation by pointer, keyboard or `move()`.
- The existing polite status line (`role="status"`, string `game.resync`) shows
  "Syncing game…".
- Clocks keep counting down from the last authoritative values, and the next
  `STATE` corrects them.
- Resign and draw remain available.
- There are no buttons and no modal during recovery.
- Keyboard focus is not moved by recovery.

**Terminal state (`FAILED`).**
- Show an alert (`role="alert"`) saying the game lost sync, with ONE action:
  "Refresh", which reloads the page. Reloading rebuilds from the server, since
  the game is held by the server, not the tab.
- Log a client error event with the game id and the trigger that started the
  episode. It goes to the console until the X-14 telemetry sink exists.
- The board stays read-only.
- Whether to auto-reload instead of prompting is Q-S4g.

**Ordering & identity assumptions.** This policy relies on:
- **a single ordered stream per socket (WebSocket/TCP)** — guaranteed by the transport;
- **`STATE` being a full authoritative fold** — §4A, `PROTOCOL.md` §4A.6;
- **`MOVE_ACCEPTED` carrying `uci` to both seats** — §4A v1.2, ADR-18.

It does NOT rely on monotonic ply.

The protocol does not give the client an event version number. The event-store
`seq` is never sent on the wire. Nor does it notify the client of an accepted
takeback. As a result the client cannot tell a replay from a rewind until a
`STATE` arrives. T2 plus STATE authority make this safe but not instant; the
real fix is a protocol amendment, tracked as Q-S4h. It is out of scope for this
slice.

### 3.5 Accessibility & i18n

Keyboard move entry (square-to-square), ARIA grid semantics on the board,
screen-reader announcements of moves/offers/results (NFR-8). All user-facing
strings via an i18n layer (NFR-11); locale list is deferred (ADR-7), but no
hard-coded strings.

## 4. Interfaces & contracts

- Consumes the frozen §4A protocol via the S4a wasm seam — no new wire bytes, no
  §4A change (S4a is additive binding surface like GS-6's client-mirror).
- Consumes the GS-6 WS edge as-is (AUTHENTICATE/SEEK/MOVE/.../RESYNC and the S→C
  set incl. DRAW_OFFERED).
- No chess logic re-implemented (NFR-5): rules come from the engine binding; the
  server is authoritative for every move/clock/result.

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| MOVE_REJECTED after optimistic apply | revert to the pre-move position; show the reason (ILLEGAL/STALE_PLY/…). |
| Socket drops mid-game | auto-reconnect + re-AUTH → STATE resync; board rebuilt from the fold. |
| ERROR{UNSUPPORTED_VERSION} | the client's protocol major mismatches the server; show a "please refresh" and close (§4A ver rule). |
| DRAW_OFFERED while an offer UI is already shown | idempotent — show one accept/decline affordance. |
| Opponent never joins (ghost) | server aborts (GS-5/GS-6 join-timeout); client gets GAME_END{ABORT}. |
| Oversized/garbled frame from server | the seam's decode throws; the client logs + ignores (never crashes the loop). A frame that DECODES but is internally inconsistent is T5 (§3.4.1), not this row. |
| Duplicate `MOVE_ACCEPTED` (same ply + same uci as the last applied move) | ignored silently; no resync (§3.4.1 T2). |
| Ply gap, mismatched/unappliable move, `STALE_PLY` | §3.4.1 T1–T4 → automatic bounded resync. |
| `STATE`/`MATCHED` FEN side-to-move ≠ wire `turn`, or garbled fullmove | §3.4.1 T5 → not trusted; automatic bounded resync. |
| RESYNC reply never arrives, or is itself inconsistent | next attempt after backoff; after N → forced reconnect; after K → `FAILED` (§3.4.1). |
| Socket drops during recovery | RESYNC re-sent on the new connection; the drop does not consume an attempt (§3.4.1). |
| `GAME_END` during recovery | applied immediately; recovery ends (§3.4.1). |
| `STATE` with a lower ply than local (takeback/rewind) | applied; STATE is authoritative (§3.4.1). |
| Recovery exhausted (`FAILED`) | read-only board + alert with one "Refresh" action; error logged; no loop (§3.4.1, Q-S4g). |

## 6. Test plan

- **S4a seam:** node parity test — every `ClientMsg`/`ServerMsg` round-trips
  byte-identically to the committed golden corpus (the Rust/Go-shared
  `vectors.json`), proving the TS seam matches the one codec (NFR-10).
- **Reducer/unit:** `ServerMsg`→state transitions (matched, accepted, rejected-
  revert, game-end) are pure-function tested.
- **WS client (fake socket):** a fake `WebSocket` feeds frames; assert the client
  builds the right `ClientMsg` frames and reaches the right state — no browser.
- **e2e (nightly, not a per-commit gate — Q-S4d/e decided):** a headless browser
  (or a node WS client) plays a full game against a locally-run game-server (the
  GS-6 e2e harness already proves the server side; this proves the real client
  speaks to the real server). Runs as a nightly against the real-dependency
  stack, so first-playable is not blocked on CI browser infra.
- **Consistency & recovery (§3.4.1)** — one test per row, each shown to FAIL
  when the production line it guards is deleted (non-vacuity is part of done):
  - Reducer: each of T1–T5 raises; the exact duplicate does not raise; a
    lower-ply consistent `STATE` is applied; `GAME_END` ends recovery in every
    phase; `MATCHED` for a new game resets recovery; frames before the awaited
    `STATE` are not applied, frames after it are.
  - `useGame` (fake socket + fake timers):
    - exactly one RESYNC per attempt;
    - nothing is sent before `connected`;
    - an inconsistent reply or a timeout leads to the next attempt after backoff;
    - after N attempts, `reconnect()` is called;
    - after K cycles, `FAILED`, with no further sends;
    - a socket drop mid-recovery re-sends without consuming an attempt;
    - triggers during recovery coalesce;
    - a consistent `STATE` resets the counters;
    - no move is sent while recovering.
  - UI: the board is read-only and the status line is shown while recovering;
    the `FAILED` alert has exactly one action; recovery never moves focus.
  - e2e (real server, the existing `web-e2e` job): drop the socket mid-game and
    assert the board recovers to the server position and play continues.
- Accessibility: automated axe checks on the board; keyboard-only move entry
  test, including Tab into the board while it is NOT the player's turn (#71.1).
- `npm run build` + typecheck (strict) + lint clean; CI job added
  (path-filtered on `clients/web/**` + `protocol/codec-wasm/**`).

## 7. Acceptance criteria (Definition of Done, F-01-web)

- [ ] S4a wasm seam: typed encode/decode for every §4A message, node-parity green
      against the shared golden corpus. (Its own PR first — Q-S4a decided.)
- [ ] `clients/web` shell (Vite+React+TS strict, lint, i18n-externalized).
- [ ] WS client: AUTHENTICATE→SEEK→MATCHED→MOVE→(accepted/rejected)→…→GAME_END,
      server-authoritative, optimistic-with-revert move UX.
- [ ] Board renders from FEN, legal-move UX via the engine binding (no rules
      re-impl), clocks shown and ticking.
- [ ] Reconnection: STATE resync (auto + RESYNC); DRAW_OFFERED accept/decline UI.
- [ ] §3.4.1 consistency & recovery policy implemented with the layer ownership
      stated there; every §6 recovery test green and shown non-vacuous; owner
      has checked the recovery UX in a real browser (socket drop → "Syncing
      game…" → play resumes).
- [ ] Accessibility baseline (NFR-8) + i18n (NFR-11).
- [ ] Tests above green; CI job added; `make`/`npm` build + typecheck + lint clean.
- [ ] A human can play a full Standard game in a browser against the server
      (demonstrated). Makes no lettered gate green (rated needs G-B + G-C).

## 8. Open questions — DECIDED (owner-approved 2026-10-16)

- **Q-S4a — S4a (wasm seam) as its own PR first?** Like GS-6a before GS-6.
  **DECIDED: yes, separate PR** — the wasm seam is a `protocol/codec-wasm` change
  with its own node-parity gate, reviewed as a codec change (its own context-free
  review); then the client builds on the merged, parity-proven seam.
- **Q-S4b — framework: React vs Svelte.** MASTER §V.5 lists "React (or Svelte)".
  **DECIDED: React + Vite + TypeScript (strict)** — the master's first-named
  option, the largest ecosystem for the reference client, TS-strict throughout.
- **Q-S4c — auth before X-3.** Same seam as GS-6's `Authenticator`: a dev-token
  input field whose value is sent in AUTHENTICATE; X-3 later replaces it with a
  real login (one-line swap, no protocol change). **DECIDED: dev-token field.**
- **Q-S4d — board: build vs adopt.** **DECIDED: build the board from scratch** —
  render + interaction (click/drag, highlights, coordinates, a11y) hand-built, NO
  third-party board renderer. Tradeoff accepted: more build time, in exchange for
  full control over interaction + accessibility, no third-party UI dependency, no
  license question, and the strongest possible NFR-5 posture (no JS UI lib that
  could drag in a chess-rules dependency). Legality/state still come from OUR
  engine+codec; the server stays authoritative. (Recommendation had been to adopt
  a render-only lib; owner chose build-from-scratch — recorded as decided.)
- **Q-S4e — e2e against a real server in CI.** Standing up game-server + redis +
  dynamodb + a headless browser per-commit is heavier than the server-side gated
  tiers. **DECIDED: nightly for now** — the per-commit gate is fake-socket +
  reducer + node-parity; the full browser-vs-server e2e runs as a nightly (not a
  per-commit gate), so first-playable is not blocked on CI browser infra.

## 9. Open questions — v1.1 amendment (DECIDED, owner-approved 2026-11-01)

- **Decisions:** Q-S4f is **DECIDED: the proposed values** (N=3, K=2,
  T_reply=1000 ms, B=250 ms). Q-S4g is **DECIDED: prompt with one "Refresh"
  action**, no auto-reload. Q-S4h is **DEFERRED** to an S2 protocol issue,
  outside this slice. It is tracked as GitHub issue #81.


- **Q-S4f — recovery bounds.** Proposed: N = 3 resyncs, K = 2 reconnect cycles,
  `T_reply` = 1000 ms, backoff B = 250 ms doubling. A healthy resync costs one
  round trip; the worst case before `FAILED` is about 7–8 s of the player's
  clock. Lichess retries a version gap for about 2 s (10 × 200 ms) before a full
  reload. Tighter bounds give up sooner; looser ones burn more clock.
  **Recommendation:** the proposed values. They keep the common case
  sub-second, and the worst case is still well under a bullet game's clock.
- **Q-S4g — terminal behaviour: prompt vs auto-reload.** Lichess auto-reloads.
  For us a reload is equivalent to a reconnect, which `RECONNECTING` has just
  failed twice, so auto-reloading risks a reload loop.
  **Recommendation:** prompt with one "Refresh" action, as proposed in §3.4.1.
  Revisit once X-14 telemetry shows how often `FAILED` happens.
- **Q-S4h — protocol gaps found while writing §3.4.1 (OUT OF SCOPE here).**
  - §4A sends no per-game event version (the event-store `seq`) to the client.
  - It has no server→client notification of an accepted takeback.
  - Its reconnect row says the client "sends last known ply", but no v1 message
    carries it.
  Lichess orders client messages by an event version for exactly this reason.
  Fixing these is a frozen-protocol amendment: ADR, cross-stream review, and
  batched with other §4A changes. To be filed as an S2 issue, not part of this
  slice.
