# S2 · Real-time Play Platform — specs

| Field | Value |
|---|---|
| Stream | S2 · Real-time Play Platform |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | X-2 game server, matchmaking, clocks, reconnection; F-01, F-04, F-06, F-07 |
| Gates this stream feeds | G-E scale proven |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## Specs in this stream
- **PROTOCOL.md** — WS binary frame + event-store schema + entity model (SPEC-S2-PROTOCOL, **RATIFIED**; the frozen §4A contract, feeds G-D). Covers the WS frame layout (MASTER §VIII.4) — no separate WS-PROTOCOL.md needed.
- **PLAN-CODEC.md** — the ADR-14 wire codec implementation plan (**RATIFIED**).
- **GAME-SERVER.md** — lifecycle, clock, matchmaking, reconnection, codec consumption / OPEN-C4 (SPEC-S2-GAME-SERVER, **RATIFIED** 2026-10-13; feeds G-E). GS-1 (lifecycle + clock core) implemented + merged (PR #34).
- **GAME-SERVER-GS2-ENGINE-CGO.md** — implementation slice of SPEC-S2-GAME-SERVER §3.6/§4: the engine-cgo legality seam (binds the S1 engine into the Go server over cgo, ADR-15; fills GS-1's stubbed `LegalityOracle`). **RATIFIED** 2026-10-13. Implemented + merged (PR #38).
- **GAME-SERVER-GS3-LIVE-SESSION.md** — implementation slice of SPEC-S2-GAME-SERVER §3.5/§3.6/§6.3: the live in-memory game session (owns CurFEN + clocks + folded Zobrist history, drives the GS-1 pipeline against the GS-2 engine, serves reconnection resync, **closes Q-GS5 repetition**). **RATIFIED** 2026-10-13. No new wire bytes, no new ADR; carries one coordinated LegalityOracle/MoveIntent change (Q-GS3B).
- **GAME-SERVER-GS4-EVENT-STORE.md** — implementation slice of SPEC-S2-GAME-SERVER §2c / ADR-10: the durable append-only event store replacing the GS-1 MemStore stub (makes NFR-2/NFR-7 real, enables atomic terminal append). **REVIEW**. Carries one ADR-10 sub-decision — the store product (Q-GS4A, proposed Postgres) — for architecture sign-off.

## Still to write (P0+)
- MATCHMAKING.md (if the §3.4 model needs more depth than GAME-SERVER carries)
- F-01-play-online.md (the first vertical slice, feature template)

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
