# Data Model — relational entities (MASTER §VIII.2 ratified) — S3 Platform

| Field | Value |
|---|---|
| Spec ID | SPEC-S3-DATA-MODEL |
| Owner | S3 Platform (stream lead — fill in); co-reviewed by PE + S2 + Architecture group |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-10-12 (owner-approved) |
| Implements (master IDs) | X-3, X-4, X-13, X-14, ADR-8, ADR-10; MASTER §VIII.2, §VIII.6; NFR-7 |
| Depends on | ADR-10 (Postgres = relational truth; event store = game truth); SPEC-S2-PROTOCOL (event model it projects from); SPEC-PE-API-CONTRACT (the REST shapes these entities back) |
| Gates it feeds | **G-D** (entity model is part of the frozen v1 contract, with SPEC-S2-PROTOCOL + SPEC-PE-API-CONTRACT); feeds **G-C** (rating entities) |

## 1. Purpose & scope

Ratify the **relational entity model** (MASTER §VIII.2) as the authoritative schema that:
1. the REST contract's response bodies project (SPEC-PE-API-CONTRACT Appendix A cites these fields);
2. the event store's projections (`Game`, `Move`) fold into (SPEC-S2-PROTOCOL §3.2);
3. the rating service writes (X-4, ADR-1, G-C).

This spec is the **single definition of relational truth**. The REST and WS specs *cite* these fields; they do not redefine them (CONVENTIONS cross-stream contract). It closes the Q-A4/Q-A1 reconciliation open against S3.

**In scope:** entity list (MASTER §VIII.2 verbatim set, no additions), each field's type/nullability, primary + foreign keys, the enumerations, the public-vs-authed visibility that PE Appendix A relies on, the `RatingPool`/`TimeControl` tables that back `GET /v1/pools`, and the **projection boundary** (what is relational truth vs a fold of the event store).

**Non-goals (explicit):**
- **No game truth.** `Game`/`Move` here are **projections** of the event store (ADR-10); the authoritative game stream is SPEC-S2-PROTOCOL §3.2. This spec defines the projection *shape*, not a second source of truth.
- **No DDL dialect / migration tooling.** Column types are given logically (e.g. `uuid`, `text`, `int`, `timestamptz`); the physical Postgres DDL, indexes beyond keys, and migration framework are an implementation concern of `services/*`.
- **No auth/credential internals.** `Credential.secret_ref` is a vault reference; token/password/WebAuthn handling is X-3 `ACCOUNTS.md`.
- **No rating algorithm.** `Rating` columns are defined here; the Glicko-2 computation is X-4 `RATING-SERVICE.md` (ADR-1, G-C).
- **No analytics/columnar schema.** The warehouse (ADR-10) is fed from the event stream, never this relational store; out of scope.

## 2. Requirements traced

| Master ID | Requirement | Where |
|---|---|---|
| MASTER §VIII.2 | Core relational entities | §3 (all tables) |
| ADR-10 | Postgres = relational truth; event store = game truth; relational `Game`/`Move` are projections | §3.4 projection boundary |
| NFR-7 | Rating/result immutable once written; corrections are compensating events | §3.3 (Rating/Game immutability), §5 |
| ADR-8 | Entitlement hook | §3.5 (`Entitlement`) |
| X-3 | Accounts/profile/credential entities | §3.1 |
| X-4 | Rating entities (G-C) | §3.3 |
| X-13 | Entity fields the REST contract projects | §4 (reconciliation with PE Appendix A) |
| SPEC-PE-API-CONTRACT Q-A4 | `time_control` id → struct expansion lives in the pool catalog | §3.2 (`TimeControl`/`RatingPool`) |

## 3. Design — entities

Types: `uuid`, `text`, `int`, `bigint`, `float`, `bool`, `timestamptz` (epoch), `enum(...)`, `T[]` array, `jsonb`. `PK` primary key, `FK→` foreign key, `?` nullable, `U` unique. Visibility tags (`public`/`authed-self`/`internal`) are what SPEC-PE-API-CONTRACT Appendix A projects.

### 3.1 Accounts & identity (X-3)
```
User
  id            uuid PK
  handle        text U            (public)   -- login + display handle. UNIQUENESS SCOPE (X-3 §11.2 amendment, 2026-10-23): unique over all live rows (account_state ∈ {active, restricted}) PLUS any row with handle_reserved=true. See the handle_reserved column + §5 note.
  email_enc     text             (internal)  -- encrypted; never in any API body
  email_hash    text U            (internal)  -- for lookup/dedupe, breach-check (T-5)
  country       text?            (public)
  locale        text              (authed-self)  default from Accept-Language (ADR-7)
  kid_account   bool             (internal)  -- COPPA gating (F-41, ADR-6)
  account_state enum(active | restricted | tombstoned)  default active  (internal)
                   -- X-3 §11.2 amendment (2026-10-23). Lowercase canonical casing, used on the
                   --   server Session record + every comparison. restricted: comms-muted + social/rated
                   --   disabled pending verifiable parental consent (ADR-6) or other hold. tombstoned:
                   --   erased-person record (GDPR); row + FKs kept, PII anonymised in PublicUser projection.
  handle_provisional bool  default false  (internal)
                   -- X-3 §11.2 amendment. true when the handle was server-generated on an OAuth/magic-link
                   --   sign-up; the user is prompted to claim a permanent handle.
  handle_reserved    bool  default false  (internal)
                   -- X-3 §11.2 amendment. Discriminates two tombstone sub-kinds for handle reuse:
                   --   true = GDPR-erased/established-account tombstone (handle stays reserved); false =
                   --   purged-child-with-games tombstone (handle released). account_state=tombstoned alone
                   --   never decides reuse — handle_reserved does.
  flags         jsonb             (internal)  -- moderation/state flags
  created_at    timestamptz       (public)

Credential
  user_id       uuid FK→User PK(part)
  type          enum(oauth|password|webauthn) PK(part)
  provider      text?            -- oauth provider
  secret_ref    text              (internal)  -- vault reference, NOT the secret (steering: no secrets in store)

Profile
  user_id       uuid FK→User PK
  display_name  text              (public)
  avatar_ref    text?            (public)
  bio           text?            (public, subject to privacy)
  title         enum(GM|IM|FM|WGM|WIM|WFM|NM|WNM|CM|WCM)?  (public)  -- F-08.5 title filter set
  privacy       enum(public|friends|private)  (authed-self)

# --- X-3 §11.2 amendment (2026-10-23): ephemeral identity-surface entities ---
# These are EPHEMERAL (hot store w/ TTL, e.g. Redis), NOT relational truth, NOT Postgres rows.
# Listed here for completeness of the identity surface. A guest is distinct from User BY DESIGN
# (ACC-INV-1: guests are unrated-only) — no handle, no Profile, no Credential, no Entitlement,
# no Rating, no GDPR tombstone.

GuestSession   (EPHEMERAL — hot store w/ TTL; distinct from User)
  id                        text PK        -- opaque guest subject id (becomes Session.user_id for a guest)
  provisional_display_name  text           -- supplied or server-generated; NOT a unique handle
  created_at                timestamptz
  expires_at                timestamptz    -- short TTL

MagicLinkIssue (EPHEMERAL — hot store w/ TTL)   -- passwordless single-use issuance
  token_hash   text PK     -- hash of the single-use token (raw token never stored)
  email_hash   text        -- hash of the REQUESTED email (recorded even on the unknown-email 202 path, for the per-target throttle); same hashing as User.email_hash, no User FK
  expires_at   timestamptz
  consumed     bool        -- atomic compare-and-set on verify (replay guard)

UpgradeClaim   (EPHEMERAL — hot store w/ TTL)   -- guest→account pending intent across the email round-trip
  token_hash           text PK   -- hash of the single-use claim token; raw token NEVER stored or logged
  guest_session_id     text      -- originating GuestSession.id, captured at the authenticated initial /upgrade call
  handle               text      -- requested handle (validated + collision-checked at the initial call)
  email_hash           text      -- hash of the requested email
  cred_ref             text      -- password: vault secret_ref. oauth: verified provider identity (exchanged at step 1, NOT the code). passwordless: null. NEVER a raw secret.
  cred_kind            text      -- password | oauth | passwordless
  kid_account_outcome  bool      -- age outcome RESOLVED at the initial call from the guest's flow_id
  claim_kind           text      -- create (email was free) | login (email already has an account)
  expires_at           timestamptz
  consumed             bool      -- atomic compare-and-set on confirm (single-use; replay → 401)
  -- NO raw password/secret stored (only cred_ref → vault); NO FK to a not-yet-created User.
```

### 3.2 Rating pools & time controls (X-4; backs `GET /v1/pools`, Q-A4)
```
RatingPool                                   -- one per (format × variant), MASTER §VIII.2
  id            text PK            (public)   -- stable key, e.g. "blitz-standard"
  format        enum(bullet|blitz|rapid|daily)  (public)
  variant       enum(standard|chess960|crazyhouse|…)  (public)  -- v1 live: standard

TimeControl                                  -- the id→struct map; the ONLY expansion point (Q-A4)
  id            text PK            (public)   -- stable key, e.g. "blitz-5+3", "daily-3", "custom-300+2"
  kind          enum(bullet|blitz|rapid|daily|custom)  (public)
  base_ms       int               (public)
  increment_ms  int               (public)   -- 0 for no increment
  per_move_ms   int?             (public)    -- daily/correspondence only
  pool_id       text FK→RatingPool            -- which pool this TC belongs to (a pool has many TCs)
```
`GET /v1/pools` projects `RatingPool` + its `TimeControl[]`. The WS/event `time_control` field carries only `TimeControl.id` (Q-A4). Custom TCs use the canonical id form and need no stored row.

### 3.3 Ratings (X-4, G-C — immutable history via events)
```
Rating                                       -- current snapshot (projection of RatingApplied events)
  user_id       uuid FK→User PK(part)
  pool_id       text FK→RatingPool PK(part)
  rating        int               (public)
  rd            int               (public)   -- Glicko-2 rating deviation
  volatility    float             (authed-self)
  games_played  int               (public)
  provisional   bool              (public)   -- true while rd high / few games
  updated_at    timestamptz       (public)
```
The **authoritative rating history** is the `RatingApplied` event stream (SPEC-S2-PROTOCOL §3.2), not this row — this is the current fold. Corrections are compensating `RatingApplied` events, never in-place edits (NFR-7, ADR-1 deterministic/auditable → G-C).

### 3.4 Games & moves (PROJECTIONS of the event store — ADR-10, not truth)
```
Game                                         -- fold of game/{id} event stream; NOT the source of truth
  id            uuid PK            (public)
  pool_id       text FK→RatingPool  (public)
  rated         bool              (public)
  status        enum(live|ended|aborted)  (public)
  start_fen     text              (public)
  result_vector int[]?           (public)    -- per seat_idx; null until GameEnded
  time_control  text FK→TimeControl  (public)  -- the id (Q-A4)
  source        enum(online|bot|friend|tournament)  (public)
  termination   enum(mate|resign|flag|abort|draw|stalemate|repetition|50move|insufficient|timeout_vs_insufficient)?  (public)
  started_at    timestamptz       (public)
  ended_at      timestamptz?     (public)

GameSeat                                     -- N-seat (ADR-3); v1 = 2 seats
  game_id       uuid FK→Game PK(part)
  seat_idx      int PK(part)                 (public)  -- 0-based
  user_id       uuid? FK→User     (public)
  bot_id        text?            (public)
  color         enum(white|black)  (public)   -- v1; N-seat variants extend
  clock_initial int               (public)   -- ms
  clock_remaining int             (public)   -- ms, last known (projection)

Move                                         -- projection of MovePlayed events
  game_id       uuid FK→Game PK(part)
  ply           int PK(part)                 (public)
  seat_idx      int               (public)
  uci           text              (public)
  san           text              (public)
  clock_after_ms int              (public)
  server_ts     timestamptz       (public)
```
**Rebuild rule:** `Game`/`GameSeat`/`Move` are derivable *entirely* by folding the event stream; if they disagree with the stream, the stream wins and the projection is rebuilt. No write path mutates them except the projector consuming events (ADR-10).

### 3.5 Puzzles, social, content, entitlements
```
Puzzle          id uuid PK, fen text, solution_uci text[] (internal until solved), themes text[], rating int, source_game_id uuid? FK→Game   -- (public: fen,themes,rating)
PuzzleAttempt   user_id FK→User PK(part), puzzle_id FK→Puzzle PK(part), ts timestamptz PK(part), solved bool, ms int, rating_delta int
Challenge       id uuid PK, from_user uuid FK→User, to_user uuid? FK→User, pool_id FK→RatingPool, time_control FK→TimeControl, color enum(white|black|random), rated bool, status enum(pending|accepted|declined|expired), created_at timestamptz   -- (public to the two parties)
Friendship      user_a FK→User PK(part), user_b FK→User PK(part), status enum(pending|accepted|blocked), since timestamptz
Club            id uuid PK, slug text U, name text, created_at timestamptz
Membership      club_id FK→Club PK(part), user_id FK→User PK(part), role enum(member|admin|owner), joined_at timestamptz
Tournament      id uuid PK, kind enum(arena|swiss|daily), pool_id FK→RatingPool, status enum(scheduled|live|finished), starts_at timestamptz, …   -- F-06 detail deferred to F-06 spec
TournamentEntry tournament_id FK→Tournament PK(part), user_id FK→User PK(part), score float, joined_at timestamptz
ContentItem     id uuid PK, kind enum(lesson|course|article|news|blog), slug text, locale text, body_ref text, status enum(draft|published), PK(slug,locale)   -- (public when published)
Entitlement     user_id FK→User PK(part), capability text PK(part), plan text, granted_at timestamptz, expires_at timestamptz?   -- ADR-8; server-verified (T-11)
```
`Tournament`/`Club` carry only the fields the entity model needs now; their full feature schemas are added by the F-06/F-23 specs as `/v1` additive changes (not in the F-01 slice).

## 4. Interfaces & contracts — reconciliation with PE Appendix A

This section is the **field-by-field reconciliation** the quality bar requires: every value object in SPEC-PE-API-CONTRACT Appendix A maps to entities here, and the public/authed split matches.

| PE Appendix A object | Backed by | Visibility reconciliation |
|---|---|---|
| `PublicUser {id,handle,country?,title?,created_at}` | `User` + `Profile.title` | all `public`; email never projected (`email_enc`/`email_hash` are `internal`) ✓ |
| `Profile {…,privacy}` | `Profile` | `bio` honored per `privacy`; `privacy` itself `authed-self` ✓ |
| `GET /v1/me` adds `email` | `User.email_enc` (decrypted) | only on the `authed-self` path ✓ |
| `RatingSummary` | `Rating` | `volatility` is `authed-self` in the entity; PE `RatingSummary` exposes it only on self/stats — **tightened** (see §4.1) |
| `Pool`+`TimeControl` | `RatingPool`+`TimeControl` | all `public`; the `/v1/pools` expansion point (Q-A4) ✓ |
| `GameSummary`,`GameSeatPub`,`MoveProjection` | `Game`/`GameSeat`/`Move` projections | all `public`; `clock_remaining` exposed as live clock ✓ |
| `Challenge` | `Challenge` | public to the two parties only ✓ |
| `Entitlement` | `Entitlement` | server-verified; never client-trusted (T-11) ✓ |
| `Puzzle` (solution withheld) | `Puzzle` (`solution_uci` `internal` until solved) | ✓ — matches "solution withheld until solved/revealed" |

### 4.1 One reconciliation fix found (applied)
PE Appendix A originally listed `RatingSummary.volatility` without a visibility note, but the entity marks `volatility` **`authed-self`** (it is a signal a sandbagger could exploit — T-4). **Resolution (applied 2026-10-12):** `volatility` is returned only on the self-scoped stats path, omitted from another user's public `GET /v1/users/{handle}/stats`. Already mirrored into SPEC-PE-API-CONTRACT Appendix A (`volatility?` + note) and its stats endpoint — the two specs agree now, not "at RATIFIED".

## 5. Edge cases & failure modes

| Case | Model behavior |
|---|---|
| Projection vs event-store disagreement | Event stream wins; projection rebuilt from the fold (ADR-10). Projections are never authoritative. |
| Rating correction | New compensating `RatingApplied` event → `Rating` row re-folded; no in-place edit (NFR-7). |
| Deleted/anonymized user (GDPR, G-F) | `User` tombstoned; `PublicUser` projections show an anonymized handle; game history (truth) retained in the event store per retention policy. |
| Handle reuse after deletion | `handle` uniqueness scope = all live rows PLUS any row with `handle_reserved=true` (X-3 §11.2 amendment, 2026-10-23). A GDPR-erased/established-account tombstone keeps `handle_reserved=true` (handle stays reserved); a purged never-consented child releases its handle (`handle_reserved=false`, or the row is physically removed when it played no games). `account_state=tombstoned` alone never decides reuse. |
| Custom time control | No `TimeControl` row; id is canonically encoded (`custom-<base>+<inc>`) and parsed, not looked up (Q-A4). |
| N-seat variant (ADR-3) | `GameSeat` already keyed by `(game_id, seat_idx)`; `color` enum extends for >2-seat variants (future, not v1). |

## 6. Non-functional handling
- **Integrity (NFR-7).** Game/rating truth lives in the append-only event store; relational rows are rebuildable projections, so a relational corruption is recoverable by re-fold. Immutability enforced by "corrections are events" (§5).
- **Read-path (§IX.1).** Public projections (`PublicUser`, leaderboards, `RatingPool`) are the cacheable reads PE §6 relies on; keys chosen so the PE cursor-paginated lists index cleanly.

## 7. Security & abuse
- **PII (T-12, NFR-6).** `email_enc`/`email_hash`/`secret_ref` are `internal` and never appear in any API projection; audit logs on PII access (Part XI T-12). No secret stored in a column — `secret_ref` points to the vault (steering).
- **Rating manipulation (T-4).** `volatility` withheld from other users (§4.1); provisional handling via `Rating.provisional`.
- **Entitlement forgery (T-11).** `Entitlement` is the server's record; no API trusts a client plan claim.
- **Child safety (T-9, F-41).** `kid_account` gates restricted-comms model surfaces (enforced in service logic; the flag lives here).

## 8. Test plan
- **Projection round-trip.** Fold a known event stream → assert `Game`/`GameSeat`/`Move`/`Rating` rows match; mutate the stream → re-fold → rows update; never a divergent authoritative write.
- **Visibility conformance.** For every entity, assert the `internal` fields (`email_*`, `secret_ref`, `solution_uci` pre-solve, `volatility` to others) never appear in the corresponding PE Appendix A projection. Run against the PE contract tests (shared, NFR-10).
- **Referential integrity.** FK constraints hold; `RatingPool`/`TimeControl` ids referenced by `Game`/`Challenge` exist.
- **Immutability.** No code path updates a `RatingApplied`-derived value except via a new event; attempt → rejected in test.
- **Enum completeness.** `termination`, `title`, `format`, `variant` enums cover the master-plan sets (F-08.5 titles, F-01.1 formats, F-07 variants).

## 9. Acceptance criteria (Definition of Done)
Part of the frozen v1 contract (with SPEC-S2-PROTOCOL + SPEC-PE-API-CONTRACT) = **G-D**; feeds **G-C**. DONE when:
- [ ] Every MASTER §VIII.2 entity present, no invented entity, no master entity redefined.
- [ ] Every field typed, keyed, and visibility-tagged.
- [x] §4 reconciliation with PE Appendix A complete; the §4.1 `volatility` tightening mirrored into PE Appendix A (applied).
- [ ] Projection boundary (§3.4) agreed with S2 — relational `Game`/`Move` are folds, event store is truth.
- [ ] `time_control` id model (Q-A4) consistent across S3/S2/PE.
- [ ] Enum sets reconciled with the master plan (titles, formats, variants, terminations).
- [ ] Reviewed by S3 + PE + S2 + architecture group; threat-model pass on PII (Part XI).
- [ ] RATIFIED and frozen with the other two specs as the G-D baseline.

## 10. Decisions & open items
- **D-DM1 — relational `Game`/`Move` are projections, not truth — SETTLED by ADR-10.** Recorded explicitly (§3.4) because it is the most load-bearing and most mis-implementable rule here.
- **Q-DM1 — freed-handle reuse policy (defer to X-3 `ACCOUNTS.md`).** Whether a deleted user's handle can be reclaimed; affects uniqueness. Not a data-model shape question; flagged, not decided here.
- **Q-DM2 — `flags`/`Profile` moderation fields (defer to S6).** The moderation flag taxonomy in `User.flags` is owned by X-11/S6; this spec reserves the `jsonb` column, S6 defines its contents.
- **Cross-spec action — DONE.** §4.1 `volatility` visibility mirrored into PE Appendix A + its stats endpoint (2026-10-12); the two specs no longer diverge.

---

### Reviewer note (not part of the frozen contract)
This spec is the relational half of the three-way v1 contract (WS = SPEC-S2-PROTOCOL, REST = SPEC-PE-API-CONTRACT, entities = here). It deliberately defines `Game`/`Move` only as projection shapes so there is exactly one source of game truth (the event store, ADR-10) — restating them as authoritative tables would be the single worst correctness bug available in this layer.
