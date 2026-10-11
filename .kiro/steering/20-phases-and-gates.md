---
inclusion: always
---
# Phases, Gates & Acceptance Criteria (what "done" means)

Condensed from `docs/MASTER-PLAN.md` §IV–V, §XII. Build phase by phase; each gate is a hard stop.

## Dependency gates (hard)
| Gate | Green when | Blocks until green |
|---|---|---|
| **G-A** Engine correct | perft + conformance corpus 100%; sub-ms move-gen | all live play, variants, puzzles, analysis |
| **G-B** Fair-play baseline | anti-cheat flags engine-correlated play + review queue exists | any *rated* play |
| **G-C** Rating integrity | Glicko-2 deterministic + auditable, per-pool | stats, leaderboards, rating matchmaking |
| **G-D** API frozen (v1) | `protocol/` stable for clients | mobile parity at scale |
| **G-E** Scale proven | load-tested to IX.2 with chaos/reconnect, zero game loss | public beta |
| **G-F** Compliance | GDPR + COPPA + residency signed off | ChessKid + public launch |

## Phases & deliverables (propose work in this order)
- **P0 Foundations:** engine (Standard) + perft corpus (→G-A); `protocol/` draft; accounts; rating service skeleton; game-server spike; CI/observability. **Exit: G-A green, protocol v0 drafted.**
- **P1 Core loop (web):** F-01 Play Online (Standard, all time controls), F-08 history, ratings live, anti-cheat baseline wired, F-17 analysis, F-22-GR report, F-09/F-13 puzzles, F-46 profile, F-48 chat, F-49 notifications. **Exit: G-B + G-C green; a rated Standard game is correct/fair/credible on web.**
- **P2 Mobile parity + depth:** iOS+Android to parity (G-D frozen), F-02 bots, F-04 friend, F-06 tournaments, Chess960, F-05 full stats, F-25–27, F-10/11 puzzles. 
- **P3 Variants + real-time competition + social:** F-07 variants incl. Spell Chess v1 (ADR-4), F-12 puzzle battle, F-23 clubs, F-03 coach, F-18 insights, F-19 classroom.
- **P4 Learn/Train + ecosystem:** F-14–16, F-37/34/35/36/43, F-28–32, F-33/39/40, F-50–52 watch.
- **P5 ChessKid + commerce + hardening:** F-41 (COPPA), F-44/45 + entitlements live, G-E scale proven, accessibility (NFR-8), i18n locales, security review. **Exit: Part XII launch bar met.**

## Definition of Done (per module — MASTER §XII.1)
A module is DONE only when: spec approved + implemented on all target platforms for its tier; unit+contract+integration tests green; engine/variant work passes its conformance corpus; accessibility checked (NFR-8); i18n-externalized (NFR-11); observability wired; spec + docs updated; acceptance criteria demonstrably met. "Code complete" ≠ done.

## Per-phase acceptance (generic rule)
A phase is complete only when: all its modules are DONE, its exit gate is green, and the owner has approved the phase review. Then propose the next phase.
