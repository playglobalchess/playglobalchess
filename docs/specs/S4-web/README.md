# S4 · Web Client — specs

| Field | Value |
|---|---|
| Stream | S4 · Web Client |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | Web app for all features (reference client) |
| Gates this stream feeds | — (consumes G-A/B/C/D) |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- BOARD-COMPONENT.md — render + legal-move UX
- CLIENT-SHELL.md — app shell, design system, i18n (NFR-11)
- PLAY-UI.md — against frozen WS protocol

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
