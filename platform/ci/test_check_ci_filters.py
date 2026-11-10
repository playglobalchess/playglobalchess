#!/usr/bin/env python3
"""Unit tests for check_ci_filters.py, the fail-safe CI filter policy (#86, #87).

Run: python3 -m unittest discover -s platform/ci -p 'test_*.py'

AcceptanceMatrixTest is the behavioural regression test: it simulates dorny's
'some-with-excludes' semantics over the REAL ci.yml for the #86 and #87
scenarios. Every other class pins one policy rule or one bypass a review found.
"""
from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402

REAL = (c.REPO / c.CI_YML_REL).read_text(encoding="utf-8")


def errors_for(text: str, repo: Path = c.REPO) -> str:
    return "\n".join(c.check(repo, text))


def mutate(old: str, new: str, text: str = REAL) -> str:
    assert text.count(old) >= 1, f"fixture drift: {old!r} not in ci.yml"
    return text.replace(old, new, 1)


class AcceptanceMatrixTest(unittest.TestCase):
    """Which jobs run for a PR that changes only these files (real ci.yml)."""

    def runs(self, *files: str) -> set[str]:
        return c.jobs_that_run(REAL, list(files))

    def test_86_docs_only_contract_change_runs_rest_audit(self) -> None:
        self.assertIn("rest-contract", self.runs("docs/specs/PE-platform-eng/API-CONTRACT.md"))
        self.assertIn("rest-contract", self.runs("docs/specs/S2-realtime/PROTOCOL.md"))

    def test_86_script_only_change_runs_rest_audit(self) -> None:
        self.assertIn("rest-contract", self.runs("platform/ci/check_rest_contract.py"))

    def test_87_protocol_only_change_runs_services_web_and_e2e(self) -> None:
        for f in ("protocol/codec-cgo/src/lib.rs", "protocol/codec-cgo/include/pgc_protocol.h",
                  "protocol/vectors/vectors.json", "protocol/codec/src/types.rs"):
            self.assertTrue({"services", "web", "web-e2e", "protocol"} <= self.runs(f), f)

    def test_87_engine_only_change_runs_services_and_e2e(self) -> None:
        for f in ("engine/src/movegen.rs", "engine/engine-cgo/src/lib.rs"):
            self.assertTrue({"services", "web-e2e", "engine"} <= self.runs(f), f)

    def test_unknown_new_folder_runs_every_consumer(self) -> None:
        self.assertTrue({"services", "web", "web-e2e"} <= self.runs("newlib/src/lib.rs"))

    def test_docs_only_change_runs_only_audits(self) -> None:
        self.assertEqual(self.runs("docs/ops/PROJECT-STATUS.md"), {"ci-filter-audit", "rest-contract"})
        self.assertEqual(self.runs("README.md"), {"ci-filter-audit", "rest-contract"})

    def test_markdown_inside_a_source_tree_is_not_excluded(self) -> None:
        self.assertIn("services", self.runs("engine/README.md"))

    def test_other_applications_are_excluded_from_each_consumer(self) -> None:
        self.assertNotIn("web", self.runs("services/game-server/internal/session/session.go"))
        self.assertNotIn("services", self.runs("clients/web/src/App.tsx"))
        self.assertTrue({"services", "web", "web-e2e"}.isdisjoint(self.runs("clients/android/x/build.gradle.kts")))

    def test_workflow_change_runs_everything_gated(self) -> None:
        self.assertTrue(set(c.LEAF_GATES) | set(c.CONSUMER_GATES) <= self.runs(c.CI_YML_REL))


class SimulatorTest(unittest.TestCase):
    """The simulator must match dorny 'some-with-excludes' (src/filter.ts)."""

    def test_exclusion_is_final_and_negated_only_never_matches(self) -> None:
        self.assertFalse(c.filter_matches(["**", "!docs/**"], ["docs/a.md"]))
        self.assertTrue(c.filter_matches(["**", "!docs/**"], ["docs/a.md", "x/y"]))
        self.assertFalse(c.filter_matches(["!docs/**"], ["x/y"]))
        self.assertFalse(c.filter_matches(["docs/**", "!docs/**"], ["docs/a.md"]))

    def test_glob_semantics(self) -> None:
        self.assertTrue(c.filter_matches(["**"], [".github/workflows/ci.yml"]))  # dot: true
        self.assertTrue(c.filter_matches(["platform/mobile/*[Cc]odec*"], ["platform/mobile/run_codec_parity.sh"]))
        self.assertFalse(c.filter_matches(["*.md"], ["engine/README.md"]))
        self.assertTrue(c.filter_matches(["x/[!a]"], ["x/b"]))
        self.assertFalse(c.filter_matches(["x/[!a]"], ["x/a"]))


class PolicyTest(unittest.TestCase):
    def test_real_workflow_follows_the_policy(self) -> None:
        self.assertEqual(c.check(c.REPO, REAL), [])

    # P1
    def test_consumer_filter_must_be_exclusion_style(self) -> None:
        e = errors_for(mutate("services: ['**', '!docs/**'", "services: ['services/**', '!docs/**'"))
        self.assertIn("filter 'services' must start with '**'", e)

    def test_exclusion_must_be_allowlisted(self) -> None:
        e = errors_for(mutate("services: ['**', '!docs/**'", "services: ['**', '!protocol/**', '!docs/**'"))
        self.assertIn("excludes 'protocol/**', which is not in ALLOWED_EXCLUSIONS", e)

    def test_no_positive_pattern_after_star_star(self) -> None:
        e = errors_for(mutate("web: ['**', '!docs/**'", "web: ['**', 'clients/web/**', '!docs/**'"))
        self.assertIn("may only add '!' exclusions", e)

    # P2
    def test_needs_v4_and_some_with_excludes(self) -> None:
        self.assertIn("dorny/paths-filter@v4", errors_for(mutate("dorny/paths-filter@v4", "dorny/paths-filter@v3")))
        self.assertIn("predicate-quantifier", errors_for(mutate("predicate-quantifier: 'some-with-excludes'",
                                                                "predicate-quantifier: 'some'")))

    # P3, P8
    def test_consumer_gated_on_wrong_output_fails(self) -> None:
        e = errors_for(mutate("if: ${{ needs.changes.outputs.e2e == 'true' }}",
                              "if: ${{ needs.changes.outputs.web == 'true' }}"))
        self.assertIn("job 'web-e2e' must be gated on ['e2e']", e)

    def test_and_gate_fails(self) -> None:
        e = errors_for(mutate("if: ${{ needs.changes.outputs.engine == 'true' }}",
                              "if: ${{ needs.changes.outputs.engine == 'true' && github.event_name != 'push' }}"))
        self.assertIn("job 'engine': gate", e)

    def test_new_gated_job_must_join_the_policy(self) -> None:
        e = errors_for(mutate("  engine:\n    needs: changes",
                              "  newjob:\n    needs: changes\n    if: ${{ needs.changes.outputs.engine == 'true' }}\n"
                              "    runs-on: ubuntu-latest\n    steps:\n      - run: make -C services/game-server test\n\n"
                              "  engine:\n    needs: changes"))
        self.assertIn("new gated job 'newjob' is not in the policy", e)

    def test_removed_job_fails(self) -> None:
        self.assertIn("job 'web-e2e' not found", errors_for(mutate("  web-e2e:\n", "  web-e2e-renamed:\n")))

    # P5
    def test_owner_filter_without_ci_yml_or_with_negation_fails(self) -> None:
        self.assertIn("filter 'engine' must include", errors_for(mutate("engine: ['engine/**', '.github/workflows/ci.yml']", "engine: ['engine/**']")))
        self.assertIn("owner filter 'engine' must not use '!'",
                      errors_for(mutate("engine: ['engine/**', '.github/workflows/ci.yml']", "engine: ['engine/**', '!engine/ffi/**', '.github/workflows/ci.yml']")))

    def test_leaf_folder_pattern_required(self) -> None:
        self.assertIn("leaf filter 'protocol' must contain 'protocol/**'",
                      errors_for(mutate("protocol: ['protocol/**',", "protocol: ['protocol/codec/**',")))

    # P6: every bypass the third review found
    def test_unconditional_audit_bypasses(self) -> None:
        cases = {
            "job-level if": ("  ci-filter-audit:\n    runs-on", "  ci-filter-audit:\n    if: ${{ github.event_name == 'push' }}\n    runs-on", "job-level `if:`"),
            "needs": ("  ci-filter-audit:\n    runs-on", "  ci-filter-audit:\n    needs: engine\n    runs-on", "remove `needs:`"),
            "continue-on-error": ("  ci-filter-audit:\n    runs-on", "  ci-filter-audit:\n    continue-on-error: true\n    runs-on", "continue-on-error"),
            "step if": ("      - name: CI path-filter policy (fail-safe filters, unconditional audits)\n",
                        "      - name: CI path-filter policy (fail-safe filters, unconditional audits)\n        if: false\n", "step-level `if:`"),
            "echo": ("run: python3 platform/ci/check_ci_filters.py", "run: echo python3 platform/ci/check_ci_filters.py", "run lines must be exactly"),
            "|| true": ("run: python3 platform/ci/check_ci_filters.py", "run: python3 platform/ci/check_ci_filters.py || true", "run lines must be exactly"),
        }
        for name, (old, new, expect) in cases.items():
            with self.subTest(name):
                self.assertIn(expect, errors_for(mutate(old, new)))

    def test_rest_contract_must_stay_unconditional(self) -> None:
        e = errors_for(mutate("  rest-contract:\n    runs-on", "  rest-contract:\n    needs: changes\n    runs-on"))
        self.assertIn("job 'rest-contract' must run on every change", e)
        e2 = errors_for(mutate("          python3 platform/ci/check_rest_contract.py\n",
                               "          python3 platform/ci/check_rest_contract.py || true\n"))
        self.assertIn("job 'rest-contract' must run on every change: its run lines", e2)

    # P7
    def test_workflow_level_paths_fail(self) -> None:
        e = errors_for(mutate("  pull_request:\n", "  pull_request:\n    paths: ['services/**']\n"))
        self.assertIn("workflow-level", e)


def leaf_repo(root: Path) -> None:
    def w(rel: str, text: str) -> None:
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")
    w("engine/Cargo.toml", "[package]\nname='e'\n")
    w("engine/engine-cgo/Cargo.toml", '[dependencies]\npgc-engine = { path = ".." }\n')
    w("protocol/codec/Cargo.toml", "[package]\nname='p'\n")
    w("protocol/codec-cgo/Cargo.toml", '[dependencies.pgc-protocol]\npath = "../codec"\n')
    w("services/gs/Cargo.toml", "[package]\nname='s'\n")
    w("settings.gradle.kts", 'include(":p")\nproject(":p").projectDir = file("clients/android/p")\n')
    w("build.gradle.kts", "")
    w("clients/android/p/build.gradle.kts", "")


class LeafTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        leaf_repo(self.root)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def cargo_errors(self) -> str:
        errs: list[str] = []
        for r in c.LEAF_CRATE_ROOTS:
            c.check_cargo_leaf(self.root, r, errs)
        return "\n".join(errs)

    def gradle_errors(self, patterns: list[str] | None = None) -> str:
        errs: list[str] = []
        c.check_gradle_leaf(self.root, patterns if patterns is not None else
                            ["settings.gradle.kts", "build.gradle.kts", "clients/android/**"], errs)
        return "\n".join(errs)

    def write(self, rel: str, text: str) -> None:
        (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
        (self.root / rel).write_text(text, encoding="utf-8")

    def test_inside_deps_pass(self) -> None:
        self.assertEqual(self.cargo_errors(), "")
        self.assertEqual(self.gradle_errors(), "")

    def test_every_cargo_form_leaving_the_leaf_fails(self) -> None:
        forms = {
            "inline": '[dependencies]\ns = { path = "../../services/gs" }\n',
            "table": '[dependencies.s]\npath = "../../services/gs"\n',
            "dotted": '[dependencies]\ns.path = "../../services/gs"\n',
            "dev": '[dev-dependencies]\ns = { path = "../../services/gs" }\n',
            "target": "[target.'cfg(unix)'.dependencies]\ns = { path = \"../../services/gs\" }\n",
            "patch": '[patch.crates-io]\ns = { path = "../../services/gs" }\n',
            "workspace member": '[workspace]\nmembers = ["../../services/gs"]\n',
        }
        for name, toml in forms.items():
            with self.subTest(name):
                self.write("protocol/codec-cgo/Cargo.toml", toml)
                self.assertIn("leaves protocol/", self.cargo_errors())

    def test_unparseable_cargo_fails(self) -> None:
        self.write("engine/Cargo.toml", "[dependencies\n")
        self.assertIn("cannot parse", self.cargo_errors())

    def test_gradle_module_rules(self) -> None:
        self.write("settings.gradle.kts", 'include(":p")\ninclude(":apps")\nproject(":p").projectDir = file("clients/android/p")\n')
        self.assertIn("module :apps has no projectDir", self.gradle_errors())
        self.write("settings.gradle.kts", 'include(":p")\nproject(":p").projectDir = file("engine/ffi")\n')
        self.assertIn("outside clients/", self.gradle_errors())
        leaf_repo(self.root)
        self.write("clients/android/p/build.gradle.kts", 'sourceSets { main { java.srcDir("../../../engine/ffi") } }\n')
        self.assertIn("references '../'", self.gradle_errors())

    def test_mobile_filter_must_list_root_gradle_files(self) -> None:
        self.write("gradle.properties", "")
        self.assertIn("root Gradle file 'gradle.properties'", self.gradle_errors())


if __name__ == "__main__":
    unittest.main()
