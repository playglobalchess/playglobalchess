# S5 · Mobile Clients (iOS + Android) — specs

| Field | Value |
|---|---|
| Stream | S5 · Mobile Clients (iOS + Android) |
| Owner | <stream lead — fill in> |
| Owns (MASTER IDs) | Native apps over shared Rust core (ADR-9) |
| Gates this stream feeds | G-D API frozen |

See `../README.md` for how specs relate to `../../MASTER-PLAN.md`, and `../_shared/` for templates + conventions. Cite master IDs; never redefine them. Decisions beyond the master doc → propose a new ADR there.

## First specs to write (P0)
- RUST-CORE-FFI.md — UniFFI boundary, shared with S1
- IOS-APP.md
- ANDROID-APP.md
- both consume the frozen protocol

## How to add a spec
1. Copy `../_shared/SPEC-TEMPLATE.md` (system/component) or `../_shared/FEATURE-SPEC-TEMPLATE.md` (an F-xx module) into this folder.
2. Fill the header (Spec ID, Implements, Depends on, Gates).
3. Status `DRAFT → REVIEW → RATIFIED → BUILDING → DONE` per `../_shared/CONVENTIONS.md`.
