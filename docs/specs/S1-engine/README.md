# S1 · Core Chess Engine — specs

| Field | Value |
|---|---|
| Stream | S1 · Core Chess Engine |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-1 rules engine (all variants), X-6 analysis/engine integration, X-7 puzzle pipeline |
| Gates this stream feeds | G-A engine correct (critical path) |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- ENGINE-CORE.md — board rep, move-gen, perft+conformance corpus (G-A)
- VARIANTS.md — data-driven variant config incl. Spell Chess v1 (ADR-4)
- ANALYSIS-INTEGRATION.md — Stockfish/NNUE + tablebases

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
