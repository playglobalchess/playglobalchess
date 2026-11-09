#!/usr/bin/env python3
"""Unit tests for the WS type-code check in check_docs.py (#106).

Run: python3 -m unittest discover -s platform/ci -p 'test_*.py'
Stdlib only, like the script under test.
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_docs  # noqa: E402

CODEC = """
pub mod client_code {
    pub const AUTHENTICATE: u8 = 0x01;
    pub const RESYNC: u8 = 0x0A;
}

pub mod server_code {
    pub const MATCHED: u8 = 0x81;
    pub const ERROR: u8 = 0x8F;
}
"""

SPEC = """
### 4A.2 Message type codes (u8) — frozen
| Code | Dir | Message |   | Code | Dir | Message |
|---|---|---|---|---|---|---|
| 0x01 | C | AUTHENTICATE | | 0x81 | S | MATCHED |
| 0x0A | C | RESYNC | | 0x8F | S | ERROR |

### 4A.3 Payload field layouts
"""


class ParseCodecTest(unittest.TestCase):
    def test_parses_both_modules(self) -> None:
        codes, problems = check_docs.parse_codec_type_codes(CODEC)
        self.assertEqual(problems, [])
        self.assertEqual(
            codes,
            {("C", "AUTHENTICATE"): 0x01, ("C", "RESYNC"): 0x0A, ("S", "MATCHED"): 0x81, ("S", "ERROR"): 0x8F},
        )

    def test_missing_module_is_a_problem(self) -> None:
        _, problems = check_docs.parse_codec_type_codes(CODEC.replace("pub mod server_code", "pub mod srv"))
        self.assertTrue(any("server_code" in p for p in problems), problems)

    def test_renamed_modules_parse_nothing_and_fail(self) -> None:
        _, problems = check_docs.parse_codec_type_codes("pub mod codes { pub const X: u8 = 0x01; }")
        self.assertEqual(len(problems), 2, problems)

    def test_empty_module_is_a_problem(self) -> None:
        text = "pub mod client_code {\n}\n\npub mod server_code {\n    pub const ERROR: u8 = 0x8F;\n}\n"
        _, problems = check_docs.parse_codec_type_codes(text)
        self.assertTrue(any("declares no constants" in p for p in problems), problems)

    def test_decimal_literal_is_not_skipped(self) -> None:
        text = CODEC.replace("pub const RESYNC: u8 = 0x0A;", "pub const RESYNC: u8 = 10;")
        _, problems = check_docs.parse_codec_type_codes(text)
        self.assertTrue(any("only 1 match" in p for p in problems), problems)

    def test_name_with_digit_is_parsed(self) -> None:
        text = CODEC.replace("RESYNC", "RESYNC2")
        codes, problems = check_docs.parse_codec_type_codes(text)
        self.assertEqual(problems, [])
        self.assertIn(("C", "RESYNC2"), codes)

    def test_same_name_both_directions_does_not_overwrite(self) -> None:
        text = CODEC.replace("MATCHED", "RESYNC")
        codes, problems = check_docs.parse_codec_type_codes(text)
        self.assertEqual(problems, [])
        self.assertEqual(codes[("C", "RESYNC")], 0x0A)
        self.assertEqual(codes[("S", "RESYNC")], 0x81)


class ParseSpecTest(unittest.TestCase):
    def test_parses_two_column_table(self) -> None:
        codes, problems = check_docs.parse_spec_type_codes(SPEC)
        self.assertEqual(problems, [])
        self.assertEqual(len(codes), 4)

    def test_missing_section_is_a_problem(self) -> None:
        _, problems = check_docs.parse_spec_type_codes(SPEC.replace("### 4A.2", "### 4B.2"))
        self.assertEqual(problems, ["section 4A.2 not found"])

    def test_section_without_rows_is_a_problem(self) -> None:
        _, problems = check_docs.parse_spec_type_codes("### 4A.2 codes\nnothing here\n### 4A.3 x\n")
        self.assertEqual(problems, ["section 4A.2 contains no type-code rows"])

    def test_duplicate_row_is_a_problem(self) -> None:
        text = SPEC.replace("| 0x0A | C | RESYNC |", "| 0x0A | C | AUTHENTICATE |")
        _, problems = check_docs.parse_spec_type_codes(text)
        self.assertTrue(any("duplicate C row AUTHENTICATE" in p for p in problems), problems)


class CompareTest(unittest.TestCase):
    def setUp(self) -> None:
        self.codec, _ = check_docs.parse_codec_type_codes(CODEC)
        self.spec, _ = check_docs.parse_spec_type_codes(SPEC)

    def test_in_sync(self) -> None:
        self.assertEqual(check_docs.compare_type_codes(self.codec, self.spec), [])

    def test_code_missing_from_spec(self) -> None:
        spec = dict(self.spec)
        del spec[("C", "RESYNC")]
        out = check_docs.compare_type_codes(self.codec, spec)
        self.assertEqual(out, ["§4A.2 is missing C 0x0A RESYNC (present in the codec)"])

    def test_value_mismatch(self) -> None:
        spec = dict(self.spec)
        spec[("S", "ERROR")] = 0x8E
        out = check_docs.compare_type_codes(self.codec, spec)
        self.assertEqual(out, ["§4A.2 lists S ERROR as 0x8E, codec has 0x8F"])

    def test_direction_mismatch(self) -> None:
        spec = dict(self.spec)
        spec[("S", "RESYNC")] = spec.pop(("C", "RESYNC"))
        out = check_docs.compare_type_codes(self.codec, spec)
        self.assertIn("§4A.2 is missing C 0x0A RESYNC (present in the codec)", out)
        self.assertIn("§4A.2 lists S 0x0A RESYNC, which the codec does not define", out)

    def test_extra_spec_row(self) -> None:
        spec = dict(self.spec)
        spec[("S", "GHOST")] = 0x90
        out = check_docs.compare_type_codes(self.codec, spec)
        self.assertEqual(out, ["§4A.2 lists S 0x90 GHOST, which the codec does not define"])

    def test_same_value_twice_in_one_direction(self) -> None:
        codec = dict(self.codec)
        codec[("C", "OTHER")] = 0x01
        spec = dict(self.spec)
        spec[("C", "OTHER")] = 0x01
        out = check_docs.compare_type_codes(codec, spec)
        self.assertTrue(any("assigns C 0x01 to both" in m for m in out), out)


class FailClosedTest(unittest.TestCase):
    def test_missing_codec_file_is_an_error_not_a_warning(self) -> None:
        saved_errors, saved_warnings = list(check_docs.errors), list(check_docs.warnings)
        saved_path = check_docs.CODEC_MESSAGES
        try:
            check_docs.errors.clear()
            check_docs.warnings.clear()
            check_docs.CODEC_MESSAGES = check_docs.REPO / "protocol" / "codec" / "src" / "moved_away.rs"
            check_docs.check_ws_type_codes()
            self.assertEqual(len(check_docs.errors), 1, check_docs.errors)
            self.assertIn("cannot run", check_docs.errors[0])
            self.assertEqual(check_docs.warnings, [])
        finally:
            check_docs.CODEC_MESSAGES = saved_path
            check_docs.errors[:] = saved_errors
            check_docs.warnings[:] = saved_warnings

    def test_real_repo_is_in_sync(self) -> None:
        saved = list(check_docs.errors)
        try:
            check_docs.errors.clear()
            check_docs.check_ws_type_codes()
            self.assertEqual(check_docs.errors, [])
        finally:
            check_docs.errors[:] = saved


if __name__ == "__main__":
    unittest.main()
