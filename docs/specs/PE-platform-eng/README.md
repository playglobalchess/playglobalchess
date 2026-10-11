# PE · Platform Engineering — specs

| Field | Value |
|---|---|
| Stream | PE · Platform Engineering |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-14 observability, CI/CD, environments, the API contract X-13 |
| Gates this stream feeds | G-D API frozen, G-E scale proven |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- API-CONTRACT.md — freeze v1 REST+WS surface (G-D)
- CI-CD.md — pipelines, release gates (MASTER X.4)
- ENVIRONMENTS.md
- OBSERVABILITY.md (NFR-9)
- LOAD-TEST-HARNESS.md (G-E)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
