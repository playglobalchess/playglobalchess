#!/usr/bin/env python3
"""Unit tests for ci_ok.py, the verdict of the single required check (#117).

Run (needs PyYAML, see platform/ci/requirements-ci.txt):
  python3 -m unittest discover -s platform/ci -p 'test_*.py'

The workflow under test is the REAL ci.yml, so a renamed or re-gated job changes
these answers. The point of each test is a hollow green the verdict must refuse.
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402
import ci_ok  # noqa: E402

WF = c.load_yaml((c.REPO / c.CI_YML_REL).read_text(encoding="utf-8"))
JOBS = [j for j in WF["jobs"] if j != "changes" and j != "ci-ok"]
FILTERS = list(c.filters_of(WF)[0])
NIGHTLY = {"engine-nightly-perft", "engine-perf-bench"}


def needs_for(event: str = "pull_request", changed: set[str] | None = None, **override: str) -> dict:
    """What toJSON(needs) would hold if every job did what the filters imply."""
    changed = changed or set()
    outputs = {f: ("true" if f in changed else "false") for f in FILTERS}
    needs: dict = {"changes": {"result": "success", "outputs": outputs}}
    for j in JOBS:
        outs, _ = c.gate_outputs(WF["jobs"][j].get("if"))
        if outs is not None:
            runs = event == "schedule" or any(o in changed for o in outs)
        elif j in NIGHTLY:
            runs = event == "schedule"
        else:
            runs = True
        needs[j] = {"result": "success" if runs else "skipped", "outputs": {}}
    for j, r in override.items():
        needs[j.replace("_", "-")]["result"] = r
    return needs


class VerdictTest(unittest.TestCase):
    def problems(self, needs: dict, event: str = "pull_request") -> str:
        return "\n".join(ci_ok.verdict(WF, needs, event))

    def test_docs_only_pr_passes_with_gated_jobs_skipped(self) -> None:
        needs = needs_for()
        self.assertEqual(needs["services"]["result"], "skipped")
        self.assertEqual(self.problems(needs), "")

    def test_code_pr_passes_when_gated_jobs_ran(self) -> None:
        self.assertEqual(self.problems(needs_for(changed={"services", "web", "e2e"})), "")

    def test_schedule_passes_when_everything_ran(self) -> None:
        self.assertEqual(self.problems(needs_for("schedule"), "schedule"), "")

    def test_failed_job_fails(self) -> None:
        self.assertIn("services: 'failure'", self.problems(needs_for(changed={"services"}, services="failure")))

    def test_cancelled_job_fails(self) -> None:
        self.assertIn("web: 'cancelled'", self.problems(needs_for(changed={"web"}, web="cancelled")))

    def test_skipped_job_whose_gate_is_true_fails(self) -> None:
        # The hollow green: the gate said run, the job did not.
        self.assertIn("services: skipped, but its gate output(s) ['services'] are 'true'",
                      self.problems(needs_for(changed={"services"}, services="skipped")))

    def test_skipped_unconditional_job_fails(self) -> None:
        for job in ("govulncheck", "npm-audit", "cargo-deny", "secret-scan", "rest-contract", "ci-filter-audit"):
            with self.subTest(job=job):
                self.assertIn(f"{job}: skipped, but it has no gate", self.problems(needs_for(**{job: "skipped"})))

    def test_failed_security_job_fails(self) -> None:
        for job in ("govulncheck", "npm_audit", "cargo_deny", "secret_scan"):
            with self.subTest(job=job):
                self.assertIn(job.replace("_", "-"), self.problems(needs_for(**{job: "failure"})))

    def test_changes_job_failure_fails(self) -> None:
        needs = needs_for()
        needs["changes"]["result"] = "failure"
        out = self.problems(needs)
        self.assertIn("changes: 'failure'", out)

    def test_changes_failure_cannot_hide_behind_skips(self) -> None:
        # changes failed, so every gated job is skipped and its output is empty.
        needs = needs_for()
        needs["changes"] = {"result": "failure", "outputs": {}}
        self.assertNotEqual(self.problems(needs), "")

    def test_nightly_job_skipped_on_schedule_fails(self) -> None:
        needs = needs_for("schedule")
        needs["engine-nightly-perft"]["result"] = "skipped"
        self.assertIn("engine-nightly-perft: skipped", self.problems(needs, "schedule"))

    def test_gated_job_skipped_on_schedule_fails(self) -> None:
        needs = needs_for("schedule")
        needs["engine"]["result"] = "skipped"
        self.assertIn("every gated job must run on a schedule event", self.problems(needs, "schedule"))

    def test_job_missing_from_needs_fails(self) -> None:
        needs = needs_for()
        del needs["secret-scan"]
        self.assertIn("secret-scan: not in ci-ok's needs", self.problems(needs))

    def test_unknown_job_in_needs_fails(self) -> None:
        needs = needs_for()
        needs["ghost"] = {"result": "success", "outputs": {}}
        self.assertIn("ghost: in needs but not a job", self.problems(needs))

    def test_missing_result_fails(self) -> None:
        needs = needs_for()
        needs["services"] = {}
        self.assertIn("services: None", self.problems(needs))


if __name__ == "__main__":
    unittest.main()
