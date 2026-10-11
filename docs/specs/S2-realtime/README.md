# S2 · Real-time Play Platform — specs

| Field | Value |
|---|---|
| Stream | S2 · Real-time Play Platform |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-2 game server, matchmaking, clocks, reconnection; F-01, F-04, F-06, F-07 |
| Gates this stream feeds | G-E scale proven |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- GAME-SERVER.md — lifecycle, clock, reconnection
- WS-PROTOCOL.md — binary frame layout (cites MASTER VIII.4)
- MATCHMAKING.md
- F-01-play-online.md (first vertical slice)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
