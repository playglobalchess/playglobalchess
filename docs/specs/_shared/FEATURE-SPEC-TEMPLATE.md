<!-- Copy this file to spec a feature module (F-xx). Delete these comments. -->
# F-<nn> <Feature name> — detailed spec

| Field | Value |
|---|---|
| Feature ID | F-<nn> |
| Owner stream | <S-x> |
| Status | DRAFT |
| Version | 0.1 |
| Tier (MASTER §IV) | <0–4> |
| Phase (MASTER §V) | <P0–P5> |
| Platforms | web / iOS / android |
| Depends on | <systems + specs, e.g. X-1 engine, X-2 server, SPEC-S3-DATA-MODEL> |

## 1. User stories
As a <persona P-x>, I want … so that …. (One per key capability.)

## 2. Functional requirements
Expand every sub-requirement from MASTER §II for this module (e.g. F-01.1…F-01.5), each to buildable detail.

## 3. UX / flows
Screens, states, interactions. For play features: the board interaction model, premove, clock display.

## 4. State machine
The authoritative states and transitions (e.g. for a game: seeking → active → ended; with abort/draw/resign/flag/reconnect paths). Enumerate illegal transitions.

## 5. Data & events
Entities touched (MASTER §VIII.2), events emitted/consumed (§VIII.3). New fields → propose schema change via the owning contract spec.

## 6. API / protocol
Endpoints + messages used (cite §VIII.4/§VIII.5). Request/response/error shapes.

## 7. Edge cases & failure modes
Disconnect/reconnect, concurrent actions, race conditions (e.g. `ply_expected` guard), timeout/flag, abuse paths.

## 8. Non-functional
Latency/availability targets for this feature; what degrades gracefully under load.

## 9. Security & fair play
Relevant Part XI threats; for rated features, how anti-cheat (ADR-2) observes it.

## 10. Test plan & acceptance criteria
Scenarios, the e2e happy path, the failure-mode cases, and a DONE checklist (MASTER §XII.1).

## 11. Open questions
Unresolved items; link any proposed ADR.
