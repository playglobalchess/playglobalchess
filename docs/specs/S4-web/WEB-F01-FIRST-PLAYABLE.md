# S4 Web Client — F-01 first-playable slice (implementation spec)

| Field | Value |
|---|---|
| Spec ID | SPEC-S4-WEB-F01 (first slice of the S4 Web Client stream; implements F-01 on web) |
| Owner | S4 Web |
| Status | RATIFIED (owner-approved 2026-10-16; all 5 Q-S4 decided) |
| Version | 1.0 |
| Last updated | 2026-10-16 |
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
- **Clocks tick (#112).** The side to move's clock counts down locally from the
  last server value; the server stays authoritative and the client never flags.
  `MATCHED`/`STATE` start `running_seat`; `MOVE_ACCEPTED` sets the mover's
  `clock_after_ms` and starts the other side; our own move stops our clock at
  once (a reject, or a duplicate or gap `MOVE_ACCEPTED` that makes the pending
  move moot, restarts it from its anchor); `GAME_END` freezes both. The
  anchor is the local monotonic time a frame ARRIVES, not `server_t`: the client
  sends no `PING`, so it has no offset to map `server_t`, and `MOVE_ACCEPTED`
  carries none. The display is therefore up to one network latency generous,
  and every server frame corrects it. The clock keeps ticking while
  disconnected, because the server keeps charging that time (GS6 §3.4); the
  reconnect `STATE` resyncs it. The running clock is marked by a visible ▶ and
  the text "running", each clock is labelled You / Opponent, and clocks are
  `role="timer"` (not live regions); low-time warnings are #142.
- `RESIGN`/`OFFER_DRAW` buttons send the client messages; a received
  `DRAW_OFFERED{by}` shows an accept/decline affordance (accept = send
  `OFFER_DRAW`, the mutual-offer agreement; decline = `DECLINE_DRAW`, §4A v1.3);
  `GAME_END` shows the result. While our own offer is pending, "Offer draw" is
  disabled and a status line says the offer is waiting.
- **Takeback (GS-7, §4A v1.3; added 2026-11-10, modelled on chess.com).** In an
  UNRATED game a "Takeback" button sends `TAKEBACK` (a request). It is enabled
  from ply 2 when no request is pending either way. The opponent sees "Opponent
  asks to take back a move" with Accept (`TAKEBACK_ACCEPT{at_ply}`) and Decline
  (`TAKEBACK_DECLINE{at_ply}`); the requester sees a waiting status line. An
  accept rewinds both boards through the server's `STATE`. Either side's next
  move declines a pending prompt (GS-7 §6.4). Deviation from chess.com: there is
  no per-game "Allow Takebacks" setting; every unrated game allows requests (a
  setting would need a SEEK and protocol change). The button is hidden when the
  client does not know the game is unrated (after a reload, since neither
  `MATCHED` nor `STATE` carries `rated`).
- "Play" seeks `rated:false`, as §3.2 says (the client had sent `rated:true`).
- "Offer draw" is enabled only when the server would accept a new offer (ply ≥ 2
  and none of ours in the last 20 plies, GS-7 §6.3), so a click never shows a
  "waiting" line the server then silently clears. The two decline buttons are
  labelled "Decline draw" and "Decline takeback", and both prompts are announced
  when both are pending.
- On STATE the client clears the screen-reader `lastMove` instead of setting it
  from `last_move_uci` (GS-7 §7.4 amendment). It stores the clock anchor from
  `running_seat`, anchored at arrival rather than `server_t` (§3.3, #112).

### 3.4 Reconnection

On socket close, reconnect + re-AUTHENTICATE; the server auto-pushes `STATE`
(Option A) and the client may also send `RESYNC{game_id}` to force a fresh
`STATE` (§4A v1.1). The board rebuilds from the `STATE` fold — the client never
reconstructs truth locally.

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
| Oversized/garbled frame from server | the seam's decode throws; the client logs + ignores (never crashes the loop). |

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
- Accessibility: automated axe checks on the board; keyboard-only move entry test.
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
