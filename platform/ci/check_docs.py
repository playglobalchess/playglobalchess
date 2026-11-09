#!/usr/bin/env python3
"""
docs-check: validate the planning doc set.
Runs in CI (and locally: `python3 platform/ci/check_docs.py`).

Checks:
  1. Every spec under docs/specs/**/ (excluding _shared templates and READMEs)
     has a 'Status' header with a valid lifecycle value.
  2. Master-ID references (F-xx / X-x / ADR-xx / G-x / NFR-x) that look malformed.
  3. Relative markdown links point at files that exist.
  4. WS message type codes: SPEC-S2-PROTOCOL §4A.2 (docs/specs/S2-realtime/
     PROTOCOL.md) lists exactly the codes the codec defines in
     protocol/codec/src/message.rs (client_code / server_code), with the same
     direction and value (#106). This check READS CODEC SOURCE: a codec-side
     change can fail docs-check. It fails closed: a missing file, a missing
     module or section, an unparseable const, or a duplicate name is an error.

Exit non-zero on any failure. Zero external dependencies (stdlib only).
Unit tests: platform/ci/test_check_docs.py (run in the ci.yml platform job).
"""
from __future__ import annotations
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
DOCS = REPO / "docs"
VALID_STATUS = {"DRAFT", "REVIEW", "RATIFIED", "BUILDING", "DONE", "SUPERSEDED"}

errors: list[str] = []
warnings: list[str] = []


def is_spec(p: Path) -> bool:
    # A "spec" is a .md under docs/specs that is not a README or a _shared template.
    if p.suffix != ".md":
        return False
    rel = p.relative_to(DOCS)
    parts = rel.parts
    if "_shared" in parts:
        return False
    if p.name.upper() == "README.MD":
        return False
    return parts[0] == "specs"


def check_status_headers() -> None:
    for p in sorted(DOCS.rglob("*.md")):
        if not is_spec(p):
            continue
        text = p.read_text(encoding="utf-8", errors="replace")
        m = re.search(r"(?im)^\|?\s*Status\s*\|?\s*([A-Za-z]+)", text)
        if not m:
            errors.append(f"{p.relative_to(REPO)}: spec has no 'Status' header")
            continue
        status = m.group(1).upper()
        if status not in VALID_STATUS:
            errors.append(
                f"{p.relative_to(REPO)}: invalid Status '{m.group(1)}' "
                f"(expected one of {sorted(VALID_STATUS)})"
            )


def check_relative_links() -> None:
    link_re = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
    for p in sorted(DOCS.rglob("*.md")):
        text = p.read_text(encoding="utf-8", errors="replace")
        for mobj in link_re.finditer(text):
            target = mobj.group(1).split("#")[0].strip()
            if not target or target.startswith(("http://", "https://", "mailto:")):
                continue
            resolved = (p.parent / target).resolve()
            if not resolved.exists():
                errors.append(
                    f"{p.relative_to(REPO)}: broken relative link -> {target}"
                )


def check_id_shapes() -> None:
    # Flag obviously malformed IDs, e.g. 'F -01', 'ADR 1', 'G--A'. Advisory (warnings).
    bad = re.compile(r"\b(F|X|NFR|ADR)\s-\s?\d|\bADR\s+\d\b")
    for p in sorted(DOCS.rglob("*.md")):
        for i, line in enumerate(p.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            if bad.search(line):
                warnings.append(f"{p.relative_to(REPO)}:{i}: possibly malformed ID reference")


CODEC_MESSAGES = REPO / "protocol" / "codec" / "src" / "message.rs"
PROTOCOL_SPEC = DOCS / "specs" / "S2-realtime" / "PROTOCOL.md"

# Codes are keyed by (direction, NAME) so the same name in both directions can
# never overwrite one another.
Codes = dict[tuple[str, str], int]

_CONST_RE = re.compile(r"pub\s+const\s+([A-Za-z0-9_]+)\s*:\s*u8\s*=\s*0x([0-9A-Fa-f]{2})\s*;")
_ANY_CONST_RE = re.compile(r"\bpub\s+const\b")
_ROW_RE = re.compile(r"\|\s*(0x[0-9A-Fa-f]{2})\s*\|\s*([CS])\s*\|\s*([A-Za-z0-9_]+)\s*\|")


def parse_codec_type_codes(text: str) -> tuple[Codes, list[str]]:
    """Parse client_code/server_code modules. Returns (codes, problems).

    Fails closed: a missing module, an empty module, or any `pub const` line the
    strict pattern does not understand (e.g. a decimal literal or a non-u8 type)
    is a problem, never silently skipped.
    """
    codes: Codes = {}
    problems: list[str] = []
    for module, direction in (("client_code", "C"), ("server_code", "S")):
        m = re.search(r"pub\s+mod\s+" + module + r"\s*\{(.*?)\n\}", text, re.S)
        if not m:
            problems.append(f"module '{module}' not found")
            continue
        body = m.group(1)
        parsed = _CONST_RE.findall(body)
        declared = len(_ANY_CONST_RE.findall(body))
        if declared == 0:
            problems.append(f"module '{module}' declares no constants")
        if len(parsed) != declared:
            problems.append(
                f"module '{module}': {declared} pub const line(s) but only {len(parsed)} match "
                f"'pub const NAME: u8 = 0xNN;' (update the parser, do not skip)"
            )
        for name, value in parsed:
            key = (direction, name)
            if key in codes:
                problems.append(f"duplicate {direction} constant {name}")
            codes[key] = int(value, 16)
    return codes, problems


def parse_spec_type_codes(text: str) -> tuple[Codes, list[str]]:
    """Parse the PROTOCOL.md §4A.2 table rows. Returns (codes, problems)."""
    problems: list[str] = []
    m = re.search(r"### 4A\.2 .*?\n(.*?)\n### 4A\.3", text, re.S)
    if not m:
        return {}, ["section 4A.2 not found"]
    codes: Codes = {}
    for value, direction, name in _ROW_RE.findall(m.group(1)):
        key = (direction, name)
        if key in codes:
            problems.append(f"duplicate {direction} row {name}")
        codes[key] = int(value, 16)
    if not codes:
        problems.append("section 4A.2 contains no type-code rows")
    return codes, problems


def compare_type_codes(codec: Codes, spec: Codes) -> list[str]:
    """Return mismatches between the codec and the spec (empty = in sync)."""
    out: list[str] = []
    for (direction, name), code in sorted(codec.items()):
        if (direction, name) not in spec:
            out.append(f"§4A.2 is missing {direction} 0x{code:02X} {name} (present in the codec)")
        elif spec[(direction, name)] != code:
            out.append(
                f"§4A.2 lists {direction} {name} as 0x{spec[(direction, name)]:02X}, codec has 0x{code:02X}"
            )
    for (direction, name), code in sorted(spec.items()):
        if (direction, name) not in codec:
            out.append(f"§4A.2 lists {direction} 0x{code:02X} {name}, which the codec does not define")
    # Same code value used twice in one direction is also drift.
    for direction in ("C", "S"):
        seen: dict[int, str] = {}
        for (d, name), code in sorted(spec.items()):
            if d == direction:
                if code in seen:
                    out.append(f"§4A.2 assigns {direction} 0x{code:02X} to both {seen[code]} and {name}")
                seen[code] = name
    return out


def check_ws_type_codes() -> None:
    # #106. Fails closed: if either input is missing, the check errors rather than
    # skipping, so moving or renaming message.rs cannot turn it into a green no-op.
    for path in (CODEC_MESSAGES, PROTOCOL_SPEC):
        if not path.exists():
            errors.append(
                f"{path.relative_to(REPO)} not found: WS type-code check cannot run "
                f"(update CODEC_MESSAGES/PROTOCOL_SPEC in check_docs.py and the docs-check.yml paths)"
            )
            return
    codec, codec_problems = parse_codec_type_codes(CODEC_MESSAGES.read_text(encoding="utf-8"))
    spec, spec_problems = parse_spec_type_codes(PROTOCOL_SPEC.read_text(encoding="utf-8"))
    for msg in codec_problems:
        errors.append(f"{CODEC_MESSAGES.relative_to(REPO)}: {msg}")
    for msg in spec_problems:
        errors.append(f"{PROTOCOL_SPEC.relative_to(REPO)}: {msg}")
    if codec_problems or spec_problems:
        return
    for msg in compare_type_codes(codec, spec):
        errors.append(f"{PROTOCOL_SPEC.relative_to(REPO)}: {msg}")


def main() -> int:
    if not DOCS.exists():
        print("docs/ not found — nothing to check", file=sys.stderr)
        return 0
    check_status_headers()
    check_relative_links()
    check_id_shapes()
    check_ws_type_codes()

    for w in warnings:
        print(f"::warning::{w}")
    for e in errors:
        print(f"::error::{e}")

    spec_count = sum(1 for p in DOCS.rglob("*.md") if is_spec(p))
    print(f"docs-check: scanned docs/, {spec_count} spec file(s), "
          f"{len(errors)} error(s), {len(warnings)} warning(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
