#!/usr/bin/env python3
"""Unit tests for check_ci_filters.py (#86, #87). Stdlib unittest.

Run: python3 -m unittest discover -s platform/ci -p 'test_*.py'

Several cases pin fail-open holes found in review, so they must stay:
digit-named constants, multi-line Cargo tables, renamed Makefile variables,
filepath.Join paths, a filtered audit job, && gates, brace/negation patterns.
"""
from __future__ import annotations

import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402


def write(root: Path, rel: str, text: str) -> None:
    p = root / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(text, encoding="utf-8")


def make_repo(root: Path) -> None:
    """A minimal repo shaped like the real one."""
    write(root, "engine/Cargo.toml", "[package]\nname = 'e'\n")
    write(root, "engine/engine-cgo/Cargo.toml", "[dependencies]\npgc-engine = { path = \"..\" }\n")
    write(root, "protocol/codec/Cargo.toml", "[package]\nname = 'p'\n")
    write(root, "protocol/codec-cgo/Cargo.toml", "[dependencies]\npgc-protocol = { path = \"../codec\" }\n")
    write(root, "protocol/codec-cgo/include/pgc.h", "")
    write(root, "protocol/codec-wasm/Cargo.toml", "[dependencies]\npgc-protocol = { path = \"../codec\" }\n")
    write(root, "protocol/vectors/vectors.json", "{}")
    write(root, "protocol/rest/openapi.yaml", "openapi: 3.1.0\n")
    write(root, "docs/specs/S2-realtime/PROTOCOL.md", "x")
    write(root, "docs/specs/PE-platform-eng/API-CONTRACT.md", "x")
    write(root, "services/game-server/go.mod", "module gs\n")
    write(root, "services/game-server/Makefile",
          "CODEC_CGO_DIR  := ../../protocol/codec-cgo\nENGINE_CGO_DIR := ../../engine/engine-cgo\n"
          "ENGINE_OUT := $(ENGINE_CGO_DIR)/target/release\n\nlib:\n\tcargo build\n")
    write(root, "services/game-server/internal/codec/parity_test.go",
          'package codec\n// #cgo CFLAGS: -I${SRCDIR}/../../../../protocol/codec-cgo/include\n'
          'const v = "../../../../protocol/vectors/vectors.json"\nconst inside = "../../go.mod"\n')
    write(root, "clients/web/package.json",
          '{"scripts": {"e2e": "make -C ../../services/game-server lib"},'
          ' "dependencies": {"pgc-protocol-wasm": "file:../../protocol/codec-wasm/pkg"}}')
    write(root, "platform/ci/check_rest_contract.py",
          'REPO = x\nOPENAPI = REPO / "protocol" / "rest" / "openapi.yaml"\n'
          'PE_SPEC = REPO / "docs" / "specs" / "PE-platform-eng" / "API-CONTRACT.md"\n'
          'S2_SPEC = REPO / "docs" / "specs" / "S2-realtime" / "PROTOCOL.md"\n')
    write(root, "platform/ci/check_ci_filters.py", "REPO = x\n")
    write(root, "platform/mobile/run_codec_parity.sh", 'FFI="protocol/codec-ffi"\ncargo build --manifest-path "$FFI/Cargo.toml"\n')
    write(root, "protocol/codec-ffi/Cargo.toml", "[dependencies]\npgc-protocol = { path = \"../codec\" }\n")
    write(root, "settings.gradle.kts", 'include(":p")\nproject(":p").projectDir = file("clients/android/p")\n')
    write(root, "build.gradle.kts", "")
    write(root, "gradle.properties", "")
    write(root, "gradlew", "")
    write(root, "clients/android/p/build.gradle.kts", "")


GATED_JOBS = """\
  ci-filter-audit:
    runs-on: ubuntu-latest
    steps:
      - run: python3 platform/ci/check_ci_filters.py
  engine:
    needs: changes
    if: ${{{{ needs.changes.outputs.engine == 'true' }}}}
    steps:
      - run: cargo test --manifest-path engine/Cargo.toml
  protocol:
    needs: changes
    if: ${{{{ needs.changes.outputs.protocol == 'true' }}}}
    steps:
      - run: cargo test --manifest-path protocol/codec-cgo/Cargo.toml
  rest-contract:
    needs: changes
    if: ${{{{ needs.changes.outputs.rest == 'true' }}}}
    steps:
      - run: python3 platform/ci/check_rest_contract.py
  protocol-parity:
    needs: changes
    if: ${{{{ needs.changes.outputs.protocol == 'true' || needs.changes.outputs.mobile == 'true' }}}}
    steps:
      - run: platform/mobile/run_codec_parity.sh
  services:
    needs: changes
    if: ${{{{ needs.changes.outputs.services == 'true' }}}}
    steps:
      - run: make -C services/game-server test
  web:
    needs: changes
    if: ${{{{ needs.changes.outputs.web == 'true' }}}}
    steps:
      - uses: actions/setup-node@v4
      - working-directory: clients/web
        run: npm ci && npm test
  web-e2e:
    needs: changes
    if: ${{{{ needs.changes.outputs.web == 'true' || needs.changes.outputs.services == 'true' }}}}
    steps:
      - working-directory: clients/web
        run: npm run e2e
  mobile:
    needs: changes
    if: ${{{{ needs.changes.outputs.mobile == 'true' }}}}
    steps:
      - uses: gradle/actions/setup-gradle@v4
      - run: ./gradlew :p:assembleRelease
  platform:
    needs: changes
    if: ${{{{ needs.changes.outputs.platform == 'true' }}}}
    steps:
      - run: python3 -m unittest discover -s platform/ci
"""

GOOD_FILTERS = {
    "engine": "'engine/**', '.github/workflows/ci.yml'",
    "protocol": "'protocol/**', 'platform/mobile/*[Cc]odec*', '.github/workflows/ci.yml'",
    "rest": "'protocol/rest/**', 'docs/specs/PE-platform-eng/API-CONTRACT.md', 'docs/specs/S2-realtime/PROTOCOL.md', 'platform/ci/check_rest_contract.py', '.github/workflows/ci.yml'",
    "services": "'services/**', 'protocol/**', 'engine/**', '.github/workflows/ci.yml'",
    "web": "'clients/web/**', 'protocol/**', '.github/workflows/ci.yml'",
    "mobile": "'clients/android/**', 'build.gradle.kts', 'settings.gradle.kts', 'gradle.properties', 'gradlew', 'gradle/**', '.github/workflows/ci.yml'",
    "platform": "'platform/**', '.github/workflows/**'",
}


def workflow(filters: dict[str, str] | None = None, jobs: str = GATED_JOBS) -> str:
    f = dict(GOOD_FILTERS, **(filters or {}))
    block = "\n".join(f"                {k}: [{v}]" for k, v in f.items())
    return ("name: ci\njobs:\n  changes:\n    steps:\n      - uses: dorny/paths-filter@v3\n"
            "        with:\n          filters: |\n" + block + "\n" + jobs.format())


class ParseTest(unittest.TestCase):
    def test_filters_and_gates(self) -> None:
        filters, problems = c.parse_filters(workflow())
        self.assertEqual(problems, [])
        self.assertIn("engine/**", filters["engine"])
        gates, gprob = c.parse_job_gates(workflow())
        self.assertEqual(gprob, [])
        self.assertEqual(gates["web-e2e"], {"web", "services"})
        self.assertEqual(gates["ci-filter-audit"], set())

    def test_multiline_if_is_read_in_full(self) -> None:
        yml = workflow(jobs=GATED_JOBS.replace(
            "    if: ${{{{ needs.changes.outputs.web == 'true' }}}}\n    steps:\n      - uses: actions/setup-node",
            "    if: >-\n      ${{{{ needs.changes.outputs.web == 'true' ||\n      needs.changes.outputs.platform == 'true' }}}}\n    steps:\n      - uses: actions/setup-node"))
        gates, _ = c.parse_job_gates(yml)
        self.assertEqual(gates["web"], {"web", "platform"})

    def test_and_or_not_equal_gate_is_a_problem(self) -> None:
        yml = workflow(jobs=GATED_JOBS.replace("outputs.web == 'true' }}}}\n    steps:\n      - uses",
                                               "outputs.web == 'true' && github.event_name != 'push' }}}}\n    steps:\n      - uses"))
        _, problems = c.parse_job_gates(yml)
        self.assertTrue(any("&& or !=" in p for p in problems), problems)

    def test_unparseable_line_and_unsupported_patterns_are_problems(self) -> None:
        _, p1 = c.parse_filters(workflow().replace("engine: ['engine/**', '.github/workflows/ci.yml']", "engine:"))
        self.assertTrue(any("unparseable" in p for p in p1), p1)
        _, p2 = c.parse_filters(workflow({"web": "'!clients/web/**'"}))
        self.assertTrue(any("does not model" in p for p in p2), p2)
        _, p3 = c.parse_filters(workflow({"web": "'{clients,x}/web/**'"}))
        self.assertTrue(any("does not model" in p for p in p3), p3)

    def test_comment_naming_the_audit_is_not_an_audit_job(self) -> None:
        jobs = GATED_JOBS.replace("      - run: python3 platform/ci/check_ci_filters.py",
                                  "      # python3 platform/ci/check_ci_filters.py\n      - run: echo hi")
        self.assertFalse(c.has_unconditional_audit(workflow(jobs=jobs)))
        self.assertTrue(c.has_unconditional_audit(workflow()))


class GlobTest(unittest.TestCase):
    def test_semantics(self) -> None:
        self.assertTrue(c.covers(["engine/**"], "engine/engine-cgo", True))
        self.assertTrue(c.covers(["engine/**"], "engine", True))
        self.assertFalse(c.covers(["engine/**"], "engines", True))
        self.assertTrue(c.covers(["platform/mobile/*[Cc]odec*"], "platform/mobile/run_codec_parity.sh", False))
        self.assertFalse(c.covers(["platform/mobile/*[Cc]odec*"], "platform/mobile/x/codec.sh", False))
        self.assertTrue(c.covers([".github/workflows/**"], ".github/workflows/docs-check.yml", False))

    def test_bracket_negation_is_negation(self) -> None:
        # glob [!a] = any one char except 'a'. The old translation passed it to
        # Python re raw, where it means "'!' or 'a'", so x/a wrongly matched.
        self.assertTrue(c.covers(["x/[!a]"], "x/b", False))
        self.assertFalse(c.covers(["x/[!a]"], "x/a", False))


class DeriveTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        make_repo(self.root)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def cargo(self, crate: str) -> tuple[set[str], list[str]]:
        p, seen = c.Problems(), set()
        c.cargo_path_deps(self.root / crate, self.root, p, seen)
        return seen, list(p)

    def test_cargo_inline_and_multiline_tables_dev_target_workspace(self) -> None:
        write(self.root, "x/a/Cargo.toml", "[package]\nname='a'\n")
        write(self.root, "x/b/Cargo.toml", "[package]\nname='b'\n")
        write(self.root, "x/c/Cargo.toml", "[package]\nname='c'\n")
        write(self.root, "x/d/Cargo.toml", "[package]\nname='d'\n")
        write(self.root, "x/top/Cargo.toml", textwrap.dedent("""\
            [dependencies.a]
            path = "../a"
            [dev-dependencies]
            b = { path = "../b" }
            [target.'cfg(unix)'.build-dependencies]
            c = { path = "../c" }
            [workspace]
            members = ["../d"]
            """))
        seen, problems = self.cargo("x/top")
        self.assertEqual(problems, [])
        self.assertEqual(seen, {"x/top", "x/a", "x/b", "x/c", "x/d"})

    def test_cargo_bad_toml_and_missing_path_are_problems(self) -> None:
        write(self.root, "x/bad/Cargo.toml", "[dependencies\n")
        self.assertTrue(any("cannot parse" in p for p in self.cargo("x/bad")[1]))
        write(self.root, "x/miss/Cargo.toml", '[dependencies]\nm = { path = "../nope" }\n')
        self.assertTrue(any("no Cargo.toml" in p for p in self.cargo("x/miss")[1]))

    def test_makefile_any_var_expanded_and_build_output_resolves(self) -> None:
        p, out = c.Problems(), set()
        c.makefile_deps(self.root / "services/game-server", self.root, p, out)
        self.assertEqual(list(p), [])
        self.assertTrue({"protocol/codec-cgo", "protocol/codec", "engine/engine-cgo", "engine"} <= out)

    def test_makefile_renamed_var_still_found(self) -> None:
        write(self.root, "services/game-server/Makefile", "ENGINE_CGO_PATH := ../../engine/engine-cgo\nlib:\n\tcargo build\n")
        p, out = c.Problems(), set()
        c.makefile_deps(self.root / "services/game-server", self.root, p, out)
        self.assertIn("engine", out)

    def test_makefile_unexpandable_and_pathless_cargo_are_problems(self) -> None:
        write(self.root, "services/game-server/Makefile", "X := $(UNDEFINED)/../../engine\nlib:\n\tcargo build\n")
        p, out = c.Problems(), set()
        c.makefile_deps(self.root / "services/game-server", self.root, p, out)
        joined = "\n".join(p)
        self.assertIn("cannot expand", joined)
        self.assertIn("runs cargo but declares no ../ dependency paths", joined)

    def test_go_literals_join_and_cgo(self) -> None:
        write(self.root, "services/game-server/internal/codec/join_test.go",
              'package codec\nvar p = filepath.Join("..", "..", "..", "..", "protocol", "vectors", "vectors.json")\n'
              'var q = `../../../../protocol/rest/openapi.yaml`\n')
        p, out = c.Problems(), set()
        c.go_deps(self.root / "services/game-server", self.root, p, out)
        self.assertEqual(list(p), [])
        self.assertEqual(out, {"protocol/vectors/vectors.json", "protocol/codec-cgo/include", "protocol/rest/openapi.yaml"})

    def test_go_join_with_non_literal_is_a_problem(self) -> None:
        write(self.root, "services/game-server/x.go", 'package x\nvar p = filepath.Join("..", base, "vectors.json")\n')
        p, out = c.Problems(), set()
        c.go_deps(self.root / "services/game-server", self.root, p, out)
        self.assertTrue(any("non-literal" in x for x in p), list(p))

    def test_npm_forms_and_outside_repo(self) -> None:
        write(self.root, "clients/web/package.json",
              '{"dependencies": {"a": "file:../../protocol/codec-wasm/pkg", "b": "link:../../protocol/codec"},'
              ' "devDependencies": {"c": "../../protocol/rest", "registry": "^1.0.0"}}')
        p, out = c.Problems(), set()
        c.npm_deps(self.root / "clients/web", self.root, p, out)
        self.assertEqual(list(p), [])
        self.assertTrue({"protocol/codec-wasm", "protocol/codec", "protocol/rest"} <= out)
        write(self.root, "clients/web/package.json", '{"dependencies": {"x": "file:../../../../../../../../tmp"}}')
        p2, out2 = c.Problems(), set()
        c.npm_deps(self.root / "clients/web", self.root, p2, out2)
        self.assertTrue(any("outside the repository" in x for x in p2), list(p2))

    def test_python_inputs_include_digit_named_constants(self) -> None:
        p, out = c.Problems(), set()
        c.python_script_deps(self.root / "platform/ci/check_rest_contract.py", self.root, p, out)
        self.assertEqual(list(p), [])
        self.assertIn("docs/specs/S2-realtime/PROTOCOL.md", out)
        write(self.root, "platform/ci/check_rest_contract.py", "REPO = x\nSPEC = REPO / from_env()\n")
        p2, out2 = c.Problems(), set()
        c.python_script_deps(self.root / "platform/ci/check_rest_contract.py", self.root, p2, out2)
        self.assertTrue(any("cannot read input path" in x for x in p2), list(p2))

    def test_step_scan_skips_uses_and_comments_and_follows_scripts(self) -> None:
        lines = ["    steps:", "      - uses: gradle/actions/setup-gradle@v4",
                 "      # protocol/never-read is only mentioned in a comment",
                 "      - run: platform/mobile/run_codec_parity.sh"]
        p = c.Problems()
        got = c.derive_job_inputs(lines, self.root, p)
        self.assertEqual(list(p), [])
        self.assertIn("protocol/codec-ffi", got)
        self.assertIn("protocol/codec", got)
        self.assertNotIn("gradle/actions/setup-gradle", got)


class CheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        make_repo(self.root)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def errors(self, yml: str) -> str:
        return "\n".join(c.check(self.root, yml))

    def test_complete_workflow_passes(self) -> None:
        self.assertEqual(c.check(self.root, workflow()), [])

    def test_owner_only_services_filter_fails_like_issue_87(self) -> None:
        e = self.errors(workflow({"services": "'services/**', '.github/workflows/ci.yml'"}))
        for path in ("protocol/codec-cgo", "engine/engine-cgo", "protocol/vectors/vectors.json"):
            self.assertIn(f"job 'services' reads {path}", e)
        self.assertIn("job 'web-e2e' reads engine", e)

    def test_rest_audit_under_protocol_filter_fails_like_issue_86(self) -> None:
        jobs = GATED_JOBS.replace("outputs.rest == 'true'", "outputs.protocol == 'true'")
        self.assertIn("job 'rest-contract' reads docs/specs/S2-realtime/PROTOCOL.md", self.errors(workflow(jobs=jobs)))

    def test_filtered_audit_job_fails(self) -> None:
        jobs = GATED_JOBS.replace("  ci-filter-audit:\n    runs-on",
                                  "  ci-filter-audit:\n    if: ${{{{ needs.changes.outputs.platform == 'true' }}}}\n    runs-on")
        self.assertIn("a filtered audit is itself a hollow gate", self.errors(workflow(jobs=jobs)))

    def test_missing_gradle_properties_fails(self) -> None:
        e = self.errors(workflow({"mobile": "'clients/android/**', 'build.gradle.kts', 'settings.gradle.kts', 'gradlew', 'gradle/**', '.github/workflows/ci.yml'"}))
        self.assertIn("job 'mobile' reads gradle.properties", e)

    def test_missing_ci_yml_trigger_and_undefined_output_fail(self) -> None:
        self.assertIn("job 'web' reads .github/workflows/ci.yml", self.errors(workflow({"web": "'clients/web/**', 'protocol/**'"})))
        jobs = GATED_JOBS.replace("outputs.engine == 'true'", "outputs.engin == 'true'")
        self.assertIn("undefined filter output(s) ['engin']", self.errors(workflow(jobs=jobs)))

    def test_removed_or_ungated_expected_job_fails(self) -> None:
        jobs = GATED_JOBS.replace("    if: ${{{{ needs.changes.outputs.platform == 'true' }}}}\n", "")
        self.assertIn("job 'platform' is not gated", self.errors(workflow(jobs=jobs)))


class RealRepoTest(unittest.TestCase):
    def test_real_workflow_has_no_hollow_gate(self) -> None:
        self.assertEqual(c.check(c.REPO, (c.REPO / c.CI_YML_REL).read_text(encoding="utf-8")), [])


if __name__ == "__main__":
    unittest.main()
