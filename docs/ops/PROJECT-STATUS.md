# Project Status & Infra State

> Living snapshot of where the project actually is — read this at the start of a fresh session. Update it whenever infra, accounts, or the current phase change. The authoritative *plan* is `docs/MASTER-PLAN.md`; this is the *state*.

| Field | Value |
|---|---|
| Last updated | 2026-10-12 |
| Phase | **Pre-P0 / P0 foundations** — scaffolding + planning complete; no product code yet |
| Next step | Kiro drafts the **S1 engine spec** (`docs/specs/S1-engine/ENGINE-CORE.md`), sets it REVIEW, **waits for owner approval** before any Rust (spec-first workflow) |

## Accounts & infra (external — not in git)

| Thing | State |
|---|---|
| Domain | `playglobalchess.com` registered at **Cloudflare** (DNS authoritative: casey/lorna.ns.cloudflare.com) |
| Email | Zoho Mail, **India data center** (`.zoho.in`). Primary mailbox **`chess@playglobalchess.com`** created. MX = `mx.zoho.in`/`mx2`/`mx3`. Domain verification TXT live. DKIM/DMARC + aliases/groups per `email-domain-setup.md` still to finish. |
| GitHub org | **`playglobalchess`** (github.com/playglobalchess), owned by account **`playglobalchess-admin`** (on chess@). A separate personal account `geniiashu-chessarena` also has CLI auth on this machine. |
| Repo | **`playglobalchess/playglobalchess`** — PRIVATE, default branch `main`. |
| Teams | 10 created: engine, realtime, platform-team, platform-eng, web, mobile, trust-safety, learn-content, community, architecture. |
| Branch protection | **NOT enforced** — GitHub **Free** plan blocks classic protection AND rulesets on private repos (confirmed 403). `main` is protected by CONVENTION + the Kiro Crew policy + the preToolUse guard hook only. **Fix: upgrade org to GitHub Team**, then apply the protection ruleset (JSON ready in session history / re-derivable from CONTRIBUTING §9). |
| CI | GitHub Actions live: `docs-check` + `ci` (path-filtered, honest stubs). PR #1 + #2 merged to main. |

## Decisions locked (all in MASTER-PLAN ADRs)
- ADR-1 Glicko-2 per pool · ADR-2 anti-cheat in-house, standing team · ADR-3 N-seat container · ADR-4 variant rules pinned, **Spell Chess v1 ruleset defined**, Fog/Duck = standard · ADR-5 1–52 numbering · ADR-6 **global launch, COPPA as needed** (legal confirms specifics pre-G-F) · ADR-7 i18n-ready P0 · ADR-8 **freemium** · ADR-9 native + shared Rust core · ADR-10 Postgres+event-store+Redis+columnar, WS binary · **ADR-11 Rust engine/core** · **ADR-12 Go services, Rust engine**.

## Outstanding owner/human actions (not buildable by Kiro)
1. Finish Zoho DKIM/DMARC + create aliases/groups (`email-domain-setup.md` Phases 2,3,6).
2. **Upgrade GitHub org to Team** → then real branch protection + CODEOWNERS enforcement.
3. **Approve each spec** before Kiro implements (spec-first workflow).
4. Legal: confirm per-market compliance specifics before G-F (ADR-6).
5. Open items OPEN-1/2/3/7/8 (lesson taxonomy, community "others", bot personalities, tools list, shop scope) — answer when Kiro reaches those modules.

## Open ADR delegations recorded this session
ADR-11 (Rust) and ADR-12 (Go services) were **delegated to Kiro and recorded as DECIDED**. Owner may veto; otherwise they stand.
