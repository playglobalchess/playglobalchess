# S8 · Community/Watch/Commerce — specs

| Field | Value |
|---|---|
| Stream | S8 · Community/Watch/Commerce |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | F-22-24, F-33, F-38-40, F-44-45, F-49-52; X-9/10/12 |
| Gates this stream feeds | — (feeds ecosystem + revenue) |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- SOCIAL-GRAPH.md (F-22/23)
- ENTITLEMENTS.md — freemium hook (ADR-8)
- NOTIFICATIONS.md (X-9)
- WATCH.md — events/stream/playing-now (X-10, F-50-52)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
