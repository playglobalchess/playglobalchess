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

### MX (receive mail) — exact Zoho values
| Type | Name | Mail server | Priority | Proxy |
|---|---|---|---|---|
| MX | `@` | `mx.zoho.com` | 10 | DNS only |
| MX | `@` | `mx2.zoho.com` | 20 | DNS only |
| MX | `@` | `mx3.zoho.com` | 50 | DNS only |

> If your Zoho data center is EU/IN/AU, Zoho may give region-specific hosts (e.g. `mx.zoho.eu`, `mx.zoho.in`). **Use whatever the Zoho admin console shows for your account** — verify against the console, don't blindly trust this table.

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
