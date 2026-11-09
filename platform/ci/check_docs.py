#!/usr/bin/env python3
"""
docs-check: validate the planning doc set.
Runs in CI (and locally: `python3 platform/ci/check_docs.py`).

Checks:
  1. Every spec under docs/specs/**/ (excluding _shared templates and READMEs)
     has a 'Status' header with a valid lifecycle value.
  2. Master-ID references (F-xx / X-x / ADR-xx / G-x / NFR-x) that look malformed.
  3. Relative markdown links point at files that exist.

Exit non-zero on any failure. Zero external dependencies (stdlib only).
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


def read_codec_type_codes() -> dict[str, tuple[str, int]]:
    """Return {NAME: (direction, code)} from the codec's client_code/server_code modules."""
    text = CODEC_MESSAGES.read_text(encoding="utf-8")
    codes: dict[str, tuple[str, int]] = {}
    for module, direction in (("client_code", "C"), ("server_code", "S")):
        m = re.search(r"pub mod " + module + r"\s*\{(.*?)\n\}", text, re.S)
        if not m:
            errors.append(f"{CODEC_MESSAGES.relative_to(REPO)}: module '{module}' not found")
            continue
        for name, value in re.findall(r"pub const ([A-Z_]+): u8 = (0x[0-9A-Fa-f]{2});", m.group(1)):
            codes[name] = (direction, int(value, 16))
    return codes


def read_spec_type_codes() -> dict[str, tuple[str, int]]:
    """Return {NAME: (direction, code)} from the PROTOCOL.md §4A.2 table rows."""
    text = PROTOCOL_SPEC.read_text(encoding="utf-8")
    m = re.search(r"### 4A\.2 .*?\n(.*?)\n### 4A\.3", text, re.S)
    if not m:
        errors.append(f"{PROTOCOL_SPEC.relative_to(REPO)}: section 4A.2 not found")
        return {}
    codes: dict[str, tuple[str, int]] = {}
    for value, direction, name in re.findall(
        r"\|\s*(0x[0-9A-Fa-f]{2})\s*\|\s*([CS])\s*\|\s*([A-Z_]+)\s*\|", m.group(1)
    ):
        codes[name] = (direction, int(value, 16))
    return codes


def check_ws_type_codes() -> None:
    # #106: PROTOCOL.md §4A.2 must list exactly the codec's message type codes,
    # with the same direction and value. Field lists are not compared (that needs
    # a parser for §4A.3); the golden vectors remain the byte-level truth.
    if not CODEC_MESSAGES.exists() or not PROTOCOL_SPEC.exists():
        warnings.append("codec message.rs or PROTOCOL.md missing: WS type-code check skipped")
        return
    codec = read_codec_type_codes()
    spec = read_spec_type_codes()
    rel = PROTOCOL_SPEC.relative_to(REPO)
    for name, (direction, code) in sorted(codec.items()):
        if name not in spec:
            errors.append(f"{rel}: §4A.2 is missing {direction} 0x{code:02X} {name} (present in the codec)")
        elif spec[name] != (direction, code):
            d, c = spec[name]
            errors.append(
                f"{rel}: §4A.2 lists {name} as {d} 0x{c:02X}, codec has {direction} 0x{code:02X}"
            )
    for name, (direction, code) in sorted(spec.items()):
        if name not in codec:
            errors.append(f"{rel}: §4A.2 lists {direction} 0x{code:02X} {name}, which the codec does not define")


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
