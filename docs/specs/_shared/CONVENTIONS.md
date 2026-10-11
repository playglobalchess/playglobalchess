# Spec Conventions (shared)

Applies to every document under `specs/`. Keeps 8+ parallel streams speaking one language.

## ID vocabulary (defined in MASTER-PLAN, never redefined here)
| Prefix | Meaning | Example |
|---|---|---|
| `F-xx` | Feature module | F-01 Play Online |
| `X-x` | Cross-cutting system | X-2 Real-time game server |
| `NFR-x` | Non-functional requirement | NFR-1 move latency |
| `G-x` | Business goal | G-2 fair real-time play |
| `P-x` | Persona | P-2 competitive player |
| `S-x` | Delivery stream (squad) | S1 Engine |
| `G-A…G-F` | Dependency gate | G-A engine correct |
| `P0…P5` | Phase | P1 core play loop |
| `ADR-x` | Architecture decision | ADR-1 Glicko-2 |
| `OPEN-x` | Open item | OPEN-1 lesson scope |

A spec **cites** these; it does not invent new ones in these namespaces. New feature sub-IDs use dotted form under their module (e.g. `F-01.6`).

## Spec lifecycle (status field at top of every spec)
```
DRAFT      author is still writing
REVIEW     ready for stream + cross-stream review
RATIFIED   reviewed, agreed; architecture group signed off where it touches shared contracts
BUILDING   tickets cut, implementation in progress
DONE       meets its acceptance criteria + the Definition of Done (MASTER §XII.1)
SUPERSEDED by <doc> — kept for history, no longer current
```

## When a spec needs a decision the master doc didn't make
Do **not** decide architecture locally. Open a new ADR in `MASTER-PLAN.md` (next free number), get it ratified, then reference it. Local specs implement decisions; they don't make them.

## Cross-stream contracts
Anything two streams share — the API/WS protocol, the event-store schema, the entity model — is a **contract** and changes to it require review by **both** streams + the architecture group. These live in `PE-platform-eng/` and `S3-platform/` and are cited, not copied, by consumers.

## Review rules
- Every spec is reviewed before `RATIFIED`.
- A spec touching a shared contract or an NFR budget also needs architecture-group review.
- A spec touching rated play, ratings, or anti-cheat also needs S6 review (fair-play has veto on anything that weakens detection).

## Writing standard
State the edge cases and failure modes, not just the happy path. For engine/rules work, the **test corpus is part of the spec** (MASTER §X). Prefer explicit acceptance criteria a QA engineer could execute.
