# Ops Runbook — Email & Domain Setup (Zoho on Cloudflare)

| Field | Value |
|---|---|
| Domain | `playglobalchess.com` (registered at Cloudflare) |
| Mail provider | Zoho Mail (custom domain) |
| DNS | Cloudflare (where the domain lives) |
| Status | TODO — execute in order |
| Owner | Ashish |

> These are **account/billing/identity actions only you can perform** (they need your logins and payment). This runbook is the checklist + the exact DNS records; follow it top to bottom.

---

## Phase 1 — Zoho account + domain verification

1. Sign up at **zoho.com/mail** → choose a plan (free tier or ~$1/user/mo paid for custom domain).
2. Add domain `playglobalchess.com`.
3. Zoho gives you a **domain-verification record** — a TXT (or CNAME) with a unique value. Add it in Cloudflare DNS (see §DNS). Verify in Zoho.
4. Create your primary mailbox (e.g. `ashish@playglobalchess.com`).

## Phase 2 — DNS records in Cloudflare

In Cloudflare → your domain → **DNS → Records**. Add these. **Set every mail record's proxy status to "DNS only" (grey cloud), NOT proxied (orange)** — mail must not go through Cloudflare's proxy.

### MX (receive mail) — India DC (`.zoho.in`), confirmed for this account
| Type | Name | Mail server | Priority | Proxy |
|---|---|---|---|---|
| MX | `@` | `mx.zoho.in` | 10 | DNS only |
| MX | `@` | `mx2.zoho.in` | 20 | DNS only |
| MX | `@` | `mx3.zoho.in` | 50 | DNS only |

> This account is in Zoho's **India data center** (verification record resolved as `...zoho.in`), so the MX hosts are `.zoho.in`, not `.zoho.com`. Always confirm against the Zoho admin console.

### SPF (authorize senders) — TXT
| Type | Name | Content |
|---|---|---|
| TXT | `@` | `v=spf1 include:zoho.com ~all` |

> One SPF record only per domain. When you add a transactional email provider later (e.g. for app email F-49), **merge** its include into this same record — do not add a second `v=spf1` line.

### DKIM (sign mail) — TXT
Generate DKIM in **Zoho Admin → Email Config → DKIM**. Zoho gives a selector (e.g. `zmail`) and a public key. Add:
| Type | Name | Content |
|---|---|---|
| TXT | `zmail._domainkey` | `v=DKIM1; k=rsa; p=<long public key from Zoho>` |

Then enable/verify DKIM in the Zoho console.

### DMARC (policy + reports) — TXT
| Type | Name | Content |
|---|---|---|
| TXT | `_dmarc` | `v=DMARC1; p=quarantine; rua=mailto:dmarc@playglobalchess.com; ruf=mailto:dmarc@playglobalchess.com; fo=1; adkim=s; aspf=s` |

> Start at `p=quarantine` (or `p=none` for the first week to observe reports), then move to `p=reject` once SPF+DKIM are confirmed passing. `rua` = aggregate reports; create the `dmarc@` alias to receive them.

## Phase 3 — Aliases / shared mailboxes to create

| Address | Purpose |
|---|---|
| `ashish@` (or founder handle) | Primary / admin |
| `support@` | User support inbox |
| `abuse@` | **Fair-play / cheat reports, DMCA, security** — a chess platform needs this |
| `security@` | Vuln reports / responsible disclosure |
| `legal@` | Legal / compliance (GDPR/COPPA data requests — ties to ADR-6) |
| `noreply@` or `no-reply@` | Transactional app mail FROM address (F-49) |
| `dmarc@` | Receives DMARC reports (Phase 2) |
| `billing@` | Vendor/billing |

## Phase 4 — Re-parent company accounts onto the new email

The point of the domain email: company assets shouldn't hang off a personal address.

- [ ] **GitHub:** create (or rename) a company-owned account on `@playglobalchess.com`, or add the company email to the current `geniiashu-chessarena` account and verify. Then **transfer the `playglobalchess` repo** to it (or create an **org**, e.g. `playglobalchess`, and put the repo there — recommended for a team).
- [ ] **Cloudflare:** account should be on the company email (or add it as a member).
- [ ] **Cloud provider** (AWS/GCP/Azure): root account on company email, not personal.
- [ ] **Future:** Apple Developer, Google Play, Stripe, analytics — all on company email.

> **Recommendation:** create a **GitHub Organization** named `playglobalchess` now and transfer the repo into it. A team monorepo belongs in an org (teams, CODEOWNERS, branch protection), not a personal account. This is reversible and free for private repos on the Free plan.

## Phase 5 — Verify

- [ ] Send a test email **to** `ashish@playglobalchess.com` from an external account → arrives.
- [ ] Send **from** it to Gmail → lands in inbox (not spam), and in Gmail "Show original" shows **SPF: PASS, DKIM: PASS, DMARC: PASS**.
- [ ] Check [mxtoolbox.com](https://mxtoolbox.com) for MX, SPF, DKIM, DMARC on the domain.
- [ ] After a week of DMARC reports look clean, raise DMARC to `p=reject`.

---

## Notes
- Keep records' TTL at Cloudflare default (Auto) — fine for mail.
- Propagation is usually minutes on Cloudflare; MX/DKIM can take up to a few hours.
- Do **not** proxy (orange-cloud) any mail record.
- When the app sends bulk/transactional mail, use a dedicated subdomain (e.g. `mail.playglobalchess.com`) and a sending provider so app-mail reputation is isolated from your human mailboxes — plan this in with the F-49 Notifications spec (S8).

> **Data center = India (`.zoho.in`).** The verification record resolved as `...zmverify.zoho.in`, so this account lives in Zoho's India DC. Your **MX hosts are the `.in` variants** (`mx.zoho.in`, `mx2.zoho.in`, `mx3.zoho.in`), **not** the `.com` hosts in the MX table above. Always use exactly what the Zoho India-DC admin console shows.

---

## Phase 6 — Zoho Groups (shared addresses)

A Zoho **Group** is a shared email address that delivers to multiple members, with: an **access level** (who may send to it), an optional **shared inbox** (Streams), and **moderation**. Create these in **Zoho Admin → Groups**. Getting the *access level* right per group is the important part.

### Groups to create

| Group address | Members | Access level (who can email it) | Shared inbox? | Purpose |
|---|---|---|---|---|
| `support@` | support/ops staff | **Anyone** (external users must reach it) | Yes | User help desk |
| `abuse@` | S6 Trust & Safety | **Anyone** (external cheat/DMCA reports) | Yes | Fair-play / cheating / DMCA reports |
| `security@` | S6 + eng leads | **Anyone** (responsible disclosure) | Yes | Vulnerability reports |
| `legal@` | founder + legal | **Anyone** (GDPR/COPPA data requests — ADR-6) | Yes | Legal / compliance / data-subject requests |
| `privacy@` | founder + legal | **Anyone** | Yes | (Optional alias of legal@ for GDPR data requests) |
| `billing@` | founder/finance | **Organization only** | No | Vendor/billing (keep external spam out) |
| `team@` | all staff | **Organization only** | No | Internal all-hands announcements |
| `eng@` | all engineers | **Organization only** | No | Engineering-wide internal |
| `dmarc@` | admin | **Anyone** (receives DMARC aggregate/forensic reports) | Yes (or auto-filter) | DMARC `rua`/`ruf` reports from §Phase 2 |
| `noreply@` | — (no members) | **No delivery / send-only** | No | Outbound transactional FROM address (F-49). Create as an address that discards or auto-replies "unmonitored"; do NOT make it a real inbox people email. |

### Per-stream internal groups (create as the team grows)
Mirror the delivery streams so each squad has an internal list — matches the repo's `docs/specs/Sx-…/` ownership:

| Group | Maps to |
|---|---|
| `engine@` | S1 Core Engine |
| `realtime@` | S2 Real-time Play |
| `platform-team@` | S3 Platform (name avoids clash with PE) |
| `web@` | S4 Web |
| `mobile@` | S5 Mobile |
| `trust@` | S6 Trust & Safety |
| `content@` | S7 Learn/Content |
| `community@` | S8 Community/Watch/Commerce |

> Keep these **Organization-only** access. Don't over-create on day one — add a stream group when that squad actually has members.

### Access-level cheat sheet
- **Anyone** → public-facing addresses that external users/attackers/regulators must reach: `support`, `abuse`, `security`, `legal`, `dmarc`.
- **Organization only** → internal lists: `team`, `eng`, `billing`, all per-stream groups. Blocks external spam.
- **Members only / Moderated** → use for a group where even internal non-members shouldn't post, or where you want a human to approve external mail (e.g. moderate `abuse@` if it gets spammy).
- **Send-only / no inbox** → `noreply@` — it sends app mail but is not a destination.

### Settings to set on each group
- **Moderation:** off for internal; consider "moderate external senders" on high-spam public groups later.
- **Streams (shared inbox):** on for `support`/`abuse`/`security`/`legal` so the team collaborates on threads instead of forwarding.
- **Reply behavior:** reply-to the group for shared inboxes; reply-to-sender for announcement lists (`team@`).
- **Anti-spoofing:** ensure SPF/DKIM/DMARC (Phase 2) are passing so inbound to these groups isn't easily spoofed.

