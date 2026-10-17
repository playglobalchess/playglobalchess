# Accounts & Identity — auth, sessions, guest play (X-3) — S3 Platform

| Field | Value |
|---|---|
| Spec ID | SPEC-S3-ACCOUNTS |
| Owner | S3 Platform (stream lead — fill in); co-reviewed by PE + S2 + S6 (fair-play veto) + Architecture group |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-17 |
| Implements (master IDs) | X-3; F-46 (profile hook); ADR-6 (GDPR/COPPA), ADR-8 (entitlements hook); NFR-6 (ASVS L2), NFR-12 (compliance); Part XI T-5, T-9, T-10 |
| Depends on | SPEC-S3-DATA-MODEL (`User`/`Credential`/`Profile`/`Entitlement` entity truth — cited, never redefined); SPEC-PE-API-CONTRACT (RATIFIED — the auth endpoint catalog, Bearer convention, RFC-7807 errors, this spec fulfils its explicit "token internals + service implementation delegated to X-3" out-of-scope lines); ADR-6, ADR-8 |
| Gates it feeds | Identity substrate for **G-B** (fair-play: only account-holders carry the persistent identity anti-cheat assumes, NFR-4) and **G-C** (rating integrity: rated play requires an account). Not itself a gate. |

## 1. Purpose & scope

Define the **accounts & identity service contract (X-3)**: how a human (or a guest) obtains, presents, and ends a credential; how a session is minted, bound, rotated, and invalidated; the five authentication mechanisms the owner scoped this session; the ephemeral **guest-identity** model and the invariant that **guests are confined to unrated play**; and the guest→account upgrade flow. This is the spec SPEC-PE-API-CONTRACT points at when it writes *"Auth token internals… is X-3's spec"* and *"How `services/accounts`… fulfils an endpoint is each service's spec"* (API-CONTRACT §1 non-goals, Q-A3).

This is a **spec, not code.** Per the build manual §0 it is written, set to REVIEW, and **STOPs for owner ratification**; no `services/accounts` code is written until it is ratified. It also carries, as separate owner-gated amendments (§11), the additive changes to the two frozen contracts that guest + passwordless + upgrade require.

**In scope**
1. **Token & session model** — session-credential format, issuance, binding, rotation, expiry, revocation, and the storage model behind it (which store, per ADR-10 role separation).
2. **The five auth mechanisms** — per-mechanism request/response behaviour and state transitions: email+password, OAuth, passwordless (magic-link / email-code), guest play, and guest→account upgrade.
3. **The guest-identity model** — an ephemeral identity distinct from `User`, with the unrated-only invariant stated and its enforcement points named.
4. **Security posture** — NFR-6 ASVS L2 controls, and the Part XI threats this surface owns (T-5 account takeover, T-10 credential stuffing, T-9 child safety): rate limits, lockout, session invalidation.
5. **Compliance seams** — ADR-6 GDPR/COPPA (`kid_account`), data-subject export/delete hooks, the ADR-8 entitlements hook fired at account creation.
6. **Additive contract amendments** (§11) — the API-CONTRACT + DATA-MODEL rows that the new mechanisms require, drafted REVIEW, owner-gated, **not self-applied**.

**Non-goals (explicit)**
- **No `services/accounts` implementation code.** A later chunk, after this spec is ratified (§5, build manual §0).
- **No ratings or anti-cheat.** X-4 (`RATING-SERVICE.md`, G-C) and X-5 (`ANTICHEAT.md`, G-B) are separate specs. This spec only states the identity *contract* those gates stand on.
- **No relational schema (DDL).** Entity truth is `DATA-MODEL.md`; this spec cites `User`/`Credential`/`Profile`/`Entitlement` fields and never redefines them. The one new entity (guest identity) is proposed as a DATA-MODEL amendment in §11, not defined twice.
- **No REST convention restatement.** Versioning, Bearer presentation, pagination, RFC-7807 error envelope, idempotency, rate-limit headers are API-CONTRACT §3.1; this spec cites them.
- **No OAuth provider selection, email-delivery infrastructure, or payment/PCI.** Owner/infra actions (ADR-8 payment is provider-side, T-11). Referenced, not built. Provider list is **Q-ACC7**.
- **No web/mobile login UI.** A later S4/S5 chunk once X-3 is built.
- **No `/v1/pools` / time-control work and no F-01 #64 picker.** Unrelated; #64 stays blocked until `/v1/pools` ships.

## 2. Requirements traced

| Master ID | Requirement | Where handled |
|---|---|---|
| X-3 | Signup/login (OAuth + email), profiles, sessions, device management, COPPA for F-41 | whole spec; §3.2–3.7 mechanisms, §3.1 sessions, §3.8 device/session management, §3.9 compliance |
| F-46 | Profile created at account creation | §3.2 (register provisions `Profile`), cites DATA-MODEL `Profile` |
| ADR-6 | GDPR baseline + COPPA-as-needed | §3.9 (export/delete hooks, `kid_account`, age-gate seam) |
| ADR-8 | Freemium entitlements hook, server-verified | §3.10 (entitlement granted at account creation; guests get none) |
| NFR-6 | OWASP ASVS L2 | §7 (password storage, session binding, transport, input validation) |
| NFR-12 | GDPR/COPPA, export/delete within statutory windows | §3.9 |
| NFR-4 (indirect) | 100% of rated games scored → persistent identity | §3.6 (unrated-only guest invariant), §4 |
| T-5 Account takeover | §7 (session binding, rotation, breach-password check, anomalous-login hook) |
| T-10 Credential stuffing / API abuse | §7 (per-IP + per-user rate limits, lockout, bot-challenge hook) |
| T-9 Child safety | §3.9 (COPPA controls, restricted-comms seam via `kid_account`) |

## 3. Design

### 3.0 Model overview

Two identity kinds, one session shape:

- **Account identity** — a `User` row (DATA-MODEL §3.1) with one or more `Credential` rows. Persistent, has a unique public `handle`, a `Profile`, a GDPR tombstone path, and (once rated games exist) rating/anti-cheat history. Produced by register / login / OAuth / passwordless.
- **Guest identity** — an **ephemeral** identity, **distinct from `User`** (proposed entity `GuestSession`, §11 DATA-MODEL amendment). No unique handle, no `Profile`, no GDPR tombstone, no rating/anti-cheat history, no `Entitlement`. A provisional display name only. Produced by guest play; destroyed on expiry; converted by upgrade.

Both resolve to the **same `Session` output shape** (API-CONTRACT Appendix A `Session { token, user_id, expires_at }`) so one client auth path handles both — with the subject-kind distinction carried **inside the opaque token**, not as a new public field (§3.1). A guest's `Session.user_id` is its `GuestSession.id` (an opaque subject id); clients never branch on the id's shape.

> **Design rule (server-authoritative identity).** The token is opaque to clients (API-CONTRACT §3.1 Auth: *"token minted by X-3, opaque here"*). Every authorization decision — "is this subject an account or a guest?", "may it enter a rated pool?" — is made **server-side** from the session record, never from a client-supplied claim. This mirrors the move-path rule (clients send intents, server decides) and closes T-11-style client-trust gaps on the identity surface.

### 3.1 Token & session model

**Session credential (token).** An **opaque, high-entropy bearer token** (not a client-readable JWT in v1). Rationale: the API-CONTRACT already fixes "opaque here"; an opaque server-side session reference gives instant server-side revocation (T-5) without JWT-revocation-list complexity, keeps all authorization claims server-side (the design rule above), and leaks nothing to a client or a logger. The token is a reference key; its claims live in the server session record.

**Session record (server-side).** Keyed by a hash of the token (never the raw token at rest — §7). Fields (shape proposed here, owner ratifies lifetimes in Q-ACC1):

```
Session (server-side record — NOT a public API body; the public body is API-CONTRACT Session{token,user_id,expires_at})
  token_hash      — hash of the bearer token (lookup key; raw token never stored)
  subject_id      — User.id (account) OR GuestSession.id (guest)
  subject_kind    — enum(account | guest)        -- drives the rated-pool gate (§3.6, §4)
  created_at
  expires_at                                       -- absolute lifetime (Q-ACC1)
  last_seen_at                                     -- idle-timeout + anomalous-login signal
  rotated_from?   — prior token_hash on rotation (audit chain, T-5)
  binding         — { ip_prefix?, user_agent_hash?, device_id? }  -- session binding (T-5; Q-ACC2 strength)
  mfa_level       — enum(none | mfa)              -- for the 2FA option (T-5); v1 may be none-only (Q-ACC8)
  revoked_at?
```

**Storage (ADR-10 role separation).** Session records are **hot, high-read, expiring** state → **Redis** (ADR-10: "Redis = hot/ephemeral… presence, rate-limit counters… never the source of truth"). The **durable account truth** (`User`, `Credential`) stays Postgres. A session is reconstructible (re-login) so Redis loss logs users out but loses no account truth — consistent with "Redis never the source of truth". The **audit trail** of logins/rotations/revocations (T-5, T-12) is written to the durable audit path (Postgres or the event/audit log), not only Redis. **Q-ACC3** confirms the session store choice with Architecture (it is the first component to pin Redis's auth role, which ADR-16's scope boundary left open per non-event-store component).

**Issuance.** Any successful auth (§3.2–3.5) mints a new session record + returns `Session{token,user_id,expires_at}`. Account creation additionally fires the ADR-8 entitlement grant (§3.10) and provisions `Profile` (F-46).

**Binding (T-5).** On issuance, the session records a binding fingerprint (§3.1 `binding`); a later request whose fingerprint diverges beyond the configured tolerance is a session-binding anomaly → re-auth challenge (not a hard fail by default, to avoid breaking mobile roaming). Binding strength (how strict, which signals) is **Q-ACC2**.

**Rotation.** The token rotates (a) on privilege elevation (guest→account upgrade, §3.7 — the old guest token is revoked and a fresh account token issued), (b) on MFA step-up, and (c) optionally on a sliding schedule. Rotation writes `rotated_from` for the audit chain. Rotation cadence is **Q-ACC1**.

**Expiry.** Two clocks: an **absolute** lifetime (`expires_at`) and an **idle** timeout (`last_seen_at` + idle window). Guests get a **shorter** lifetime than accounts (a guest is meant to be transient — exact values **Q-ACC4** for guests, **Q-ACC1** for accounts). Expiry is enforced server-side on every request; an expired token → `401 unauthenticated` (API-CONTRACT Appendix B).

**Revocation / invalidation.** `/v1/auth/logout` revokes the current session. A security event (password change, breach detection, user "log out everywhere") revokes **all** of a subject's sessions (device management, §3.8). Because the store is server-side and opaque-reference, revocation is immediate (the T-5 advantage over stateless JWT).

### 3.2 Email + password — `/v1/auth/register`, `/v1/auth/login`

Both endpoints already exist in the ratified API-CONTRACT (§3.2 catalog + Appendix A.2); this spec defines their **behaviour**, not their shape.

**Register (`POST /v1/auth/register`, auth: none → `201 Session`).**
1. Validate `{handle, email, password?, locale?}` (API-CONTRACT A.2). `handle` unique among live users (DATA-MODEL `User.handle U`); `email` well-formed; `password` meets policy (**Q-ACC5** — ASVS L2 password policy: min length, breach-check via a k-anonymity range API against a breached-password corpus, no composition rules per NFR-6/ASVS).
2. Create `User` (`email_enc` + `email_hash` per DATA-MODEL — the raw email is never stored plain), create a `Credential{type=password, secret_ref}` where `secret_ref` is a **vault pointer to the password verifier** (argon2id hash held in the vault, never a column — DATA-MODEL `Credential.secret_ref` is "a vault reference, NOT the secret").
3. Provision `Profile` (F-46), fire the ADR-8 entitlement grant (§3.10).
4. Age-gate seam (§3.9): if the registration indicates an under-13 user (or a jurisdiction requires it), set `kid_account=true` and route to the COPPA consent flow before the account is usable. The age-collection mechanism is **Q-ACC9**.
5. Mint a session (§3.1), return `201 Session`.

**Login (`POST /v1/auth/login`, auth: none → `200 Session | 401`).**
1. Look up by `handle_or_email` (via `email_hash` for email). Verify the password against the vault verifier.
2. On success: breach-password check (T-5 — if the password appears in a breach corpus, succeed but flag for forced rotation), mint a session, return `200 Session`.
3. On failure: `401 unauthenticated`. Apply per-account + per-IP throttle and lockout (§7, T-10). Never reveal whether the handle/email exists (uniform `401`, T-6-style enumeration guard).

Password storage: **argon2id** (ASVS L2), parameters pinned by the vault/service, never in client reach. The hash lives behind `secret_ref`; the service calls the vault to verify.

### 3.3 OAuth — `/v1/auth/oauth/{provider}`

Exists in API-CONTRACT (`POST /v1/auth/oauth/{provider}`, auth: none, req `{code, redirect_uri}` → `200 Session | 401`). Behaviour:
1. Exchange the authorization `code` with the provider (server-side, standard OAuth 2.0 / OIDC authorization-code flow; the client never handles the provider secret).
2. Resolve the provider identity to a `User`: match an existing `Credential{type=oauth, provider}`; else, if the verified provider email matches an existing `User.email_hash`, **account-linking policy applies** (link vs. separate account — **Q-ACC10**); else create a new `User` + `Credential{type=oauth, provider, secret_ref=provider-subject-ref}` + `Profile` + entitlement grant, with a generated/claimed `handle`.
3. Mint a session, return `200 Session`.

**The provider list is NOT decided here — Q-ACC7.** The spec does not assume Google/Apple/etc. The `{provider}` path param and the `Credential.provider` column already generalise over whatever set the owner ratifies; adding a provider later is config, not a contract change.

### 3.4 Passwordless (magic-link / email-code) — NEW: `POST /v1/auth/magic-link` (+ verify)

A credential model **distinct from the password path**: single-use, short-lived, **no stored secret** (`secret_ref` is password-only; a magic-link uses no `Credential.secret_ref`). Shares the `Session` output shape. Two steps (both NEW endpoints, §11 API-CONTRACT amendment):

1. **Request** — `POST /v1/auth/magic-link` req `{email}` → `202` (always `202`, even for an unknown email — never reveal account existence, T-6). The service mints a **single-use, short-lived token** (random, high-entropy), stores only its **hash** with an expiry and a consumed-flag, and sends the link/code out of band (email — delivery infra is an infra action, not built here). Lifetime + single-use semantics are **Q-ACC6**.
2. **Verify/consume** — `POST /v1/auth/magic-link/verify` req `{token}` (from the link) → `200 Session | 401`. Validates: token hash exists, not expired, not already consumed. On success: mark consumed (atomic compare-and-set so a replay cannot double-consume), resolve/create the `User` (first magic-link login for a new email creates a `User` + `Profile` + entitlement, like register-without-password), mint a session. On failure (expired/consumed/unknown): `401`.

Design notes: the magic-link token is itself a bearer credential for one step, so it gets the **same at-rest discipline as the session token** (hash-at-rest, never logged, §7). It is **not** a `Credential` row — it is a transient issuance record (proposed storage shape in §11; it is ephemeral → Redis with a TTL, like sessions). An email-code variant (6-digit OTP) is the same model with a different delivery/entry shape and a stricter attempt limit (brute-force guard, T-10); whether v1 ships link, code, or both is folded into **Q-ACC6**.

### 3.5 Guest play — NEW: `POST /v1/auth/guest`

Instant, accountless, **unrated-only**. `POST /v1/auth/guest` req `{display_name?}` (auth: none) → `201 Session` (NEW endpoint, §11).

1. Create a `GuestSession` (ephemeral entity, §11 DATA-MODEL amendment): `id` (opaque), `provisional_display_name` (supplied or server-generated — e.g. "Guest-7F3A"; naming policy + validation/moderation is **Q-ACC11**), `created_at`, `expires_at` (short — **Q-ACC4**). **No** `User`, **no** `handle`, **no** `Profile`, **no** `Credential`, **no** `Entitlement`.
2. Mint a session with `subject_kind=guest`, `subject_id=GuestSession.id`.
3. Return `201 Session` — same shape as any other auth, `user_id` = the guest id.

A guest can play **unrated** games only (§3.6 invariant). A guest has no persistent history; when its session expires, its ephemeral identity is destroyed (what happens to any in-flight/finished unrated game references at expiry is **Q-ACC12**).

### 3.6 The unrated-only invariant (enforced, not advisory)

> **INVARIANT ACC-INV-1 — rated matchmaking requires an account; a guest is confined to unrated play.**

This is the load-bearing identity rule and the reason the guest/account split exists. It is what keeps **NFR-4** ("100% of rated games scored" by anti-cheat) and gates **G-B/G-C** clean *by construction*: only account-holders carry the persistent identity that anti-cheat correlation and Glicko-2 per-pool history require. It also aligns with the existing **F-01.3 rated/unrated toggle** (MASTER §II) — a guest simply cannot flip it to rated.

**Enforcement points (defence in depth — a single check is a single point of failure):**
1. **At the identity contract (here).** The session carries `subject_kind`; a guest session is the authoritative "not rated-eligible" signal.
2. **At the realtime seek path (S2).** The WS `SEEK` handler must reject a `rated=true` seek from a `subject_kind=guest` session — the server decides from the session, never from the client's `rated` flag (server-authoritative). This requires S2 to read `subject_kind` from the authenticated session; named as a **cross-stream obligation on S2** (§4), not something this spec can enforce alone — this is a cross-cutting requirement that must have a named owning layer (the lesson from the S4 UNSUPPORTED_VERSION layer-gap: a rule spanning two layers must be pinned to one owner, with the other explicitly deferring). **S2 owns the terminal rejection; X-3 owns the signal.**
3. **At rating application (X-4).** The rating service applies `RatingApplied` only to account subjects; a guest subject has no `Rating` row to write. Belt-and-braces: even if (2) failed, no rating is ever written for a guest.

The invariant is stated in all three specs' contracts (this one authoritative; S2 and X-4 cite ACC-INV-1).

### 3.7 Guest → account upgrade ("claim your games") — NEW: `POST /v1/auth/upgrade`

Lets a guest convert to a full account and keep their session's games. `POST /v1/auth/upgrade` (auth: **guest session required**) req `{handle, email, password? | oauth{...} | magic-link intent}` → `200 Session` (a fresh **account** session; NEW endpoint, §11).

1. Require a valid `subject_kind=guest` session (else `401`/`403`).
2. Create a full `User` + `Credential` (via whichever mechanism the body carries — password / oauth / passwordless) + `Profile` + ADR-8 entitlement grant, exactly as register would.
3. **Re-key the guest's game references** to the new `User.id`. Because guest games are **unrated**, this is a provenance re-attribution of unrated game history, not a rating transfer (a guest never had a rating). The authoritative game events live in the event store (ADR-10); the upgrade writes the new `user_id` into the appropriate projection/association. Exactly which references carry over (unrated game list only vs. anything else) is **Q-ACC12** (shared with guest expiry — the two are the same "what is a guest's game history" question from opposite ends).
4. **Rotate the token** (§3.1): revoke the guest session, mint a fresh account session (privilege elevation). Return `200 Session`.
5. Destroy the `GuestSession` ephemeral record (its purpose is served).

Edge: upgrade with a `handle`/`email` that already belongs to a live account → this is a *collision*, not a login; return `409 conflict` (the user should log into the existing account instead; whether to then offer "merge this guest's games into that existing account" is out of v1 scope and flagged in Q-ACC12).

### 3.8 Device & session management (X-3 "device management")

- `GET /v1/me/sessions` (NEW, §11) → the subject's active sessions (device/binding summary, `last_seen_at`, current flag) — account subjects only.
- `DELETE /v1/me/sessions/{id}` and `POST /v1/me/sessions/logout-all` (NEW, §11) → revoke one / all sessions (the "log out everywhere" that a password change or breach also triggers, §3.1).
- Device binding is the `binding` fingerprint (§3.1); a `device_id` the client supplies is an *advisory* label, never trusted for authorization (server-authoritative). Whether v1 ships full device management or only logout-all is **Q-ACC8** (folded with the 2FA question — both are "how much account-security surface in v1").

### 3.9 Compliance seams (ADR-6 GDPR/COPPA, NFR-12)

- **`kid_account` (COPPA, F-41, T-9).** Set at registration via the age-gate seam (§3.2 step 4; mechanism **Q-ACC9**). It gates restricted-comms surfaces downstream (DATA-MODEL §7: "enforced in service logic; the flag lives here"). COPPA verifiable-parental-consent flow is referenced (ADR-6) — the *consent capture* is a product/legal flow this spec names but does not design (**Q-ACC13**, routed to the ADR-6 legal track).
- **Data-subject export/delete (GDPR, NFR-12, "within statutory windows").** The identity service exposes the account-side hooks: an **export** request (assemble the subject's account + profile + game-history references) and a **delete/erasure** request → `User` **tombstone** (DATA-MODEL §5: "`User` tombstoned; `PublicUser` projections show an anonymized handle; game history (truth) retained in the event store per retention policy"). The erasure contract (what is erased vs. anonymised-and-retained, and the statutory window) is pinned with legal (**Q-ACC13**). Guests have **no** such obligations (no PII beyond an ephemeral provisional name, no persistent identity) — a documented simplification of the guest model.
- **Freed-handle reuse (Q-DM1, deferred to X-3).** DATA-MODEL §5 explicitly defers this here. **Decision proposed (owner ratifies as Q-ACC14):** a tombstoned account's `handle` is **not** reclaimable by default (held reserved) — reuse risks impersonation of a known player and breaks external references (PGN archives, links). If a reclaim policy is wanted, it needs a cooling-off period + an explicit non-impersonation guard. Flagged, not self-decided.

### 3.10 Entitlements hook (ADR-8)

At **account creation** (register / oauth-create / passwordless-create / upgrade), the service fires the ADR-8 entitlement grant: write the baseline freemium `Entitlement` row(s) (DATA-MODEL §3.5 `Entitlement`, server-verified, T-11). The capability/plan values are owned by the ADR-8 entitlement model (tier boundaries are "a later product decision" per ADR-8) — this spec fixes **when** the hook fires (at creation) and **that** it is server-written, not **which** tiers exist (**Q-ACC15** routes any tier detail the auth flow needs to ADR-8). **Guests get no `Entitlement`** (consistent with the guest model) — a guest's capabilities are the fixed anonymous-unrated baseline, enforced by `subject_kind=guest`, not by a row.

## 4. Interfaces & contracts

**Consumes:** `DATA-MODEL.md` entities `User`, `Credential`, `Profile`, `Entitlement` (cited, not redefined); the vault (secret storage, steering); the OAuth providers (external); the email channel (infra, for passwordless + verification).

**Exposes:** the auth REST surface — the three ratified endpoints (register/login/oauth) whose *behaviour* this spec defines, plus the NEW endpoints this spec requires (guest, upgrade, magic-link + verify, session management) which land as the §11 API-CONTRACT amendment.

**Emits (for other streams):** the authenticated **session** carrying `subject_id` + `subject_kind`, read by:
- **S2 (realtime)** — the WS `AUTHENTICATE` handler resolves a token to `{subject_id, subject_kind}`; the `SEEK` handler enforces ACC-INV-1 (reject `rated=true` from a guest). **Cross-stream obligation named on S2** (§3.6 point 2).
- **X-4 (rating)** — applies ratings only to `subject_kind=account` (§3.6 point 3).
- **ADR-8 entitlements** — reads `subject_kind` to resolve a guest's fixed baseline vs. an account's `Entitlement` rows.

**Changes to shared contracts.** This spec requires **additive** changes to two RATIFIED contracts (API-CONTRACT, DATA-MODEL). Per CONVENTIONS + API-CONTRACT §3.1 versioning, these are **owner-gated amendments** reviewed by both affected streams + architecture (+ S6 for the rated-play invariant). They are drafted in §11 as Status: REVIEW and are **NOT self-applied** — same change-control discipline as the ADR-17/18 frozen-wire amendments. They are additive `/v1` changes (new rows, new optional entity), not a `/v2` break.

## 5. Edge cases & failure modes

| Case | Contract behaviour |
|---|---|
| Login with wrong password | `401 unauthenticated`; per-account + per-IP throttle; lockout after N failures (§7, T-10). Uniform message — no handle/email enumeration (T-6). |
| Register with a taken handle | `409 conflict` (API-CONTRACT Appendix B `conflict`); handle uniqueness is DATA-MODEL `User.handle U`. |
| Register with an email already in use | Uniform success-shaped response path **or** `409` — must not leak which emails are registered (T-6). Resolution pinned in **Q-ACC5** (register enumeration policy). |
| OAuth email matches an existing account | Account-linking policy — **Q-ACC10** (link vs. separate); default proposed: link only on a *verified* provider email, else separate. |
| Magic-link token reused (replay) | Atomic consume (compare-and-set); a second verify → `401` (already consumed). Never double-mints a session. |
| Magic-link token expired | `401`; the user requests a new one. |
| Magic-link requested for unknown email | `202` anyway (no enumeration, T-6); no email sent / a generic "if an account exists…" message. |
| Guest tries a rated seek | Rejected at the S2 `SEEK` path (ACC-INV-1 point 2); the server decides from `subject_kind`, never the client flag. X-4 is the backstop (point 3). |
| Guest session expires mid-interaction | `401`; client must re-guest or register. Fate of the guest's unrated game refs → **Q-ACC12**. |
| Upgrade from a non-guest (account) session | `403` — upgrade is guest-only; an account is already upgraded. |
| Upgrade into a handle/email that already exists | `409 conflict`; direct the user to log in; guest-game merge into an existing account is out of v1 scope (Q-ACC12). |
| Expired session token on any request | `401 unauthenticated` (API-CONTRACT Appendix B). |
| Session-binding anomaly (fingerprint diverges) | Re-auth challenge (not a hard fail by default — mobile roaming); strictness is **Q-ACC2**. Anomalous-login is also a T-5 signal to the audit path. |
| Password appears in breach corpus at login | Login succeeds but flags forced rotation (T-5); surfaced to the user, not a hard block (avoid lockout-by-breach). |
| "Log out everywhere" / password change | Revoke **all** the subject's sessions immediately (server-side store makes this instant). |
| Token leaked in a log/URL | Mitigated by hash-at-rest + never-logged discipline (§7); a leaked *live* token is revocable immediately (opaque server-side session). Magic-link tokens are single-use + short-lived to bound the window. |
| Redis (session store) loss | Users are logged out (must re-auth); **no account truth lost** (Postgres is truth; sessions are reconstructible) — consistent with ADR-10 "Redis never the source of truth". |

## 6. Non-functional handling

- **NFR-6 (ASVS L2).** argon2id password verifier in the vault (§3.2); opaque server-side sessions with immediate revocation (§3.1); session binding (§3.1); input validation on every body (API-CONTRACT §3.1); HTTPS-only; secrets in a vault not columns (DATA-MODEL `secret_ref`); RFC-7807 errors that never leak internals (API-CONTRACT Appendix B).
- **NFR-12 (compliance).** Export/delete hooks + tombstone path (§3.9); `kid_account` COPPA gate; GDPR-baseline-worldwide (ADR-6) with the statutory-window contract pinned in Q-ACC13.
- **Read-path latency (§IX.1).** Auth is a write-ish path; session *validation* on every subsequent request is a single hot keyed lookup in Redis (§3.1) — designed to stay well inside the read budget. The measured number is proven under load (G-E), not here.
- **NFR-4 (indirect).** ACC-INV-1 guarantees every *rated* subject is an account with persistent identity — the precondition anti-cheat (G-B) and ratings (G-C) assume.

## 7. Security & abuse

Part XI threats this surface owns, each with its control:

- **T-5 account takeover.** argon2id verifier (vault); opaque server-side sessions → **instant revocation**; session binding + anomalous-login challenge; rotation on elevation/step-up with an audit chain (`rotated_from`); breach-password check at register/login; optional 2FA/WebAuthn (DATA-MODEL `Credential.type` already includes `webauthn`) — v1 scope of 2FA is **Q-ACC8**. Tokens are hashed at rest (never the raw token in Redis or logs) and never placed in a URL (magic-link verify takes the token in the body, not a logged query string where feasible — the link transport is the one exception, mitigated by single-use + short TTL).
- **T-10 credential stuffing / API abuse.** Per-IP **and** per-account rate limits with `429 + Retry-After` (API-CONTRACT §3.1/§7); progressive lockout/backoff after repeated failures; a bot-challenge hook on `/v1/auth/*` (API-CONTRACT §7 names this hook); email-code (if shipped) gets a strict attempt cap (brute-force guard). Lockout thresholds are **Q-ACC5** (tuned to not become a self-DoS vector — a locked-out legitimate user must have a recovery path, i.e. passwordless/reset).
- **T-9 child safety.** `kid_account` gates restricted-comms surfaces (§3.9); COPPA consent flow referenced (ADR-6). The flag is set server-side at registration, never client-asserted.
- **T-6 (enumeration, borrowed).** Uniform responses on login failure, magic-link request, and (per Q-ACC5) register — never reveal which handles/emails exist.
- **NFR-6 input validation / least privilege.** Every body validated against the API-CONTRACT schema; the accounts service holds least-privilege access to the vault and the PII columns; PII access is audit-logged (T-12, DATA-MODEL §7).
- **S6 fair-play veto.** ACC-INV-1 (guests unrated-only) is a fair-play-load-bearing rule; **S6 reviews this spec** and holds veto on any change that would let an unauthenticated/ephemeral subject into rated play (CONVENTIONS: "a spec touching rated play… also needs S6 review").

## 8. Test plan

The contract's conformance corpus (CONVENTIONS: the test plan is part of the spec). Edge-first (steering §30 adversarial review):

- **Session lifecycle.** Issue → validate → expire (absolute + idle) → `401`; revoke → immediate `401`; rotate on upgrade → old token dead, new token live, `rotated_from` chain intact.
- **Mechanism conformance.** register (creates `User`+`Credential`+`Profile`+`Entitlement`, fires the hook once); login (success/fail/lockout, uniform-failure enumeration guard); oauth (new vs. existing-link vs. collision); magic-link (request→verify happy path; **replay → single `401`**; expired → `401`; unknown email → `202` no-leak); guest (creates `GuestSession`, **no** `User`/`Profile`/`Entitlement`); upgrade (guest→account re-keys game refs, rotates token, destroys `GuestSession`; non-guest → `403`; collision → `409`).
- **ACC-INV-1 (the load-bearing invariant) — prove it has teeth.** A `subject_kind=guest` session attempting a `rated=true` seek is rejected; assert the rejection is driven by `subject_kind` server-side, **not** by the client's `rated` flag (set the client flag to the "wrong" value and confirm the server still decides correctly — mirrors the "prove the e2e has teeth" lesson). Assert X-4 writes **no** `Rating` row for a guest even if the seek guard is bypassed in the test (belt-and-braces test).
- **Per-field write-coverage (steering §30 pass 3).** Every `Session` record field written on every path that should (e.g. `rotated_from` set on rotation, `revoked_at` on logout, `subject_kind` on both guest and account issuance — the guest path must not leave it defaulted to `account`).
- **Callee-contract enumeration (steering §30 pass 1).** For the vault verify, the OAuth code-exchange, and the atomic magic-link consume: enumerate each callee's success/failure/edge returns and prove each is handled (the consume's "already consumed" branch is the replay guard; a vault "verifier not found" is a `401`, not a `500`).
- **Failure/retry/restart walk (steering §30 pass 2).** A client resending a register (idempotency); a crash between `User` create and entitlement grant (is the account left without entitlements? — the grant must be idempotent / repair-on-next-login); a magic-link verify racing a second verify of the same token (atomic consume serialises).
- **Security.** Token never stored raw (assert only a hash at rest); token never in a log line (assert); breach-password path flags not blocks; lockout triggers and *clears* on a recovery path (not a permanent self-DoS).
- **Compliance.** Tombstone on delete anonymises `PublicUser` projections (DATA-MODEL §5) while the event-store game history is retained; guest delete is a no-op (nothing persistent to erase).
- **Contract-schema conformance.** The new endpoints' request/response bodies validate against the §11 Appendix-A amendment, run as part of the shared contract suite (NFR-10) once the service exists (R2, deferred with API-CONTRACT §9 R2).

**Not tested here:** rating math (X-4/G-C), anti-cheat detection (X-5/G-B), email-delivery infra, the OAuth provider's own flow, load/latency (G-E).

## 9. Acceptance criteria (Definition of Done)

This spec is DONE (ready to leave REVIEW for RATIFIED) when:
- [ ] All five mechanisms (email+password, OAuth, passwordless, guest, upgrade) have a defined per-endpoint behaviour + state transitions, citing DATA-MODEL entities and never redefining fields.
- [ ] The token/session model (format, issuance, binding, rotation, expiry, revocation, storage) is unambiguous, with every open lifetime/strength parameter raised as a Q-item (not silently chosen).
- [ ] ACC-INV-1 (guests unrated-only) is stated with its three enforcement points and the S2/X-4 cross-stream obligations named.
- [ ] The guest-identity model is defined as distinct from `User`, with the §11 DATA-MODEL amendment drafted (not self-applied).
- [ ] The §11 API-CONTRACT amendment (new endpoint rows + Appendix-A schemas) is drafted REVIEW (not self-applied).
- [ ] Security posture (§7) maps T-5/T-10/T-9 to concrete controls; compliance seams (§3.9) map ADR-6/NFR-12.
- [ ] The ADR-8 entitlement hook firing point is pinned (at account creation; guests excluded).
- [ ] Every open question (§10) is Q-numbered and routed to the owner — **none decided unilaterally**.
- [ ] Reviewed by S3 + PE + S2 + S6 (fair-play) + architecture group (it touches two shared contracts + the rated-play invariant).
- [ ] **Owner ratifies** the spec AND the §11 amendments before any `services/accounts` code is written (build manual §0).

## 10. Open questions (owner ratification required — none decided here)

Each is a decision the owner (or legal/architecture) owns; the spec raises them and proceeds no further on the ambiguous ones until signed off (build manual §0 "make no silent assumptions"; handoff §8 "STOP and ask").

| Q | Question | Routed to | Proposed default (if any) — owner confirms |
|---|---|---|---|
| **Q-ACC1** | Account session absolute lifetime + idle timeout + rotation cadence | Owner + Architecture | proposed: 30-day absolute, 14-day idle, rotate on elevation only — **not** decided |
| **Q-ACC2** | Session-binding strength (which signals; challenge vs. hard-fail on divergence) | Owner + S6 | proposed: soft (challenge) binding on IP-prefix + UA; device_id advisory |
| **Q-ACC3** | Session store = Redis (first component to pin Redis's auth role; ADR-10/16 left per-component open) | Architecture | proposed: Redis (hot/ephemeral, reconstructible) — confirm it is not an ADR-scope expansion |
| **Q-ACC4** | Guest session lifetime (short); retention of a guest's unrated game refs on expiry | Owner | proposed: 24h; refs dropped on expiry unless upgraded — **not** decided |
| **Q-ACC5** | Password policy (ASVS L2: min length, breach-check), lockout thresholds, register-email enumeration policy | Owner + S6 | proposed: ≥12 chars, breach-check, no composition rules; lockout w/ recovery path |
| **Q-ACC6** | Magic-link / email-code: lifetime, single-use semantics, link vs. code vs. both in v1 | Owner | proposed: 15-min single-use link; code variant deferred — **not** decided |
| **Q-ACC7** | **OAuth provider list** (do NOT assume Google/Apple/etc.) | Owner | **none** — owner names the set |
| **Q-ACC8** | 2FA/WebAuthn + device-management surface scope in v1 (full vs. logout-all only) | Owner | proposed: logout-all + sessions list in v1; WebAuthn opt-in later |
| **Q-ACC9** | Age-collection mechanism at registration (how under-13 / COPPA is triggered) | Owner + legal (ADR-6) | **none** — legal-sensitive |
| **Q-ACC10** | OAuth account-linking policy (link on matching verified email vs. always separate) | Owner | proposed: link only on verified provider email |
| **Q-ACC11** | Guest provisional display-name policy (generation, validation, moderation) | Owner | proposed: server-generated "Guest-XXXX"; user override validated/moderated |
| **Q-ACC12** | What of a guest's unrated game history carries on upgrade vs. is lost on expiry; guest-game merge into an existing account on collision | Owner | proposed: unrated game list re-keyed on upgrade; dropped on expiry; no merge-into-existing in v1 |
| **Q-ACC13** | COPPA verifiable-parental-consent capture flow + GDPR erasure contract (what is erased vs. anonymised-retained; statutory windows) | Legal (ADR-6 track) | **none** — pre-G-F legal gate |
| **Q-ACC14** | Freed-handle reuse policy (DATA-MODEL Q-DM1, deferred to X-3) | Owner | proposed: tombstoned handle **not** reclaimable (impersonation risk) |
| **Q-ACC15** | Any ADR-8 entitlement-tier detail the auth flow must know at creation (which baseline capabilities a new account/guest gets) | Owner (ADR-8) | proposed: new account = freemium baseline; guest = fixed anonymous baseline |

> **Discovery-during-implementation guard.** Per the build manual, none of the above is to be resolved by guessing at code time. Each is ratified (or explicitly deferred-with-a-named-owner) before the `services/accounts` build chunk starts. A proposed default is a *starting point for the owner to accept or change*, not a decision this spec makes.

## 11. Additive amendments to the two RATIFIED contracts (Status: REVIEW — owner-gated, NOT self-applied)

Guest + passwordless + upgrade + session-management add surface the frozen specs do not have. These amendments are drafted here for review and are applied **only after owner ratification**, by the owning stream (PE for API-CONTRACT, S3 for DATA-MODEL), with the same change-control as the ADR-17/18 frozen-wire process (both affected streams + architecture + S6). They are **additive `/v1`** changes (new endpoint rows, new optional entity, new optional fields) — no existing row/shape is mutated, so the frozen F-01 slice is untouched.

### 11.1 `API-CONTRACT.md` — new endpoint rows (additive to §3.2 "Auth & accounts") + Appendix A schemas

New catalog rows:

| Method | Path | Auth | Serves | Returns |
|---|---|---|---|---|
| POST | `/v1/auth/guest` | none | X-3 (guest play) | session (guest subject) |
| POST | `/v1/auth/magic-link` | none | X-3 (passwordless) | `202` (link/code sent out of band) |
| POST | `/v1/auth/magic-link/verify` | none | X-3 (passwordless) | session |
| POST | `/v1/auth/upgrade` | guest | X-3 (guest→account) | session (account subject) |
| GET | `/v1/me/sessions` | user | X-3 (device mgmt) | active-session list |
| DELETE | `/v1/me/sessions/{id}` | user | X-3 (device mgmt) | `204` |
| POST | `/v1/me/sessions/logout-all` | user | X-3 (device mgmt) | `204` |

New Appendix-A schemas (language-neutral JSON; cite DATA-MODEL; `?` optional):
```
POST /v1/auth/guest            req { display_name?: str }                         → 201 Session
POST /v1/auth/magic-link       req { email: str }                                 → 202 {}            // no enumeration (T-6)
POST /v1/auth/magic-link/verify req { token: str }                                → 200 Session | 401
POST /v1/auth/upgrade          req { handle: str, email: str, password?: str,
                                     oauth?: { provider: str, code: str, redirect_uri: str } }
                               (auth: guest session)                              → 200 Session | 403 | 409
GET  /v1/me/sessions           → 200 { items: SessionInfo[] }
SessionInfo  { id: str, created_at: int, last_seen_at: int, current: bool,
               device_label?: str }                                               // never the token; binding summary only
DELETE /v1/me/sessions/{id}    → 204
POST /v1/me/sessions/logout-all → 204
```
Reuses the ratified `Session { token, user_id, expires_at }` shape (API-CONTRACT A.1) for all auth returns — a guest and an account are indistinguishable to the client at the token shape; the distinction is server-side (`subject_kind`). New error uses only the **existing** Appendix-B registry (`unauthenticated` 401, `forbidden` 403, `conflict` 409, `validation` 400, `rate-limited` 429) — **no new error `type`** is introduced, so Appendix B stays frozen as-is.

### 11.2 `DATA-MODEL.md` — new ephemeral `GuestSession` entity (additive to §3.1) + note

`GuestSession` is **ephemeral** (hot store, not relational truth — Redis with TTL, like a session record; it is NOT a Postgres `User`). It is listed in the data model for completeness of the identity surface, flagged as non-relational:

```
GuestSession   (EPHEMERAL — hot store w/ TTL, NOT relational truth; distinct from User)
  id                        text PK        -- opaque guest subject id (becomes Session.user_id for a guest)
  provisional_display_name  text           -- supplied or server-generated (Q-ACC11); NOT a unique handle
  created_at                timestamptz
  expires_at                timestamptz    -- short TTL (Q-ACC4)
  -- NO handle (not unique, not public-reserved), NO Profile, NO Credential,
  -- NO Entitlement, NO Rating, NO GDPR tombstone. A guest has none of these BY DESIGN.
```
Plus a one-line transient issuance record for passwordless (also ephemeral, hot store w/ TTL):
```
MagicLinkIssue (EPHEMERAL — hot store w/ TTL)
  token_hash   text PK     -- hash of the single-use token (raw token never stored)
  email_hash   text        -- which email it was issued for (cites User.email_hash shape)
  expires_at   timestamptz
  consumed     bool        -- atomic compare-and-set on verify (replay guard)
```
Note added to DATA-MODEL §3.1 / §5: the `User` entity is the **account** identity; `GuestSession` is the **ephemeral** identity; ACC-INV-1 (guests unrated-only) is why the two are distinct. Q-DM1 (freed-handle reuse) is addressed by Q-ACC14. No existing `User`/`Credential`/`Profile` field is changed; `Credential.type` already covers `oauth|password|webauthn` (passwordless uses **no** `Credential` — it is a transient issuance, so no enum change is needed).

---

### Reviewer note (not part of the frozen contract)

This spec fulfils the API-CONTRACT's explicit delegation of "token internals + service implementation" to X-3 (API-CONTRACT §1 non-goals, Q-A3). It deliberately (1) keeps the guest identity a **separate ephemeral entity** rather than a crippled `User`, so the unrated-only invariant is structural, not a flag someone can forget to check; (2) names **S2 as the owning layer** for the terminal rated-seek rejection, with X-3 owning only the `subject_kind` signal and X-4 as a backstop — the explicit cross-layer ownership the S4 layer-gap lesson demands; and (3) decides **nothing** that the owner, legal, or architecture must own — every lifetime, provider, policy, and compliance specific is a Q-item in §10. The §11 contract amendments are additive and drafted REVIEW, honouring the ADR-17/18 frozen-contract change-control: a frozen spec is amended by an owner-ratified additive version, never self-edited.
