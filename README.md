# PlayGlobalChess

Competitive chess platform — web + iOS + Android. Monorepo holding **both the planning docs and the code**, so specs live next to the code they describe and shared contracts have one source of truth.

## Layout

```
playglobalchess/
├── docs/              Planning & specs
│   ├── MASTER-PLAN.md     Foundation doc (v2.1): BRD + roadmap + ADRs + budgets + test/security/launch
│   └── specs/             Per-stream deep specs (S1–S8, PE) + shared templates/conventions
├── engine/            S1 · Rust chess rules engine + variants (shared core, bound into clients via FFI — ADR-9)
├── protocol/          Shared API/WS + event-store schema (ADR-10) — ONE source of truth, cited not copied
├── services/          Backend services
│   ├── game-server/       S2 · real-time play, clocks, matchmaking (X-2)
│   ├── accounts/          S3 · auth, profiles (X-3)
│   ├── rating/            S3 · Glicko-2 per-pool, event-sourced (X-4, ADR-1)
│   ├── anti-cheat/        S6 · fair-play detection + review (X-5, ADR-2)
│   ├── puzzles/           S7 · puzzle pipeline (X-7)
│   └── content/           S7 · CMS (X-8)
├── clients/
│   ├── web/               S4 · web app (TypeScript/React)
│   ├── ios/               S5 · native iOS (Swift) over the shared Rust engine
│   └── android/           S5 · native Android (Kotlin) over the shared Rust engine
├── platform/          PE · infra, CI/CD, IaC, observability (X-14)
└── README.md
```

## Where to start
Read `docs/MASTER-PLAN.md`, then `docs/specs/README.md`, then `docs/specs/_shared/CONVENTIONS.md`. Build order and the "start this week" list are in MASTER-PLAN Part V. The first vertical slice is **F-01 Play Online** (engine + game-server + rating + web).

## Conventions
Code and tickets reference the master IDs (`F-xx`, `X-x`, `G-A…F`, `ADR-x`). Decisions beyond the master doc → add a new ADR in `docs/MASTER-PLAN.md`; never decide architecture locally. Nothing rated ships until gates G-A/G-B/G-C are green (MASTER §V.2); nothing public ships until the Part XII launch bar is met.

Status: **planning — P0 foundations.** Code folders are stubbed and awaiting their streams.
