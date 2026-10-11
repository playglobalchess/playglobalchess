# S7 · Learn/Train/Content — specs

| Field | Value |
|---|---|
| Stream | S7 · Learn/Train/Content |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-8 CMS; F-14-21, F-28-37, F-42-43 |
| Gates this stream feeds | — (feeds retention funnel) |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- CMS.md — content model, locales
- LESSONS-COURSES.md (F-14/16, OPEN-1 taxonomy)
- ANALYSIS-INSIGHTS.md (F-17/18)
- PUZZLE-UX.md (Section B shared features)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
