# accounts

**S3 · Auth, profiles, sessions (X-3).** Implements the ratified spec
[`SPEC-S3-ACCOUNTS`](../../docs/specs/S3-platform/ACCOUNTS.md) (RATIFIED
2026-10-23, v0.19). The API-CONTRACT and DATA-MODEL amendments this service
depends on are applied to the frozen contracts on `main`.

## Status: scaffold

This is the first build chunk — a clean-compiling, tested skeleton. It
deliberately ships only the pure domain core; transport, storage, and the auth
mechanisms land in later chunks, one reviewable slice at a time.

**Here now**
- `go.mod` — module `…/services/accounts`, Go 1.26 (matches game-server), pure Go.
- `internal/identity/` — the load-bearing domain types from the spec §3.1/§3.6:
  `SubjectKind`, `AccountState`, the per-market `JurisdictionPolicy` lever, and
  the single `RatedEligible()` derivation (ACC-INV-1 + the RESTRICTED-child gate
  + the minor-rated market lever folded into one signal). Fully unit-tested,
  including the live-recompute property (finding S3) and the minor-rated lever
  both ways.
- `cmd/accounts/` — entrypoint stub (starts, logs, exits).
- `Makefile` — `build` / `vet` / `test` / `clean`.

**Deferred to later chunks (NOT here yet)**
- HTTP transport + the §11.1 endpoints (`/v1/auth/*`, `/v1/me/*`).
- Persistence: the `User`/`Credential`/`Profile` store + the ephemeral
  `GuestSession` / `MagicLinkIssue` / `UpgradeClaim` hot-store records.
- The auth mechanisms: register, login, OAuth (+ link), magic-link, guest,
  and the verify-first guest→account upgrade flow.
- The age-gate (`/age-screen` + the register cookie binding) and the consent
  state machine.

## Build

```
make -C services/accounts build   # compile
make -C services/accounts vet      # go vet
make -C services/accounts test     # unit tests
```
