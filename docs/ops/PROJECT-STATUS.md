# Project Status & Infra State

> Living snapshot of where the project actually is — read this at the start of a fresh session. Update it whenever infra, accounts, or the current phase change. The authoritative *plan* is `docs/MASTER-PLAN.md`; this is the *state*.

| Field | Value |
|---|---|
| Last updated | 2026-10-13 |
| Phase | **P0 foundations** — G-A (engine) GREEN. The three G-D contract specs are all **RATIFIED**: SPEC-S2-PROTOCOL (WS + event store + entity shape) + SPEC-PE-API-CONTRACT (REST) + SPEC-S3-DATA-MODEL. ADR-14 (wire codec) DECIDED; PLAN-S2-CODEC **v1.1 RATIFIED**. The wire codec is **COMPLETE** across all five consumers (Rust/iOS/Android/web/Go), CI-gated. `services/game-server` (X-2, Go, ADR-12) is now being built chunk by chunk (see Game-server progress below). |
| Next step | Continue `services/game-server` toward the F-01 vertical slice. GS-1 (lifecycle + clock) and GS-2 (engine-cgo legality seam) are done; the dependency-correct next chunks are the **live in-memory game session** (holds `CurFEN` + the folded Zobrist history — **owns Q-GS5 repetition wiring**), the **real event store** (ADR-10, replaces the GS-1 in-memory stub), then **matchmaking** (§3.4) + **reconnection** (§3.5) + the **WS transport** — after which the F-01 end-to-end slice (match→play→end→rating) can be proven on web. All implementation spec-first/approval-gated. G-D freeze still needs SPEC-S2-PROTOCOL + SPEC-PE-API-CONTRACT jointly (Q-P1). |

## Codec progress (ADR-14 / PLAN-S2-CODEC) — toward G-D

| Chunk | What | Status |
|---|---|---|
| 4.1 | `pgc-protocol` core crate + codec | **MERGED** (PR #23) |
| 4.2 | golden-vector corpus (`protocol/vectors/`) | **MERGED** (PR #23) |
| 4.3 | `pgc-protocol-ffi` UniFFI + mobile parity | **MERGED** (PR #24) — iOS 4/4 + Android 4/4 on-device |
| 4.6 | `protocol` CI gate (ubuntu Rust; advisory macOS parity) | **MERGED** (PR #25) |
| 4.4 | `pgc-protocol-wasm` web binding + web parity | **MERGED** (PR #28) — node parity green |
| 4.5 | `codec-cgo` C-ABI + Go game-server consumption + Go parity | **MERGED** (PRs #23–#31) — Go byte-for-byte parity green on the hosted runner |
| — | engine parity harness hardening (JNA, scheme) | **MERGED** (PR #26, #27) |

Codec parity proven byte-for-byte on **all five consumers** — Rust + iOS + Android + web + **Go**. ADR-14 codec is COMPLETE and CI-gated (`protocol` + `services` + macOS `protocol-parity`).

## Game-server progress (X-2, SPEC-S2-GAME-SERVER, Go/ADR-12) — toward F-01 + G-E

| Chunk | What | Status |
|---|---|---|
| GS-1 | lifecycle state machine (§3.2) + clock core (§3.3), pure Go; move-pipeline decision logic with stubbed `LegalityOracle`/`EventStore` | **MERGED** (PR #34; O(ply²) replay fix in the same PR) |
| GS-2 | engine-cgo legality seam — `engine/engine-cgo` (`pgc-engine-cgo`) + `services/game-server/internal/engine` (`CgoOracle`); real S1 engine replaces the stub (NFR-5) | **IN REVIEW** (PR #38; spec slice SPEC-S2-GAME-SERVER-GS2 RATIFIED PR #36) |
| — | engine helper `has_mating_material` (per-side FIDE-6.9 flag capacity) needed by GS-2 | **MERGED** (PR #37) |
| next | live in-memory game session (owns `CurFEN` + folded Zobrist history; **wires Q-GS5 repetition**); then real event store (ADR-10); matchmaking (§3.4); reconnection (§3.5); WS transport; F-01 slice | not started |

**Known-deferred / tracked gaps (so a fresh session does not miss them):**
- **Q-GS5 — repetition not yet detected on the live move path.** GS-2 passes empty `prior_keys` to the engine, so mate/stalemate/insufficient/75-move work but threefold/fivefold do NOT auto-draw yet. The **live-session chunk** must thread the folded Zobrist history (which `pgc_engine_apply`'s `out_zobrist` already returns per move) into `CgoOracle.Legality`. Recorded as **Q-GS5** in SPEC-S2-GAME-SERVER §10.
- **Engine `from_uci` panics on non-ASCII UCI** (`engine/src/moves.rs` slices by byte index after a byte-length check). The GS-2 cgo seam CONTAINS it safely (typed error, no UB — found by the GS-2 fuzz sweep), but the engine itself should not panic on untrusted input (PGN/UCI import path). Needs a small **S1 engine-hardening PR** (out of GS-2's frozen-engine scope).

## Accounts & infra (external — not in git)

| Thing | State |
|---|---|
| Domain | `playglobalchess.com` registered at **Cloudflare** (DNS authoritative: casey/lorna.ns.cloudflare.com) |
| Email | Zoho Mail, **India data center** (`.zoho.in`). Primary mailbox **`chess@playglobalchess.com`** created. MX = `mx.zoho.in`/`mx2`/`mx3`. Domain verification TXT live. DKIM/DMARC + aliases/groups per `email-domain-setup.md` still to finish. |
| GitHub org | **`playglobalchess`** (github.com/playglobalchess), owned by account **`playglobalchess-admin`** (on chess@). A separate personal account `geniiashu-chessarena` also has CLI auth on this machine. |
| Repo | **`playglobalchess/playglobalchess`** — PRIVATE, default branch `main`. |
| Teams | 10 created: engine, realtime, platform-team, platform-eng, web, mobile, trust-safety, learn-content, community, architecture. |
| Branch protection | **NOT enforced** — GitHub **Free** plan blocks classic protection AND rulesets on private repos (confirmed 403). `main` is protected by CONVENTION + the Kiro Crew policy + the preToolUse guard hook only. **Fix: upgrade org to GitHub Team**, then apply the protection ruleset (JSON ready in session history / re-derivable from CONTRIBUTING §9). |
| CI | GitHub Actions live: `docs-check`/`validate-docs` + `ci` (path-filtered). **Real gates now:** `engine` + `protocol` (fmt + clippy + cargo test) AND `services` (runs `make -C services/game-server test` — builds the codec + engine cgo staticlibs on Linux and runs `go test ./...`, incl. the game-server lifecycle/clock/engine packages + codec parity). `web`/`mobile`/`platform` remain honest stubs until those streams land. A macOS `protocol-parity` job runs the host Swift/Kotlin parity (currently `continue-on-error` — advisory until a green run on a hosted mac runner, then flip to hard). PRs #1–#37 merged to main; #38 (GS-2) in review. |

## Decisions locked (all in MASTER-PLAN ADRs)
- ADR-1 Glicko-2 per pool · ADR-2 anti-cheat in-house, standing team · ADR-3 N-seat container · ADR-4 variant rules pinned, **Spell Chess v1 ruleset defined**, Fog/Duck = standard · ADR-5 1–52 numbering · ADR-6 **global launch, COPPA as needed** (legal confirms specifics pre-G-F) · ADR-7 i18n-ready P0 · ADR-8 **freemium** · ADR-9 native + shared Rust core · ADR-10 Postgres+event-store+Redis+columnar, WS binary · **ADR-11 Rust engine/core** · **ADR-12 Go services, Rust engine** · **ADR-14 one wire codec** · **ADR-15 uniform Go↔Rust cgo C-ABI**.

## Outstanding owner/human actions (not buildable by Kiro)
1. Finish Zoho DKIM/DMARC + create aliases/groups (`email-domain-setup.md` Phases 2,3,6).
2. **Upgrade GitHub org to Team** → then real branch protection + CODEOWNERS enforcement.
3. **Approve each spec/PR** before Kiro implements/merges (spec-first workflow). Done this session: SPEC-S2-GAME-SERVER (#29) RATIFIED; SPEC-S2-GAME-SERVER-GS2 engine-cgo slice (#36) RATIFIED; GS-1 (#34), engine `has_mating_material` (#37) MERGED. **Pending: review + merge GS-2 (#38).** Next approvals are the live-session + event-store chunk PRs.
4. **Flip the macOS `protocol-parity` CI job to a hard gate** once it has one green run on a hosted mac runner (currently `continue-on-error`).
5. Legal: confirm per-market compliance specifics before G-F (ADR-6).
6. Open items OPEN-1/2/3/7/8 (lesson taxonomy, community "others", bot personalities, tools list, shop scope) — answer when Kiro reaches those modules.
7. **Engine-hardening backlog:** `engine/src/moves.rs` `Move::from_uci` panics on non-ASCII UCI (byte-index slice after a byte-length check). Contained safely at the GS-2 cgo seam but should be fixed at the engine (untrusted PGN/UCI import path) — small S1 PR. Add a non-ASCII case to the engine's move-encoding tests when fixed.

## Open ADR delegations recorded this session
ADR-11 (Rust) and ADR-12 (Go services) were **delegated to Kiro and recorded as DECIDED**. Owner may veto; otherwise they stand.
