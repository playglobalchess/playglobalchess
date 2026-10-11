# Contributing to PlayGlobalChess

This repo is a **monorepo**: planning docs (`docs/`) and all code live together. Read `README.md` and `docs/MASTER-PLAN.md` before your first change.

## 1. The golden rules (from the master plan)

1. **Reference master IDs; never redefine them.** Use `F-xx`, `X-x`, `G-A…G-F`, `ADR-x`, `NFR-x` exactly as `docs/MASTER-PLAN.md` defines them — in specs, branch names, commit messages, tickets, and tests.
2. **Decide architecture only via an ADR.** If a change needs a decision the master doc didn't make, add a new ADR in `docs/MASTER-PLAN.md` (next free number), get it ratified, then implement. Never decide architecture inside a stream folder.
3. **The master plan is versioned, not frozen.** Change a decision by adding/superseding an ADR and bumping the version — never silently rewrite. The reasoning trail is the asset.
4. **Gates are hard.** Nothing rated ships until **G-A** (engine correct), **G-B** (fair-play baseline), **G-C** (rating integrity) are green. Nothing public ships until the **Part XII launch bar** is met.
5. **Specs cite shared contracts (`protocol/`), never copy them.**

## 2. Repository layout → ownership

| Path | Stream | Owner |
|---|---|---|
| `engine/` | S1 Engine | *(assign)* |
| `services/game-server/` | S2 Real-time | *(assign)* |
| `services/{accounts,rating}/` | S3 Platform | *(assign)* |
| `services/anti-cheat/` | S6 Trust & Safety | *(assign)* |
| `services/{puzzles,content}/` | S7 Learn/Content | *(assign)* |
| `clients/web/` | S4 Web | *(assign)* |
| `clients/{ios,android}/` | S5 Mobile | *(assign)* |
| `platform/`, `protocol/` | PE Platform-Eng | *(assign)* |
| `docs/` | shared; each stream owns its `specs/Sx-…/` folder | — |

A change to a **shared contract** (`protocol/`, the event schema, the entity model) needs review from **both** affected streams **and** the architecture group. A change touching rated play, ratings, or anti-cheat also needs **S6 review** (fair-play has veto on anything that weakens detection).

## 3. Branching & commits

- Branch from `main`: `feat/F-01-play-online`, `fix/X-2-reconnect`, `docs/adr-11-matchmaking`, `chore/ci`.
- Never push directly to `main` — open a PR.
- Commit messages reference the ID: `F-01.4: handle reconnect resync` / `ADR-11: add matchmaking algorithm decision`.
- Keep commits scoped; don't mix a doc change and a code change in unrelated areas.

## 4. The workflow

```
Pick work → find its ID in MASTER-PLAN
  → write/locate the spec in docs/specs/Sx-…/ (copy a _shared template)
  → spec status DRAFT → REVIEW → RATIFIED
  → cut tickets referencing the spec + IDs
  → branch, implement, add tests
  → open PR (CI must pass) → review → merge
  → feature DONE only when it meets its acceptance criteria + MASTER §XII.1
```

A feature is not built before its spec exists and is RATIFIED.

## 5. Spec lifecycle

`DRAFT → REVIEW → RATIFIED → BUILDING → DONE` (or `SUPERSEDED by <doc>`). See `docs/specs/_shared/CONVENTIONS.md`. Every spec starts from a `_shared/` template and fills its header (Spec ID, Implements, Depends on, Gates).

## 6. What CI enforces (see `.github/workflows/`)

- **docs-check**: ID references resolve, spec files have a Status header, no stray `TODO`-only specs merged as RATIFIED, markdown links not broken.
- **i18n-guard** (clients): no hard-coded user-facing strings (NFR-11) — becomes active when client code lands.
- **per-stream build/test**: unit + contract + lint, gated on changed paths — becomes active as each folder gets a build.
- **release gate**: the engine conformance corpus (G-A) must be 100% before merge to `main` touching `engine/` — active when the engine exists.

CI is additive: jobs light up automatically as the paths they watch gain real build files. Until then they no-op with a clear "stub — no build yet" message, so a green check never means "untested code shipped."

## 7. Testing expectations (MASTER §X)

For engine/rules work, **the test corpus is part of the spec** — perft + conformance cases ship with the code. All code: unit + property where it fits; anything crossing the wire: contract tests against `protocol/`. See the test pyramid in `docs/MASTER-PLAN.md` Part X.

## 8. Security & secrets

Never commit secrets (`.env`, keys, tokens — `.gitignore` blocks the common ones, but you are the last line). New surfaces get a threat-model pass against `docs/MASTER-PLAN.md` Part XI. Dependencies: pin versions; flag anything unusual.

## 9. Code review

At least one owner approval; shared-contract/NFR changes also need architecture-group approval; rated-play/ratings/anti-cheat also need S6. Reviews check: correct ID references, tests present, edge cases handled, no secret leakage, spec updated if behavior changed.
