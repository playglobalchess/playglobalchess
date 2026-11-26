"""ci_ok: the verdict behind the single required `ci-ok` check (#117).

Runs as the last step of the `ci-ok` job in ci.yml with
  NEEDS_JSON   toJSON(needs): every job's result and, for `changes`, its outputs
  EVENT_NAME   github.event_name
and fails unless every job ci-ok depends on either succeeded or was skipped for
a reason the workflow itself justifies:

  - a gated job (`if: needs.changes.outputs.X == 'true' || ... || schedule`) may
    be skipped only when the event is not a schedule and every output it is
    gated on is not 'true';
  - a nightly-only job (`if: github.event_name == 'schedule'`) may be skipped
    only when the event is not a schedule;
  - any other job (no `if`) must succeed. A skip there means an upstream job
    failed, so the skip is not a pass.

A failure or a cancellation always fails. The `changes` job must succeed. The
set of jobs ci-ok depends on is audited statically by check_ci_filters.py (P9),
so a job added to ci.yml cannot be left out of this verdict.

Local use (needs PyYAML, see requirements-ci.txt):
  NEEDS_JSON='{...}' EVENT_NAME=pull_request python3 platform/ci/ci_ok.py
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402

SELF = "ci-ok"
SCHEDULE_ONLY = c.SCHEDULE_TERM


def verdict(wf: dict, needs: dict, event: str) -> list[str]:
    """Problems that must fail ci-ok; empty means pass."""
    errors: list[str] = []
    jobs: dict = wf.get("jobs") or {}
    changes_out = ((needs.get("changes") or {}).get("outputs")) or {}

    for name in sorted(set(jobs) - {SELF}):
        if name not in needs:
            errors.append(f"{name}: not in ci-ok's needs, so its result is not checked")

    for name, info in sorted(needs.items()):
        result = (info or {}).get("result")
        spec = jobs.get(name)
        if not isinstance(spec, dict):
            errors.append(f"{name}: in needs but not a job of ci.yml")
        elif result == "success":
            continue
        elif result == "skipped":
            why = skip_problem(name, spec, changes_out, event)
            if why:
                errors.append(f"{name}: skipped, {why}")
        else:
            errors.append(f"{name}: {result!r}")
    if (needs.get("changes") or {}).get("result") != "success":
        errors.append("changes: must succeed (every path gate depends on it)")
    return sorted(set(errors))


def skip_problem(name: str, spec: dict, changes_out: dict, event: str) -> str | None:
    """None when this job may be skipped for this event, else why not."""
    expr = spec.get("if")
    outs, _ = c.gate_outputs(expr)
    if outs is not None:
        if event == "schedule":
            return "but every gated job must run on a schedule event"
        hot = sorted(o for o in outs if changes_out.get(o) == "true")
        if hot:
            return f"but its gate output(s) {hot} are 'true', so it had to run"
        return None
    if isinstance(expr, str) and expr.strip().strip("${} ").strip() == SCHEDULE_ONLY:
        return "but this is a schedule event, so it had to run" if event == "schedule" else None
    return "but it has no gate that allows a skip (an upstream job failed or did not run)"


def main() -> int:
    wf = c.load_yaml((c.REPO / c.CI_YML_REL).read_text(encoding="utf-8"))
    try:
        needs = json.loads(os.environ["NEEDS_JSON"])
        event = os.environ["EVENT_NAME"]
    except (KeyError, ValueError) as e:  # fail closed
        print(f"::error::ci-ok needs NEEDS_JSON and EVENT_NAME: {e!r}")
        return 1
    errors = verdict(wf, needs, event)
    for e in errors:
        print(f"::error::{e}")
    print(f"ci_ok: {len(needs)} jobs, event={event}, {len(errors)} problem(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
