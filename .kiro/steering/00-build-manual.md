---
inclusion: always
---
# PlayGlobalChess — Kiro Build Manual (read first, every session)

You are building **PlayGlobalChess**, a competitive chess platform (web + iOS + Android) to rival Chess.com and Lichess. This is a multi-year monorepo. These steering files are your standing operating contract; the full detail lives in `docs/MASTER-PLAN.md` (the authoritative foundation doc). **Always consult the master plan by section before building — do not duplicate its content from memory.**

## 0. How you operate (MANDATORY)

- **Spec-first, approval-gated.** For any feature/system: (1) write or update its spec under `docs/specs/Sx-…/` using a `_shared/` template, (2) set it to `REVIEW`, (3) **STOP and wait for the owner's approval**, (4) only then implement. **Never write implementation code before its spec is approved.** This is the owner's chosen workflow — honor it.
- **You sequence the work** from the roadmap (MASTER §IV–V), following the critical path (see §2 below). Propose the next item, get approval, build it.
- **Make no silent assumptions.** If scope, tech, priority, or a rule is unclear or missing, STOP and ask the owner. A wrong assumption in a multi-year build is expensive.
- **One source of truth.** Reference master IDs (`F-xx`, `X-x`, `G-A…G-F`, `ADR-x`, `NFR-x`); never invent IDs or redefine them. A new architecture decision = a new ADR appended to `docs/MASTER-PLAN.md`, approved, then implemented — never decided ad hoc in code.
- **Gates are hard.** Nothing rated ships until **G-A** (engine correct), **G-B** (fair-play baseline), **G-C** (rating integrity) are green. Nothing public ships until the **Part XII launch bar** is met.

## 1. Scope in one screen (detail: MASTER §I–III)

- **Goal:** feature parity with Chess.com/Lichess across Play, Puzzles, Learn, Train, Community, Watch — 52 feature modules `F-01…F-52`.
- **How it fits:** every feature sits on 14 cross-cutting systems `X-1…X-14` — the engine (X-1), real-time game server (X-2), accounts (X-3), ratings (X-4), anti-cheat (X-5), analysis (X-6), puzzle pipeline (X-7), CMS (X-8), notifications (X-9), media (X-10), moderation (X-11), payments/entitlements (X-12), shared API (X-13), observability/data (X-14).
- **8 delivery streams** `S1…S8` map 1:1 onto the repo's `docs/specs/Sx-…/` and the code folders.

## 2. Build order (critical path — propose in this order)

1. **S1 engine** (`engine/`, Rust — ADR-11): board rep + move-gen for Standard; the **perft + conformance corpus IS the deliverable** (G-A). Nothing live ships before this is 100%.
2. **protocol/** (ADR-10): the WS binary frame + event-store schema + entity model (MASTER §VIII). Freeze = **G-D**.
3. **services/game-server** (`Go` — ADR-12): clocks, matchmaking, reconnection, move relay (X-2).
4. **services/rating** (Go): Glicko-2 per-pool, event-sourced, deterministic (ADR-1) — **G-C**.
5. **services/anti-cheat** (Go): signal collection from day one (ADR-2) — **G-B** before any rated play.
6. **F-01 Play Online** vertical slice (engine + game-server + rating + `clients/web`): the first end-to-end proof.
7. Then fan out per MASTER §V phases P2→P5.

Build each only after the prior gate it depends on is green. Propose, get approval, build.

## 3. Where things live

- `docs/ops/PROJECT-STATUS.md` — **read this first**: current phase, next step, infra/account state, outstanding human actions.
- `docs/MASTER-PLAN.md` — authoritative plan (BRD, roadmap, ADRs, budgets, test/security/launch).
- `docs/specs/Sx-…/` — your deep specs (write these first, approval-gated).
- `docs/ops/` — operational runbooks (email/domain, etc.).
- `engine/ protocol/ services/* clients/* platform/` — code, one owner stream each (see `CONTRIBUTING.md`).

## 4. When you finish a unit of work

State what you did, the IDs touched, tests run, and open concerns. Update the spec status. If you changed behavior, update the spec. If you made/need an architecture decision, write/propose the ADR. Then propose the next critical-path item and wait for approval.
