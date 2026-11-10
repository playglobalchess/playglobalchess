# Project Status & Infra State

> Living snapshot of where the project actually is. Read this at the start of a fresh session. Update it whenever infra, accounts or the current phase change. The authoritative *plan* is `docs/MASTER-PLAN.md`; this is the *state*. Facts here were checked against the repo and GitHub on the date below; anything not checked says so.

| Field | Value |
|---|---|
| Last updated | 2026-11-10 (main at PR #161) |
| Phase | **P0 foundations, with a first playable game.** G-A (engine) GREEN. The three G-D contract specs are RATIFIED (SPEC-S2-PROTOCOL, SPEC-PE-API-CONTRACT, SPEC-S3-DATA-MODEL). The wire codec is at **v1.3** and runs in Rust, iOS/Android bindings, wasm, Go and the web TS mirror. The game server (X-2) and the first web client (F-01) are merged and play a full game end to end in CI (`web-e2e`). X-3 accounts is specced (RATIFIED) and scaffolded. |
| Next step | **GS-7: the game-server decide/evolve reducer** (SPEC-S2-GAME-SERVER-GS7, RATIFIED; ADR-19 DECIDED). It makes the server do what the v1.3 wire already carries: takeback with consent, clock rewind, draw decline, `ClockStarted`, duplicate-MOVE with recurring plies. It fixes #84 and #85 and must pass the harness with `GS7_STRICT=1` (`internal/session/invariant_harness_test.go`). Then the web reducer for the new messages, then game-server bugs #90, #91, #93, then X-3 accounts (start with the handle spec, #134). |

## What is done

| Area | State |
|---|---|
| Engine (S1, Rust) | GREEN (G-A). Perft corpus and bench run nightly. `Move::from_uci` non-ASCII panic is fixed. |
| Wire codec (ADR-14) | **v1.3 merged** (#161): messages `0x0B-0x0D`, `0x88-0x8A`, events `0x0D-0x11`, `status` enum, the full STATE body with six decode invariants enforced on encode and decode. Golden vectors reproduced byte-for-byte by Rust, Go, wasm; Swift and Kotlin host parity read the v1.3 fields through the bindings. `PROTOCOL.md` carries wire 1.3; the takeback/decline/duplicate-MOVE **behaviour** rows are still in `PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md` §7 until GS-7 lands. |
| Game server (X-2, Go) | Lifecycle, clock, engine seam, live session, resign/abort/draw, DynamoDB event store, Redis matchmaking and WS transport are merged. STATE and MATCHED are built per recipient with the v1.3 body (no running clock reported at ply 0). A message the codec refuses is logged and dropped. **Not yet:** takeback with consent, `ClockStarted`, draw decline events. The fold still rejects the new event types. |
| Web client (S4, F-01) | First-playable client merged (#62, #66): board, GameSocket, optimistic-with-revert moves, STATE/RESYNC reconnection, a11y, i18n. The reducer now takes `ply` from STATE. It ignores the three new server messages until the GS-7 web reducer. |
| Accounts (X-3) | Spec RATIFIED (#72, #73); contract and data-model amendments applied (#74); pure-Go skeleton scaffolded (#75). Handle syntax is undefined (#134), so handles are not final. Other `services/*` folders exist (rating, anti-cheat, puzzles, content); their state was not re-verified for this update. |
| Mobile | Codec parity only (Swift and Kotlin host runs in CI; device runs are manual). No client app yet. The `mobile` CI job builds the Android parity Gradle modules and compiles no parity tests or bindings (#133). |

## CI (`.github/workflows/ci.yml`)

- **Fail-safe filters (#160, closes #86 and #87).** `services`, `web` and `web-e2e` run on every change except docs and other applications, so a `protocol/` or `engine/` change always runs them. `rest-contract` and `ci-filter-audit` run on every change. Leaf jobs (`engine`, `protocol`, `mobile`, `platform`, `protocol-parity`) keep owner filters. `platform/ci/check_ci_filters.py` enforces the policy.
- **Nightly runs every job** (each gate has a schedule bypass), plus the slow perft corpus and the perf bench.
- `protocol-parity` (macOS, Swift run + Kotlin compile) is a **hard gate**.
- **Known CI gaps (open issues):** `engine-cgo` has no gate (#114); toolchains, images and actions are not pinned (#115); nightly failures are silent (#116); branch protection is not enforced (#117); stub jobs pass by finding nothing (#146).
- **Flaky:** `TestRedis_ConcurrentMutualPairCreatesOneGame` failed in a nightly (existing issue #76).

## Open work (78 open issues on 2026-11-10)

- **Blockers (2) and the GS-7 cluster:** #84 (takeback re-ack and empty FEN), #85 (mutual draw writes an unfoldable stream), #101 (STATE could not resume a reload; wire done, server fill pending), #107 (takeback consent), #123 (stale clocks), #124 (clock-start rule), #157 (flagged clock after rebuild), #159 (draw offer lost on rebuild). All are covered by GS-7.
- **Game server, separate:** #90 (Redis re-claim after Remove), #91 (WS writes have no deadline), #93 (SEEK trusts client pool and time control).
- **Protocol:** #131 (`MATCHED.opponent` carries the user id, not the handle), #150 (codec tests miss RESYNC/DRAW_OFFERED; `schema_ver` rule untested).
- Counts by label: game-server 22, protocol 15, web 13, ci 7, specs 4, accounts 3, engine 5, mobile 1. 29 are `spec-first`.

## Accounts & infra (external, not in git)

| Thing | State |
|---|---|
| Domain | `playglobalchess.com` at **Cloudflare** (DNS authoritative). |
| Email | Zoho Mail, **India data center** (`.zoho.in`). Primary mailbox `chess@playglobalchess.com`. MX = `mx.zoho.in`/`mx2`/`mx3`. DKIM/DMARC and aliases/groups per `email-domain-setup.md` were still to finish (not re-checked). |
| GitHub org | `playglobalchess`, owned by `playglobalchess-admin`. A personal account `geniiashu-chessarena` also has CLI auth on this machine. |
| Repo | `playglobalchess/playglobalchess`, PRIVATE, default branch `main`. PRs are squash-merged. |
| Teams | 10 created: engine, realtime, platform-team, platform-eng, web, mobile, trust-safety, learn-content, community, architecture. |
| Branch protection | **NOT enforced** (#117). The GitHub **Free** plan blocks classic protection and rulesets on private repos. `main` is protected by convention, the Kiro Crew policy and the preToolUse guard hook. Fix: upgrade the org to GitHub Team, then apply a ruleset with a real required-check design. |
| Production data | None exists. Wire and event-format changes need no migration (owner decision Q-V3, 2026-11-09). |

## Decisions locked (details in MASTER-PLAN ADRs)
ADR-1 Glicko-2 per pool · ADR-2 anti-cheat in-house · ADR-3 N-seat container · ADR-4 variant rules pinned · ADR-5 1-52 numbering · ADR-6 global launch, COPPA as needed · ADR-7 i18n-ready P0 · ADR-8 freemium · ADR-9 native + shared Rust core · ADR-10 Postgres + event store + Redis + columnar, WS binary · ADR-11 Rust engine · ADR-12 Go services · ADR-14 one wire codec · ADR-15 uniform Go-Rust cgo C-ABI · ADR-17 RESYNC and DRAW_OFFERED · ADR-18 `MOVE_ACCEPTED` carries `uci` · **ADR-19 takeback consent, draw decline, `ClockStarted`, full STATE (DECIDED 2026-11-09 on the owner's attestation that S2, S4, PE, S6 and the architecture group approved).**

## Outstanding owner/human actions (not buildable by Kiro)
1. Upgrade the GitHub org to Team, then real branch protection and CODEOWNERS enforcement (#117).
2. Finish Zoho DKIM/DMARC and create aliases/groups (`email-domain-setup.md`).
3. Approve each spec before Kiro implements it (spec-first). GS-7 is approved; the X-3 handle spec (#134) needs a decision before sign-up work.
4. Legal: confirm per-market compliance specifics before G-F (ADR-6).
5. Open items OPEN-1/2/3/7/8 (lesson taxonomy, community "others", bot personalities, tools list, shop scope): answer when Kiro reaches those modules.

## Working agreements worth remembering
- Do not write `fixes #N` in a PR body for work the PR does not do. GitHub closes the issue on merge (this closed #84 early on #161 and was reopened).
- Independent review is for load-bearing work (game rules, clocks, wire format). Triage its findings against the actual bug; file the rest as low-priority issues. Cap at one or two rounds.
