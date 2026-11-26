#!/usr/bin/env python3
"""docs-check must report on every PR (#117).

A required status from a path-filtered workflow stays pending on PRs that do not
touch those paths, which blocks every code-only PR once branch protection is on.
Run (needs PyYAML): python3 -m unittest discover -s platform/ci -p 'test_*.py'
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402

WF = c.load_yaml((c.REPO / ".github/workflows/docs-check.yml").read_text(encoding="utf-8"))


class DocsCheckWorkflowTest(unittest.TestCase):
    def test_pull_request_trigger_has_no_restrictions(self) -> None:
        on = c.workflow_on(WF)
        self.assertIn("pull_request", on)
        self.assertIn(on["pull_request"], (None, {}), "pull_request must have no paths/branches/types")

    def test_push_trigger_has_no_paths(self) -> None:
        on = c.workflow_on(WF)
        self.assertEqual(on.get("push"), {"branches": ["main"]})

    def test_the_required_job_is_unconditional(self) -> None:
        job = WF["jobs"]["validate-docs"]
        for key in ("if", "needs", "continue-on-error"):
            self.assertNotIn(key, job)
        for step in job["steps"]:
            self.assertNotIn("if", step)
            self.assertNotIn("continue-on-error", step)


if __name__ == "__main__":
    unittest.main()
