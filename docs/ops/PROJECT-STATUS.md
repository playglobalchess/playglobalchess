# Project Status & Infra State

> Living snapshot of where the project actually is — read this at the start of a fresh session. Update it whenever infra, accounts, or the current phase change. The authoritative *plan* is `docs/MASTER-PLAN.md`; this is the *state*.

| Field | Value |
|---|---|
| Last updated | 2026-10-13 |
| Phase | **P0 foundations** — G-A (engine) GREEN. The three G-D contract specs are all **RATIFIED**: SPEC-S2-PROTOCOL (WS + event store + entity shape) + SPEC-PE-API-CONTRACT (REST) + SPEC-S3-DATA-MODEL. ADR-14 (wire codec) DECIDED; PLAN-S2-CODEC **v1.1 RATIFIED** (Go consumes a sibling `codec-cgo` crate). The codec is being implemented chunk by chunk (see Codec progress below). |
| Next step | Finish the codec consumers (chunk 4.5 Go/cgo) so the §4A contract is implemented + its golden-vector corpus passes on all consumers; then the three contract specs tag as the frozen **G-D** baseline (each spec §9 — RATIFIED ≠ G-D-frozen; G-D needs the contract implemented + corpus green across Rust/web/iOS/Android/Go, jointly with the PE REST spec per Q-P1). In parallel, `services/game-server` (X-2, Go, ADR-12) — spec SPEC-S2-GAME-SERVER currently in REVIEW. All implementation spec-first/approval-gated. |

## Codec progress (ADR-14 / PLAN-S2-CODEC) — toward G-D

| Chunk | What | Status |
|---|---|---|
| 4.1 | `pgc-protocol` core crate + codec | **MERGED** (PR #23) |
| 4.2 | golden-vector corpus (`protocol/vectors/`) | **MERGED** (PR #23) |
| 4.3 | `pgc-protocol-ffi` UniFFI + mobile parity | **MERGED** (PR #24) — iOS 4/4 + Android 4/4 on-device |
| 4.6 | `protocol` CI gate (ubuntu Rust; advisory macOS parity) | **MERGED** (PR #25) |
| 4.4 | `pgc-protocol-wasm` web binding + web parity | **MERGED** (PR #28) — node parity green |
| 4.5 | `codec-cgo` C-ABI + Go game-server consumption + Go parity | **BLOCKED on SPEC-S2-GAME-SERVER (#29) approval**; Go toolchain installed |
| — | engine parity harness hardening (JNA, scheme) | **MERGED** (PR #26, #27) |

Codec parity proven byte-for-byte on **Rust + iOS + Android + web**; **Go** is the last consumer (4.5). `main` is at the PR #30 merge (PLAN-S2-CODEC v1.1).

## Accounts & infra (external — not in git)

| Thing | State |
|---|---|
| Domain | `playglobalchess.com` registered at **Cloudflare** (DNS authoritative: casey/lorna.ns.cloudflare.com) |
| Email | Zoho Mail, **India data center** (`.zoho.in`). Primary mailbox **`chess@playglobalchess.com`** created. MX = `mx.zoho.in`/`mx2`/`mx3`. Domain verification TXT live. DKIM/DMARC + aliases/groups per `email-domain-setup.md` still to finish. |
| GitHub org | **`playglobalchess`** (github.com/playglobalchess), owned by account **`playglobalchess-admin`** (on chess@). A separate personal account `geniiashu-chessarena` also has CLI auth on this machine. |
| Repo | **`playglobalchess/playglobalchess`** — PRIVATE, default branch `main`. |
| Teams | 10 created: engine, realtime, platform-team, platform-eng, web, mobile, trust-safety, learn-content, community, architecture. |
| Branch protection | **NOT enforced** — GitHub **Free** plan blocks classic protection AND rulesets on private repos (confirmed 403). `main` is protected by CONVENTION + the Kiro Crew policy + the preToolUse guard hook only. **Fix: upgrade org to GitHub Team**, then apply the protection ruleset (JSON ready in session history / re-derivable from CONTRIBUTING §9). |
| CI | GitHub Actions live: `docs-check` + `ci` (path-filtered). The `engine` and `protocol` jobs are REAL gates now (fmt + clippy + cargo test on their crates); `services`/`web`/`mobile`/`platform` remain honest stubs until those streams land. A macOS `protocol-parity` job runs the host Swift/Kotlin parity (currently `continue-on-error` — advisory until a green run on a hosted mac runner, then flip to hard). PRs #1–#30 merged to main. |

## Decisions locked (all in MASTER-PLAN ADRs)
- ADR-1 Glicko-2 per pool · ADR-2 anti-cheat in-house, standing team · ADR-3 N-seat container · ADR-4 variant rules pinned, **Spell Chess v1 ruleset defined**, Fog/Duck = standard · ADR-5 1–52 numbering · ADR-6 **global launch, COPPA as needed** (legal confirms specifics pre-G-F) · ADR-7 i18n-ready P0 · ADR-8 **freemium** · ADR-9 native + shared Rust core · ADR-10 Postgres+event-store+Redis+columnar, WS binary · **ADR-11 Rust engine/core** · **ADR-12 Go services, Rust engine**.

## Outstanding owner/human actions (not buildable by Kiro)
1. Finish Zoho DKIM/DMARC + create aliases/groups (`email-domain-setup.md` Phases 2,3,6).
2. **Upgrade GitHub org to Team** → then real branch protection + CODEOWNERS enforcement.
3. **Approve each spec** before Kiro implements (spec-first workflow). **Pending now:** SPEC-S2-GAME-SERVER (PR #29, REVIEW) — needs S2 + architecture-group + S6 sign-off, plus answers to Q-GS1 (cgo decode return shape) and Q-GS4 (should the engine share the `codec-cgo` cgo pattern? maybe an ADR). Approving it unblocks codec chunk 4.5.
4. **Flip the macOS `protocol-parity` CI job to a hard gate** once it has one green run on a hosted mac runner (currently `continue-on-error`).
5. Legal: confirm per-market compliance specifics before G-F (ADR-6).
6. Open items OPEN-1/2/3/7/8 (lesson taxonomy, community "others", bot personalities, tools list, shop scope) — answer when Kiro reaches those modules.

## Open ADR delegations recorded this session
ADR-11 (Rust) and ADR-12 (Go services) were **delegated to Kiro and recorded as DECIDED**. Owner may veto; otherwise they stand.
