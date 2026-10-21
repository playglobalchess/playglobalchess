# Accounts & Identity — auth, sessions, guest play (X-3) — S3 Platform

| Field | Value |
|---|---|
| Spec ID | SPEC-S3-ACCOUNTS |
| Owner | S3 Platform (stream lead — fill in); co-reviewed by PE + S2 + S6 (fair-play veto) + Architecture group |
| Status | REVIEW |
| Version | 0.8 |
| Last updated | 2026-10-21 |
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
- **No email-delivery infrastructure or payment/PCI.** Owner/infra actions (ADR-8 payment is provider-side, T-11). Referenced, not built. The OAuth provider set itself is **DECIDED** (Google/Apple/Facebook + email magic-link + username/password, §3.3 / Q-ACC7); only the provider-side delivery/payment infra is out of scope.
- **No web/mobile login UI.** A later S4/S5 chunk once X-3 is built.
- **No `/v1/pools` / time-control work and no F-01 #64 picker.** Unrelated; #64 stays blocked until `/v1/pools` ships.

## 2. Requirements traced

| Master ID | Requirement | Where handled |
|---|---|---|
| X-3 | Signup/login (OAuth + email), profiles, sessions, device management, COPPA for F-41 | whole spec; §3.2–3.7 mechanisms, §3.1 sessions, §3.8 device/session management, §3.9 compliance |
| F-46 | Profile created at account creation | §3.10 step 4 (shared pipeline provisions `Profile` on every creation path), cites DATA-MODEL `Profile` |
| ADR-6 | GDPR baseline + COPPA-as-needed | §3.9.1 (age gate, run via §3.10 step 2 on all creation paths), §3.9.2 (parental consent), §3.9.3 (erasure boundary) |
| ADR-8 | Freemium entitlements hook, server-verified | §3.10 step 5 (entitlement granted in the shared creation pipeline; guests get none) |
| NFR-6 | OWASP ASVS L2 | §7 (password storage, session binding, transport, input validation) |
| NFR-12 | GDPR/COPPA, export/delete within statutory windows | §3.9.3 (erasure boundary + 30-day windows) |
| NFR-4 (indirect) | 100% of rated games scored → persistent identity | §3.6 (unrated-only guest invariant), §4 |
| T-5 Account takeover | §7 (session binding, rotation, breach-password check, anomalous-login hook) |
| T-10 Credential stuffing / API abuse | §7 (per-IP + per-user rate limits, lockout, bot-challenge hook) |
| T-9 Child safety | §3.9.1–§3.9.2 (age gate + COPPA consent-and-restrict, `kid_account`) |

## 3. Design

### 3.0 Model overview

Two identity kinds, one session shape:

- **Account identity** — a `User` row (DATA-MODEL §3.1) with one or more `Credential` rows. Persistent, has a unique public `handle`, a `Profile`, a GDPR tombstone path, and (once rated games exist) rating/anti-cheat history. Produced by register / login / OAuth / passwordless.
- **Guest identity** — an **ephemeral** identity, **distinct from `User`** (proposed entity `GuestSession`, §11 DATA-MODEL amendment). No unique handle, no `Profile`, no GDPR tombstone, no rating/anti-cheat history, no `Entitlement`. A provisional display name only. Produced by guest play; destroyed on expiry; converted by upgrade.

Both resolve to the **same `Session` output shape** (API-CONTRACT Appendix A `Session { token, user_id, expires_at }`) so one client auth path handles both — with the subject-kind distinction carried **inside the opaque token**, not as a new public field (§3.1). A guest's `Session.user_id` is its `GuestSession.id` (an opaque subject id); clients never branch on the id's shape.

> **Design rule (server-authoritative identity).** The token is opaque to clients (API-CONTRACT §3.1 Auth: *"token minted by X-3, opaque here"*). Every authorization decision — "is this subject an account or a guest?", "may it enter a rated pool?" — is made **server-side** from the session record, never from a client-supplied claim. This mirrors the move-path rule (clients send intents, server decides) and closes T-11-style client-trust gaps on the identity surface.

### 3.0.1 Handle assignment (DECIDED — all account-creation paths) — was Q-ACC-HANDLE

`handle` is the unique public identifier (§3.0; DATA-MODEL `User.handle U` — NOT NULL, unique). Every account-creation path must populate it; the population strategy differs by whether the user supplied one, and every path uses the same collision resolution:

- **Register / upgrade** — the user **supplies** a `handle`; a taken one → `409 conflict` (the user picks another). No server generation.
- **OAuth-create / magic-link-create** — the user supplied **no** handle, so the server **generates a provisional unique handle** and the account is flagged `handle_provisional=true` (additive `User` field, §11.2): the user is **prompted to claim a permanent handle** on first authenticated use (a later S4/S5 UI step), and may keep the provisional one. Generation algorithm (deterministic, collision-safe): derive a candidate base (from the OAuth display name or the email local-part, slugified to the handle charset), then append a short random discriminator and **retry on the uniqueness constraint** (bounded retries, widen the discriminator on repeated collision) until the insert succeeds — the uniqueness check is the DB constraint, never a read-then-write race. A generated handle is **never null** and never guessable-by-position (the random discriminator prevents enumeration of "GoogleUser1, GoogleUser2…").
- **Guest** — has **no** handle at all (a `GuestSession` is not a `User`, §3.5); this rule does not apply to guests.

This closes the "uniqueness-constrained column with no defined population strategy on two creation paths" gap: all four paths now have an explicit, consistent, collision-safe handle origin.

### 3.1 Token & session model

**Session credential (token).** An **opaque, high-entropy bearer token** (not a client-readable JWT in v1). Rationale: the API-CONTRACT already fixes "opaque here"; an opaque server-side session reference gives instant server-side revocation (T-5) without JWT-revocation-list complexity, keeps all authorization claims server-side (the design rule above), and leaks nothing to a client or a logger. The token is a reference key; its claims live in the server session record.

**Session record (server-side).** Keyed by a hash of the token (never the raw token at rest — §7). Fields (shape proposed here, owner ratifies lifetimes in Q-ACC1):

```
Session (server-side record — NOT a public API body; the public body is API-CONTRACT Session{token,user_id,expires_at})
  token_hash      — hash of the bearer token (lookup key; raw token never stored)
  subject_id      — User.id (account) OR GuestSession.id (guest)
  subject_kind    — enum(account | guest)        -- account vs guest; input to rated_eligible (S2 reads rated_eligible, not this directly)
  account_state   — enum(active | restricted | tombstoned[never on a LIVE session]) | null  -- MIRRORED from User.account_state at issuance AND refreshed per-request (null for guests). Lowercase canonical casing (see §11.2). Only active|restricted|null appear on a live session — a tombstoned user has all login paths dead (§3.9.3) so no session is ever minted for it
  rated_eligible  — bool                          -- DERIVED, server-authoritative: (subject_kind==account AND account_state==active). A consented minor is account_state==active (kid_account=true but rated-enabled), an unconsented child is account_state==restricted → excluded; so account_state==active is the complete rated gate — no separate kid_account term (closes N-3). The single signal S2 reads for the rated-seek gate (§3.6) — folds BOTH ACC-INV-1 (guest) AND the RESTRICTED-child gate into one field so neither can be forgotten
  created_at
  expires_at                                       -- absolute lifetime (Q-ACC1)
  last_seen_at                                     -- idle-timeout + anomalous-login signal
  rotated_from?   — prior token_hash on rotation (audit chain, T-5)
  binding         — { ip_prefix?, user_agent_hash?, device_id? }  -- session binding (T-5; Q-ACC2 strength)
  mfa_level       — enum(none | mfa)              -- v1 = `none` always; `mfa` reserved for the deferred 2FA/WebAuthn option (Q-ACC8)
  revoked_at?
```

**Storage (ADR-10 role separation).** Session records are **hot, high-read, expiring** state → **Redis** (ADR-10: "Redis = hot/ephemeral… presence, rate-limit counters… never the source of truth"). The **durable account truth** (`User`, `Credential`) stays Postgres. A session is reconstructible (re-login) so Redis loss logs users out but loses no account truth — consistent with "Redis never the source of truth". The **audit trail** of logins/rotations/revocations (T-5, T-12) is written to the durable audit path (Postgres or the event/audit log), not only Redis. **Q-ACC3** confirms the session store choice with Architecture (it is the first component to pin Redis's auth role, which ADR-16's scope boundary left open per non-event-store component).

**Issuance.** Any successful auth (§3.2–3.5) mints a new session record + returns `Session{token,user_id,expires_at}`. Account creation additionally fires the ADR-8 entitlement grant (§3.10) and provisions `Profile` (F-46).

**Binding (T-5).** On issuance, the session records a binding fingerprint (§3.1 `binding`); a later request whose fingerprint diverges beyond the configured tolerance is a session-binding anomaly → re-auth challenge (not a hard fail by default, to avoid breaking mobile roaming). Binding strength (how strict, which signals) is **Q-ACC2**.

**Rotation.** The token rotates (a) on privilege elevation (guest→account upgrade, §3.7 — the old guest token is revoked and a fresh account token issued), (b) on MFA step-up, and (c) optionally on a sliding schedule. Rotation writes `rotated_from` for the audit chain. Rotation cadence is **Q-ACC1**.

**Expiry.** Two clocks: an **absolute** lifetime (`expires_at`) and an **idle** timeout (`last_seen_at` + idle window). Guests get a **shorter** lifetime than accounts (a guest is meant to be transient — exact values **Q-ACC4** for guests, **Q-ACC1** for accounts). Expiry is enforced server-side on every request; an expired token → `401 unauthenticated` (API-CONTRACT Appendix B).

**Revocation / invalidation.** `/v1/auth/logout` revokes the current session. A security event (password change, breach detection, user "log out everywhere") revokes **all** of a subject's sessions (device management, §3.8). Because the store is server-side and opaque-reference, revocation is immediate (the T-5 advantage over stateless JWT).

### 3.1.1 Shared session-issuance rule (every session-minting path) — closes B-1/B-2

> **Invariant ACC-INV-3 — account-state is re-read at issuance and enforced per request, never trusted from a one-time creation check.**

Every path that mints an account session — **login (§3.2), magic-link-verify of an existing user (§3.4), OAuth-login of an existing user (§3.3), and the creation pipeline (§3.10)** — MUST run this shared rule (the same "obligation lives in one shared place, not in one mechanism's prose" discipline as §3.10):

1. **Read the live `User.account_state` and `kid_account`** from durable truth (never a client claim, never a stale cached value).
2. **Stamp the session** with `account_state` and the **derived `rated_eligible`** (§3.1): `rated_eligible = (subject_kind==account AND account_state==active)`. A consented minor is `active` (and so rated-eligible; `kid_account=true` restricts *comms*, not rated play — N-3); an unconsented child is `restricted` and thus excluded. A guest gets `account_state=null`, `rated_eligible=false`. A `restricted` child gets `account_state=restricted`, `rated_eligible=false`.
3. **A `restricted` account still receives a valid session** (so the child can complete the consent flow and play unrated) — login is **not** refused — but the session advertises `restricted`, so the comms-muted / social-disabled / rated-disabled surfaces are enforced **per request** by their owning layers (below), not assumed from account creation.

**Enforcement owners (named, per the layer-gap discipline):**
- **Rated-play gate** → **S2** reads `rated_eligible` (NOT `subject_kind` alone — that was the B-2 gap: a RESTRICTED child is `subject_kind=account` and would have slipped the guest-only check). One field now folds both ACC-INV-1 (guests) and the RESTRICTED-child gate.
- **Signal transport, S2↔X-3 seam (S-6).** The `{subject_kind, account_state, rated_eligible}` fields live on the **server-side session record** (§3.1), NOT on the frozen public `Session{token,user_id,expires_at}` body. S2's WS `AUTHENTICATE` handler obtains them by **resolving the opaque token against the shared server-side session store** (the Redis session store of §3.1 / ADR-10), which is the single source both services read — **not** a copy on the public body and **not** a client claim. This is the same mechanism §3.0's server-authoritative design rule assumes; it needs no API-CONTRACT row because it is a shared-store read, not a new HTTP endpoint. (If a deployment prefers an RPC over a shared store, X-3 exposes a token-resolve API returning the same tuple; either way the public `Session` body is unchanged.)
- **Comms / social surfaces** (chat, challenges, public profile listing) → the **social/content services (S8 / S4 as applicable)** read `account_state==restricted` and mute/disable; named as a cross-stream obligation in §4. X-3 owns the **signal** (`account_state` on the session record + a `User.account_state` read API); the surface owners own the **enforcement**.
- Because the signal is refreshed at issuance (and re-readable per request), a consent capture that flips `restricted→active` takes effect on the next session issuance / state read — no stale-restriction and no stale-permission.

### 3.2 Email + password — `/v1/auth/register`, `/v1/auth/login`

Both endpoints already exist in the ratified API-CONTRACT (§3.2 catalog + Appendix A.2); this spec defines their **behaviour**, not their shape.

**Register (`POST /v1/auth/register`, auth: none → `201 Session`).**
1. Validate `{handle, email, password?, locale?}` (API-CONTRACT A.2). `handle` unique among live users (DATA-MODEL `User.handle U`); `email` well-formed; `password` meets the canonical policy pinned in **Q-ACC5** (**≥12 chars**, breach-check via a k-anonymity range API against a breached-password corpus, no composition rules per NFR-6/ASVS L2). The canonical minimum (≥12) lives in Q-ACC5 / §3.10 step 1; this and every password-setting path cite it rather than restating a number (S-7).
2. Create `User` (`email_enc` + `email_hash` per DATA-MODEL — the raw email is never stored plain), create a `Credential{type=password, secret_ref}` where `secret_ref` is a **vault pointer to the password verifier** (argon2id hash held in the vault, never a column — DATA-MODEL `Credential.secret_ref` is "a vault reference, NOT the secret").
3. Run the **shared account-creation pipeline (§3.10)** — provision `Profile` (F-46), run the **age gate / `kid_account` assignment (§3.9.1)**, and fire the ADR-8 entitlement grant. The age gate is **not** register-specific; it is step 2 of the shared pipeline (§3.10) and therefore applies identically to OAuth-create, magic-link-create, and upgrade.
4. Mint a session (§3.1), return `201 Session` (or route to the consent-and-restrict flow if the age gate set `kid_account=true`, §3.9.2).

**Login (`POST /v1/auth/login`, auth: none → `200 Session | 401`).**
1. Look up by `handle_or_email` (via `email_hash` for email). Verify the password against the vault verifier.
2. On success: breach-password check (T-5 — if the password appears in a breach corpus, succeed but flag for forced rotation), then mint a session **via the shared session-issuance rule (§3.1.1)** — which reads the current `User.account_state` and `kid_account` and stamps `account_state` + the derived `rated_eligible` into the session, so a `RESTRICTED` child account is re-gated on **this** login, not only at creation. Return `200 Session`.
3. On failure: `401 unauthenticated`. Apply per-account + per-IP throttle and lockout (§7, T-10). Never reveal whether the handle/email exists (uniform `401`, T-6-style enumeration guard).

Password storage: **argon2id** (ASVS L2), parameters pinned by the vault/service, never in client reach. The hash lives behind `secret_ref`; the service calls the vault to verify.

### 3.3 OAuth — `/v1/auth/oauth/{provider}`

Exists in API-CONTRACT (`POST /v1/auth/oauth/{provider}`, auth: none, req `{code, redirect_uri}` → `200 Session | 401`). **The frozen endpoint's return is left UNCHANGED — `200 Session | 401`, never a new body variant (closes B-2).** Behaviour:
1. Exchange the authorization `code` with the provider (server-side, standard OAuth 2.0 / OIDC authorization-code flow; the client never handles the provider secret).
2. Resolve the provider identity to a `User`:
   - **Match an existing `Credential{type=oauth, provider}`** → **OAuth-login** of that existing user → mint the session **via the shared session-issuance rule §3.1.1** (a RESTRICTED child is re-gated here) → **`200 Session`**.
   - **No existing account under the verified email** → **create a new account** via the shared account-creation pipeline (§3.10) incl. the **age gate (§3.9.1)** and `handle` per §3.0.1; an under-age OAuth sign-up lands `RESTRICTED` → **`200 Session`** (a RESTRICTED session, `rated_eligible=false`).
   - **The verified email matches an existing `User.email_hash`** → the sign-in **cannot complete** (no auto-link, no duplicate-email account — §3.3.1) → **`401 unauthenticated`**. The frozen endpoint simply does not mint a session; it returns the existing `401`, carrying no statement about *why* (so it is not an email-existence oracle beyond what `401` already is — an attacker already cannot tell "wrong credentials" from "must link" and gains nothing an ordinary failed OAuth wouldn't give). The linking affordance lives entirely in the NEW `/v1/auth/oauth/start` + `/v1/auth/oauth/link` flow (§3.3.1), NOT in a changed body on this frozen endpoint.
3. On the two session-minting cases, return `200 Session` exactly as the frozen contract specifies.

**Richer client flow (additive, §3.3.1 / §11.1).** Because a bare `401` is poor UX for "you already have an account — link instead," clients SHOULD use the NEW additive `POST /v1/auth/oauth/start` endpoint, which performs the same provider exchange but is free to return the richer `{status:"link_required"}` outcome (it is a NEW endpoint, so it mutates no frozen shape). The frozen `/oauth/{provider}` remains available and unchanged for any existing caller.

**Provider list (DECIDED — Q-ACC7).** v1 ships **three federated providers** plus the **two first-party credential paths**, for **five** total sign-up/login methods:

| Method | Mechanism | Notes |
|---|---|---|
| **Google** | OAuth 2.0 / OIDC | `/v1/auth/oauth/google` |
| **Apple** | Sign in with Apple (OIDC) | `/v1/auth/oauth/apple` — **required on iOS** by App Store Review §4.8 whenever any other third-party social login ships in the app, so Apple is non-optional once Google/Facebook are present on the mobile client; relay-email (`@privaterelay.appleid.com`) is a verified email for linking (§Q-ACC10) |
| **Facebook** | OAuth 2.0 | `/v1/auth/oauth/facebook` |
| **Email (magic-link)** | passwordless (§3.4) | first-class sign-up method, not only a recovery path |
| **Username + password** | first-party (§3.2) | `handle` + password |

The `{provider}` path param and `Credential.provider` column already generalise over this set; adding or removing a provider later is configuration, not a contract change. The provider whose verified email matches an existing account triggers the linking policy (§3.3.1).

**Provider-count note.** Five distinct methods — three federated (Google, Apple, Facebook) + two first-party (email magic-link, username/password).

### 3.3.1 OAuth account-linking policy (DECIDED — Q-ACC10) — no auto-link, closes B-4

A verified provider email proves the user controls **that provider account** — it does **NOT** prove they control a pre-existing PlayGlobalChess account registered under the same address. **Auto-linking on an `email_hash` match is therefore an account-takeover vector (T-5)** and is **forbidden**:

- **No existing account under that email** → create a new account (the §3.10 pipeline), as above.
- **An existing account under that email** → neither the frozen `/oauth/{provider}` nor the NEW `/oauth/start` **creates a `User` row or grants access** to the existing account. The frozen endpoint returns `401` (§3.3); the NEW **`POST /v1/auth/oauth/start`** returns the richer **`200 {status:"link_required"}`** (a NEW endpoint, so no frozen shape is mutated — closes B-2) carrying **no session token**. To actually link, the user completes an **explicit, authenticated confirm step** via **`POST /v1/auth/oauth/link` (NEW, §11.1)**: they must already hold (or establish, by logging in with the existing account's password or a magic-link to the on-file address) a **valid session for the existing account**, and that authenticated call attaches the `Credential{type=oauth, provider, secret_ref=provider-subject-ref}` to the **existing** `User`. The flow is non-circular: linking is an action taken *from within* an authenticated session of the pre-existing account, carrying the pending OAuth grant — never a path that logs the OAuth party in first. No second `User` row is ever created for the same email — this both prevents takeover **and** respects the frozen `DATA-MODEL User.email_hash U` uniqueness constraint (a "separate account under the same email" is impossible by construction and is explicitly NOT an outcome; closes B-1-of-round-2).
- There is **no "pending" dangling account state** and **no duplicate-email account**: until the confirm step completes, nothing is persisted for the OAuth identity against that email.

This replaces the ambiguous "link vs. separate" phrasing: the normative rule is **never auto-link; never create a duplicate-email account; link only via the authenticated `/v1/auth/oauth/link` confirm step**. (Apple private-relay gives a distinct per-app address, so it rarely collides with an existing `email_hash`; when it does, the same confirm-step rule applies.)

### 3.4 Passwordless (magic-link / email-code) — NEW: `POST /v1/auth/magic-link` (+ verify)

A credential model **distinct from the password path**: single-use, short-lived, **no stored secret** (`secret_ref` is password-only; a magic-link uses no `Credential.secret_ref`). Shares the `Session` output shape. Two steps (both NEW endpoints, §11 API-CONTRACT amendment):

1. **Request** — `POST /v1/auth/magic-link` req `{email}` → `202` (always `202`, even for an unknown email — never reveal account existence, T-6). The service mints a **single-use, short-lived token** (random, high-entropy), stores only its **hash** with an expiry and a consumed-flag, and sends the link/code out of band (email — delivery infra is an infra action, not built here). Lifetime + single-use semantics are **Q-ACC6**. **Abuse guard (S-1):** a **per-target-email** request throttle (not only per-IP/per-account) bounds how many links can be sent to one address in a window, so an attacker cannot mail-bomb a victim address; this cap applies to the magic-link path in v1 regardless of whether the OTP variant ships (§7 T-10).
2. **Verify/consume** — `POST /v1/auth/magic-link/verify` req `{token}` (from the link) → `200 Session | 401`. Validates: token hash exists, not expired, not already consumed. On success: mark consumed (atomic compare-and-set so a replay cannot double-consume), resolve the `User`; **a first magic-link login for a new email creates a new account via the shared account-creation pipeline (§3.10)** — `User` + `Profile` + entitlement grant + **age gate / `kid_account` (§3.9.1)**, with a `handle` populated per the **handle-assignment rule (§3.0.1)** (like register-without-password). An under-age magic-link sign-up lands in `RESTRICTED` pending consent (§3.9.2), identically to register. **For an existing user**, mint the session **via the shared session-issuance rule (§3.1.1)** — so a `RESTRICTED` child signing back in via magic-link is re-gated here too (B-1), not handed an unrestricted session. On failure (expired/consumed/unknown): `401`.

Design notes: the magic-link token is itself a bearer credential for one step, so it gets the **same at-rest discipline as the session token** (hash-at-rest, never logged, §7). It is **not** a `Credential` row — it is a transient issuance record (proposed storage shape in §11; it is ephemeral → Redis with a TTL, like sessions). An email-code variant (6-digit OTP) is the same model with a different delivery/entry shape and a stricter attempt limit (brute-force guard, T-10); whether v1 ships link, code, or both is folded into **Q-ACC6**.

### 3.5 Guest play — NEW: `POST /v1/auth/guest`

Instant, accountless, **unrated-only**. `POST /v1/auth/guest` req `{display_name?}` (auth: none) → `201 Session` (NEW endpoint, §11).

1. Create a `GuestSession` (ephemeral entity, §11 DATA-MODEL amendment): `id` (opaque), `provisional_display_name` (supplied or server-generated — e.g. "Guest-7F3A"; naming policy + validation/moderation is **Q-ACC11**), `created_at`, `expires_at` (short — **Q-ACC4**). **No** `User`, **no** `handle`, **no** `Profile`, **no** `Credential`, **no** `Entitlement`. **Supplied-name moderation failure (guest-name guard):** if a client-supplied `display_name` fails the moderation filter, the service **rejects with `400 validation`** (it does **not** silently substitute a generated name — a silent swap hides the rejection from the user); the client may retry or omit the field to get a server-generated `Guest-XXXX`.
2. Mint a session with `subject_kind=guest`, `subject_id=GuestSession.id`, `account_state=null`, `rated_eligible=false` (§3.1.1).
3. Return `201 Session` — same shape as any other auth, `user_id` = the guest id.

**Guest-minting abuse guard (guest-rate-limit).** `/v1/auth/guest` is unauthenticated and "free," which the spec leans on elsewhere as a *threat premise* (the enumeration reasoning in §3.7/§7) — so it must itself be bounded: a **per-IP guest-creation rate limit** (`429 + Retry-After`, §7 T-10) caps unlimited guest minting (session-store/Redis load, display-name farming). The "free guest" is free to a human, not to a botnet.

A guest can play **unrated** games only (§3.6 invariant). A guest has no persistent history; when its session expires, its ephemeral identity is destroyed. **Event-store subject reference (S-9):** a guest's unrated game events are written with a **non-identifying opaque subject token** (the `GuestSession.id`, which is a random opaque id — **never** the provisional display name, which is never written into the event stream as PII). So on expiry nothing further is required: no PII was ever written to the append-only stream, the opaque token simply resolves to nothing, and there is no dangling-PII or anonymisation gap (unlike an account, whose historical reference must be anonymised via `PublicUser` on erasure, §3.9.3). What becomes of the expired guest's unrated game *list reference* (vs. an upgrade that re-keys it) is **Q-ACC12**.

### 3.6 The unrated-only invariant (enforced, not advisory)

> **INVARIANT ACC-INV-1 — rated matchmaking requires an account; a guest is confined to unrated play.**

This is the load-bearing identity rule and the reason the guest/account split exists. It is what keeps **NFR-4** ("100% of rated games scored" by anti-cheat) and gates **G-B/G-C** clean *by construction*: only account-holders carry the persistent identity that anti-cheat correlation and Glicko-2 per-pool history require. It also aligns with the existing **F-01.3 rated/unrated toggle** (MASTER §II) — a guest simply cannot flip it to rated.

**Enforcement points (defence in depth — a single check is a single point of failure):**
1. **At the identity contract (here).** The session carries `subject_kind` **and** the derived **`rated_eligible`** flag (§3.1 / §3.1.1). `rated_eligible` is the authoritative "may enter a rated pool" signal — it is false for a guest **and** for a `RESTRICTED` child account (a RESTRICTED child is `subject_kind=account`, so a guest-only check would wrongly pass them — this is why the gate reads `rated_eligible`, not `subject_kind`; closes B-2).
2. **At the realtime seek path (S2).** The WS `SEEK` handler must reject a `rated=true` seek from any session whose **`rated_eligible=false`** — the server decides from the session, never from the client's `rated` flag (server-authoritative). This folds both ACC-INV-1 (guests) and the RESTRICTED-child gate into one read. Named as a **cross-stream obligation on S2** (§4), not something this spec can enforce alone — a rule spanning two layers must be pinned to one owner, with the other explicitly deferring (the S4 UNSUPPORTED_VERSION layer-gap lesson). **S2 owns the terminal rejection; X-3 owns the `rated_eligible` signal.**
3. **At rating application (X-4).** The rating service applies `RatingApplied` only to account subjects; a guest subject has no `Rating` row to write. Belt-and-braces: even if (2) failed, no rating is ever written for a guest.

The invariant is stated in all three specs' contracts (this one authoritative; S2 and X-4 cite ACC-INV-1).

### 3.7 Guest → account upgrade ("claim your games") — NEW: `POST /v1/auth/upgrade`

Lets a guest convert to a full account and keep their session's games. `POST /v1/auth/upgrade` (auth: **guest session required**) req `{handle, email, password? | oauth{...} | magic-link intent}` → **`202 {status:"verification_sent"}`** (verify-first; the account + rotated account session are minted only by `POST /v1/auth/upgrade/confirm` when the emailed claim link is consumed — NEW endpoints, §11). This two-step shape is deliberate: it is what closes the account-enumeration oracle (B-R4-1) by minting no probeable session until email control is proven.

1. Require a valid `subject_kind=guest` session (else `401`/`403`).
2. **Verify email ownership BEFORE creating any account (closes the enumeration oracle, round-4 B-R4-1).** Upgrade does **not** mint an account session synchronously on the taken/free-email distinction. The flow is: the server sends a **verification/claim link to the supplied email** and returns a **`202 {status:"verification_sent"}`** — the **same response whether or not that email already has an account**. No account is created, no session is minted, and no account-gated token is handed back at this step, so there is **nothing for the caller to probe** (this is why it closes B-R4-1: the prior "rotate the guest token" fix still left a usable token whose `subject_kind` was observable via `GET /v1/me`; returning no session at all removes the probe). The account is created (via the shared pipeline §3.10, incl. the age gate) **only when the user consumes the verification link** — proving they control the email. If the email already belongs to an account, the link instead says "you already have an account — log in to claim your games"; the **prober, who does not control the target email, never receives the link and cannot tell the two cases apart.** A guest who supplies **their own** email completes the round-trip and gets their account; an attacker probing someone else's email gets an identical `202` and no further signal.
3. **On verification-link consumption → create the account** via the shared account-creation pipeline (§3.10) — `User` + `Credential` + `Profile` + ADR-8 entitlement grant **+ age gate / `kid_account` (§3.9.1)** — then **re-key the guest's unrated game references** to the new `User.id` (provenance only, never a rating; Q-ACC12), **rotate the token** (revoke the guest session, mint a fresh account session), and **destroy the `GuestSession`**. This is the only path that mints an account session, and it is reachable only by someone who proved email control — so it discloses nothing to a prober.
4. **Handle collision is still synchronous** (handle is public): a taken **handle** → `409 conflict` at the initial call (mirrors register, §3.2); credential-policy failure → `400 validation`. Neither discloses account existence.

**Why this closes the oracle the prior fixes could not.** Every earlier attempt returned *some* `200 Session` on the taken-email branch, and a returned session is a capability the attacker can probe (its `subject_kind` leaks via `GET /v1/me` 200-vs-403). The only robust fix is to **return no account-bearing session until email control is proven** — so the synchronous response is identical (`202 verification_sent`) for taken and free emails alike, and the account-minting path is gated behind a link only the real email owner receives. This mirrors the §3.3.1 OAuth posture (existence is only ever disclosed to someone who controls the identity).

Net: a taken **handle** is a public `409` at the initial call; email existence is **never** disclosed because upgrade mints no account session synchronously — it returns the same `202 {verification_sent}` for taken and free emails and only creates the account when the emailed claim link is consumed (B-R4-1).

### 3.8 Device & session management (X-3 "device management")

- `GET /v1/me/sessions` (NEW, §11) → the subject's active sessions (device/binding summary, `last_seen_at`, current flag) — **account subjects only; a `subject_kind=guest` caller gets `403 forbidden`** (existing Appendix-B `forbidden` type — a guest has no device-management surface, and returning its own session would leak the guest/account branch to the client, which §3.0 forbids) (S-2).
- `DELETE /v1/me/sessions/{id}` and `POST /v1/me/sessions/logout-all` (NEW, §11) → revoke one / all sessions (the "log out everywhere" that a password change or breach also triggers, §3.1). **Guest caller → `403 forbidden`** (S-2).
- **`GET /v1/me`** (frozen endpoint, auth: user) → for an **account** subject returns the `User`+`Profile`; for a **guest** subject → **`403 forbidden`** (S-5): a guest has no `User`/`Profile`/`email`, and returning anything else would either 500 or leak the guest/account branch to the client (which §3.0 forbids). Same guest rule as `/v1/me/sessions*`. `GET /v1/me/entitlements` for a guest resolves to the fixed anonymous-unrated baseline (§3.10 / §4), not a `403`, because a guest legitimately has a (null) entitlement view.
- Device binding is the `binding` fingerprint (§3.1); a `device_id` the client supplies is an *advisory* label, never trusted for authorization (server-authoritative). Whether v1 ships full device management or only logout-all is **Q-ACC8** (folded with the 2FA question — both are "how much account-security surface in v1").

### 3.9 Compliance: age gate, parental consent, GDPR erasure (ADR-6, NFR-12) — DECIDED

The owner delegated this to the acting legal/compliance/trust-&-safety function (this spec). It is designed below, grounded in current practice at the two reference platforms (chess.com: no DOB at signup, self-attested minimum age, per-country age table, minor accounts auto-muted with social features disabled until a guardian consent form is returned, a separate under-13 walled-garden product; Lichess: GDPR delete with a trailing completion window, dormant-account auto-deletion, the game record retained as the public history). **This is engineering-grade compliance design, not a substitute for a licensed review before the G-F legal gate** — §3.9.4 names what still needs a lawyer's sign-off.

#### 3.9.1 Age thresholds & jurisdiction (DECIDED — was Q-ACC9)

- **No date of birth is stored.** The neutral age screen (**shared account-creation pipeline, §3.10 step 2** — runs on register, OAuth-create, magic-link-create, and upgrade alike) collects a DOB only to compute an outcome, then discards it; persisting a child's DOB is itself regulated PII we have no need to keep (data-minimisation, GDPR Art. 5(1)(c)).
- **Digital-consent age is jurisdiction-dependent.** The service resolves the applicable age from the user's declared/located jurisdiction against a **configuration table** (not a hard-coded constant), defaulting to **13** (COPPA / GDPR member-state floor) and rising where a jurisdiction sets it higher (GDPR Art. 8 permits 13–16; e.g. 16 in DE/NL/IE, 15 in FR, 14 in IT). The table is config so a new jurisdiction is an ops change, not a code change — the same shape as the chess.com per-country table. Under the resolved age → `kid_account=true` + consent flow (§3.9.2).
- **Two outcomes, not a hard block.** A user at/over the age proceeds normally. A user under it is **not rejected** (rejection just trains kids to lie about their age — the FTC's stated concern); they are routed to parental consent and held restricted. This is the design the reference platforms converged on.
- **ChessKid-style under-13 walled garden is OUT of v1 scope** (a separate product like chess.com's ChessKid); PGC v1 handles under-age users via the consent-and-restrict flow, and names the walled-garden option as a future product decision (§3.9.4).
- **Where the DOB is collected and bound (S-4 + closes round-2 B-1/B-3: no frozen-body change, no transferable bearer token).** None of the auth-create request bodies carry a DOB, and none may carry a client-computed `kid_account` (client-asserted = the T-9 violation the gate prevents). The age screen is a **dedicated pre-account step** `POST /v1/auth/age-screen` (NEW, §11.1) that posts the raw DOB; the server computes the outcome against the jurisdiction table, **discards the DOB**, and writes a **short-lived server-side pending-age record** keyed to a **flow id** (an opaque id the server returns and the client carries as a `flow_id` on the subsequent create call) **and bound to the same client/session fingerprint** that requested it (so the record is not a transferable bearer assertion — closes S-8). The create pipeline (§3.10 step 2) **reads that server-side record by `flow_id`** and sets `kid_account` from it; the assertion is never a signed token the client could hand to another subject.
  - **Frozen `register` is NOT changed (closes round-2 B-1).** `flow_id` is **not** a required field added to the frozen `register` body. A create call that presents no resolvable pending-age record is handled by the create pipeline's own policy (§3.10 step 2), **not** by a new mandatory field on the frozen endpoint: the pipeline treats "no age-screen record for this flow" as **age-unknown → route to the age screen** (the client is bounced to `/age-screen` and retries), rather than a `400` on a frozen-body validation. For the NEW create endpoints (`/oauth/start`, `/magic-link/verify`, `/upgrade`) `flow_id` is part of their new schema. So the age gate binds on every path, but the frozen `register` request/response shape is untouched — the binding is a **server-side flow record**, not a frozen-body field.
  - Net: the outcome is server-computed, server-stored, fingerprint-bound, and read server-side by the pipeline — T-9 is realized without mutating a frozen contract and without a transferable assertion.

#### 3.9.2 Verifiable parental consent flow (DECIDED — COPPA §312.5 / GDPR Art. 8) — was Q-ACC13 (consent half)

When `kid_account=true` is set by the age gate (§3.10 step 2) on **any** account-creation path (register / OAuth-create / magic-link-create / upgrade):

1. **Account created in `RESTRICTED` state.** The child account exists (so progress isn't lost) but with: comms **muted**, all social/UGC surfaces disabled, profile not publicly listed, and **rated play disabled** (independently of ACC-INV-1). It can play **unrated** chess against the engine or in unrated pools. This is the chess.com "muted until consent" pattern, stated as an explicit account state.
2. **Guardian contact captured, child PII minimised.** The flow collects a **parent/guardian email** (not more child data) and sends a consent request. Until consent, the only child PII held is what registration already required (`email_enc`/`email_hash`, `handle`) plus the `kid_account` flag — no DOB, no extra profile data.
3. **Verifiable consent method (COPPA §312.5(b)).** v1 uses **email-plus** (consent email to the guardian + a confirming follow-up step) as the baseline COPPA-acceptable method for an internal, no-disclosure-to-third-parties service; the flow is built so a stronger method (government-ID check, small-charge card verification, signed form) can be slotted in per-jurisdiction without changing the state machine. The *specific* verifiable method per market is the item still routed to a licensed reviewer (§3.9.4) — the **mechanism and state machine are decided here**, the legally-sufficient method-per-market is confirmed, not invented, at the G-F gate.
4. **On consent captured** → account transitions `RESTRICTED → ACTIVE` (`account_state=active`; social features and rated play enabled subject to ACC-INV-1 / `rated_eligible`); a consent record (who consented, when, method, scope) is written to the durable audit path (COPPA requires retaining consent evidence). The next session issuance (§3.1.1) picks up the new state.
5. **On consent refused or not captured within a window** (proposed **30 days**) → the restricted account is **purged** — holding a child's data with no consent is the violation. **Purge ≠ established-account erasure:** a never-consented child account had **no public presence** (profile not listed, never rated), so the purge **physically removes the `User` row and RELEASES the `handle`** back to the pool — it does **NOT** tombstone-and-reserve the handle. (Reserving it would both waste the namespace and create a trivial handle-squatting DoS: register "under-age," never consent, burn the handle forever.) This is explicitly distinct from §3.9.3 erasure of an *established* account, which **does** tombstone the handle. **Unrated-game events written while RESTRICTED (S-9):** a RESTRICTED child may have played unrated games (step 1), writing events under its `User.id`. On purge, those event references are **anonymised to a non-identifying opaque token** (the same `PublicUser`-style treatment §3.9.3 applies to an erased account) so the removed `User.id` is never a dangling identifier and no child PII (e.g. a display name) remains resolvable in the append-only stream; the events themselves stay (ADR-10 append-only), unattributed.
6. **Consent is revocable.** A guardian can withdraw consent later → account returns to `RESTRICTED` and, on request, is erased. COPPA requires the revocation path.
7. **`kid_account` gates downstream forever** (DATA-MODEL §7: "enforced in service logic; the flag lives here") — restricted comms stay enforced for a known-minor account even after consent, until the account ages out (a later re-attestation chunk; named in §3.9.4).

#### 3.9.3 GDPR/CCPA erasure boundary (DECIDED — GDPR Art. 17 / NFR-12) — was Q-ACC13 (erasure half)

The hard question is *what is erased vs. anonymised-and-retained*. The boundary, grounded in the Lichess pattern (erase the person, keep the game record as anonymised history) and the event-sourcing architecture (ADR-10: the event store is append-only truth):

**ERASED on a delete request (the person):**
- `User.email_enc`, `email_hash`, `secret_ref` (vault credential destroyed → all login paths dead), `Profile` free-text/PII (display name, bio, avatar, country if set), all `Session` records, all `MagicLinkIssue` records, OAuth `Credential` provider-subject links, device bindings, the parental-consent contact email and any `kid_account` PII.
- **The `User` row is RETAINED as a tombstone, not physically deleted (S-3):** erasure sets **`account_state=tombstoned`** and nulls the PII fields above. The retained tombstone row is what reserves the `handle` — so the explicit transition for the `account_state` enum's `TOMBSTONED` value is "GDPR/established-account erasure." (Physical-delete-the-row would free the handle and contradict Q-ACC14; the tombstone row is deliberate.)
- The `handle` is **released from the person but tombstoned** (held reserved on the TOMBSTONED row, not reassigned — Q-ACC14) so it can no longer resolve to them. (Contrast: a never-consented child *purge* releases the handle, §3.9.2 step 5 / S-4.)

**ANONYMISED-AND-RETAINED (the game record, not the person):**
- Game **events** in the append-only event store (ADR-10 truth; GDPR Art. 17(3) + recital 65 allow retention where erasure is technically incompatible with an append-only integrity-bearing record, and legitimate-interest/anti-cheat-integrity applies). The player reference in historical games becomes an **anonymised opaque id** via the `PublicUser` projection (DATA-MODEL §5: "`PublicUser` projections show an anonymized handle"). An observer sees "Anonymous" / a non-identifying token, never the erased person.
- **Anti-cheat/fair-play signals (NFR-4, G-B)** keyed to the anonymised id are retained as a **legitimate interest** (integrity of a competitive system) — this is the one place erasure is *bounded* by a stated lawful basis, and it must be in the privacy policy. This is also why guests (no persistent identity, unrated-only) carry none of this.

**Guests:** a `GuestSession` has **no erasure obligation** — its only PII is an ephemeral provisional name on a short TTL; it self-erases at expiry. A guest "delete" is a no-op.

**Statutory windows (DECIDED, proposed for legal confirmation):**
- **Export (Art. 15 / Art. 20 portability)**: fulfil within **30 days** (GDPR max, extendable once by 60 with notice). Returns account + profile + the subject's game-reference list in a portable format.
- **Erasure (Art. 17)**: acknowledge immediately, **complete within 30 days**; PII purged from live systems, projections re-anonymised, backups rotated out within the backup-retention cycle (named, not infinite). Lichess's own trailing window is comparable.
- **Dormant auto-deletion**: an unused account with no activity for a long window (proposed **24 months**) is auto-erased (the Lichess dormant-deletion pattern) — data-minimisation, reduces breach surface.

#### 3.9.4 Still requires a licensed legal reviewer before G-F (named, not a blocker for the spec)

The mechanism and state machine above are decided and buildable. A lawyer confirms, before the **G-F compliance gate** (not before this spec ratifies): (a) the *sufficient* verifiable-consent method per target market (email-plus may not suffice everywhere); (b) the exact per-jurisdiction digital-consent ages for the launch markets (the config table's values); (c) the final privacy-policy text for the anti-cheat legitimate-interest retention and the backup-rotation window; (d) whether any launch market needs the ChessKid-style under-13 walled garden rather than consent-and-restrict; **(e) whether a *consented* minor may play RATED/ranked chess — this spec's `rated_eligible` intentionally allows it (consent lifts the rated block; `kid_account` restricts only comms, Q-ACC-RATED-MINOR), but some markets restrict competitive features for minors regardless of parental consent, so the per-market answer is a named legal confirmation, not an open design question.** These are confirmations of a built mechanism, not open design — so they do **not** block `services/accounts` implementation, only the G-F launch gate.


### 3.10 Shared account-creation pipeline (register / oauth-create / passwordless-create / upgrade)

All four account-creation paths — register (§3.2), OAuth-create (§3.3), magic-link-create (§3.4), and guest→account upgrade (§3.7) — **MUST** run this single ordered pipeline so that no creation path can skip a cross-cutting obligation (the defect this spec closes: the age gate was previously defined only on register, letting an under-age OAuth/magic-link sign-up reach a full `ACTIVE` account and defeat T-9/COPPA). The pipeline is defined **once, here**, and each mechanism cites it rather than re-specifying creation:

1. **Create the identity rows** — `User` (+ the path's `Credential`, if any; magic-link creates none), with `handle` populated per the **handle-assignment rule (§3.0.1)**. Two row-population obligations live here so **every** path inherits them (not just register — the same discipline as the age gate):
   - **Password policy + breach-check (S-5)** — any path that **sets a password credential** (register, upgrade-with-password, any future password path) MUST apply the §3.2/Q-ACC5 policy: ≥12 chars, breach-check via the k-anonymity range API, reject a breached/weak password as `400 validation`. Previously this lived only in §3.2 prose; pinning it in the shared step closes the "upgrade-with-password can set a breached password" gap.
   - **`locale` population (S-6)** — `DATA-MODEL User.locale` is NOT NULL with default source `Accept-Language`. When the request body omits `locale`, populate it from the `Accept-Language` header; never leave it null. This covers OAuth/magic-link/upgrade paths that carry no `locale?` field.
2. **Age gate / `kid_account` assignment (§3.9.1)** — run the **neutral age screen** (a date-of-birth entry, not a yes/no "are you over 13?" — a yes/no toggle is not a neutral screen under FTC guidance and invites mis-answer). The DOB is **not stored**; only the derived outcome is persisted: compute age against the jurisdiction's digital-consent age (§3.9.1 table) and set `kid_account=true` when the user is **under that age**. This step is **identical on every creation path** — an OAuth or magic-link sign-up presents the same neutral age screen as register; a provider never supplies an age we trust. The age-screen outcome cannot be edited by the client afterward (server-authoritative, T-9).
3. **Consent-and-restrict branch (§3.9.2)** — if step 2 set `kid_account=true`, the account is created in **`account_state=restricted`** (comms-muted, social + rated disabled, unrated-only play allowed) and routed to the verifiable-parental-consent flow (§3.9.2); it does **not** become a normal `ACTIVE` account on any path. Otherwise `account_state=active`.
4. **Profile provisioning (F-46)** — create the `Profile`.
5. **Entitlement grant (ADR-8)** — write the baseline freemium `Entitlement` row(s), server-verified (T-11). The grant is **idempotent / repair-on-next-login** so a crash between row-create and grant cannot leave a permanently entitlement-less account (§8 restart walk). **Guests never enter this pipeline**, so they get no `Entitlement` (consistent with the guest model).
6. **Mint the session** via the **shared session-issuance rule (§3.1.1)** — `subject_kind=account`, with `account_state` + derived `rated_eligible` stamped from the row just created (a RESTRICTED child therefore gets `rated_eligible=false` from its very first session).

> **Invariant ACC-INV-2 — every account-creation path runs steps 1–6 in order.** No path may create a `User` without running the age gate (step 2). This is the structural fix for the register-only age-gate gap: the obligation lives in the shared pipeline, not in one mechanism's prose, so an implementer wiring a new OAuth provider cannot forget it. (The same layer-gap lesson that put the ACC-INV-1 rated-seek rejection under a named owner.)

**Entitlement timing detail (ADR-8).** The capability/plan values are owned by the ADR-8 entitlement model (tier boundaries are "a later product decision" per ADR-8) — this spec fixes **when** the hook fires (step 5, at creation) and **that** it is server-written, not **which** tiers exist (**Q-ACC15**). On **upgrade** (§3.7), the pipeline runs for the new account; the former guest held no `Entitlement`, so step 5 is a first grant, not a migration.

## 4. Interfaces & contracts

**Consumes:** `DATA-MODEL.md` entities `User`, `Credential`, `Profile`, `Entitlement` (cited, not redefined); the vault (secret storage, steering); the OAuth providers (external); the email channel (infra, for passwordless + verification).

**Exposes:** the auth REST surface — the three ratified endpoints (register/login/oauth) whose *behaviour* this spec defines, plus the NEW endpoints this spec requires (guest, upgrade, magic-link + verify, session management) which land as the §11 API-CONTRACT amendment.

**Emits (for other streams):** the authenticated **session** carrying `{subject_id, subject_kind, account_state, rated_eligible}` (§3.1), read by:
- **S2 (realtime)** — the WS `AUTHENTICATE` handler resolves a token to `{subject_id, subject_kind, account_state, rated_eligible}`; the `SEEK` handler rejects a `rated=true` seek whenever **`rated_eligible=false`** (folds ACC-INV-1 guests **and** RESTRICTED children). **Cross-stream obligation named on S2** (§3.6 point 2).
- **Social / comms surfaces (S8 chat/challenges, S4 public profile listing)** — read **`account_state==restricted`** to mute/disable comms and social features for an unconsented child. **Cross-stream obligation** (§3.1.1 enforcement owners); X-3 owns the signal, the surface owners own the enforcement.
- **X-4 (rating)** — applies ratings only to a subject with `rated_eligible=true` (§3.6 point 3 backstop).
- **ADR-8 entitlements** — reads `subject_kind` to resolve a guest's fixed baseline vs. an account's `Entitlement` rows.

**Changes to shared contracts.** This spec requires **additive** changes to two RATIFIED contracts (API-CONTRACT, DATA-MODEL). Per CONVENTIONS + API-CONTRACT §3.1 versioning, these are **owner-gated amendments** reviewed by both affected streams + architecture (+ S6 for the rated-play invariant). They are drafted in §11 as Status: REVIEW and are **NOT self-applied** — same change-control discipline as the ADR-17/18 frozen-wire amendments. They are additive `/v1` changes (new rows, new optional entity), not a `/v2` break.

## 5. Edge cases & failure modes

| Case | Contract behaviour |
|---|---|
| Login with wrong password | `401 unauthenticated`; per-account + per-IP throttle; lockout after N failures (§7, T-10). Uniform message — no handle/email enumeration (T-6). |
| Register with a taken handle | `409 conflict` (API-CONTRACT Appendix B `conflict`); handle uniqueness is DATA-MODEL `User.handle U`. |
| Register with an email already in use | **Uniform success-shaped response** (never a `409`) + an out-of-band "this email is already registered, log in?" email — the response must **not** reveal which emails are registered (T-6). **DECIDED, Q-ACC5** (no `409` enumeration branch). |
| Register by an under-age user (below jurisdiction digital-consent age) | Not rejected: `kid_account=true`, account created in `RESTRICTED` (comms-muted, social + rated disabled, unrated-only play allowed), routed to parental-consent flow (§3.9.2). DOB discarded. |
| Parental consent not captured within the window (30d) | `RESTRICTED` child account is **purged and its `handle` RELEASED** (reusable — a never-consented child had no public presence, so tombstoning would be a handle-squatting DoS, S-4) — holding a child's data without consent is the violation. Consent captured → `RESTRICTED→ACTIVE`; consent later revoked → back to `RESTRICTED`, erasable on request. |
| GDPR delete request on an **established** account | Sets `account_state=tombstoned` (S-3); person erased (email/secret_ref/profile PII, sessions, consent contact); `handle` **tombstoned+reserved** (not reusable, Q-ACC14 — contrast the never-consented purge above); game record anonymised-and-retained in the event store via `PublicUser`; completed within 30 days. |
| OAuth email matches an existing account | **No auto-link, no duplicate-email account (§3.3.1, B-1):** returns `200 {status:"link_required"}` (no token, not a login); linking requires the authenticated `/v1/auth/oauth/link` confirm step against the existing account. Never silent entry, never a second `User` under the same `email_hash`. |
| RESTRICTED child logs back in (password / magic-link / OAuth) | Session re-minted via §3.1.1 carries `account_state=restricted`, `rated_eligible=false` — the restriction is re-enforced on **this** sign-in, not only at creation (B-1). |
| RESTRICTED child's session hits a rated seek | Rejected on `rated_eligible=false` (NOT on `subject_kind`, which is `account` — B-2). S2 reads `rated_eligible`. |
| Magic-link token reused (replay) | Atomic consume (compare-and-set); a second verify → `401` (already consumed). Never double-mints a session. |
| Magic-link token expired | `401`; the user requests a new one. |
| Magic-link requested for unknown email | `202` anyway (no enumeration, T-6); no email sent / a generic "if an account exists…" message. Per-target-email request cap bounds mail-bombing (S-1). |
| Guest tries a rated seek | Rejected at the S2 `SEEK` path on `rated_eligible=false` (ACC-INV-1); the server decides from the session, never the client flag. X-4 is the backstop (§3.6 point 3). |
| Guest calls `/v1/me/sessions*` | `403 forbidden` (device management is account-only, S-2). |
| Supplied guest display-name fails moderation | `400 validation` (no silent substitution, S-8); client retries or omits for a generated name. |
| Guest session expires mid-interaction | `401`; client must re-guest or register. Guest event-store subject token was never the display name (S-9). Fate of unrated game refs → **Q-ACC12**. |
| Upgrade from a non-guest (account) session | `403` — upgrade is guest-only; an account is already upgraded. |
| Upgrade into a **handle** that already exists | `409 conflict` (handle is a public id — disclosing it is taken leaks nothing, same as register's handle `409`). |
| Upgrade into an **email** (taken OR free) | **Same `202 {verification_sent}` either way; no account session minted synchronously** (B-R4-1). A verification/claim link goes to the email; the account + rotated account session are created only on `/upgrade/confirm` when the link is consumed (proves email control). A prober who does not control the target email gets the identical `202` and no probeable token — the capability-probe oracle (prior rounds' rotated-guest-token still leaked `subject_kind` via `GET /v1/me`) is gone. Games re-keyed on confirm (Q-ACC12). |
| Expired session token on any request | `401 unauthenticated` (API-CONTRACT Appendix B). |
| Session-binding anomaly (fingerprint diverges) | Re-auth challenge (not a hard fail by default — mobile roaming); strictness is **Q-ACC2**. Anomalous-login is also a T-5 signal to the audit path. |
| Password appears in breach corpus at login | Login succeeds but flags forced rotation (T-5); surfaced to the user, not a hard block (avoid lockout-by-breach). |
| "Log out everywhere" / password change | Revoke **all** the subject's sessions immediately (server-side store makes this instant). |
| Token leaked in a log/URL | Mitigated by hash-at-rest + never-logged discipline (§7); a leaked *live* token is revocable immediately (opaque server-side session). Magic-link tokens are single-use + short-lived to bound the window. |
| Redis (session store) loss | Users are logged out (must re-auth); **no account truth lost** (Postgres is truth; sessions are reconstructible) — consistent with ADR-10 "Redis never the source of truth". |

## 6. Non-functional handling

- **NFR-6 (ASVS L2).** argon2id password verifier in the vault (§3.2); opaque server-side sessions with immediate revocation (§3.1); session binding (§3.1); input validation on every body (API-CONTRACT §3.1); HTTPS-only; secrets in a vault not columns (DATA-MODEL `secret_ref`); RFC-7807 errors that never leak internals (API-CONTRACT Appendix B).
- **NFR-12 (compliance).** Export/delete hooks + tombstone path (§3.9.3); `kid_account` COPPA gate + verifiable-consent state machine (§3.9.2); GDPR-baseline-worldwide (ADR-6) with the erasure boundary + statutory windows **DECIDED in §3.9.3** (licensed-reviewer confirmations gated to G-F, §3.9.4).
- **Read-path latency (§IX.1).** Auth is a write-ish path; session *validation* on every subsequent request is a single hot keyed lookup in Redis (§3.1) — designed to stay well inside the read budget. The measured number is proven under load (G-E), not here.
- **NFR-4 (indirect).** ACC-INV-1 guarantees every *rated* subject is an account with persistent identity — the precondition anti-cheat (G-B) and ratings (G-C) assume.

## 7. Security & abuse

Part XI threats this surface owns, each with its control:

- **T-5 account takeover.** argon2id verifier (vault); opaque server-side sessions → **instant revocation**; session binding + anomalous-login challenge; rotation on elevation/step-up with an audit chain (`rotated_from`); breach-password check at register/login; optional 2FA/WebAuthn (DATA-MODEL `Credential.type` already includes `webauthn`) — v1 scope of 2FA is **Q-ACC8**. Tokens are hashed at rest (never the raw token in Redis or logs) and never placed in a URL (magic-link verify takes the token in the body, not a logged query string where feasible — the link transport is the one exception, mitigated by single-use + short TTL).
- **T-10 credential stuffing / API abuse.** Per-IP **and** per-account rate limits with `429 + Retry-After` (API-CONTRACT §3.1/§7); progressive lockout/backoff after repeated failures; a bot-challenge hook on `/v1/auth/*` (API-CONTRACT §7 names this hook); email-code (if shipped) gets a strict attempt cap (brute-force guard). Lockout thresholds are **Q-ACC5** (tuned to not become a self-DoS vector — a locked-out legitimate user must have a recovery path, i.e. passwordless/reset).
- **T-9 child safety.** `kid_account` gates restricted-comms surfaces (§3.9); COPPA consent flow referenced (ADR-6). The flag is set server-side by the age gate in the **shared account-creation pipeline (§3.10 step 2)** — on **every** creation path (register, OAuth-create, magic-link-create, upgrade), never client-asserted and never skipped by a federated/passwordless sign-up.
- **T-6 (enumeration, borrowed).** Uniform responses on login failure, magic-link request, register, **and guest→account upgrade** — never reveal which handles/emails exist. The upgrade path is explicitly included because a guest session is free (minted with zero credentials via `/v1/auth/guest`), so an email-keyed `409` there would be a zero-cost enumeration oracle; upgrade returns `409` only on a **public handle** collision, and the **uniform success shape** on an email collision (§3.7).
- **NFR-6 input validation / least privilege.** Every body validated against the API-CONTRACT schema; the accounts service holds least-privilege access to the vault and the PII columns; PII access is audit-logged (T-12, DATA-MODEL §7).
- **S6 fair-play veto.** ACC-INV-1 (guests unrated-only) is a fair-play-load-bearing rule; **S6 reviews this spec** and holds veto on any change that would let an unauthenticated/ephemeral subject into rated play (CONVENTIONS: "a spec touching rated play… also needs S6 review").

## 8. Test plan

The contract's conformance corpus (CONVENTIONS: the test plan is part of the spec). Edge-first (steering §30 adversarial review):

- **Session lifecycle.** Issue → validate → expire (absolute + idle) → `401`; revoke → immediate `401`; rotate on upgrade → old token dead, new token live, `rotated_from` chain intact.
- **Mechanism conformance.** register (runs the §3.10 pipeline: creates `User`+`Credential`+`Profile`+`Entitlement`, fires the hook once, runs the age gate); login (success/fail/lockout, uniform-failure enumeration guard; **register-with-existing-email returns the uniform success shape, never `409`** — Q-ACC5 enumeration guard); oauth (new-via-§3.10-pipeline vs. existing-link vs. collision); magic-link (request→verify happy path, new-email verify runs the §3.10 pipeline; **replay → single `401`**; expired → `401`; unknown email → `202` no-leak); guest (creates `GuestSession`, **no** `User`/`Profile`/`Entitlement`, **does not** enter the §3.10 pipeline); upgrade (verify-first: initial call → `202 {verification_sent}`, identical for taken/free email, NO synchronous account session; `/upgrade/confirm` consuming the emailed link runs the §3.10 pipeline, re-keys game refs, rotates to an account session, destroys `GuestSession`; non-guest → `403`; **taken-handle → `409`** synchronously; assert the initial call returns no account-gated token so there is nothing to capability-probe — B-R4-1).
- **ACC-INV-1 (the load-bearing invariant) — prove it has teeth.** A `subject_kind=guest` session attempting a `rated=true` seek is rejected; assert the rejection is driven by `subject_kind` server-side, **not** by the client's `rated` flag (set the client flag to the "wrong" value and confirm the server still decides correctly — mirrors the "prove the e2e has teeth" lesson). Assert X-4 writes **no** `Rating` row for a guest even if the seek guard is bypassed in the test (belt-and-braces test).
- **Per-field write-coverage (steering §30 pass 3).** Every `Session` record field written on every path that should (e.g. `rotated_from` set on rotation, `revoked_at` on logout, `subject_kind` on both guest and account issuance — the guest path must not leave it defaulted to `account`).
- **Callee-contract enumeration (steering §30 pass 1).** For the vault verify, the OAuth code-exchange, and the atomic magic-link consume: enumerate each callee's success/failure/edge returns and prove each is handled (the consume's "already consumed" branch is the replay guard; a vault "verifier not found" is a `401`, not a `500`).
- **Failure/retry/restart walk (steering §30 pass 2).** A client **re-submitting register** — the frozen API-CONTRACT defines **no `Idempotency-Key` for the auth-create endpoints** (idempotency is required only on payment/entitlement + challenge writes), so a second register is **not** a replayed success: it resolves to the handle-`409` / uniform-email path (§3.2 / Q-ACC5), and the test asserts **that** behaviour, not a replayed `201 Session` (S-7 — the earlier "idempotency" wording was unsatisfiable against the frozen shape). A crash between `User` create and entitlement grant (is the account left without entitlements? — the grant must be idempotent / repair-on-next-login, §3.10 step 5); a magic-link verify racing a second verify of the same token (atomic consume serialises).
- **Security.** Token never stored raw (assert only a hash at rest); token never in a log line (assert); breach-password path flags not blocks; lockout triggers and *clears* on a recovery path (not a permanent self-DoS).
- **RESTRICTED re-enforced at every sign-in (ACC-INV-3 / §3.1.1 — closes B-1/B-2).** Create an under-age account (lands RESTRICTED), end the session, then **log back in via password AND via magic-link-verify AND via OAuth-login**; assert each re-minted session carries `account_state=restricted` and `rated_eligible=false` — the restriction is re-read at issuance, not a one-time creation check. Assert the **emitted session contract** (§4) includes `account_state` + `rated_eligible`, and that an S2-style `rated=true` seek from a RESTRICTED child's session is rejected on `rated_eligible=false` (the B-2 gap: it must NOT pass merely because `subject_kind=account`). After consent → ACTIVE, the next issued session flips `rated_eligible=true`.
- **OAuth no-auto-link + no-duplicate-email + frozen-endpoint-unchanged (§3.3 / §3.3.1).** With an existing email+password account under `alice@x.com`: the frozen `/oauth/{provider}` returns **`401`** on the collision (no session, no new row, frozen shape intact); the NEW `/oauth/start` returns **`200 {status:"link_required"}`** (no token); neither creates a second `User` (assert the `email_hash U` constraint is never violated / no second insert attempted); linking succeeds only via `POST /v1/auth/oauth/link` from **within an authenticated session of the existing account**. Assert the frozen `/oauth/{provider}` body on the session-minting paths is still exactly `Session` (no `link_required` variant on the frozen endpoint). An OAuth-bearing `/upgrade` whose provider email collides yields `link_required`, never a new account.
- **Age-screen flow binding (§3.9.1 / S-4-age / S-8 / frozen-register-unchanged).** `POST /v1/auth/age-screen` **persists no DOB** (assert) and writes a fingerprint-bound server-side pending-age record keyed to `flow_id`; a create call sets `kid_account` from that record, never from a client-sent flag (assert a client-sent `kid_account` is ignored). Assert the record is **not transferable**: a `flow_id`/record minted under one client fingerprint cannot be presented by a different subject (S-8). Assert the **frozen `register` body is unchanged** — register with no pending-age record is routed to the age screen, not failed on a missing frozen-body field (round-2 B-1).
- **Upgrade taken-email is not an oracle — including the capability probe (B-R4-1).** Attempt `/upgrade` into a **registered** email and an **unregistered** email; assert BOTH return the **identical `202 {verification_sent}`** and that **neither returns an account-gated session token** — so the round-4 attack (take the returned token, call `GET /v1/me`, read 200-vs-403) has no token to use. Assert the account is created only via `/upgrade/confirm` with a link token, and that a caller who does not possess the emailed link cannot advance either case. Assert taken-handle still `409` synchronously.
- **Enum casing (S-1-round-1).** Assert `account_state` stored on `User` and mirrored onto the session use the **same lowercase tokens**, so `rated_eligible`'s `account_state==active` test and the social-surface `==restricted` test actually match.
- **Consented-minor is rated-eligible (Q-ACC-RATED-MINOR / round-2 S-1).** After an under-age account gets consent → `active` with `kid_account=true`, assert `rated_eligible=true` (consent enables rated; `kid_account` only mutes comms) — the deliberate, owner-ratified behaviour, flagged for the G-F legal reviewer.
- **Compliance — age gate runs on EVERY creation path (ACC-INV-2).** Prove the age gate is not register-only: run an **under-age sign-up through each of register, OAuth-create, magic-link-create, and upgrade** and assert **all four** land in `account_state=restricted` (comms-muted, rated disabled) with `kid_account=true`, never a full `ACTIVE` account (this is the regression test for the register-only-age-gate defect — a test that would fail on the pre-fix spec where OAuth/magic-link skipped the gate). Age gate sets `kid_account` under the jurisdiction age and **discards DOB** (assert no DOB persisted on any path). Consent captured → `RESTRICTED→ACTIVE` + consent evidence written to the audit path; consent not captured within the window → **account purged and its `handle` RELEASED** (S-4: a never-consented child's handle is reusable, NOT tombstoned); consent revoked → back to `RESTRICTED`. Erasure of an **established** account: sets `account_state=tombstoned` (S-3), anonymises `PublicUser` projections (DATA-MODEL §5) and **tombstones** the `handle` (reserved, not reusable) while the event-store game history + anti-cheat signals are retained anonymised (§3.9.3); assert the erased person's email/secret_ref/profile PII are gone and all login paths dead; guest delete is a no-op and a guest's event-store subject token was never the display name (S-9). Export returns account+profile+game-ref list within the window.
- **Handle assignment (§3.0.1 / ACC-INV-2) on every path.** register/upgrade with a taken handle → `409`; OAuth-create and magic-link-create with **no** supplied handle → a **non-null, unique** handle is generated, `handle_provisional=true` is set, and the user is flagged to claim one; force a generation collision (seed the DB with the first candidate) and assert the retry widens the discriminator and still produces a unique non-null handle (never a null-constraint violation, never a position-guessable sequence); a guest has no handle.
- **Abuse guards (S-1/S-8).** A per-target-email cap bounds magic-link **request** volume to one address (not just per-IP); a per-IP cap bounds guest minting; a supplied guest `display_name` that fails moderation → `400 validation` (not a silent swap); a guest calling `/v1/me/sessions*` → `403` (S-2).
- **Credential policy on every password-setting path (S-5).** A **breached/weak password supplied at upgrade** (not just register) → `400 validation` — the shared-pipeline breach-check fires on upgrade-with-password, not only register.
- **Contract-schema conformance.** The new endpoints' request/response bodies validate against the §11 Appendix-A amendment, run as part of the shared contract suite (NFR-10) once the service exists (R2, deferred with API-CONTRACT §9 R2).

**Not tested here:** rating math (X-4/G-C), anti-cheat detection (X-5/G-B), email-delivery infra, the OAuth provider's own flow, load/latency (G-E).

## 9. Acceptance criteria (Definition of Done)

This spec is DONE (ready to leave REVIEW for RATIFIED) when:
- [ ] All five mechanisms (email+password, OAuth, passwordless, guest, upgrade) have a defined per-endpoint behaviour + state transitions, citing DATA-MODEL entities and never redefining fields.
- [ ] **All four account-creation paths (register / oauth-create / passwordless-create / upgrade) run the single shared creation pipeline (§3.10), so the age gate / `kid_account` assignment (ACC-INV-2) and the handle-assignment rule (§3.0.1) apply on every path — not register-only.**
- [ ] The token/session model (format, issuance, binding, rotation, expiry, revocation, storage) is unambiguous, with every lifetime/strength parameter **DECIDED** in the §10 ledger (not left ambiguous for code time).
- [ ] ACC-INV-1 (guests unrated-only) is stated with its three enforcement points and the S2/X-4 cross-stream obligations named.
- [ ] The guest-identity model is defined as distinct from `User`, with the §11 DATA-MODEL amendment drafted (not self-applied).
- [ ] The §11 API-CONTRACT amendment (new endpoint rows + Appendix-A schemas) is drafted REVIEW (not self-applied).
- [ ] Security posture (§7) maps T-5/T-10/T-9 to concrete controls; the compliance design (§3.9.1–§3.9.4) specifies the COPPA consent state machine and GDPR erasure boundary, with licensed-reviewer confirmations named as a **G-F gate item** (not open design).
- [ ] The ADR-8 entitlement hook firing point is pinned (at account creation; guests excluded).
- [ ] Every §10 ledger item is **DECIDED** with a recorded basis; nothing is left for discovery at code time.
- [ ] Reviewed by S3 + PE + S2 + S6 (fair-play) + architecture group (it touches two shared contracts + the rated-play invariant).
- [ ] **Owner ratifies** the spec AND the §11 amendments before any `services/accounts` code is written (build manual §0).

## 10. Decisions ledger (all items DECIDED this round; legal confirmations named in §3.9.4)

The owner accepted the engineering defaults, named the OAuth/sign-up set, and delegated the COPPA/GDPR design to the acting compliance function (this spec). Every Q-item below (the original 15 plus Q-ACC-HANDLE, surfaced during review) is now **DECIDED**; the only residue is a set of *confirmations of a built mechanism* that a licensed reviewer signs off before the **G-F launch gate** (§3.9.4) — these do **not** block the `services/accounts` build chunk.

| Q | Decision (ratified value) | Basis |
|---|---|---|
| **Q-ACC1** | **DECIDED:** account session 30-day absolute, 14-day idle, rotate on privilege elevation + MFA step-up | consumer-app norm; opaque server-side sessions give instant revocation regardless |
| **Q-ACC2** | **DECIDED:** soft (challenge) binding on IP-prefix + UA-hash; `device_id` advisory-only; divergence → re-auth challenge, not hard-fail | avoids breaking mobile roaming/CGNAT while closing the T-5 hijack window |
| **Q-ACC3** | **DECIDED:** session store = **Redis** (hot/ephemeral/reconstructible); **not** an ADR-scope expansion — straight application of ADR-10's Redis role (ADR-16 pinned only the event store) | ADR-10 role separation |
| **Q-ACC4** | **DECIDED:** guest session 24h absolute; game refs dropped on expiry unless upgraded (§3.9.3 / Q-ACC12) | a guest is transient; longer = shadow-account PII liability |
| **Q-ACC5** | **DECIDED:** ≥12 chars, breach-check via k-anonymity range API, no composition rules (ASVS L2 / NIST 800-63B); exponential per-account + per-IP backoff with a passwordless/reset recovery path (never a permanent self-DoS); register returns a uniform success-shaped response + out-of-band "already registered" email (no `409` enumeration leak, T-6) | NFR-6 / ASVS L2 |
| **Q-ACC6** | **DECIDED:** 15-min single-use magic-link in v1 (atomic compare-and-set consume); 6-digit email-code OTP variant deferred (additive later) | ship one passwordless shape faster |
| **Q-ACC7** | **DECIDED:** **Google, Apple, Facebook** (OAuth) + **Email magic-link** + **username/password** — **five** sign-up/login methods (three federated + two first-party, §3.3). Apple is non-optional on iOS per App Store §4.8 once any other social login ships | owner-named set |
| **Q-ACC8** | **DECIDED:** v1 = sessions list + logout-all; WebAuthn/TOTP opt-in deferred (`Credential.type=webauthn` already reserved → additive) | covers "lost my laptop" without TOTP enrollment UX cost |
| **Q-ACC9** | **DECIDED (§3.9.1):** neutral DOB age screen, DOB **not stored**, jurisdiction age-config table defaulting to 13; under-age → `kid_account=true` + consent-and-restrict, not rejected | chess.com pattern + FTC neutral-screen guidance |
| **Q-ACC10** | **DECIDED (§3.3.1):** **never auto-link** on an `email_hash` match — a verified provider email proves control of the provider account, not of a pre-existing PGC account; linking requires an **authenticated confirm step** (log into the existing account), else the OAuth sign-in creates a separate/pending account | auto-link on email match is an account-takeover vector (T-5) — closes B-4 |
| **Q-ACC11** | **DECIDED:** server-generated `Guest-XXXX` default; a user override is validated + run through the same moderation filter as handles | no unfiltered free-text on an opponent-visible surface (T-8) |
| **Q-ACC12** | **DECIDED:** on upgrade, re-key the guest's **unrated game list** to the new `User.id` (provenance only, never a rating); on expiry, drop the refs (events stay in the event store, unattributed); upgrade into an existing **handle** → `409` (public id), upgrade into an existing **email** → **no synchronous disclosure: the same `202 {verification_sent}` for taken and free emails, account minted only on `/upgrade/confirm` (emailed link consumed), so no probeable session is returned** (closes the B-R4-1 capability-probe oracle the earlier rotate-guest-token fix left open), **no** merge-into-existing in v1 | "claim your games" = the unrated list; cross-account merge is a later feature; the email branch must not become a guest-session-powered enumeration oracle |
| **Q-ACC13** | **DECIDED (§3.9.2 + §3.9.3):** COPPA email-plus verifiable-consent state machine (RESTRICTED→ACTIVE, 30-day purge-if-unconsented, revocable); GDPR erasure boundary (erase the person, anonymise-retain the game record + anti-cheat legitimate-interest), 30-day export/erasure windows, 24-month dormant auto-delete. Licensed-reviewer confirmations are named in §3.9.4 as a **G-F gate item**, not open design | COPPA §312.5 / GDPR Art. 8 & 17; chess.com + Lichess patterns |
| **Q-ACC14** | **DECIDED:** tombstoned `handle` **not** reclaimable (held reserved) | impersonation risk + breaks external references (PGN archives, links) |
| **Q-ACC15** | **DECIDED:** new account = freemium baseline `Entitlement` at creation; guest = fixed anonymous-unrated baseline (no `Entitlement` row). Tier *names* remain owned by the ADR-8 entitlement model | ADR-8 hook fires at creation; this pins *when*, not *which tiers* |
| **Q-ACC-HANDLE** | **DECIDED (§3.0.1):** register/upgrade take a user-supplied handle (`409` on collision); OAuth/magic-link **generate** a unique handle (slug base + random discriminator, retry on the DB uniqueness constraint) flagged `handle_provisional=true` and prompt the user to claim one; guests have no handle | closes the undefined handle-population gap on the two no-supplied-handle creation paths |
| **Q-ACC-AGESCREEN** | **DECIDED (§3.9.1 / §11.1):** the age gate is a dedicated `POST /v1/auth/age-screen` that takes a raw DOB, computes the outcome, **discards the DOB**, and writes a short-lived **server-side pending-age record keyed to `flow_id` and bound to the caller's fingerprint** (not a transferable signed token); the create pipeline reads it and sets `kid_account` (never a client-sent flag). **Frozen `register` body is unchanged** — no `flow_id` field is required on it; the pipeline resolves the record from the caller's flow/fingerprint and routes to the age screen if absent | makes ACC-INV-2's "age gate on every path" server-authoritative without mutating the frozen `register` shape and without a transferable assertion (closes S-4-age/S-8) |
| **Q-ACC-RATED-MINOR** | **DECIDED (§3.9.2 / §3.1.1):** a **consented** `kid_account` (account_state=active) **IS rated-eligible** — `kid_account` restricts *comms*, not rated play; `rated_eligible` deliberately omits a `kid_account` term. This is an intentional product call (not a formula side-effect) and is named for the G-F legal reviewer (§3.9.4) in case a market restricts ranked play for minors regardless of consent | surfaced explicitly so the owner/legal ratify it knowingly (round-2 S-1) |

> **No open design remains.** The spec made no silent assumptions: each decision above is recorded with its basis. The §3.9.4 legal confirmations are gated to **G-F (launch)**, downstream of implementation — a lawyer confirms a built mechanism rather than designing it. The §11 contract amendments still need the formal cross-stream (PE/S3) + architecture + S6 sign-off before PE/S3 apply them to the frozen files; that is a review step, not an undecided question.

## 11. Additive amendments to the two RATIFIED contracts (Status: REVIEW — owner-gated, NOT self-applied)

Guest + passwordless + upgrade + session-management add surface the frozen specs do not have. These amendments are drafted here for review and are applied **only after owner ratification**, by the owning stream (PE for API-CONTRACT, S3 for DATA-MODEL), with the same change-control as the ADR-17/18 frozen-wire process (both affected streams + architecture + S6). The **API-CONTRACT** changes are **purely additive** — only NEW endpoint rows + a NEW optional entity; **no frozen endpoint's request body or response shape is changed** (the link-aware OAuth outcome lives on the NEW `/oauth/start`, not the frozen `/oauth/{provider}`; the age gate binds via a server-side flow record, not a new field on frozen `register` — round-2 B-1/B-2). The **DATA-MODEL** change has **one genuine frozen-semantic amendment, surfaced explicitly as such** (not hidden as additive): the `handle` uniqueness **scope** moves to `{live ∪ tombstoned}` with `account_state` as discriminator (§11.2, B-3). Everything else is additive (new entities, new optional `User` columns). The frozen F-01 slice is untouched.

### 11.1 `API-CONTRACT.md` — new endpoint rows (additive to §3.2 "Auth & accounts") + Appendix A schemas

New catalog rows:

| Method | Path | Auth | Serves | Returns |
|---|---|---|---|---|
| POST | `/v1/auth/age-screen` | none | X-3 (age gate, §3.9.1) | `{flow_id}` (server-side pending-age record; DOB discarded) |
| POST | `/v1/auth/guest` | none | X-3 (guest play) | session (guest subject) |
| POST | `/v1/auth/oauth/start` | none | X-3 (OAuth sign-in/sign-up with link-aware outcome, §3.3.1) | session OR `{status:"link_required"}` |
| POST | `/v1/auth/magic-link` | none | X-3 (passwordless) | `202` (link/code sent out of band) |
| POST | `/v1/auth/magic-link/verify` | none | X-3 (passwordless) | session |
| POST | `/v1/auth/upgrade` | guest | X-3 (guest→account; verify-first) | `202 verification_sent` |
| POST | `/v1/auth/upgrade/confirm` | none (link token) | X-3 (consume claim link → account session) | session |
| POST | `/v1/auth/oauth/link` | user | X-3 (confirm OAuth link to existing acct, §3.3.1) | `204` |
| GET | `/v1/me/sessions` | user | X-3 (device mgmt) | active-session list |
| DELETE | `/v1/me/sessions/{id}` | user | X-3 (device mgmt) | `204` |
| POST | `/v1/me/sessions/logout-all` | user | X-3 (device mgmt) | `204` |

> **Frozen endpoints are UNCHANGED.** `/v1/auth/register`, `/v1/auth/login`, `/v1/auth/oauth/{provider}`, `/v1/auth/logout`, `GET /v1/me`, `GET /v1/me/entitlements` keep their frozen request/response **shapes** exactly (closes round-2 B-1/B-2). New behaviour (link-aware OAuth, the age-gate binding, guest/upgrade/passwordless, session mgmt) lives entirely in the NEW endpoints above.
>
> **One behavioral amendment on frozen endpoints, surfaced explicitly for ratification (round-4 S-R4-2):** guests are a new subject kind that did not exist at freeze yet satisfy "auth: `user`" (they hold a valid session). On `GET /v1/me`, `GET /v1/me/entitlements` and the `/v1/me/sessions*` additions, a **guest subject receives `403 forbidden`** (existing Appendix-B type; no request/response *shape* change, no new error type). This is an additive, error-only behavioral amendment to those frozen endpoints — it changes no shape but it is a new required behavior for a valid credential, so it is called out here as owner-ratified, not folded silently into §3.8. `GET /v1/me/entitlements` for a guest returns the fixed anonymous-unrated baseline (not 403), per §3.8.
>
> The only frozen-contract **semantic** change requiring owner ratification is the **DATA-MODEL amendment** in §11.2 (new entities + additive `User` columns + the explicitly-stated `handle` uniqueness-scope amendment, B-3).

New Appendix-A schemas (language-neutral JSON; cite DATA-MODEL; `?` optional):
```
POST /v1/auth/age-screen       req { dob: date }                                  → 200 { flow_id: str }
                                 // server computes outcome vs jurisdiction table, DISCARDS dob, writes a
                                 //   short-lived server-side pending-age record keyed to flow_id AND bound
                                 //   to the caller's client/session fingerprint (not a transferable token,
                                 //   closes S-8). No DOB persisted (S-4, §3.9.1).
POST /v1/auth/guest            req { display_name?: str }                         → 201 Session
                                 // guest play needs no flow_id (unrated-only, no account, no PII store)
POST /v1/auth/oauth/start      req { provider: str, code: str, redirect_uri: str, flow_id?: str }
                                                            → 200 Session | 200 { status:"link_required" } | 401
                                 // NEW endpoint (mutates NO frozen shape, closes B-2): the link-aware entry.
                                 //   existing-oauth-login or clean create → Session (create carries flow_id
                                 //   for the age gate); existing-email collision → {status:"link_required"}
                                 //   (no token); provider error → 401. The frozen /oauth/{provider} stays
                                 //   pure 200 Session | 401 and is UNCHANGED (§3.3).
POST /v1/auth/oauth/link       req { provider: str, code: str, redirect_uri: str }
                               (auth: a valid user session of the EXISTING account)
                                                                                  → 204 | 401 | 409
                                 // attaches Credential{type=oauth} to the caller's OWN account (§3.3.1).
                                 // 401 = caller not authenticated to the existing account; 409 = that
                                 //   provider identity already linked elsewhere (conflict type already in
                                 //   frozen Appendix B; cross-account disclosure accepted — caller controls
                                 //   that provider identity, S-5). Creates NO new User row.
POST /v1/auth/magic-link       req { email: str }                                 → 202 {}            // no enumeration (T-6)
POST /v1/auth/magic-link/verify req { token: str, flow_id?: str }                 → 200 Session | 401
                                 // new-email create carries flow_id for the age gate (§3.9.1)
POST /v1/auth/upgrade          req { handle: str, email: str, password?: str, flow_id?: str,
                                     oauth?: { provider: str, code: str, redirect_uri: str } }
                               (auth: guest session)              → 202 {status:"verification_sent"} | 403 | 409 | 400
                                 // 403 = non-guest session; 409 = taken HANDLE only (public id);
                                 //   400 = validation (incl. weak/breached password).
                                 // Email is NOT disclosed: the call returns the SAME 202 whether or not the
                                 //   email already has an account, mints NO account session synchronously,
                                 //   and sends a verification/claim link to the email. The account is created
                                 //   + token rotated to an account session ONLY when that link is consumed
                                 //   (proves email control) — so there is no account-gated token for a prober
                                 //   to probe (closes round-4 B-R4-1; the prior rotate-guest-token fix still
                                 //   left a probeable subject_kind via GET /v1/me). A password/magic-link
                                 //   upgrade and an oauth{} upgrade both follow this verify-first flow; an
                                 //   oauth{} upgrade whose provider email collides reuses §3.3.1 link (the
                                 //   caller already controls that provider email, so disclosure is acceptable).
POST /v1/auth/upgrade/confirm  req { token: str }   (the verification/claim link's token)
                                                                                  → 200 Session | 401
                                 // NEW: consuming the link creates the account (if the email was free) and
                                 //   returns the rotated ACCOUNT session, OR (if the email was taken) logs the
                                 //   real owner in / routes them to claim — reachable only by the email owner.
GET  /v1/me/sessions           → 200 { items: SessionInfo[] } | 403   // 403 = guest caller (S-2)
SessionInfo  { id: str, created_at: int, last_seen_at: int, current: bool,
               device_label?: str }                                               // never the token; binding summary only
DELETE /v1/me/sessions/{id}    → 204 | 403                               // 403 = guest caller
POST /v1/me/sessions/logout-all → 204
GET  /v1/me                    → 200 User+Profile | 403                  // 403 = guest caller (S-5): a guest
                                 //   has no User/Profile; this matches the /v1/me/sessions* guest rule.
```

**Age-gate binding on account-creation calls (S-4; frozen `register` unchanged, round-2 B-1).** Every path that CREATES a `User` binds to the age screen via a **server-side pending-age record keyed by `flow_id`** (§3.9.1), never a client-asserted age and never a transferable signed token:
- **NEW create endpoints** (`/oauth/start` create branch, `/magic-link/verify` new-email branch, `/upgrade`) carry `flow_id` in their new schema; the pipeline reads the fingerprint-bound record and sets `kid_account`.
- **Frozen `/v1/auth/register`** keeps its exact frozen body (no `flow_id` field added — it is **not** a required field on the frozen endpoint). The create pipeline resolves the caller's pending-age record from the request's client/session fingerprint; if none exists, the pipeline routes the caller to `/age-screen` first (age-unknown), rather than failing a frozen-body validation. So the frozen `register` request/response shape is untouched while the age gate still binds (ACC-INV-2).
- Guest creation and pure *login* (no `User` created) need no age binding.
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
  email_hash   text        -- hash of the REQUESTED email (whether or not a User exists — the unknown-email 202 path still records an issuance here for the per-target-email throttle, S-1/N-4; same hashing as User.email_hash, no User FK)
  expires_at   timestamptz
  consumed     bool        -- atomic compare-and-set on verify (replay guard)
```
Note added to DATA-MODEL §3.1 / §5: the `User` entity is the **account** identity; `GuestSession` is the **ephemeral** identity; ACC-INV-1 (guests unrated-only) is why the two are distinct. Q-DM1 (freed-handle reuse) is addressed by Q-ACC14. No existing `User`/`Credential`/`Profile` field is changed; `Credential.type` already covers `oauth|password|webauthn` (passwordless uses **no** `Credential` — it is a transient issuance, so no enum change is needed).

**Explicit `handle` uniqueness-scope amendment (B-3 — this IS a frozen-semantic change, surfaced as one, not asserted as already-consistent).** Frozen DATA-MODEL §3.1/§5 says `handle text U`, "unique among **live** users," and leaves freed-handle reuse as "an X-3 decision (flagged, not decided here)." This spec decides it (Q-ACC14) and the decision **changes the uniqueness scope**, so it is an owner-gated amendment, not additive prose:
- Uniqueness is enforced over **{live (`account_state ∈ {active, restricted}`) ∪ tombstoned (`account_state = tombstoned`)}** rows — a GDPR-erased account is **tombstoned** (row retained, PII nulled, §3.9.3) and its handle stays **reserved** (not reclaimable).
- A **purged never-consented child** that **never wrote a game event** is physically removed and its handle **released** (excluded from the uniqueness set). A child that **did** play unrated games is **tombstoned, not physically deleted** (so `GameSeat.user_id` never dangles — closes round-2 S-3), handle released separately only if policy later allows.
- `account_state` is the discriminator the uniqueness rule reads. A builder implementing from "`handle text U`" alone would get this wrong — hence it must be stated here and ratified by S3 + architecture as a DATA-MODEL amendment, not left implicit.

**Erasure is projection-layer, not FK/stream mutation (round-2 S-2).** On erase the `User` row is tombstoned with its `id`/FKs **unchanged**; anonymisation happens only in the **`PublicUser` projection** (handle → "Anonymous"). `GameSeat.user_id` and the append-only event stream are **never** rewritten (preserving DATA-MODEL §3.4's rebuild-from-stream rule).

One **additive** field on `User` (new column, nullable-defaulted so existing rows are unaffected) is required by the consent state machine (§3.9.2) and the age gate (§3.9.1):
```
User (additive fields only — existing fields unchanged)
  account_state   enum(active | restricted | tombstoned)  default active
                   -- CANONICAL CASING: lowercase enum values, used identically on the
                   --   server-side Session record (§3.1) and every comparison (§3.1.1
                   --   rated_eligible derivation, social-surface ==restricted check). Prose
                   --   elsewhere may name a state in caps as English (a "RESTRICTED account"),
                   --   but the STORED/COMPARED enum token is always lowercase (closes S-1: a
                   --   case mismatch would make account_state==active never match a stored ACTIVE).
                   -- restricted: created but comms-muted + social/rated disabled,
                   --   pending verifiable parental consent (§3.9.2) OR other hold
                   -- tombstoned: erased-person record per §3.9.3 (handle held reserved)
  handle_provisional  bool  default false
                   -- true when the handle was server-generated on an OAuth/magic-link
                   --   sign-up (§3.0.1); the user is prompted to claim a permanent handle.
                   --   false for register/upgrade (user supplied it) and once claimed.
  -- kid_account (bool) ALREADY EXISTS in DATA-MODEL §7; this spec only assigns its setter (§3.10 step 2).
  -- NO date_of_birth column — the age screen discards DOB, persists only the derived outcome (§3.9.1).
```
Consent evidence (who/when/method/scope — COPPA retention) and the GDPR export/erasure request log are written to the **durable audit path** (DATA-MODEL §7 audit log / the event-audit store), **not** new `User` columns — so no further relational-schema change is proposed here beyond `account_state`.

---

### Reviewer note (not part of the frozen contract)

This spec fulfils the API-CONTRACT's explicit delegation of "token internals + service implementation" to X-3 (API-CONTRACT §1 non-goals, Q-A3). It deliberately (1) keeps the guest identity a **separate ephemeral entity** rather than a crippled `User`, so the unrated-only invariant is structural, not a flag someone can forget to check; (2) names **S2 as the owning layer** for the terminal rated-seek rejection, with X-3 owning only the `rated_eligible`/`account_state` signal and X-4 as a backstop — the explicit cross-layer ownership the S4 layer-gap lesson demands; and (3) now carries the owner's ratified decisions for every §10 item (engineering defaults accepted, the five-method sign-up set named, the COPPA consent state machine and GDPR erasure boundary designed by the acting compliance function) — with the *only* residue a set of licensed-reviewer confirmations gated to **G-F (launch)**, downstream of implementation, named in §3.9.4. The §11 contract amendments are additive and drafted REVIEW, honouring the ADR-17/18 frozen-contract change-control: a frozen spec is amended by an owner-ratified additive version, never self-edited. The `User.account_state` + `GuestSession` + `MagicLinkIssue` additions are the only new data-model surface, all additive.
