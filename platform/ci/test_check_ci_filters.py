#!/usr/bin/env python3
"""Unit tests for check_ci_filters.py (#86, #87). Stdlib unittest.

Run: python3 -m unittest discover -s platform/ci -p 'test_*.py'
"""
from __future__ import annotations

import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_ci_filters as c  # noqa: E402

GOOD_YML = textwrap.dedent("""\
    name: ci
    jobs:
      changes:
        steps:
          - uses: dorny/paths-filter@v3
            with:
              filters: |
                engine: ['engine/**', '.github/workflows/ci.yml']
                services: ['services/**', 'protocol/**', 'engine/**', '.github/workflows/ci.yml']
      engine:
        needs: changes
        if: ${{ needs.changes.outputs.engine == 'true' }}
      services:
        needs: changes
        if: ${{ needs.changes.outputs.services == 'true' }}
    """)


class ParseTest(unittest.TestCase):
    def test_parses_filters_and_gates(self) -> None:
        filters, problems = c.parse_filters(GOOD_YML)
        self.assertEqual(problems, [])
        self.assertEqual(filters["engine"], ["engine/**", ".github/workflows/ci.yml"])
        gates = c.parse_job_gates(GOOD_YML)
        self.assertEqual(gates["services"], {"services"})
        self.assertEqual(gates["changes"], set())

    def test_combined_if_collects_every_output(self) -> None:
        yml = GOOD_YML + "  web-e2e:\n    if: ${{ needs.changes.outputs.web == 'true' || needs.changes.outputs.services == 'true' }}\n"
        self.assertEqual(c.parse_job_gates(yml)["web-e2e"], {"web", "services"})

    def test_missing_filter_block_is_a_problem(self) -> None:
        _, problems = c.parse_filters("jobs:\n  a:\n    if: x\n")
        self.assertEqual(problems, ["no `filters: |` block found"])

    def test_unparseable_filter_line_is_a_problem_not_a_skip(self) -> None:
        yml = GOOD_YML.replace("engine: ['engine/**', '.github/workflows/ci.yml']", "engine:\n                  - 'engine/**'")
        _, problems = c.parse_filters(yml)
        self.assertTrue(any("unparseable" in p for p in problems), problems)


class GlobTest(unittest.TestCase):
    def test_double_star(self) -> None:
        self.assertTrue(c.covers(["engine/**"], "engine/engine-cgo"))
        self.assertTrue(c.covers(["engine/**"], "engine"))
        self.assertFalse(c.covers(["engine/**"], "engines"))
        self.assertFalse(c.covers(["services/**"], "protocol/codec"))

    def test_single_star_and_class_stay_in_segment(self) -> None:
        self.assertTrue(c.covers(["platform/mobile/*[Cc]odec*"], "platform/mobile/build_codec_mobile.sh"))
        self.assertFalse(c.covers(["platform/mobile/*[Cc]odec*"], "platform/mobile/x/codec.sh"))

    def test_exact_file(self) -> None:
        self.assertTrue(c.covers([".github/workflows/ci.yml"], ".github/workflows/ci.yml"))
        self.assertFalse(c.covers([".github/workflows/ci.yml"], ".github/workflows/docs-check.yml"))
        self.assertTrue(c.covers([".github/workflows/**"], ".github/workflows/docs-check.yml"))


def make_repo(root: Path, *, rest_script: str | None = None) -> None:
    """A minimal repo shaped like the real one."""
    def w(rel: str, text: str) -> None:
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")

    w("engine/Cargo.toml", "[package]\nname='e'\n")
    w("engine/engine-cgo/Cargo.toml", "[dependencies]\npgc-engine = { path = \"..\" }\n")
    w("protocol/codec/Cargo.toml", "[package]\nname='p'\n")
    w("protocol/codec-cgo/Cargo.toml", "[dependencies]\npgc-protocol = { path = \"../codec\" }\n")
    w("protocol/codec-wasm/Cargo.toml", "[dependencies]\npgc-protocol = { path = \"../codec\" }\n")
    w("protocol/vectors/vectors.json", "{}")
    w("protocol/rest/openapi.yaml", "openapi: 3.1.0\n")
    w("docs/specs/S2-realtime/PROTOCOL.md", "x")
    w("docs/specs/PE-platform-eng/API-CONTRACT.md", "x")
    w("services/game-server/Makefile",
      "CODEC_CGO_DIR  := ../../protocol/codec-cgo\nENGINE_CGO_DIR := ../../engine/engine-cgo\n")
    w("services/game-server/internal/codec/parity_test.go",
      'package codec\nconst v = "../../../../protocol/vectors/vectors.json"\nconst inside = "../../go.mod"\n')
    w("clients/web/package.json",
      '{"scripts": {"e2e": "make -C ../../services/game-server lib"},'
      ' "dependencies": {"pgc-protocol-wasm": "file:../../protocol/codec-wasm/pkg"}}')
    w("platform/ci/check_rest_contract.py", rest_script if rest_script is not None else (
        'REPO = x\nOPENAPI = REPO / "protocol" / "rest" / "openapi.yaml"\n'
        'PE_SPEC = REPO / "docs" / "specs" / "PE-platform-eng" / "API-CONTRACT.md"\n'
        'S2_SPEC = REPO / "docs" / "specs" / "S2-realtime" / "PROTOCOL.md"\n'))


class DeriveTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        make_repo(self.root)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_services_follows_makefile_cargo_and_go_paths(self) -> None:
        problems: list[str] = []
        got = c.services_inputs(self.root, problems)
        self.assertEqual(problems, [])
        self.assertEqual(got, {"services", "protocol/codec-cgo", "protocol/codec", "engine/engine-cgo",
                               "engine", "protocol/vectors/vectors.json"})

    def test_web_follows_file_dependency_not_scripts(self) -> None:
        problems: list[str] = []
        got = c.web_inputs(self.root, problems)
        self.assertEqual(problems, [])
        self.assertEqual(got, {"clients/web", "protocol/codec-wasm", "protocol/codec"})

    def test_rest_inputs_include_digit_named_constants(self) -> None:
        # S2_SPEC has a digit; an earlier [A-Z_]+ pattern silently skipped it.
        problems: list[str] = []
        got = c.rest_contract_inputs(self.root, problems)
        self.assertEqual(problems, [])
        self.assertIn("docs/specs/S2-realtime/PROTOCOL.md", got)
        self.assertIn("docs/specs/PE-platform-eng/API-CONTRACT.md", got)

    def test_unreadable_rest_input_is_a_problem(self) -> None:
        make_repo(self.root, rest_script='REPO = x\nSPEC = REPO / name_from_env()\n')
        problems: list[str] = []
        c.rest_contract_inputs(self.root, problems)
        self.assertTrue(any("cannot read input path" in p for p in problems), problems)

    def test_missing_makefile_target_is_a_problem(self) -> None:
        (self.root / "services/game-server/Makefile").write_text("CODEC_CGO_DIR := ../../protocol/moved\n")
        problems: list[str] = []
        c.services_inputs(self.root, problems)
        self.assertTrue(any("does not exist" in p for p in problems), problems)


class CheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        make_repo(self.root)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def full_yml(self, services_filter: str) -> str:
        return textwrap.dedent(f"""\
            jobs:
              changes:
                steps:
                  - with:
                      filters: |
                        engine: ['engine/**', '.github/workflows/ci.yml']
                        protocol: ['protocol/**', '.github/workflows/ci.yml']
                        rest: ['protocol/rest/**', 'docs/specs/PE-platform-eng/API-CONTRACT.md', 'docs/specs/S2-realtime/PROTOCOL.md', 'platform/ci/check_rest_contract.py', '.github/workflows/ci.yml']
                        services: [{services_filter}]
                        web: ['clients/web/**', 'protocol/**', '.github/workflows/ci.yml']
                        platform: ['platform/**', '.github/workflows/**']
              engine:
                if: ${{{{ needs.changes.outputs.engine == 'true' }}}}
              protocol:
                if: ${{{{ needs.changes.outputs.protocol == 'true' }}}}
              rest-contract:
                if: ${{{{ needs.changes.outputs.rest == 'true' }}}}
              services:
                if: ${{{{ needs.changes.outputs.services == 'true' }}}}
              web:
                if: ${{{{ needs.changes.outputs.web == 'true' }}}}
              web-e2e:
                if: ${{{{ needs.changes.outputs.web == 'true' || needs.changes.outputs.services == 'true' }}}}
              platform:
                if: ${{{{ needs.changes.outputs.platform == 'true' }}}}
            """)

    def test_complete_filters_pass(self) -> None:
        yml = self.full_yml("'services/**', 'protocol/**', 'engine/**', '.github/workflows/ci.yml'")
        self.assertEqual(c.check(self.root, yml), [])

    def test_owner_only_services_filter_fails_like_issue_87(self) -> None:
        yml = self.full_yml("'services/**', '.github/workflows/ci.yml'")
        errors = c.check(self.root, yml)
        joined = "\n".join(errors)
        for path in ("protocol/codec-cgo", "engine/engine-cgo", "protocol/vectors/vectors.json"):
            self.assertIn(f"job 'services' reads {path}", joined)
        self.assertIn("job 'web-e2e' reads engine", joined)

    def test_rest_audit_inside_protocol_job_fails_like_issue_86(self) -> None:
        yml = self.full_yml("'services/**', 'protocol/**', 'engine/**', '.github/workflows/ci.yml'")
        yml = yml.replace("  rest-contract:\n    if: ${{ needs.changes.outputs.rest == 'true' }}\n", "")
        self.assertIn("job 'rest-contract' not found", "\n".join(c.check(self.root, yml)))

    def test_missing_ci_yml_trigger_fails(self) -> None:
        yml = self.full_yml("'services/**', 'protocol/**', 'engine/**'")
        self.assertIn("job 'services' reads .github/workflows/ci.yml", "\n".join(c.check(self.root, yml)))

    def test_undefined_output_and_ungated_job_fail(self) -> None:
        yml = self.full_yml("'services/**', 'protocol/**', 'engine/**', '.github/workflows/ci.yml'")
        yml = yml.replace("outputs.web == 'true' }}\n  web-e2e", "outputs.webb == 'true' }}\n  web-e2e")
        yml = yml.replace("  platform:\n    if: ${{ needs.changes.outputs.platform == 'true' }}", "  platform:\n    runs-on: x")
        joined = "\n".join(c.check(self.root, yml))
        self.assertIn("undefined filter output(s) ['webb']", joined)
        self.assertIn("job 'platform' has no `if:`", joined)


class RealRepoTest(unittest.TestCase):
    def test_real_workflow_has_no_hollow_gate(self) -> None:
        self.assertEqual(c.check(c.REPO, c.CI_YML.read_text(encoding="utf-8")), [])


if __name__ == "__main__":
    unittest.main()
