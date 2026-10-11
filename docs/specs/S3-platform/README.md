# S3 · Platform/Backend Services — specs

| Field | Value |
|---|---|
| Stream | S3 · Platform/Backend Services |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-3 accounts, X-4 ratings, X-13 API, X-14 data; F-05, F-08, F-25-27, F-46 |
| Gates this stream feeds | G-C rating integrity |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- DATA-MODEL.md — ratify MASTER VIII entities
- RATING-SERVICE.md — Glicko-2 per-pool, event-sourced (ADR-1, G-C)
- ACCOUNTS.md — auth, profiles, sessions

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
