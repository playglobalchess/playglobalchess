# Branch protection for `main` (#117)

**Status: designed and committed, NOT applied.** Applying it changes who can merge, so it is the owner's call.

## What it enforces

`.github/rulesets/main.json` is a GitHub ruleset on the default branch:

| Rule | Setting | Why |
|---|---|---|
| Pull request required | 0 approvals, squash merge only | A solo owner cannot approve their own PR. Squash matches how PRs are merged today. |
| Required checks | `ci-ok` and `validate-docs` | See below. |
| No deletion, no force-push | on | Matches the "never force-push `main`" convention. |
| Bypass | none | Nobody, the owner included, can merge past a red check. To override in an emergency, edit the ruleset, then restore it. |
| Up-to-date branch | not required | Requiring it forces a rebase after every merge. Revisit when more than one person merges. |
| CODEOWNERS review | not required yet | `.github/CODEOWNERS` names 10 teams with no members. Turn on `require_code_owner_review` once they have members. |

## Why two required checks, not one per job

A required check that never reports blocks the PR forever, and one that is skipped counts as passed. Both defeat the gate, so neither the individual `ci` jobs nor path-filtered workflows are required:

- **`ci-ok`** is the aggregate. It `needs` every other job in `ci.yml`, runs even when they fail, and fails on a failure, a cancellation, or a skip that its path gate does not justify (`platform/ci/ci_ok.py`). `check_ci_filters.py` (P9) fails the `ci-filter-audit` job if a job is added to `ci.yml` without being listed in `ci-ok`.
- **`validate-docs`** comes from `docs-check`, which now runs on every PR (no `paths:` filter), so it always reports.

Security gates (`govulncheck`, `npm-audit`, `cargo-deny`, `secret-scan`) are jobs of `ci.yml`, so `ci-ok` covers them. A new advisory against an existing dependency can fail unrelated PRs until the dependency is bumped; for Rust, an advisory can be ignored in `deny.toml` with a reason.

## Apply

```
gh api -X POST repos/playglobalchess/playglobalchess/rulesets --input .github/rulesets/main.json
```

Then open one docs-only PR and one code-only PR and confirm both can merge once `ci-ok` and `validate-docs` are green. Update an existing ruleset with `gh api -X PUT repos/playglobalchess/playglobalchess/rulesets/<id> --input .github/rulesets/main.json`.

## Not covered

- CodeQL alerts do not block merges. Add a `code_scanning` rule to the ruleset to make them block.
- Merge queue, signed commits and linear-history rules are not enabled.
