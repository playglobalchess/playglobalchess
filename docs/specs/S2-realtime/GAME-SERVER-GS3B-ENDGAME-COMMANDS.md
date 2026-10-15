# Game-server GS-3b — resign / abort / draw-offer session commands (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS3B (implementation slice of SPEC-S2-GAME-SERVER §3.3/§3.4; extends GS-3 the live session) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-15 |
| Implements (master IDs) | X-2, F-01 (the non-move game-ending actions), NFR-5 (session owns game truth; no chess logic in the transport), NFR-7 (persist-before-ack — the ending event is durable before the session reports ended) |
| Depends on | GS-3 (the live `Session` actor + command pattern), GS-4 (durable store + atomic `appendBatchAtHead`), the §4A event model (`EvResigned`/`EvAborted`/`EvDrawOffered`/`EvDrawAccepted` already defined). All merged. |
| Gates it feeds | None directly. Completes the F-01 game-ending action set so GS-6 (the WS transport) can route the full client message surface to real session commands rather than returning `ERROR`. Makes no lettered gate green. |

> **Why this slice exists — a gap GS-6 surfaced.** The wire protocol has
> `RESIGN`, `ABORT`, `OFFER_DRAW` client messages (§4A.2) and the event model has
> `EvResigned`/`EvAborted`/`EvDrawOffered`/`EvDrawAccepted` (§4A.9) — but the GS-3
> `Session` actor exposes only `SubmitMove`/`TickFlagCheck`/`StateSnapshot`/
> `Takeback`. **No session command appends a resign, abort, or draw.** GS-6's job
> is to decode a wire message and route it to a session command; without these,
> GS-6 could only decode them and return `ERROR`. The dependency runs
> session→transport, so these commands land FIRST (here), then GS-6 wires an
> already-working, already-tested surface in one clean pass. This slice adds the
> commands + their state-machine rules; it opens no socket and changes no wire
> byte.

## 1. Goal & scope

Add four game-ending / draw-negotiation commands to the `Session` actor,
appending the already-defined §4A events via the GS-4 atomic `appendBatchAtHead`
path, with their state-machine rules specified and adversarially tested. Done =
each command is a single-writer actor command (like `Takeback`), persists its
event(s) before marking the session ended (NFR-7), is idempotent/safe under
resend, and is `-race` clean.

**In scope:**
- `Resign(ctx, by)` — seat `by` resigns; appends `Resigned{by}` + `GameEnded`
  (termination `RESIGN`), result vector = `by` loses / others win.
- `Abort(ctx, by)` — seat `by` aborts an unstarted game; appends `Aborted{by}` +
  `GameEnded` (termination `ABORT`), result vector = all draw (no rated result).
- `OfferDraw(ctx, by)` — seat `by` offers a draw; records an ephemeral pending
  offer (NOT yet an event — see §3.3 Q-EG2) and appends `DrawOffered{by}`.
- `AcceptDraw(ctx, by)` — seat `by` accepts a pending offer from the OTHER seat;
  appends `DrawAccepted{by}` + `GameEnded` (termination `DRAW_AGREE`), result
  vector = all draw.

**Out of scope (later chunks — do NOT start):**
- The WS transport routing these (GS-6) — this slice exposes the Go API GS-6
  calls; it opens no socket.
- Rematch (a new-game flow, matchmaking/transport concern).
- Rating effects (X-4) — the session emits `GameEnded`; `RatingApplied` is X-4.
- Draw *decline* as a distinct wire message — §4A has no `DRAW_DECLINE`; a
  decline is implicit (the offer lapses, see §3.3).

## 2. Requirements traced

| Requirement | Where |
|---|---|
| §3.3 game-ending actions (resign/abort/draw) | §3.1–3.3 — single-writer actor commands appending the §4A events. |
| NFR-7 persist-before-ack | §3.4 — the session marks itself ended only AFTER the atomic ending append returns. |
| NFR-5 one rules engine | §3.x — no chess logic here; abort-eligibility is a ply-count rule, draw/resign are agreement/forfeit, all server-authoritative. |
| §5 idempotency / resend safety | §3.5 — a resent resign/accept on an already-ended game is a safe no-op, not a double-append. |

## 3. Design

All four are **actor commands** on the single-writer `Session` loop (the
`Takeback` pattern): a `*xCmd` with a `done` channel, dispatched via
`s.dispatch`, applied on the owner goroutine where it may touch session fields.
Each ending command computes its result vector, then appends ATOMICALLY via
`appendBatchAtHead` (the GS-4 conflict-retrying head-append), then calls
`endGame(term)`. The event(s) are durable before the command returns success
(NFR-7); if the append fails the session is NOT marked ended and the error
surfaces (same discipline as `flag()`), and `NewSession`'s half-terminal recovery
closes a crash-interrupted ending on the next rebuild.

### 3.1 Resign

`Resign(ctx, by)`:
1. If `s.ended` → no-op success (idempotent: a resend after the game already
   ended is not an error; §5).
2. Append `[Resigned{by}, GameEnded{rv, RESIGN}]` atomically, where `rv` has
   `by` = loss and every other seat = win (2-seat v1: the opponent wins).
3. `endGame(TermResign)`.

Resign is always legal while the game is live (any seat, any ply) — there is no
"too early to resign". A resign by the side NOT to move is still valid (you may
resign on your opponent's turn).

### 3.2 Abort

`Abort(ctx, by)`:
1. If `s.ended` → no-op success.
2. **Eligibility (Q-EG1):** abort is allowed only BEFORE the game has truly
   started — `s.ply < abortBeforePly` (default 2: before both players have made
   their first move). After that, quitting is a `Resign`, not an abort. If
   `s.ply >= abortBeforePly`, return a typed `ErrAbortTooLate` (the transport
   maps it to a `MOVE_REJECTED`/`ERROR` the client can show; it is NOT silently
   turned into a resign — that is the caller's/UI's choice).
3. Append `[Aborted{by}, GameEnded{rv, ABORT}]` atomically, `rv` = all draw (an
   aborted game has no winner and no rated result).
4. `endGame(TermAbort)`.

`abortBeforePly` is config (Q-EG3), defaulting to 2.

### 3.3 Draw offer / accept

Draw is a two-step negotiation, unlike takeback (which GS-3 models as an atomic
req+accept because a takeback offer has no standalone meaning). A draw OFFER is a
real, visible state the opponent responds to.

`OfferDraw(ctx, by)`:
1. If `s.ended` → no-op success.
2. Record the pending offer: `s.drawOfferBy = &by` (ephemeral session state, see
   Q-EG2) and append `DrawOffered{by}` (a visible event in the stream, so a
   reconnecting client/analysis sees the offer was made).
3. A second offer by the same seat while one is pending is a no-op (idempotent).
   An offer by the OTHER seat while one is pending is treated as an accept
   (mutual offer = agreement) — it appends `DrawAccepted{other}` + `GameEnded`.

`AcceptDraw(ctx, by)`:
1. If `s.ended` → no-op success.
2. If there is no pending offer, or the pending offer is from `by` itself (you
   cannot accept your own offer), return `ErrNoDrawToAccept`.
3. Append `[DrawAccepted{by}, GameEnded{rv, DRAW_AGREE}]` atomically, `rv` = all
   draw. `endGame(TermDrawAgree)`.

**Offer invalidation (Q-EG2):** a pending draw offer is cleared when the
OFFERER makes a move (`SubmitMove` success clears `s.drawOfferBy` — offering then
moving withdraws the offer, the standard rule). The offer is ephemeral session
state, NOT reconstructed on rebuild: a `DrawOffered` with no following
`DrawAccepted`/move in the stream is treated as lapsed on `NewSession` fold
(a reconnect does not resurrect a stale offer — the offerer must re-offer). This
keeps the fold free of offer-tracking and matches how players expect a dropped
connection to behave.

### 3.4 Persist-before-ack & recovery

Identical discipline to `flag()`: compute the result vector (no I/O) first, then
one atomic `appendBatchAtHead`, then `endGame`. A crash between the two events is
impossible (single atomic batch). A crash AFTER `Resigned`/`DrawAccepted` but the
session process dies before returning is covered by `NewSession`'s
half-terminal recovery (the stream has `GameEnded`, so the rebuild sees it ended
— no special case needed because the ending is one atomic batch).

### 3.5 Idempotency / resend

The `s.ended` early-return makes every command idempotent against a resend after
the game ended. A `DrawAccepted` resend after the draw already applied is a
no-op. This matches the §5 lost-ack-and-resend path the move pipeline already
handles.

## 4. Interfaces & contracts

New `Session` methods (mirroring `Takeback`'s signature shape):
```
func (s *Session) Resign(ctx context.Context, by uint8) error
func (s *Session) Abort(ctx context.Context, by uint8) error      // ErrAbortTooLate
func (s *Session) OfferDraw(ctx context.Context, by uint8) error
func (s *Session) AcceptDraw(ctx context.Context, by uint8) error // ErrNoDrawToAccept
```
New ephemeral field `drawOfferBy *uint8` (owner-goroutine-only, like the other
projection fields). New sentinels `ErrAbortTooLate`, `ErrNoDrawToAccept`. New
config `AbortBeforePly uint32` (default 2) on `Config`/`FoldOptions` as
appropriate. No wire-byte change; no new event types (all four already exist).

## 5. Edge cases & failure modes

| Case | Behavior |
|---|---|
| Resign/accept on an already-ended game | no-op success (idempotent, §3.5). |
| Abort after move 2 (`ply >= AbortBeforePly`) | `ErrAbortTooLate`; not auto-converted to resign. |
| Accept a draw with no pending offer | `ErrNoDrawToAccept`. |
| Accept your OWN offer | `ErrNoDrawToAccept` (an offer is for the opponent). |
| Offerer moves after offering | the move succeeds AND clears the pending offer (withdrawn). |
| Both seats offer (mutual) | the second offer is an accept → `DRAW_AGREE`. |
| Resign by the side not to move | valid (you may resign anytime while live). |
| Append fails mid-ending | session NOT marked ended; error surfaced; rebuild recovery closes it (one atomic batch, so no half-terminal). |
| Reconnect with a stale `DrawOffered` and no accept | offer treated as lapsed; not resurrected. |

## 6. Test plan

- **Resign:** live game → `Resign(0)` → stream has `Resigned{0}`+`GameEnded{RESIGN, rv=[loss,win]}`; `StateSnapshot` shows ended+RESIGN; a resend is a no-op.
- **Abort eligibility:** `Abort` at ply 0/1 succeeds (ABORT, all-draw); at ply ≥ `AbortBeforePly` returns `ErrAbortTooLate` and the stream is untouched.
- **Draw happy path:** `OfferDraw(0)` appends `DrawOffered{0}`; `AcceptDraw(1)` → `DrawAccepted{1}`+`GameEnded{DRAW_AGREE, all-draw}`.
- **Draw rejections:** `AcceptDraw(1)` with no offer → `ErrNoDrawToAccept`; `AcceptDraw(0)` of seat 0's own offer → error; mutual offer → agreement.
- **Offer withdrawn by move:** `OfferDraw(0)` then `SubmitMove(0,…)` → offer cleared; a later `AcceptDraw(1)` → `ErrNoDrawToAccept`.
- **Persist-before-ack:** a store that fails the ending append → the command errors and the session is NOT ended (assert `StateSnapshot` still live).
- **Idempotency / -race:** concurrent `Resign` + `SubmitMove` on the actor is serialized; a double `Resign` is one ending. Run the suite under `-race`.
- **Recovery:** a stream ending in `Resigned`+`GameEnded` rebuilds as ended (no half-terminal special case).
- `make test` green; no new wire bytes so no codec/parity impact.

## 7. Acceptance criteria (Definition of Done)

- [ ] `Resign`/`Abort`/`OfferDraw`/`AcceptDraw` actor commands, each appending its
      §4A event(s) via `appendBatchAtHead`, ending the game atomically (NFR-7).
- [ ] Abort eligibility rule (`ply < AbortBeforePly`, config default 2) with
      `ErrAbortTooLate`; resign always-legal-while-live; draw offer/accept with
      offer-invalidation-on-move and the mutual-offer and self-accept rules.
- [ ] Idempotent against resend; `-race` clean; persist-before-ack honored.
- [ ] No chess logic added (NFR-5, reviewer greps); no wire-byte / event-type
      change (no codec or parity impact).
- [ ] Tests above green incl the edge-first rejection cases; `make test` green.
- [ ] Context-free independent review (state-machine + actor concurrency =
      load-bearing) before merge.

## 10. Open questions

- **Q-EG1 — abort eligibility window.** When is a quit an *abort* vs a
  *resign*? **Proposed:** abort allowed only while `ply < AbortBeforePly` (default
  2 — before both players have moved once), else `ErrAbortTooLate`; the caller/UI
  decides whether to offer "resign instead", we do NOT silently convert. This
  matches Lichess/Chess.com (abort is an early, no-result exit; later it is
  resign). Alternative: unilateral abort any time before move 1 only (`ply < 1`)
  — stricter, but ply<2 is the common "first moves not yet exchanged" bar.
- **Q-EG2 — draw offer state: ephemeral vs folded.** A pending draw offer is
  ephemeral session state (`drawOfferBy`), cleared on the offerer's move, and NOT
  reconstructed from the stream on rebuild (a lapsed `DrawOffered` is treated as
  withdrawn after a reconnect). **Proposed: ephemeral**, because resurrecting a
  stale offer across a reconnect is surprising and would force the fold to track
  offer/withdraw pairing. The `DrawOffered` event is still appended (visible in
  history/analysis); only the *live pending* state is ephemeral. Alternative: fold
  the offer so a reconnect sees it still pending — rejected as more fold
  complexity for a worse UX.
- **Q-EG3 — AbortBeforePly config.** Default 2, tunable in `Config`. Trivial;
  flagged only so the default is a deliberate choice, not a magic literal.
- **Q-EG4 — resign result vector for N-seat (ADR-3).** v1 is 2-seat: resigner
  loses, opponent wins. N-seat variants (later) need a resign-result rule (does
  one seat resigning end the whole game, or just remove that seat?). **Proposed:**
  scope GS-3b to 2-seat (`seatCount == 2`); an N-seat resign rule is a variant
  concern (ADR-3 / F-07), out of scope here. The result-vector helper asserts
  `seatCount == 2` for now.
