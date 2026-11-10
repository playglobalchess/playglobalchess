#!/usr/bin/env python3
"""Unit tests for check_ci_filters.py, the fail-safe CI filter policy (#86, #87).

Run (needs PyYAML, see platform/ci/requirements-ci.txt):
  python3 -m unittest discover -s platform/ci -p 'test_*.py'

AcceptanceMatrixTest is the behavioural regression test: it simulates dorny's
'some-with-excludes' semantics over the REAL ci.yml for the #86 and #87
scenarios, for pull_request and schedule events. Every other class pins one
policy rule or one bypass a review found.
"""
from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402

REAL = (c.REPO / c.CI_YML_REL).read_text(encoding="utf-8")
GATED = set(c.LEAF_GATES) | set(c.CONSUMER_GATES)
NIGHTLY_ONLY = {"engine-nightly-perft", "engine-perf-bench"}
AUDITS = {"ci-filter-audit", "rest-contract"}

ENGINE_GATE = "if: ${{ needs.changes.outputs.engine == 'true' || github.event_name == 'schedule' }}"
E2E_GATE = "if: ${{ needs.changes.outputs.e2e == 'true' || github.event_name == 'schedule' }}"
AUDIT_RUN = "run: python3 platform/ci/check_ci_filters.py"
AUDIT_STEP = "      - name: CI path-filter policy (fail-safe filters, unconditional audits)\n"


def errors_for(text: str, repo: Path = c.REPO) -> str:
    return "\n".join(c.check(repo, text))


def mutate(old: str, new: str, text: str = REAL) -> str:
    assert text.count(old) >= 1, f"fixture drift: {old!r} not in ci.yml"
    return text.replace(old, new, 1)


class AcceptanceMatrixTest(unittest.TestCase):
    """Which jobs run for a change to only these files (real ci.yml)."""

    def runs(self, *files: str, event: str = "pull_request") -> set[str]:
        return c.jobs_that_run(REAL, list(files), event)

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

    def test_root_rust_config_runs_the_rust_leaves(self) -> None:
        for f in ("Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml", ".cargo/config.toml"):
            self.assertTrue({"engine", "protocol", "services", "web", "web-e2e"} <= self.runs(f), f)

    def test_unknown_new_folder_runs_every_consumer(self) -> None:
        self.assertTrue({"services", "web", "web-e2e"} <= self.runs("newlib/src/lib.rs"))

    def test_docs_only_change_runs_only_audits(self) -> None:
        self.assertEqual(self.runs("docs/ops/PROJECT-STATUS.md"), AUDITS)
        self.assertEqual(self.runs("README.md"), AUDITS)

    def test_markdown_inside_a_source_tree_is_not_excluded(self) -> None:
        self.assertIn("services", self.runs("engine/README.md"))

    def test_other_applications_are_excluded_from_each_consumer(self) -> None:
        self.assertNotIn("web", self.runs("services/game-server/internal/session/session.go"))
        self.assertNotIn("services", self.runs("clients/web/src/App.tsx"))
        self.assertTrue({"services", "web", "web-e2e"}.isdisjoint(self.runs("clients/android/x/build.gradle.kts")))

    def test_workflow_change_runs_everything_gated(self) -> None:
        self.assertTrue(GATED <= self.runs(c.CI_YML_REL))

    def test_nightly_jobs_never_run_on_a_pr(self) -> None:
        self.assertTrue(NIGHTLY_ONLY.isdisjoint(self.runs(c.CI_YML_REL)))

    def test_schedule_runs_every_job_whatever_the_filter_says(self) -> None:
        # On a schedule event dorny diffs the last commit, which may be docs only
        # or nothing at all. The schedule bypass in every gate must still run all.
        everything = GATED | NIGHTLY_ONLY | AUDITS
        self.assertEqual(self.runs(event="schedule"), everything)
        self.assertEqual(self.runs("docs/ops/PROJECT-STATUS.md", event="schedule"), everything)


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

    def test_yaml_errors_fail_closed(self) -> None:
        self.assertIn("does not parse", errors_for(REAL + "\n  : : [\n"))
        self.assertIn("duplicate key", errors_for(mutate("  engine:\n    needs: changes",
                                                         "  rest-contract:\n    runs-on: x\n  engine:\n    needs: changes")))

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

    def test_brace_and_change_type_filters_fail(self) -> None:
        self.assertIn("brace pattern", errors_for(mutate("engine: ['engine/**',", "engine: ['{engine,x}/**',")))
        self.assertIn("must be a list of glob strings", errors_for(
            mutate("            platform: ['platform/**', '.github/workflows/**']",
                   "            platform:\n              - added: 'platform/**'")))

    # P2
    def test_needs_v4_and_some_with_excludes(self) -> None:
        self.assertIn("dorny/paths-filter@v4", errors_for(mutate("dorny/paths-filter@v4", "dorny/paths-filter@v3")))
        self.assertIn("predicate-quantifier", errors_for(mutate("predicate-quantifier: 'some-with-excludes'",
                                                                "predicate-quantifier: 'some'")))

    def test_quantifier_only_in_a_comment_fails(self) -> None:
        e = errors_for(mutate("          predicate-quantifier: 'some-with-excludes'\n",
                              "          # predicate-quantifier: 'some-with-excludes'\n"))
        self.assertIn("predicate-quantifier", e)

    def test_changes_job_and_filter_step_cannot_be_skipped(self) -> None:
        self.assertIn("changes job must not have 'if'", errors_for(
            mutate("  changes:\n    runs-on", "  changes:\n    if: false\n    runs-on")))
        self.assertIn("must not have if/continue-on-error", errors_for(
            mutate("        id: filter\n", "        id: filter\n        continue-on-error: true\n")))
        self.assertIn("id 'filter'", errors_for(mutate("        id: filter\n", "        id: filt\n")))

    def test_outputs_must_be_wired_to_the_filter(self) -> None:
        for name, (old, new) in {
            "constant": ("engine: ${{ steps.filter.outputs.engine }}", "engine: 'true'"),
            "crossed": ("services: ${{ steps.filter.outputs.services }}", "services: ${{ steps.filter.outputs.docs }}"),
            "missing": ("      e2e: ${{ steps.filter.outputs.e2e }}\n", ""),
        }.items():
            with self.subTest(name):
                self.assertIn("changes.outputs must be exactly", errors_for(mutate(old, new)))

    # P3, P8
    def test_consumer_gated_on_wrong_output_fails(self) -> None:
        e = errors_for(mutate(E2E_GATE, E2E_GATE.replace("outputs.e2e", "outputs.web")))
        self.assertIn("job 'web-e2e' must be gated on ['e2e']", e)

    def test_gate_shape_is_exact(self) -> None:
        for name, new in {
            "and": "if: ${{ needs.changes.outputs.engine == 'true' && github.event_name != 'push' }}",
            "no schedule bypass": "if: ${{ needs.changes.outputs.engine == 'true' }}",
            "schedule && false": "if: ${{ needs.changes.outputs.engine == 'true' || github.event_name == 'schedule' && false }}",
            "bare (no ${{ }})": "if: needs.changes.outputs.engine == 'true' || github.event_name == 'schedule'",
        }.items():
            with self.subTest(name):
                self.assertIn("job 'engine': gate", errors_for(mutate(ENGINE_GATE, new)))

    def test_gated_job_needs_only_changes(self) -> None:
        e = errors_for(mutate("  engine:\n    needs: changes", "  engine:\n    needs: [changes, rest-contract]"))
        self.assertIn("job 'engine' must have needs: changes", e)

    def test_gated_job_continue_on_error_fails(self) -> None:
        e = errors_for(mutate(ENGINE_GATE, ENGINE_GATE + "\n    continue-on-error: true"))
        self.assertIn("job 'engine' must not have continue-on-error", e)

    def test_new_gated_job_must_join_the_policy(self) -> None:
        e = errors_for(mutate("  engine:\n    needs: changes",
                              "  newjob:\n    needs: changes\n    " + ENGINE_GATE + "\n"
                              "    runs-on: ubuntu-latest\n    steps:\n      - run: make -C services/game-server test\n\n"
                              "  engine:\n    needs: changes"))
        self.assertIn("new gated job 'newjob' is not in the policy", e)

    def test_removed_job_fails(self) -> None:
        self.assertIn("job 'web-e2e' not found", errors_for(mutate("  web-e2e:\n", "  web-e2e-renamed:\n")))

    # P4 root Rust config
    def test_leaf_filters_need_root_rust_config(self) -> None:
        for p in c.ROOT_RUST_CONFIG:
            with self.subTest(p):
                e = errors_for(mutate("engine: ['engine/**', " + ", ".join(f"'{x}'" for x in c.ROOT_RUST_CONFIG),
                                      "engine: ['engine/**', " + ", ".join(f"'{x}'" for x in c.ROOT_RUST_CONFIG if x != p)))
                self.assertIn(f"leaf filter 'engine' must contain '{p}'", e)

    # P5
    def test_owner_filter_without_ci_yml_or_with_negation_fails(self) -> None:
        self.assertIn("filter 'engine' must include", errors_for(mutate("'.cargo/**', '.github/workflows/ci.yml']\n            protocol",
                                                                        "'.cargo/**']\n            protocol")))
        self.assertIn("owner filter 'engine' must not use '!'",
                      errors_for(mutate("engine: ['engine/**',", "engine: ['engine/**', '!engine/ffi/**',")))

    def test_leaf_folder_pattern_required(self) -> None:
        self.assertIn("leaf filter 'protocol' must contain 'protocol/**'",
                      errors_for(mutate("protocol: ['protocol/**',", "protocol: ['protocol/codec/**',")))

    # P6: every bypass the third and fourth reviews found
    def test_unconditional_audit_bypasses(self) -> None:
        job = "  ci-filter-audit:\n    runs-on"
        cases = {
            "job-level if": (job, "  ci-filter-audit:\n    if: ${{ github.event_name == 'push' }}\n    runs-on", "only runs-on, steps and name"),
            "quoted if key": (job, '  ci-filter-audit:\n    "if": false\n    runs-on', "only runs-on, steps and name"),
            "spaced if key": (job, "  ci-filter-audit:\n    if : false\n    runs-on", "only runs-on, steps and name"),
            "needs": (job, "  ci-filter-audit:\n    needs: engine\n    runs-on", "only runs-on, steps and name"),
            "continue-on-error": (job, "  ci-filter-audit:\n    continue-on-error: true\n    runs-on", "only runs-on, steps and name"),
            "timeout-minutes": (job, "  ci-filter-audit:\n    timeout-minutes: 0\n    runs-on", "only runs-on, steps and name"),
            "concurrency": (job, "  ci-filter-audit:\n    concurrency: x\n    runs-on", "only runs-on, steps and name"),
            "runner": ("  ci-filter-audit:\n    runs-on: ubuntu-latest", "  ci-filter-audit:\n    runs-on: self-hosted", "runs-on must be"),
            "step if": (AUDIT_STEP, AUDIT_STEP + "        if: false\n", "disallowed keys ['if']"),
            "step shell": (AUDIT_STEP, AUDIT_STEP + "        shell: bash {0} || true\n", "disallowed keys ['shell']"),
            "step timeout": (AUDIT_STEP, AUDIT_STEP + "        timeout-minutes: 0\n", "disallowed keys ['timeout-minutes']"),
            "step continue-on-error": (AUDIT_STEP, AUDIT_STEP + "        continue-on-error: true\n", "disallowed keys"),
            "checkout ref": ("    steps:\n      - uses: actions/checkout@v4\n      - uses: actions/setup-python@v5\n        with:\n          python-version: \"3.12\"\n      - name: Install pinned PyYAML",
                             "    steps:\n      - uses: actions/checkout@v4\n        with:\n          ref: main\n      - uses: actions/setup-python@v5\n        with:\n          python-version: \"3.12\"\n      - name: Install pinned PyYAML",
                             "step 0 must be"),
            "extra uses step": (AUDIT_STEP, "      - uses: ./.github/actions/evil\n" + AUDIT_STEP, "expected 4 steps"),
            "install skipped": ("        run: python3 -m pip install --require-hashes -r platform/ci/requirements-ci.txt\n      - name: CI path",
                                "        run: python3 -m pip install -r platform/ci/requirements-ci.txt\n      - name: CI path", "must run exactly"),
            "echo": (AUDIT_RUN, "run: echo python3 platform/ci/check_ci_filters.py", "must run exactly"),
            "|| true": (AUDIT_RUN, AUDIT_RUN + " || true", "must run exactly"),
            "two lines": (AUDIT_RUN, "run: |\n          python3 platform/ci/check_ci_filters.py\n          true", "must run exactly"),
        }
        for name, (old, new, expect) in cases.items():
            with self.subTest(name):
                e = errors_for(mutate(old, new))
                self.assertIn("job 'ci-filter-audit' must run on every change", e)
                self.assertIn(expect, e)

    def test_rest_contract_must_stay_unconditional(self) -> None:
        e = errors_for(mutate("  rest-contract:\n    runs-on", "  rest-contract:\n    needs: changes\n    runs-on"))
        self.assertIn("job 'rest-contract' must run on every change", e)
        e2 = errors_for(mutate("          python3 platform/ci/check_rest_contract.py\n",
                               "          python3 platform/ci/check_rest_contract.py || true\n"))
        self.assertIn("job 'rest-contract' must run on every change: step 2 must run exactly", e2)

    # P7
    def test_trigger_restrictions_fail(self) -> None:
        cases = {
            "pr paths": ("  pull_request:\n", "  pull_request:\n    paths: ['services/**']\n", "on.pull_request must have no restrictions"),
            "pr branches": ("  pull_request:\n", "  pull_request:\n    branches: [nope]\n", "on.pull_request must have no restrictions"),
            "pr types": ("  pull_request:\n", "  pull_request:\n    types: [closed]\n", "on.pull_request must have no restrictions"),
            "pr removed": ("  pull_request:\n", "", "the pull_request trigger is required"),
            "push paths": ("    branches: [main]\n  pull_request", "    branches: [main]\n    paths: ['x/**']\n  pull_request", "on.push must be exactly"),
            "push branch": ("    branches: [main]\n  pull_request", "    branches: [dev]\n  pull_request", "on.push must be exactly"),
        }
        for name, (old, new, expect) in cases.items():
            with self.subTest(name):
                self.assertIn(expect, errors_for(mutate(old, new)))


def leaf_repo(root: Path) -> None:
    def w(rel: str, text: str) -> None:
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")
    w("engine/Cargo.toml", "[package]\nname='e'\n")
    w("engine/src/lib.rs", 'const S: &str = include_str!("lib.rs");\n')
    w("engine/engine-cgo/Cargo.toml", '[dependencies]\npgc-engine = { path = ".." }\n')
    w("protocol/codec/Cargo.toml", "[package]\nname='p'\n")
    w("protocol/codec/tests/golden.rs",
      'let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../vectors/vectors.json");\n')
    w("protocol/codec-cgo/Cargo.toml", '[dependencies.pgc-protocol]\npath = "../codec"\n')
    w("services/gs/Cargo.toml", "[package]\nname='s'\n")
    w("settings.gradle.kts", 'rootProject.name = "pgc"\ninclude(":p")\n'
      'project(":p").projectDir = file("clients/android/p")\n')
    w("build.gradle.kts", "")
    w("clients/android/p/build.gradle.kts", "")


class LeafTest(unittest.TestCase):
    MOBILE = ["settings.gradle.kts", "build.gradle.kts", "clients/android/**"]

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

    def rust_errors(self) -> str:
        errs: list[str] = []
        for r in c.LEAF_CRATE_ROOTS:
            c.check_rust_sources_leaf(self.root, r, errs)
        return "\n".join(errs)

    def gradle_errors(self, patterns: list[str] | None = None) -> str:
        errs: list[str] = []
        c.check_gradle_leaf(self.root, patterns if patterns is not None else self.MOBILE, errs)
        return "\n".join(errs)

    def write(self, rel: str, text: str) -> None:
        (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
        (self.root / rel).write_text(text, encoding="utf-8")

    def test_inside_deps_pass(self) -> None:
        self.assertEqual(self.cargo_errors(), "")
        self.assertEqual(self.rust_errors(), "")
        self.assertEqual(self.gradle_errors(), "")

    def test_every_cargo_form_leaving_the_leaf_fails(self) -> None:
        forms = {
            "inline": '[dependencies]\ns = { path = "../../services/gs" }\n',
            "table": '[dependencies.s]\npath = "../../services/gs"\n',
            "dotted": '[dependencies]\ns.path = "../../services/gs"\n',
            "dev": '[dev-dependencies]\ns = { path = "../../services/gs" }\n',
            "build": '[build-dependencies]\ns = { path = "../../services/gs" }\n',
            "target": "[target.'cfg(unix)'.dependencies]\ns = { path = \"../../services/gs\" }\n",
            "patch": '[patch.crates-io]\ns = { path = "../../services/gs" }\n',
            "replace": '[replace]\n"s:0.1.0" = { path = "../../services/gs" }\n',
            "workspace member": '[workspace]\nmembers = ["../../services/gs"]\n',
            "workspace dep": '[workspace.dependencies]\ns = { path = "../../services/gs" }\n',
        }
        for name, toml in forms.items():
            with self.subTest(name):
                self.write("protocol/codec-cgo/Cargo.toml", toml)
                self.assertIn("leaves protocol/", self.cargo_errors())

    def test_cargo_config_leaving_the_leaf_fails(self) -> None:
        for name, toml in {
            "patch": '[patch.crates-io]\ns = { path = "../services/gs" }\n',
            "paths": 'paths = ["../services/gs"]\n',
        }.items():
            for fname in ("config.toml", "config"):
                with self.subTest(f"{name} in .cargo/{fname}"):
                    leaf_repo(self.root)
                    self.write(f"engine/.cargo/{fname}", toml)
                    self.assertIn("leaves engine/", self.cargo_errors())
                    (self.root / f"engine/.cargo/{fname}").unlink()

    def test_unparseable_cargo_fails(self) -> None:
        self.write("engine/Cargo.toml", "[dependencies\n")
        self.assertIn("cannot parse", self.cargo_errors())

    def test_missing_crate_fails_closed(self) -> None:
        for p in sorted(self.root.joinpath("engine").rglob("Cargo.toml"), reverse=True):
            p.unlink()
        self.assertIn("engine/: no Cargo.toml found", self.cargo_errors())

    def test_rust_strings_leaving_the_leaf_fail(self) -> None:
        cases = {
            "include_str": ("protocol/codec/src/lib.rs", 'const D: &str = include_str!("../../../docs/x.md");\n'),
            "include_bytes": ("engine/src/lib.rs", 'const B: &[u8] = include_bytes!("../../services/gs/x.bin");\n'),
            "manifest dir": ("engine/src/lib.rs", 'let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../docs/x");\n'),
            "build.rs": ("protocol/codec/build.rs", 'println!("cargo:rerun-if-changed=../../services/gs/x.h");\n'),
            "raw string": ("engine/src/lib.rs", 'const D: &str = include_str!(r#"../../docs/x"#);\n'),
            # read as one path it lands inside protocol/ under both readings; its
            # second token, read from the crate root, leaves the leaf
            "two paths": ("protocol/a/b/src/lib.rs", 'let s = "../x.rs ../../../docs/y";\n'),
            # a prefix glued on with ':' hides the climb unless the tail is read
            "colon prefix": ("protocol/a/b/src/lib.rs", 'let s = "x:../../../docs/y";\n'),
            # inside when read against the file, outside against the crate: fail safe
            "ambiguous": ("protocol/codec/src/lib.rs", 'let p = concat!(env!("CARGO_MANIFEST_DIR"), "/../../docs");\n'),
        }
        self.write("protocol/a/b/Cargo.toml", "[package]\nname='ab'\n")
        for name, (rel, src) in cases.items():
            with self.subTest(name):
                leaf_repo(self.root)
                self.write(rel, src)
                self.assertIn("points outside", self.rust_errors())
                (self.root / rel).unlink()

    def test_full_check_runs_every_leaf_check(self) -> None:
        # check() must wire in each leaf scan, not just define it.
        self.assertEqual(c.check(self.root, REAL), [])
        for name, (rel, text, expect) in {
            "cargo": ("engine/engine-cgo/Cargo.toml", '[dependencies]\ns = { path = "../../services/gs" }\n', "leaves engine/"),
            "rust source": ("engine/src/lib.rs", 'const D: &str = include_str!("../../docs/x.md");\n', "points outside"),
            "gradle": ("clients/android/p/build.gradle.kts", 'java.srcDir("$rootDir/engine")\n', "uses 'rootDir'"),
        }.items():
            with self.subTest(name):
                leaf_repo(self.root)
                self.write(rel, text)
                self.assertIn(expect, "\n".join(c.check(self.root, REAL)))
        leaf_repo(self.root)

    def test_not_a_path_marker_skips_a_line(self) -> None:
        self.write("engine/src/lib.rs", 'let s = "../../not/a/path"; // ci-audit: not-a-path\n')
        self.assertEqual(self.rust_errors(), "")

    def test_gradle_module_rules(self) -> None:
        self.write("settings.gradle.kts", 'include(":p")\ninclude(":apps")\nproject(":p").projectDir = file("clients/android/p")\n')
        self.assertIn("module :apps has no projectDir", self.gradle_errors())
        self.write("settings.gradle.kts", 'include(":p")\nproject(":p").projectDir = file("engine/ffi")\n')
        self.assertIn("outside clients/", self.gradle_errors())

    def test_gradle_reads_outside_the_leaf_fail(self) -> None:
        cases = {
            "../": ("clients/android/p/build.gradle.kts", 'sourceSets { main { java.srcDir("../../../engine/ffi") } }\n', "'../'"),
            "rootDir": ("clients/android/p/build.gradle.kts", 'java.srcDir("$rootDir/engine/ffi")\n', "'rootDir'"),
            "rootProject dir": ("clients/android/p/build.gradle.kts", 'java.srcDir(rootProject.file("engine"))\n', "'rootProject'"),
            "rootProject in settings": ("settings.gradle.kts", 'rootProject.name = "pgc"\nrootProject.projectDir = file("engine")\n', "'rootProject'"),
            "includeBuild": ("settings.gradle.kts", 'includeBuild("engine/gradle")\n', "'includeBuild'"),
        }
        for name, (rel, text, token) in cases.items():
            with self.subTest(name):
                leaf_repo(self.root)
                self.write(rel, text)
                self.assertIn(f"uses {token}", self.gradle_errors())

    def test_mobile_filter_must_list_root_gradle_files(self) -> None:
        for f in ("gradle.properties", "gradlew", "gradlew.bat"):
            with self.subTest(f):
                self.write(f, "")
                self.assertIn(f"root Gradle file '{f}'", self.gradle_errors())
                (self.root / f).unlink()
        (self.root / "gradle").mkdir()
        self.assertIn("'gradle/**'", self.gradle_errors())


if __name__ == "__main__":
    unittest.main()
