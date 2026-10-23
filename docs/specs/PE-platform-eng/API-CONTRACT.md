# API Contract — v1 REST/JSON surface + shared API conventions — PE Platform Engineering

| Field | Value |
|---|---|
| Spec ID | SPEC-PE-API-CONTRACT |
| Owner | PE Platform Engineering (stream lead — fill in); co-reviewed by S3 Platform + S2 Real-time + Architecture group |
| Status | RATIFIED |
| Version | 1.0 |
| Last updated | 2026-10-12 (owner-approved) |
| Implements (master IDs) | X-13, X-14; MASTER §VIII.5, §IX.1 (read-path latency), Part XI (T-5/T-6/T-10/T-11); NFR-6, NFR-10 |
| Depends on | SPEC-S1-ENGINE-CORE (G-A, GREEN); SPEC-S2-PROTOCOL (the realtime half — cited, not redefined); SPEC-S3-DATA-MODEL (entity truth — to be written) |
| Gates it feeds | **G-D** (API/protocol contract frozen v1). Per owner decision Q-P1=(b), G-D is green only when **this spec AND `../S2-realtime/PROTOCOL.md` are frozen together** — this is the REST/non-realtime half of the v1 surface. |

## 1. Purpose & scope

Freeze the **non-realtime, client-facing API** — the REST/JSON surface (MASTER §VIII.5) and the cross-cutting conventions every endpoint obeys — as the second half of the v1 contract. The realtime WebSocket + event-store + entity contract is `../S2-realtime/PROTOCOL.md` (SPEC-S2-PROTOCOL); together the two **are** G-D (owner decision Q-P1=(b)).

This exists because clients (web/iOS/Android) bind one surface, and a half-frozen surface drifts. PE owns X-13 (specs README; PE README names this spec as the first P0 artifact).

**In scope:**
1. **Shared API conventions** — the rules *every* REST endpoint follows: versioning, auth, pagination, caching/ETag, idempotency, error format, rate limits, content negotiation, deprecation. These are the contract's "constitution"; individual endpoints cite them.
2. **The v1 REST endpoint catalog** — every non-realtime endpoint a v1 client calls, with method, path, auth requirement, request/response schema shape, and the master `F-xx`/`X-x` it serves. Grounded in MASTER §VIII.5, expanded to a complete v1 catalog (§VIII.5 is "representative, not exhaustive"; freezing requires exhaustive).
3. **The REST↔realtime boundary** — exactly which operations are REST and which are WS frames (SPEC-S2-PROTOCOL), so there is no operation defined in both or neither.

**Non-goals (explicit):**
- **The realtime surface.** WS frames, the event-store schema, the move hot path — owned by SPEC-S2-PROTOCOL. This spec **cites** it for the boundary and never restates a frame. (The PE README's "REST+WS" wording is split per Q-P1: WS → S2, REST → here. See §4.1.)
- **Service implementation.** How `services/accounts`, `services/rating`, etc. fulfill an endpoint is each service's spec (ADR-12 Go). This spec is the *contract*, not the handlers.
- **The relational schema (DDL).** Entity truth is S3 `DATA-MODEL.md`; response bodies **cite** entity fields, never redefine the schema.
- **Auth token internals.** Token format/issuance/rotation is X-3's spec; this contract defines only how a request *presents* a credential and what an unauthenticated/forbidden response looks like.
- **gRPC internal RPC.** Service-to-service gRPC (ADR-10/12) is internal, not client-facing; out of the v1 client contract.
- **Payment processor integration.** PCI handling is via the provider (T-11); this spec defines only the entitlement-read endpoint shape (server-verified, ADR-8).

## 2. Requirements traced

| Master ID | Requirement | Where handled |
|---|---|---|
| X-13 | Shared API contract (client-facing) | whole spec; §9 freeze = G-D (with S2) |
| X-14 | API surface as a platform concern (versioning, deprecation, observability hooks) | §3.1 conventions |
| MASTER §VIII.5 | REST/JSON surface + stated conventions | §3.2 catalog, §3.1 conventions |
| NFR-6 | OWASP ASVS L2 baseline | §7 (auth, rate limits, input validation) |
| NFR-10 | One API, platform parity; shared contract test suite on 3 clients | §8 (contract tests run on web/iOS/Android) |
| §IX.1 | Read-path latency (p99 200 ms / 500 ms ceiling) | §6 (caching/pagination keep reads inside budget) |
| T-5 Account takeover | §7 (session binding, auth responses) |
| T-6 Scraping / exfiltration | §7 (auth + rate limits on bulk/list + PGN endpoints) |
| T-10 API abuse / credential stuffing | §7 (per-IP + per-user limits, auth bot-challenge hook) |
| T-11 Entitlement forgery | §7 (server-verified entitlements; no client-trusted plan state) |

## 3. Design

### 3.1 Shared API conventions (the contract's constitution)

Every REST endpoint obeys these; endpoints in §3.2 cite them rather than restate them. These are drawn from MASTER §VIII.5's stated conventions and frozen as rules.

| Convention | Rule (v1) |
|---|---|
| **Versioning** | Path-prefixed `/v1`. A breaking change is `/v2`, never an in-place edit of `/v1` (mirrors the WS `ver` byte, SPEC-S2-PROTOCOL §5). Additive, backward-compatible fields may be added within `/v1`. |
| **Transport** | HTTPS only (TLS ≥ 1.2). JSON request/response (`application/json; charset=utf-8`). |
| **Auth** | Bearer credential in `Authorization` header (token minted by X-3, opaque here). Unauthenticated → `401`; authenticated-but-forbidden → `403`. Public endpoints (§3.2 "auth: none") are explicitly marked and rate-limited harder (T-6). |
| **Errors** | **RFC-7807 `application/problem+json`** (`type`, `title`, `status`, `detail`, `instance`), as MASTER §VIII.5 mandates. A frozen `type` URI registry (§4.3) enumerates every error kind. Never leak internals in `detail`. |
| **Pagination** | **Cursor-based** (opaque `cursor` + `limit`), as §VIII.5. Responses carry `next_cursor` (null at end). No offset pagination (unstable under writes). |
| **Caching** | `ETag` + conditional `GET` (`If-None-Match` → `304`) on read-heavy lists (§VIII.5): user profiles, leaderboards, content, game lists. `Cache-Control` per-endpoint. |
| **Idempotency** | Writes (POST/PUT) that create or mutate accept an `Idempotency-Key` header; a repeat with the same key returns the first result, never a double effect (§VIII.5). Required on payment/entitlement and challenge-creation writes. |
| **Rate limits** | Per-user **and** per-IP (Part XI T-6/T-10). `429` with `Retry-After`; limit headers (`RateLimit-Limit`/`-Remaining`/`-Reset`). Bulk/public endpoints get stricter buckets (§7). |
| **Content negotiation** | `Accept` honored for `application/json` (default) and, where offered, `application/x-chess-pgn` for PGN download (F-08.4). |
| **i18n** | `Accept-Language` (NFR-11/ADR-7) selects localized content (`/v1/content/...?locale=` overrides); no server-rendered user-facing strings baked in. |
| **Deprecation** | A sunset endpoint carries `Deprecation` + `Sunset` headers before removal; removal only across a version bump. |
| **Observability (X-14)** | Every request carries/propagates a trace id (`traceparent`); endpoints emit the latency/error metrics NFR-9 needs. (Hook defined here; wiring is OBSERVABILITY.md.) |

### 3.2 v1 REST endpoint catalog

Grounded in MASTER §VIII.5 and completed to an exhaustive v1 set. Each row: method, path, auth, the master ID it serves, and the entity/shape it returns (entity fields cited from S3 §VIII.2, never redefined). Request/response **field-level schemas** are pinned in the §4.2 schema appendix before RATIFIED (quality bar — no discovery during implementation).

**Auth & accounts (X-3)**
| Method | Path | Auth | Serves | Returns |
|---|---|---|---|---|
| POST | `/v1/auth/register` | none | X-3 | session + `User` (public fields) |
| POST | `/v1/auth/login` | none | X-3 | session |
| POST | `/v1/auth/oauth/{provider}` | none | X-3 | session |
| POST | `/v1/auth/logout` | user | X-3 | `204` |
| GET | `/v1/me` | user | X-3/F-46 | `User`+`Profile` (self) |
| GET | `/v1/me/entitlements` | user | ADR-8 | `Entitlement[]` (server-verified, T-11) |
| POST | `/v1/auth/age-screen` | none | X-3 (age gate) | `{flow_id}` (pending-age record; DOB discarded) |
| POST | `/v1/auth/guest` | none | X-3 (guest play) | session (guest subject) |
| POST | `/v1/auth/oauth/start` | none | X-3 (OAuth w/ link-aware outcome) | session OR `{status:"link_required"}` |
| POST | `/v1/auth/magic-link` | none | X-3 (passwordless) | `202` (link/code sent out of band) |
| POST | `/v1/auth/magic-link/verify` | none | X-3 (passwordless) | session |
| POST | `/v1/auth/upgrade` | guest | X-3 (guest→account; verify-first) | `202 verification_sent` |
| POST | `/v1/auth/upgrade/confirm` | none (link token) | X-3 (consume claim link → account session) | session |
| POST | `/v1/auth/oauth/link` | user | X-3 (confirm OAuth link to existing acct) | `204` |
| GET | `/v1/me/sessions` | user | X-3 (device mgmt) | active-session list |
| DELETE | `/v1/me/sessions/{id}` | user | X-3 (device mgmt) | `204` |
| POST | `/v1/me/sessions/logout-all` | user | X-3 (device mgmt) | `204` |

> **X-3 §11.1 amendment (applied 2026-10-23; SPEC-S3-ACCOUNTS RATIFIED, PR #73).** The rows above are additive. The frozen endpoints (`register`, `login`, `oauth/{provider}`, `logout`, `GET /v1/me`, `/v1/me/entitlements`) keep their exact request/response **shapes**. Two declared **error-only** behavioral amendments apply to frozen endpoints, using the existing Appendix-B `forbidden` 403 (no new type, no shape change): (1) a **guest** subject on `GET /v1/me` and `/v1/me/sessions*` → `403` (`GET /v1/me/entitlements` for a guest stays `200`, the anonymous-unrated baseline); (2) a `register` call with **no resolvable pending-age record → `403 age_screen_required`** (the `flow_id` rides a short-TTL HttpOnly cookie set by `/age-screen`, never a new `register` body field).

**Users, profiles, stats (F-05, F-46)**
| GET | `/v1/users/{handle}` | optional | F-46 | public `Profile` |
| GET | `/v1/users/{handle}/stats` | optional | F-05 | per-pool `Rating` summary |

**Games & history (F-08, F-17)**
| GET | `/v1/pools` | optional | F-01.1/.2/.3 | list of `RatingPool` + each pool's allowed time controls (seek-screen source) |
| GET | `/v1/games?filters…` | optional | F-08 | cursor page of `Game` projections |
| GET | `/v1/games/{id}` | optional | F-08 | `Game` + moves projection |
| GET | `/v1/games/{id}.pgn` | optional | F-08.4 | PGN (`application/x-chess-pgn`) |
| POST | `/v1/games/{id}/analysis` | user | F-17 | analysis job/handle |

**Puzzles (Section B)**
| GET | `/v1/puzzles/next?themes=&rating=` | user | F-09 | `Puzzle` |
| POST | `/v1/puzzles/{id}/attempt` | user | F-09/F-13 | `PuzzleAttempt` result |

**Ratings & leaderboards (F-25–27)**
| GET | `/v1/leaderboards/{pool}` | optional | F-25/26/27 | cursor page (cacheable) |
| GET | `/v1/ratings/{pool}` | optional | F-25 | pool rating distribution |

**Social, tournaments, clubs, challenges (F-06/F-22/F-23/F-04)**
| GET/POST | `/v1/tournaments…` | mixed | F-06 | `Tournament`/entry |
| GET/POST | `/v1/clubs…` | mixed | F-23 | `Club`/`Membership` |
| GET/POST | `/v1/friends…` | user | F-22 | `Friendship` |
| GET | `/v1/challenges` | user | F-04.1 | incoming + outgoing `Challenge[]` |
| POST | `/v1/challenges` | user | F-04 | `Challenge` (idempotency-key required) |
| POST | `/v1/challenges/{id}/accept` | user | F-04 | `Challenge` → transitions to live via WS `MATCHED` |
| POST | `/v1/challenges/{id}/decline` | user | F-04 | `204` |

**Content (X-8)**
| GET | `/v1/content/{kind}/{slug}?locale=` | optional | X-8 | `ContentItem` (ETag, i18n) |

> **Catalog completeness note (Q-A1) — reconciled against F-01, 2026-10-12.** §VIII.5 is explicitly "representative, not exhaustive." Walking the F-01 play-loop client (seek → live game → post-game) plus F-04/F-05/F-08/F-46 against the catalog surfaced four REST reads/writes §VIII.5 omitted, now added above: **`GET /v1/pools`** (the seek screen needs the valid pool + time-control set before it can send a WS `SEEK` — the single most load-bearing gap), and the challenge lifecycle **`GET /v1/challenges` / `POST …/accept` / `…/decline`** (F-04 friend challenges; `SEEK`/live play is WS). All live-game mutations (move/draw/resign/abort/rematch/takeback/reconnect) are WS (SPEC-S2-PROTOCOL), confirmed not REST. The catalog is now complete for the **F-01 web vertical slice**; a later client (mobile parity, tournaments UI) may add rows as `/v1` additive changes (§3.1). See Q-A1 in §10.

### 3.3 REST ↔ realtime boundary (no overlap, no gap)

The single rule: **anything that mutates live game truth is a WS intent (SPEC-S2-PROTOCOL); everything non-realtime is REST here.**

| Operation | Surface | Why |
|---|---|---|
| Make a move, offer/accept draw, resign, abort, takeback, rematch, seek a game | **WS** (SPEC-S2-PROTOCOL §3.1) | live game truth; hot path; event-sourced |
| Reconnect/resync a live game | **WS** (`STATE`) | live state fold |
| Create a *challenge* (async invite), list/accept/decline a challenge | **REST** `/v1/challenges*` | not a live game yet; durable record |
| A challenge accept that opens a live game | **REST accept → WS `MATCHED`** | the accept is a durable REST write; the live game it spawns is announced over WS |
| Read game history / a finished game / PGN | **REST** | projection read, cacheable |
| Everything else (auth, profile, puzzles, leaderboards, content, entitlements) | **REST** | non-realtime |

No operation appears on both surfaces. A client acting on a live game uses WS; a client reading or doing account/social/content work uses REST.

## 4. Interfaces & contracts

- **4.1 This is a shared cross-stream contract** (CONVENTIONS). It spans PE (owner), S3 (entity shapes it cites), S2 (the boundary), S4/S5 (consumers). Change control: both affected streams + architecture group; post-freeze changes are a new version, never in-place (§3.1 versioning).
- **4.2 Field-level schema appendix.** Each endpoint's exact request/response JSON schema — including which `User`/`Profile`/`Game`/etc. fields are public vs authed — is enumerated in **Appendix A** below. Entity fields cite S3 `DATA-MODEL.md`; the pre-DDL field set here is the contract shape and is reconciled with S3 at RATIFIED.
- **4.3 Frozen error `type` registry.** The RFC-7807 `type` URIs for every error kind are enumerated in **Appendix B** below.
- **4.4 Relationship to S2.** `MATCHED`/live events are WS; this spec references them only to draw the §3.3 boundary. The two specs freeze together as G-D.

## 4A. Appendix A — field-level request/response schemas (frozen for v1)

Shapes are language-neutral JSON. `?` = optional/nullable; `(public)` fields appear to unauthenticated/other-user readers, `(self)`/`(authed)` only to the owner or an authed request. All responses are `application/json` unless noted. All error responses follow Appendix B (not repeated per endpoint). Entity field names cite S3 §VIII.2; types pinned here.

### A.1 Shared value objects
```
Session        { token: str, user_id: str, expires_at: int(epoch-s) }
PublicUser     { id: str, handle: str, country?: str, title?: str, created_at: int }        // (public)
Profile        { user_id: str, display_name: str, avatar_ref?: str, bio?: str,
                 title?: str, privacy: enum[public|friends|private] }                        // (public subset by privacy)
RatingSummary  { pool_id: str, rating: int, rd: int, volatility?: float,
                 games_played: int, provisional: bool }
                 // volatility present only on self (GET /v1/me stats); omitted for other users (T-4, SPEC-S3-DATA-MODEL §4.1)
Pool           { id: str, format: enum[bullet|blitz|rapid|daily], variant: enum[standard|chess960|…],
                 time_controls: TimeControl[] }
TimeControl    { id: str, kind: enum[bullet|blitz|rapid|daily|custom],
                 base_ms: int, increment_ms: int, per_move_ms?: int }                        // per_move_ms only for daily
GameSummary    { id: str, pool_id: str, rated: bool, status: enum[live|ended|aborted],
                 result_vector?: int[], termination?: enum, time_control: str,
                 seats: GameSeatPub[], started_at: int, ended_at?: int }
GameSeatPub    { seat_idx: int, color: enum[white|black], user?: PublicUser, bot_id?: str }
MoveProjection { ply: int, seat_idx: int, uci: str, san: str, clock_after_ms: int, server_ts: int }
Challenge      { id: str, from: PublicUser, to?: PublicUser, pool_id: str,
                 time_control: str, color: enum[white|black|random], rated: bool,
                 status: enum[pending|accepted|declined|expired], created_at: int }
Entitlement    { capability: str, plan: str, granted_at: int, expires_at?: int }
Puzzle         { id: str, fen: str, rating: int, themes: str[] }                              // solution withheld until solved/revealed
PuzzleResult   { puzzle_id: str, solved: bool, ms: int, rating_delta: int, solution_uci?: str[] }
Page<T>        { items: T[], next_cursor?: str }
```

### A.2 Per-endpoint schemas

**Auth & accounts (X-3)**
```
POST /v1/auth/register   req { handle: str, email: str, password?: str, locale?: str }   → 201 Session
POST /v1/auth/login      req { handle_or_email: str, password: str }                     → 200 Session | 401
POST /v1/auth/oauth/{provider}  req { code: str, redirect_uri: str }                     → 200 Session | 401
POST /v1/auth/logout     req {}                                                           → 204
GET  /v1/me              → 200 { user: PublicUser, profile: Profile, email: str(self) }  // 403 if guest subject (X-3 §11.1)
GET  /v1/me/entitlements → 200 { items: Entitlement[] }                                   // server-verified (T-11); guest → 200 anonymous-unrated baseline
POST /v1/auth/age-screen       req { dob: date }                                  → 200 { flow_id: str }
                                 // server computes outcome vs jurisdiction table, DISCARDS dob, writes a
                                 //   short-lived server-side pending-age record keyed to flow_id, bound to a
                                 //   single-use short-TTL HttpOnly cookie. No DOB persisted.
POST /v1/auth/guest            req { display_name?: str }                         → 201 Session   // guest subject; unrated-only
POST /v1/auth/oauth/start      req { provider: str, code: str, redirect_uri: str, flow_id?: str }
                                                            → 200 Session | 200 { status:"link_required" } | 401
                                 // NEW endpoint: link-aware entry. The frozen /oauth/{provider} stays pure
                                 //   200 Session | 401 and is UNCHANGED. Existing-email collision → link_required
                                 //   (no token); linking completes via POST /v1/auth/oauth/link.
POST /v1/auth/oauth/link       req { provider: str, code: str, redirect_uri: str }
                               (auth: a valid user session of the EXISTING account)         → 204 | 401 | 409
                                 // attaches Credential{type=oauth} to the caller's OWN account; creates NO new User.
POST /v1/auth/magic-link       req { email: str }                                 → 202 {}            // no enumeration (T-6)
POST /v1/auth/magic-link/verify req { token: str, flow_id?: str }                 → 200 Session | 401
POST /v1/auth/upgrade          req { handle: str, email: str, password?: str, flow_id?: str,
                                     oauth?: { provider: str, code: str, redirect_uri: str } }
                               (auth: guest session)              → 202 {status:"verification_sent"} | 403 | 409 | 400
                                 // verify-first: identical 202 for taken/free email (no enumeration), account
                                 //   minted only on /upgrade/confirm. oauth{} code is exchanged here (step 1) and
                                 //   only the verified provider identity is carried in the claim.
POST /v1/auth/upgrade/confirm  req { token: str, handle?: str }   (claim link token; handle only on a 409 re-pick)
                                                                     → 200 Session | 401 | 409 {handle_taken, repick_token}
GET  /v1/me/sessions           → 200 { items: SessionInfo[] } | 403               // 403 = guest caller
SessionInfo  { id: str, created_at: int, last_seen_at: int, current: bool, device_label?: str }  // never the token
DELETE /v1/me/sessions/{id}    → 204 | 403                               // 403 = guest caller
POST /v1/me/sessions/logout-all → 204
// New error outcomes reuse the EXISTING Appendix-B registry only (unauthenticated/forbidden/conflict/validation/rate-limited) — no new error type.
```

**Users, profiles, stats (F-05, F-46)**
```
GET /v1/users/{handle}         → 200 { user: PublicUser, profile: Profile }  | 404
GET /v1/users/{handle}/stats   ?window=7d|30d|90d|1y|all                       → 200 { pools: RatingSummary[] }  (ETag)   // volatility omitted unless {handle} is self
```

**Pools (F-01.1/.2/.3 — seek source)**
```
GET /v1/pools   → 200 { items: Pool[] }   (ETag, cacheable; the seek screen reads this before sending WS SEEK)
```

**Games & history (F-08, F-17)**
```
GET  /v1/games   ?user=&pool=&result=&rated=&color=&cursor=&limit=   → 200 Page<GameSummary>   (ETag)
GET  /v1/games/{id}            → 200 { game: GameSummary, moves: MoveProjection[] }  | 404
GET  /v1/games/{id}.pgn        Accept: application/x-chess-pgn        → 200 <pgn text> | 406 | 404
POST /v1/games/{id}/analysis   req { depth?: int }                   → 202 { job_id: str, status: enum[queued|running|done] }
```

**Puzzles (Section B)**
```
GET  /v1/puzzles/next   ?themes=&rating=   → 200 Puzzle
POST /v1/puzzles/{id}/attempt   req { moves_uci: str[], ms: int }  → 200 PuzzleResult
```

**Ratings & leaderboards (F-25–27)**
```
GET /v1/leaderboards/{pool}   ?cursor=&limit=   → 200 Page<{ rank: int, user: PublicUser, rating: int }>   (ETag)
GET /v1/ratings/{pool}        → 200 { pool_id: str, distribution: { bucket: int, count: int }[] }           (ETag)
```

**Social / challenges (F-04, F-22) — challenge create is WS-adjacent, see §3.3**
```
GET  /v1/friends        → 200 Page<{ user: PublicUser, since: int }>
GET  /v1/challenges     → 200 { incoming: Challenge[], outgoing: Challenge[] }
POST /v1/challenges     Idempotency-Key req   req { to_handle?: str, pool_id: str, time_control: str,
                                                    color: enum[white|black|random], rated: bool }
                        → 201 Challenge   | 409 (idempotency body mismatch)
POST /v1/challenges/{id}/accept    → 200 Challenge(status=accepted)   // live game then arrives via WS MATCHED
POST /v1/challenges/{id}/decline   → 204
```

**Content (X-8)**
```
GET /v1/content/{kind}/{slug}   ?locale=   Accept-Language honored   → 200 ContentItem { id, kind, slug, locale, body_ref, status }   (ETag, i18n fallback → default locale)
```

> Tournaments (`/v1/tournaments…`) and clubs (`/v1/clubs…`) are **not** in the F-01 vertical slice and are listed in §3.2 as catalog placeholders; their field-level schemas are added as `/v1` additive specs when F-06/F-23 are built (§3.1). Freezing the F-01 slice does not require them.

## 4B. Appendix B — RFC-7807 error `type` registry (frozen for v1)

All errors are `application/problem+json`: `{ type, title, status, detail?, instance? }`. `type` is a stable URI under `https://api.playglobalchess.com/problems/`. `detail` never leaks internals (NFR-6). The registry is closed for v1 (a new error kind is an additive `/v1` change).

| `type` (suffix) | HTTP | title | When |
|---|---|---|---|
| `unauthenticated` | 401 | Authentication required | Missing/expired/invalid credential |
| `forbidden` | 403 | Forbidden | Authed but not permitted for this resource |
| `forbidden-entitlement` | 403 | Entitlement required | ADR-8 capability not granted (server-verified, T-11) |
| `not-found` | 404 | Resource not found | Unknown resource (also used to avoid private-resource enumeration, T-6) |
| `validation` | 400 | Invalid request | Malformed/invalid body or query (never a 500) |
| `stale-cursor` | 400 | Invalid or expired cursor | Pagination cursor no longer valid |
| `unsupported-media` | 406 | Unsupported Accept | `Accept` the endpoint cannot satisfy (e.g. PGN not offered) |
| `conflict` | 409 | Conflict | Idempotency-Key reused with a different body; or a resource-state conflict |
| `rate-limited` | 429 | Too many requests | Per-user/per-IP limit exceeded; carries `Retry-After` + `RateLimit-*` headers (T-6/T-10) |
| `internal` | 500 | Internal error | Last-resort; opaque `detail`, correlation id in `instance` only |
| `unavailable` | 503 | Temporarily unavailable | Graceful shed of non-core surface under load (NFR-2/T-14) |

**Mapping rule:** every §5 failure row maps to exactly one registry entry; a response MUST carry a registered `type` (the §8 suite asserts this — no ad-hoc error shapes).

## 5. Edge cases & failure modes

| Case | Contract behavior |
|---|---|
| Expired/invalid credential | `401` problem+json, `type=.../unauthenticated`. |
| Authed but not entitled (ADR-8) | `403`, `type=.../forbidden-entitlement`; **server-verified**, never trusts a client plan claim (T-11). |
| Rate limit exceeded | `429` + `Retry-After` + limit headers (T-6/T-10). |
| Idempotency-key replay | Same key + same body → first response replayed; same key + *different* body → `409 conflict`. |
| Unknown resource | `404` problem+json; must not leak existence of private resources (return `404` not `403` where enumeration is a risk — T-6). |
| Conditional GET match | `304 Not Modified`, empty body (read-path latency, §6). |
| Pagination past end | `next_cursor: null`; a stale cursor → `400` not a silent wrong page. |
| Public endpoint scraping | Stricter per-IP bucket + auth-gated bulk; no un-throttled bulk export (T-6). |
| Unsupported `Accept` | `406`; PGN only where offered. |
| Partial/malformed JSON body | `400` problem+json with a validation `type`; never a `500`. |
| Localized content missing for `locale` | Defined fallback (requested → default locale), documented per endpoint, never an error (ADR-7). |

## 6. Non-functional handling

- **Read-path latency (§IX.1: p99 200 ms, 500 ms ceiling).** Cursor pagination (bounded page work) + `ETag`/`304` on read-heavy lists keep reads inside budget; heavy aggregates (leaderboards, stats) are cacheable with explicit `Cache-Control`. The contract enables the budget; the measured number is proven by the serving services under load, not here.
- **Parity (NFR-10).** One REST contract, identical on all three clients; proven by the §8 contract-conformance suite run on web/iOS/Android (the "shared protocol test suite passes on all three"). Same discipline as the engine FFI and the WS golden vectors.
- **Availability / degradation (NFR-2).** Non-core REST surfaces are the first to shed under load (T-14) — marked per-endpoint so the gateway knows what is sheddable vs play-path-critical.

## 7. Security & abuse

Part XI threats on the REST surface, each with its control here:
- **T-5 account takeover** — session binding; auth endpoints behind bot-challenge hook; anomalous-login handled by X-3 (contract exposes the `401`/`403` + challenge responses).
- **T-6 scraping / exfiltration** — auth on bulk endpoints, strict per-IP buckets on public reads, no un-throttled export; PGN/list endpoints rate-limited; `404`-not-`403` to avoid enumeration.
- **T-10 API abuse / credential stuffing** — per-IP + per-user limits with `429`; bot-challenge hook on `/v1/auth/*`.
- **T-11 entitlement forgery** — `/v1/me/entitlements` is **server-verified** (ADR-8); no endpoint trusts a client-supplied plan/capability; payment is provider-side (PCI out of scope).
- **NFR-6 ASVS L2** — input validation + parameterized access by default (steering); RFC-7807 errors never leak internals; HTTPS-only; least-privilege per endpoint.
- **Every new endpoint gets a threat-model pass** (standing practice, Part XI) before it joins the frozen catalog.

## 8. Test plan

The contract's conformance corpus is part of this spec (CONVENTIONS):
- **Schema conformance (contract).** For every endpoint, request/response validate against the frozen §4.2 schemas; run as a **shared suite on web/iOS/Android** (NFR-10) + the server — the three clients must agree on every shape.
- **Convention conformance.** Pagination (cursor round-trip, end sentinel, stale-cursor `400`), ETag/`304`, idempotency (replay + `409` on body mismatch), error envelope is valid problem+json with a registered `type`, `429` carries `Retry-After` + limit headers, `401`/`403`/`404` as specified.
- **Negative/abuse decode.** Malformed JSON → `400` not `500`; unauth → `401`; unentitled → `403`; unknown resource → `404` without leak; unsupported `Accept` → `406`. (External-input branches get dedicated tests, not only happy-path — mirrors the engine lesson.)
- **Boundary audit.** Automated check that no operation is defined in **both** this spec and SPEC-S2-PROTOCOL, and that every §3.3 live operation is WS-only.
- **Completeness audit (Q-A1).** Every REST call the v1 clients (F-01 loop + F-05/08/46) make resolves to a catalog row — no missing endpoint.

**Not tested here:** latency/throughput (G-E), service-internal logic (service specs).

## 9. Acceptance criteria (Definition of Done)

Frozen = the REST half of **G-D** (jointly with SPEC-S2-PROTOCOL). DONE when:
- [ ] §3.1 conventions are complete and unambiguous (every rule has a defined response).
- [ ] §3.2 catalog is **exhaustive for the F-01 slice** — reconciled 2026-10-12 (Q-A1 added pools + challenge lifecycle); final cross-check against S4 F-01 client code at RATIFIED.
- [x] §4A field-level request/response schemas enumerated in-spec (Appendix A) — no implementation-time discovery; reconciled with S3 `../S3-platform/DATA-MODEL.md` §4 (field-by-field; `volatility` visibility fix applied). Final sign-off at RATIFIED.
- [x] §4B RFC-7807 error `type` registry enumerated and frozen (Appendix B).
- [x] §3.3 REST↔WS boundary audit passes (no overlap, no gap) against SPEC-S2-PROTOCOL. — **automated** by `platform/ci/check_rest_contract.py` against `protocol/rest/openapi.yaml` (R1); green in CI (protocol job).
- [ ] Entity fields reconciled with S3 `DATA-MODEL.md` (public vs authed field sets agreed). — Appendix A reconciled in-spec; a machine cross-check vs S3 field sets is a later audit extension.
- [ ] §8 contract-conformance suite exists and passes on server + web + iOS + Android (NFR-10). — **R1 (static, done):** the frozen OpenAPI 3.1 artifact `protocol/rest/openapi.yaml` + the catalog/boundary/error-registry audits (`check_rest_contract.py`), gated in CI. **R2 (deferred to P1):** the request/response + convention + negative/abuse tests run against a real REST server + the three clients — gated on those implementations existing (ships with the F-01 vertical slice).
- [ ] Reviewed by PE + S3 + S2 + architecture group; threat-model pass on the surface (Part XI).
- [ ] Spec RATIFIED and frozen **together with** SPEC-S2-PROTOCOL as the G-D baseline.

## 10. Decisions & open items

Owner directive (2026-10-12): correctness and highest quality over speed; G-D scope Q-P1=(b) — this REST spec and SPEC-S2-PROTOCOL freeze together.

- **Q-A1 — v1 catalog completeness — RECONCILED against F-01 (2026-10-12).** Reconciliation added `GET /v1/pools` and the challenge lifecycle (`GET /v1/challenges`, `…/accept`, `…/decline`); the catalog is complete for the **F-01 web vertical slice** (the first client that ships). Rule set for later clients: a call a *future* v1 client needs that isn't yet listed is a backward-compatible `/v1` **additive** change (§3.1), not a `/v2` break — so freezing now does not trap us. Remaining RATIFIED gate: a final cross-check once the S4 F-01 client code exists, to confirm no further slice-level omission.
- **Q-A2 — challenge accept: REST vs WS — RESOLVED (2026-10-12).** The challenge create/accept/decline/list are **REST** (durable, non-live); an accept that spawns a live game transitions via WS `MATCHED` (§3.3). Pinned.
- **Q-A3 — token presentation detail (defer to X-3).** This spec fixes "Bearer in `Authorization`, `401`/`403` responses"; the token's own format/rotation is X-3's spec, cited not defined. No decision needed here.
- **Q-A4 — `time_control` representation seam — DECIDED (2026-10-12, cross-spec with S2).** One representation everywhere: a **stable `time_control` id string** (e.g. `"blitz-5+3"`, `"rapid-10+5"`, `"daily-3"`) is the shared key on the WS wire, in events, and in every REST body (`SEEK`, `GameCreated`, `GameSummary.time_control`, `Challenge.time_control`). **`GET /v1/pools` is the single authority that expands an id →** `TimeControl { kind, base_ms, increment_ms, per_move_ms? }`; no other surface inlines the struct. Rationale: keeps the WS move/seek path to a short string (NFR-1), gives REST clients the human form in exactly one place, and leaves one source of truth for the id→struct map (the pool catalog, projected from S3). Custom time controls (F-01.2 Custom) use a canonical-encoded id `"custom-<base>+<inc>"` so they need no catalog row. Mirrored in SPEC-S2-PROTOCOL §3.3/Q-P3.

---

### Reviewer note (not part of the frozen contract)

This spec deliberately does **not** restate any WebSocket frame or event — those live once, in `../S2-realtime/PROTOCOL.md`. The PE README's "freeze v1 REST+WS surface" is realized as **two specs that freeze together** (Q-P1=(b)): WS in S2, REST here, with §3.3 as the seam. That split is the no-compromise reading — one definition per operation, audited for no overlap or gap.
