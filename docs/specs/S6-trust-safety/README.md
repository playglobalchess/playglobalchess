# S6 · Trust & Safety — specs

| Field | Value |
|---|---|
| Stream | S6 · Trust & Safety |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-5 anti-cheat, X-11 moderation; F-32/47/48 safety; F-41 compliance |
| Gates this stream feeds | G-B fair-play baseline, G-F compliance |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- ANTICHEAT.md — signal catalogue (collect day 1), detection phases, review queue (ADR-2)
- MODERATION.md — chat/forum/club (X-11)
- COMPLIANCE.md — GDPR/COPPA controls (ADR-6)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
