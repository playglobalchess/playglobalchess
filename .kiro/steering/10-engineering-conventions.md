---
inclusion: always
---
# Engineering Conventions (always follow)

Full rationale: `docs/MASTER-PLAN.md`. This file is the enforceable summary.

## Architecture patterns
- **Server-authoritative.** Clients send *intents* (proposed move); the server validates via the engine and emits the authoritative event. Clients never compute game truth. (MASTER §VIII.1)
- **Event-sourced games.** A game is an append-only event stream; board state/PGN/Move rows are *projections*. Durably persist a move event **before** acking it (NFR-7). (ADR-10, §VIII.3)
- **One rules engine, three consumers.** The Rust engine (ADR-11) is the ONLY implementation of chess rules — server + iOS + Android all bind it. **A service or client that reimplements chess logic is a bug** (NFR-5).
- **Stores by role (ADR-10):** Postgres = relational truth (accounts/ratings/clubs); event store = game truth; Redis = hot/ephemeral only (clocks, matchmaking, presence) and never the source of truth; object storage+CDN = media/PGN; columnar warehouse = analytics, never on the play path.
- **Contracts live in `protocol/`** and are cited, not copied. Changing a shared contract needs both affected streams + architecture review.

## Languages & stacks (ADR-9/11/12)
- **Engine + shared client core:** Rust. **Services:** Go. **Web:** TypeScript + React. **iOS:** Swift (binds Rust core via UniFFI). **Android:** Kotlin (binds Rust core).
- Internal RPC: gRPC. External: REST/JSON (non-realtime) + WebSocket binary (live play).

## Coding standards
- **Rust:** `thiserror` for library error types; no `unwrap()`/`panic!` on the server hot path (use `?`/`expect("reason")`); `unsafe` only with a safety comment; `clippy` clean; `rustfmt`.
- **Go:** standard `gofmt`/`vet`/`golangci-lint`; errors wrapped with context; context.Context on all I/O; no global mutable state; table-driven tests.
- **TypeScript:** strict mode; no `any` (use `unknown`+narrow); `const` over `let`; no hard-coded user-facing strings (i18n, NFR-11).
- **All languages:** small functions; comments only for non-obvious logic; no secrets in code; pin dependency versions; input validation + parameterized queries by default.

## Directory structure (monorepo)
```
engine/           Rust rules engine + variants (S1)
protocol/         shared WS/API + event schema (cited everywhere)
services/<name>/  Go backend services (S2/S3/S6/S7)
clients/web|ios|android/   (S4/S5)
platform/         infra, CI/CD, observability (PE)
docs/             MASTER-PLAN + specs + ops
```
A new component goes in its stream's folder with its own build file; wire its CI job in `.github/workflows/ci.yml` (path-filtered).

## Testing expectations (MASTER §X)
- **Engine: the test corpus is part of the spec.** Perft suite + legal-move edge cases + game-end detection + per-variant conformance + golden-game replay. A variant ships only at 100% green. This is **G-A**.
- Test pyramid: unit + property-based → contract (schema conformance, runs on all 3 clients) → integration → e2e → load/chaos (G-E) → security.
- **Rating:** deterministic (same games → identical ratings) + auditable (replay the rating event log). This is **G-C**.
- CI must pass before merge; nothing rated-env ships unless G-A/G-B/G-C green.

## Security & fair play (MASTER §XI)
- Every new surface gets a threat-model pass. Server-side validation for all moves/clocks (T-1/T-2). Anti-cheat observes every rated game (ADR-2); S6 has review veto on anything weakening detection. Secrets in a vault, mTLS between services, least privilege.
