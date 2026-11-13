# Project Status & Infra State

> Living snapshot of where the project actually is. Read this at the start of a fresh session. Update it whenever infra, accounts or the current phase change. The authoritative *plan* is `docs/MASTER-PLAN.md`; this is the *state*. Facts here were checked against the repo and GitHub on the date below; anything not checked says so.

| Field | Value |
|---|---|
| Last updated | 2026-11-13 (main at PR #199) |
| Phase | **P0 foundations, with a first playable game.** G-A (engine) GREEN. The three G-D contract specs are RATIFIED (SPEC-S2-PROTOCOL, SPEC-PE-API-CONTRACT, SPEC-S3-DATA-MODEL). The wire codec is at **v1.3** and runs in Rust, iOS/Android bindings, wasm, Go and the web TS mirror. The game server (X-2) and the first web client (F-01) are merged and play a full game end to end in CI (`web-e2e`). X-3 accounts is specced (RATIFIED) and scaffolded. |
| Next step | **Fix the existing defects before X-3, in the owner's agreed order.** Step 1 (engine) is done: #119, #120, #121, #148 and this scorecard fix (#193); spec drift #149 is in PR #200. Step 2 is game rules and clocks: #100, #97, #125, #99, #124, #183, #182, #95, #176. Then quick safety fixes (#179, #178, #192), then multi-node (#173 with #98). X-3 accounts follows the agreed list. |

## What is done

| Area | State |
|---|---|
| Engine (S1, Rust) | GREEN (G-A). Perft corpus and bench run nightly. `Move::from_uci` non-ASCII panic is fixed. Audit defects fixed: en passant respects pins (#195, #119), saturating fullmove and bounded FEN rank parsing (#196, #148), `parse_fen` rejects impossible positions (#197, #120), the flag branch uses the Lichess mating-material rule (#199, #121). G-A scorecard box 9 corrected (#193). |
| Wire codec (ADR-14) | **v1.3 merged** (#161): messages `0x0B-0x0D`, `0x88-0x8A`, events `0x0D-0x11`, `status` enum, the full STATE body with six decode invariants enforced on encode and decode. Golden vectors reproduced byte-for-byte by Rust, Go, wasm; Swift and Kotlin host parity read the v1.3 fields through the bindings. `PROTOCOL.md` carries wire 1.3; the takeback/decline/duplicate-MOVE **behaviour** rows are still in `PROTOCOL-V1.3-TAKEBACK-DRAW-STATE.md` §7 until GS-7 lands. |
| Game server (X-2, Go) | Lifecycle, clock, engine seam, live session, resign/abort/draw, DynamoDB event store, Redis matchmaking and WS transport, plus **GS-7** (#163): one Evolve/Decide reducer, so a live game always equals a rebuild; takeback with consent and clock rewind; draw decline and a 20-ply cooldown; a durable first-clock start (`ClockStarted`); MOVE idempotency with recurring plies; STATE clocks charged to `server_t`. The invariant harness passes with nothing excused. Since then: the Redis claim requires both seekers to still be waiting, so a matched seeker cannot be re-claimed into a second game (#165, #90); every connection has a write pump (64-frame queue, 5 s write deadline, slow consumers are disconnected), so a peer that stops reading cannot stall the opponent or the flag ticker (#166, #91); the pool decides the clock, not the client (interim catalogue `transport.DefaultPools` until X-13, bounded resolver, refused SEEKs logged; #167, #93). **End of game (#169, #170, #172):**
- A finished game still serves STATE. RESYNC of an ended game the user played returns STATE(ENDED) rebuilt from the stream, and a seat that missed GAME_END is pushed it once on its next login (24 h, 100k cap; #102).
- A store error after a write is checked by a strongly consistent, retried re-read of the stream (DynamoDB `Load` now sets `ConsistentRead`). A move that did commit is acked normally, so an ambiguous commit no longer strands the opponent (#89).
- A resent move that ended the game is re-acked with the result, even after the game is dropped (#122). Lookups of games not live here are budgeted at 20 per connection per minute.
- The GAME_END claim can no longer latch (#88). The broadcast runs on a server context, its result comes from the session snapshot (no store read), and a call that does not broadcast releases the claim. The flag ticker repairs an ending that was not broadcast, and if it cannot, it records both seats as having missed it and drops the game. Session, indices and ticker are reclaimed on every ending. |
| Web client (S4, F-01) | First-playable client (#62, #66) plus the GS-7 web reducer: STATE alone resumes a game (seat, ply, status, result, prompts; #101), draw decline, and a takeback button in unrated games (request, opponent accept/decline), modelled on chess.com. A real-server e2e covers a takeback. **Clocks tick** (#168, #112): the side to move counts down from the last server value, your own move stops your clock at once, GAME_END freezes both, and the running clock is marked by ▶ and "running" text. Display only, anchored at frame arrival (the client sends no PING); it keeps ticking while disconnected because the server keeps charging. The dev pool is now `blitz-5+0`. After every reconnect the client RESYNCs the game it was in (a GAME_END lost on a dropped socket, or a game that ended while away; #169). An `ERROR{INTERNAL}` mid-game ("move not confirmed") triggers a RESYNC instead of a banner, retried with backoff (1, 2, 4, 8 s) until STATE arrives, with a refresh hint after 3 tries (#171). |
| Accounts (X-3) | Spec RATIFIED (#72, #73); contract and data-model amendments applied (#74); pure-Go skeleton scaffolded (#75). Handle syntax is undefined (#134), so handles are not final. Other `services/*` folders exist (rating, anti-cheat, puzzles, content); their state was not re-verified for this update. |
| Mobile | Codec parity only (Swift and Kotlin host runs in CI; device runs are manual). No client app yet. The `mobile` CI job builds the Android parity Gradle modules and compiles no parity tests or bindings (#133). |

## CI (`.github/workflows/ci.yml`)

- **Fail-safe filters (#160, closes #86 and #87).** `services`, `web` and `web-e2e` run on every change except docs and other applications, so a `protocol/` or `engine/` change always runs them. `rest-contract` and `ci-filter-audit` run on every change. Leaf jobs (`engine`, `protocol`, `mobile`, `platform`, `protocol-parity`) keep owner filters. `platform/ci/check_ci_filters.py` enforces the policy.
- **Nightly runs every job** (each gate has a schedule bypass), plus the slow perft corpus and the perf bench.
- `protocol-parity` (macOS, Swift run + Kotlin compile) is a **hard gate**.
- **Known CI gaps (open issues):** `engine-cgo` has no gate (#114); toolchains, images and actions are not pinned (#115); nightly failures are silent (#116); branch protection is not enforced (#117); stub jobs pass by finding nothing (#146).
- **Flaky (closed):** `TestRedis_ConcurrentMutualPairCreatesOneGame`. The 2026-11-10 nightly failed with 2 matches, the #90 race that #165 fixed, and the first nightly after the fix (2026-11-11) was green, so #76 is closed. #76 originally reported 0 matches, which #90 does not obviously explain, so one green night is weak evidence: reopen #76 if a 0-match failure returns.

## Open work (62 open issues on 2026-11-12, after #172)

- **GS-7 cluster: closed** by #163 and the GS-7 web PR (#84, #85, #67, #94, #101, #107, #123, #157, #159), and its follow-ups #102, #89, #122 by #169 and #170. Still open from it: #124 (the clock-start rule itself; the server already starts White's clock at session creation).
- **Game server:** #90, #91, #93 closed (#165-#167); #88, #89, #102, #122 closed (#169, #170, #172). Still open: #95 (ambiguous persist, non-terminal part), #98 (no rebuild of a live game after restart or failover), #92 (no cross-goroutine frame ordering; GAME_END can overtake MOVE_ACCEPTED), #104 (no rate limit, connection cap, auth timeout or heartbeat). `rated` is still taken from the client until accounts (X-3). Missed-ending records and the reconnect index are node-local until G-E routing.
- **Protocol:** #131 (`MATCHED.opponent` carries the user id, not the handle), #150 (codec tests miss RESYNC/DRAW_OFFERED; `schema_ver` rule untested).
- **Web:** #112 closed (#168). Low-time warnings and clock announcements are #142; the WEB-F01 DoD checkbox is #136.
- Counts by label (after #199, 79 open): game-server 21, protocol 15, web 17, ci 8, engine 1, specs 6, accounts 3, mobile 1. 19 are `spec-first`. The rise since #172 is the 2026-11 audit (#173-#194).

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
- Kiro decides per PR whether an independent review is needed, and states the call and the reason in the PR. Review (one round, context-free reviewer) when a bug would hurt players or data and tests cannot fully reproduce the failure: concurrency, trust boundaries, protocol and state-machine changes. Skip it for small, well-tested, cheap-to-fix changes. Fix real findings, push back with evidence on the rest, then stop. Skipping reviews to save cost only moves the review to the owner (the #165-#167 findings came from owner review). If the reviewer cannot start (low host memory), say so and retry rather than presenting a self-review as independent: on #172 the independent round found two bugs the self-review missed.
