<!-- Copy this file to start a system/component spec. Delete these comments. -->
# <Spec title> — <Stream, e.g. S2 Real-time>

| Field | Value |
|---|---|
| Spec ID | <e.g. SPEC-S2-GAME-SERVER> |
| Owner | <name / role> |
| Status | DRAFT |
| Version | 0.1 |
| Last updated | <date> |
| Implements (master IDs) | <e.g. X-2, F-01, NFR-1, NFR-3> |
| Depends on | <other specs / gates, e.g. SPEC-S1-ENGINE-CORE (G-A)> |
| Gates it feeds | <e.g. G-E scale proven> |

## 1. Purpose & scope
What this component is, what it is NOT (explicit non-goals).

## 2. Requirements traced
Which master `F-xx` / `X-x` / `NFR-x` this satisfies, and how each maps to a section below.

## 3. Design
Architecture, data structures, algorithms, interfaces. Diagrams welcome. Cite shared contracts (API/WS, event store) rather than copying them.

## 4. Interfaces & contracts
APIs exposed/consumed, events emitted/subscribed, schemas. If this changes a shared contract, say so and name the review needed (see CONVENTIONS).

## 5. Edge cases & failure modes
The non-happy paths. For rules/engine work, enumerate the correctness edge cases (MASTER §X.1).

## 6. Non-functional handling
How this meets its NFR budgets (latency IX.1, capacity IX.2, availability, etc.). State the numbers.

## 7. Security & abuse
Which Part XI threats touch this component and the controls here.

## 8. Test plan
Unit / property / contract / integration / e2e / load. The acceptance corpus. For the engine, the perft/conformance cases.

## 9. Acceptance criteria (Definition of Done)
A checklist a reviewer/QA can execute to call this DONE (ties to MASTER §XII.1).

## 10. Open questions
Anything unresolved. If it needs an architecture decision, link the proposed ADR number.
