# Chess Platform — Documentation Set

This folder is the **single home** for the platform's planning and specification documents.

```
chess-platform/
├── MASTER-PLAN.md            ← the foundation doc (v2.1): BRD + roadmap + ADRs + budgets + test/security/launch
└── specs/
    ├── _shared/
    │   ├── SPEC-TEMPLATE.md          ← copy this to start any stream/system spec
    │   ├── FEATURE-SPEC-TEMPLATE.md  ← copy this to spec one feature module (F-xx)
    │   └── CONVENTIONS.md            ← ID conventions, status vocabulary, review rules
    ├── S1-engine/              ← Core Chess Engine (X-1, X-6, X-7)
    ├── S2-realtime/            ← Real-time Play Platform (X-2, F-01/04/06/07)
    ├── S3-platform/            ← Platform/Backend Services (X-3, X-4, X-13, X-14, …)
    ├── S4-web/                 ← Web Client
    ├── S5-mobile/              ← iOS + Android (ADR-9)
    ├── S6-trust-safety/        ← Anti-cheat, moderation, compliance (X-5, X-11, ADR-2/6)
    ├── S7-learn-content/       ← Learn/Train/Content (X-8, F-14…21, F-28…37)
    ├── S8-community-watch-commerce/ ← Community/Watch/Commerce (X-9/10/12, F-22…52)
    └── PE-platform-eng/        ← Infra, CI/CD, environments, observability (X-14, API contract)
```

## The relationship between MASTER-PLAN and specs

- **MASTER-PLAN.md is the map, not the encyclopedia.** It states *what* we build, *why*, *in what order*, and the decisions already made (ADRs). It deliberately stops at "buildable starting point."
- **The specs are the encyclopedia.** Each stream writes the deep detail — algorithms, schemas, state machines, edge cases, test plans — that the master doc points to but does not contain.
- **Specs reference master IDs; they never redefine them.** Use `F-xx`, `X-x`, `G-A…G-F`, `ADR-x`, `NFR-x`, `P-x`, `S-x` exactly as the master doc defines them. If a spec needs a decision the master doc didn't make, it proposes a **new ADR** (ADR-11, ADR-12…) back in the master doc — it does not decide architecture locally.

## Rules of the road

1. **One owner per stream folder** (the stream lead). They own merges into their specs.
2. **MASTER-PLAN is versioned, not frozen.** Changing a decision = add/supersede an ADR, bump the version, keep the reasoning trail. Never silently rewrite history.
3. **Start every spec from a template** in `_shared/` so structure stays consistent across streams.
4. **Status vocabulary is shared** (see `_shared/CONVENTIONS.md`): `DRAFT → REVIEW → RATIFIED → BUILDING → DONE`.
5. **Gates are hard.** Nothing rated ships until G-A/G-B/G-C are green; nothing public ships until the Part XII launch bar is met.

## Where to start (P0)

| Stream | First spec to write |
|---|---|
| S1 | `S1-engine/ENGINE-CORE.md` — board rep, move-gen, the perft/conformance corpus (G-A) |
| S2 | `S2-realtime/GAME-SERVER.md` — game lifecycle, clock, matchmaking, the WS protocol (ties to MASTER §VIII.4) |
| S3 | `S3-platform/DATA-MODEL.md` + `RATING-SERVICE.md` — ratify MASTER §VIII entities; Glicko-2 service (ADR-1, G-C) |
| PE | `PE-platform-eng/API-CONTRACT.md` — freeze the v1 API/WS surface (G-D); CI/CD + environments |
| S4/S5 | board component + client shell specs against the frozen protocol |
| S6 | `S6-trust-safety/ANTICHEAT.md` — signal catalogue (collect from day 1), detection phases (ADR-2) |

The first cross-stream deliverable is the **F-01 Play Online** vertical slice — spec it in `S2-realtime/F-01-play-online.md` using the feature template, since it exercises S1+S2+S3+S4 end to end.
