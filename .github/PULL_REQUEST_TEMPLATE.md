## What & why

<!-- One or two lines. Link the spec and the master IDs this touches. -->
Spec: `docs/specs/Sx-…/…`  ·  IDs: `F-xx` / `X-x` / `ADR-xx`

## Checklist

- [ ] References master IDs correctly (no new IDs invented in `F/X/ADR/NFR` namespaces)
- [ ] Spec exists and is `RATIFIED` for any new feature behavior (or this PR is the spec)
- [ ] Tests added/updated (unit/contract/…); engine changes include conformance cases (MASTER §X)
- [ ] No shared-contract (`protocol/`, event schema, entity model) change — or it has both-stream + architecture-group review
- [ ] No rated-play / ratings / anti-cheat change — or it has S6 review
- [ ] No secrets committed; new surfaces threat-modelled (MASTER §XI) if relevant
- [ ] Docs updated if behavior changed; new architecture decision → new ADR in `docs/MASTER-PLAN.md`
- [ ] CI green

## Gates touched (if any)

<!-- G-A engine correct · G-B fair-play · G-C rating integrity · G-D API frozen · G-E scale · G-F compliance -->
