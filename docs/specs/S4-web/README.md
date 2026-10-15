# S4 · Web Client — specs

| Field | Value |
|---|---|
| Stream | S4 · Web Client |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | Web app for all features (reference client) |
| Gates this stream feeds | — (consumes G-A/B/C/D) |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## Specs
- **WEB-F01-FIRST-PLAYABLE.md** — the first S4 slice: the minimal F-01 web client (Vite+React+TS) that lets a human play a Standard game browser↔server against the live GS-6 edge. Carries a prerequisite **S4a** (a typed wasm message seam over `codec-wasm`, mirroring the GS-6a/GS-6 Go seam — `codec-wasm` today only reencodes frames, so TS cannot build/read §4A messages). **REVIEW** — approval-gated; folds BOARD-COMPONENT/CLIENT-SHELL/PLAY-UI (below) into one first-playable slice rather than three parallel docs. No lettered gate; the visible client half of the F-01 vertical slice.

## First specs to write (P0)
- BOARD-COMPONENT.md — render + legal-move UX
- CLIENT-SHELL.md — app shell, design system, i18n (NFR-11)
- PLAY-UI.md — against frozen WS protocol

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
